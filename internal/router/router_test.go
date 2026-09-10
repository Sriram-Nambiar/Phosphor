package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

func TestCircuitBreaker_TrippingAndReset(t *testing.T) {
	cb := NewCircuitBreaker(2, 50*time.Millisecond)

	now := time.Now()
	cb.nowFunc = func() time.Time { return now }

	if !cb.Allow() || cb.GetState() != StateClosed {
		t.Errorf("initial state should be Closed, got %s", cb.GetState())
	}

	rateLimitErr := &provider.HTTPError{StatusCode: 429, Provider: "test"}

	// First failure
	cb.RecordFailure(rateLimitErr)
	if cb.GetState() != StateClosed {
		t.Errorf("expected still Closed after 1 failure, got %s", cb.GetState())
	}

	// Second failure -> trips to OPEN
	cb.RecordFailure(rateLimitErr)
	if cb.GetState() != StateOpen {
		t.Errorf("expected Open after 2 failures, got %s", cb.GetState())
	}
	if cb.Allow() {
		t.Error("expected Allow() to be false when Open")
	}

	// Advance time past cooldown
	now = now.Add(60 * time.Millisecond)
	if cb.GetState() != StateHalfOpen {
		t.Errorf("expected HalfOpen after cooldown, got %s", cb.GetState())
	}
	if !cb.Allow() {
		t.Error("expected Allow() to be true when HalfOpen")
	}

	// Probe succeeds -> resets to CLOSED
	cb.RecordSuccess()
	if cb.GetState() != StateClosed {
		t.Errorf("expected Closed after probe success, got %s", cb.GetState())
	}
}

func TestCostEstimation(t *testing.T) {
	req := &provider.ChatRequest{
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Hello world! How are you?"},
		},
	}

	tokens := EstimatePromptTokens(req)
	if tokens <= 0 {
		t.Errorf("expected positive token estimate, got %d", tokens)
	}

	costCfg := config.CostConfig{
		PromptCostPer1M:     2.00,
		CompletionCostPer1M: 10.00,
	}

	cost := CalculateCost(1000, 500, costCfg)
	expectedCost := (1000.0/1000000.0)*2.00 + (500.0/1000000.0)*10.00
	if cost != expectedCost {
		t.Errorf("expected %f, got %f", expectedCost, cost)
	}
}

func TestRouter_StrategySorting(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			DefaultStrategy: config.StrategyPriority,
		},
		CircuitBreaker: config.CircuitBreakerConfig{
			FailureThreshold: 3,
			CooldownSeconds:  30,
		},
		Providers: []config.ProviderConfig{
			{
				Name:    "expensive-fast",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: "http://localhost:8001",
				Enabled: true,
				Cost:    config.CostConfig{PromptCostPer1M: 10.0, CompletionCostPer1M: 30.0},
			},
			{
				Name:    "cheap-slow",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: "http://localhost:8002",
				Enabled: true,
				Cost:    config.CostConfig{PromptCostPer1M: 0.1, CompletionCostPer1M: 0.2},
			},
		},
		Models: map[string]config.ModelRule{
			"test-least-cost": {
				Strategy: config.StrategyLeastCost,
				Targets: []config.TargetModel{
					{Provider: "expensive-fast", Model: "m1"},
					{Provider: "cheap-slow", Model: "m2"},
				},
			},
			"test-lowest-latency": {
				Strategy: config.StrategyLowestLatency,
				Targets: []config.TargetModel{
					{Provider: "cheap-slow", Model: "m2"},
					{Provider: "expensive-fast", Model: "m1"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Test least cost
	reqCost := &provider.ChatRequest{
		Model: "test-least-cost",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	candidates, strat, err := r.ResolveCandidates(reqCost)
	if err != nil {
		t.Fatalf("ResolveCandidates failed: %v", err)
	}
	if strat != config.StrategyLeastCost {
		t.Errorf("expected least-cost strategy, got %s", strat)
	}
	if candidates[0].ProviderName != "cheap-slow" {
		t.Errorf("expected cheap-slow to be ranked first, got %s", candidates[0].ProviderName)
	}

	// Test lowest latency
	r.latencyTracker.Record("expensive-fast", "m1", 50.0, 100.0)
	r.latencyTracker.Record("cheap-slow", "m2", 400.0, 800.0)

	reqLatency := &provider.ChatRequest{
		Model: "test-lowest-latency",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	candidatesLat, stratLat, err := r.ResolveCandidates(reqLatency)
	if err != nil {
		t.Fatalf("ResolveCandidates failed: %v", err)
	}
	if stratLat != config.StrategyLowestLatency {
		t.Errorf("expected lowest-latency strategy, got %s", stratLat)
	}
	if candidatesLat[0].ProviderName != "expensive-fast" {
		t.Errorf("expected expensive-fast to be ranked first due to low latency, got %s", candidatesLat[0].ProviderName)
	}
}

func TestRouter_FailoverCascade(t *testing.T) {
	// Primary upstream returns 429
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"rate_limited"}`, http.StatusTooManyRequests)
	}))
	defer primaryServer.Close()

	// Secondary upstream returns 200 OK
	secondaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{
			"id": "chatcmpl-backup-1",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "backup-model",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello from secondary provider!"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`)
	}))
	defer secondaryServer.Close()

	database, err := db.New(":memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{
		Routing: config.RoutingConfig{
			DefaultStrategy: config.StrategyPriority,
		},
		CircuitBreaker: config.CircuitBreakerConfig{
			FailureThreshold: 2,
			CooldownSeconds:  30,
		},
		Providers: []config.ProviderConfig{
			{
				Name:    "primary-p",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: primaryServer.URL,
				Enabled: true,
			},
			{
				Name:    "secondary-p",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: secondaryServer.URL,
				Enabled: true,
			},
		},
		Models: map[string]config.ModelRule{
			"resilient-model": {
				Strategy: config.StrategyPriority,
				Targets: []config.TargetModel{
					{Provider: "primary-p", Model: "primary-model"},
					{Provider: "secondary-p", Model: "backup-model"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, database)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	ctx := context.Background()
	req := &provider.ChatRequest{
		Model: "resilient-model",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Failover test"},
		},
	}

	result, err := r.Execute(ctx, req, "test-req-id")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Candidate.ProviderName != "secondary-p" {
		t.Errorf("expected routed provider to be secondary-p, got %s", result.Candidate.ProviderName)
	}
	if result.Response.Choices[0].Message.Content != "Hello from secondary provider!" {
		t.Errorf("unexpected content: %s", result.Response.Choices[0].Message.Content)
	}
	if len(result.FailoverTraces) != 1 {
		t.Fatalf("expected 1 failover trace, got %d", len(result.FailoverTraces))
	}
	trace := result.FailoverTraces[0]
	if trace.FromProvider != "primary-p" || trace.ToProvider != "secondary-p" {
		t.Errorf("unexpected trace from %s to %s", trace.FromProvider, trace.ToProvider)
	}

	// Verify failover trace in SQLite database
	dbTraces, err := database.GetRecentFailovers(ctx, 10)
	if err != nil {
		t.Fatalf("failed to query db failovers: %v", err)
	}
	if len(dbTraces) != 1 {
		t.Errorf("expected 1 db failover trace, got %d", len(dbTraces))
	}
}
