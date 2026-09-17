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

	// Negative tokens must be clamped to zero and not produce negative costs
	negCost := CalculateCost(-100, -50, costCfg)
	if negCost != 0.0 {
		t.Errorf("expected 0.0 for negative tokens, got %f", negCost)
	}
	negEst := EstimateRequestCost(-10, costCfg)
	if negEst != 0.0 {
		t.Errorf("expected 0.0 for negative estimated tokens, got %f", negEst)
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

func TestRouter_ConcurrencyLimitAndFailover(t *testing.T) {
	primaryMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"p1","choices":[{"message":{"role":"assistant","content":"from-primary"}}]}`)
	}))
	defer primaryMock.Close()

	secondaryMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"p2","choices":[{"message":{"role":"assistant","content":"from-secondary"}}]}`)
	}))
	defer secondaryMock.Close()

	database, err := db.New(":memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{
		Routing: config.RoutingConfig{
			DefaultStrategy: config.StrategyPriority,
		},
		Providers: []config.ProviderConfig{
			{
				Name:           "primary-p",
				Type:           config.ProviderTypeOpenAI,
				BaseURL:        primaryMock.URL,
				Enabled:        true,
				MaxConcurrency: 1, // Only 1 concurrent request allowed!
			},
			{
				Name:    "secondary-p",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: secondaryMock.URL,
				Enabled: true,
			},
		},
		Models: map[string]config.ModelRule{
			"test-model": {
				Strategy: config.StrategyPriority,
				Targets: []config.TargetModel{
					{Provider: "primary-p", Model: "test-model"},
					{Provider: "secondary-p", Model: "test-model"},
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
		Model: "test-model",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	// 1. Manually hold the concurrency slot of primary-p
	release, acquired := r.TryAcquireConcurrency("primary-p")
	if !acquired {
		t.Fatal("expected to acquire initial concurrency slot on primary-p")
	}

	active, max := r.GetConcurrency("primary-p")
	if active != 1 || max != 1 {
		t.Errorf("expected active=1, max=1, got %d/%d", active, max)
	}

	// 2. Execute request while primary is saturated -> should failover to secondary-p!
	res1, err := r.Execute(ctx, req, "req-saturated")
	if err != nil {
		t.Fatalf("expected failover to succeed, got err: %v", err)
	}
	if res1.Candidate.ProviderName != "secondary-p" {
		t.Errorf("expected failover to secondary-p, got %s", res1.Candidate.ProviderName)
	}
	if len(res1.FailoverTraces) != 1 {
		t.Fatalf("expected 1 failover trace, got %d", len(res1.FailoverTraces))
	}
	if res1.FailoverTraces[0].FromProvider != "primary-p" || res1.FailoverTraces[0].ToProvider != "secondary-p" {
		t.Errorf("unexpected failover trace: %+v", res1.FailoverTraces[0])
	}

	// 3. Release primary slot and execute again -> should route to primary-p!
	release()
	activeAfter, _ := r.GetConcurrency("primary-p")
	if activeAfter != 0 {
		t.Errorf("expected active=0 after release, got %d", activeAfter)
	}

	res2, err := r.Execute(ctx, req, "req-released")
	if err != nil {
		t.Fatalf("expected request to succeed, got: %v", err)
	}
	if res2.Candidate.ProviderName != "primary-p" {
		t.Errorf("expected request to route to primary-p, got %s", res2.Candidate.ProviderName)
	}
}

func TestRouter_CircuitBreakerCooldownPenalty(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{DefaultStrategy: config.StrategyLeastCost},
		CircuitBreaker: config.CircuitBreakerConfig{
			FailureThreshold: 2,
			CooldownSeconds:  1,
		},
		Providers: []config.ProviderConfig{
			{
				Name:    "cheap-recovering",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: "http://localhost:8001",
				Enabled: true,
				Cost:    config.CostConfig{PromptCostPer1M: 1.0},
			},
			{
				Name:    "expensive-healthy",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: "http://localhost:8002",
				Enabled: true,
				Cost:    config.CostConfig{PromptCostPer1M: 20.0},
			},
		},
		Models: map[string]config.ModelRule{
			"model-test": {
				Strategy: config.StrategyLeastCost,
				Targets: []config.TargetModel{
					{Provider: "cheap-recovering", Model: "model-test"},
					{Provider: "expensive-healthy", Model: "model-test"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	cbCheap, _ := r.GetCircuitBreaker("cheap-recovering")
	mockNow := time.Now()
	cbCheap.nowFunc = func() time.Time { return mockNow }

	// Trip cheap-recovering into StateOpen
	rateErr := &provider.HTTPError{StatusCode: 429, Provider: "cheap-recovering"}
	cbCheap.RecordFailure(rateErr)
	cbCheap.RecordFailure(rateErr)
	if cbCheap.GetState() != StateOpen {
		t.Fatalf("expected cheap-recovering to be OPEN, got %s", cbCheap.GetState())
	}

	// Advance time past cooldown so cheap-recovering transitions to StateHalfOpen
	mockNow = mockNow.Add(2 * time.Second)
	if cbCheap.GetState() != StateHalfOpen {
		t.Fatalf("expected cheap-recovering to be HALF_OPEN, got %s", cbCheap.GetState())
	}

	// Resolve candidates: Even though cheap-recovering has lower cost, expensive-healthy is StateClosed
	// so expensive-healthy MUST be ranked first!
	req := &provider.ChatRequest{
		Model: "model-test",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "test prompt"},
		},
	}
	candidates, _, err := r.ResolveCandidates(req)
	if err != nil {
		t.Fatalf("failed to resolve candidates: %v", err)
	}

	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
	if candidates[0].ProviderName != "expensive-healthy" {
		t.Errorf("cooldown penalty expected healthy provider expensive-healthy first, got %s", candidates[0].ProviderName)
	}
	if candidates[1].ProviderName != "cheap-recovering" {
		t.Errorf("expected recovering provider cheap-recovering second, got %s", candidates[1].ProviderName)
	}

	// Once cheap-recovering records success and resets to StateClosed, it should regain first place by cost!
	cbCheap.RecordSuccess()
	if cbCheap.GetState() != StateClosed {
		t.Fatalf("expected cheap-recovering to be CLOSED after success, got %s", cbCheap.GetState())
	}

	candidatesRecovered, _, err := r.ResolveCandidates(req)
	if err != nil {
		t.Fatalf("failed to resolve candidates after recovery: %v", err)
	}
	if candidatesRecovered[0].ProviderName != "cheap-recovering" {
		t.Errorf("expected recovered cheap-recovering to rank first by cost, got %s", candidatesRecovered[0].ProviderName)
	}
}

func TestRouter_ResolveCandidates_NilRequest(t *testing.T) {
	cfg := &config.Config{}
	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	_, _, err = r.ResolveCandidates(nil)
	if err == nil {
		t.Fatal("expected error for nil request, got nil")
	}

	_, _, err = r.ResolveCandidatesWithContext(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request with context, got nil")
	}
}


