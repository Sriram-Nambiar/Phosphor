package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

func TestRouter_CrossFamilyFallbackModelGroups(t *testing.T) {
	// 1. Primary server (OpenAI gpt-4o) fails with 500
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"Internal outage"}}`))
	}))
	defer primarySrv.Close()

	// 2. Fallback server (Anthropic claude-3-5-sonnet) succeeds
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "chatcmpl-fallback",
			"object": "chat.completion",
			"created": 123456789,
			"model": "claude-3-5-sonnet",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello from Claude fallback!"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 15, "total_tokens": 25}
		}`))
	}))
	defer fallbackSrv.Close()

	cfg := &config.Config{
		Routing: config.RoutingConfig{DefaultStrategy: config.StrategyPriority},
		Providers: []config.ProviderConfig{
			{
				Name:    "openai-primary",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: primarySrv.URL,
				Enabled: true,
			},
			{
				Name:    "anthropic-fallback",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: fallbackSrv.URL,
				Enabled: true,
			},
		},
		Models: map[string]config.ModelRule{
			"gpt-4o": {
				Strategy: config.StrategyPriority,
				Targets: []config.TargetModel{
					{Provider: "openai-primary", Model: "gpt-4o"},
				},
				Fallbacks: []string{"claude-3-5-sonnet"},
			},
			"claude-3-5-sonnet": {
				Strategy: config.StrategyPriority,
				Targets: []config.TargetModel{
					{Provider: "anthropic-fallback", Model: "claude-3-5-sonnet"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	req := &provider.ChatRequest{
		Model: "gpt-4o",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Hello gateway"},
		},
	}

	// Verify candidate list has primary gpt-4o followed by fallback claude-3-5-sonnet
	cands, _, err := r.ResolveCandidates(req)
	if err != nil {
		t.Fatalf("failed to resolve candidates: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cands))
	}
	if cands[0].ProviderName != "openai-primary" || cands[0].Model != "gpt-4o" {
		t.Errorf("expected primary candidate openai-primary/gpt-4o, got %+v", cands[0])
	}
	if cands[1].ProviderName != "anthropic-fallback" || cands[1].Model != "claude-3-5-sonnet" {
		t.Errorf("expected fallback candidate anthropic-fallback/claude-3-5-sonnet, got %+v", cands[1])
	}

	// Execute request -> primary fails, automatically fails over to fallback!
	res, err := r.Execute(context.Background(), req, "req-fallback-test")
	if err != nil {
		t.Fatalf("expected failover to succeed, got error: %v", err)
	}

	if res.Candidate.ProviderName != "anthropic-fallback" {
		t.Errorf("expected successful execution on anthropic-fallback, got %s", res.Candidate.ProviderName)
	}
	if res.Response == nil || len(res.Response.Choices) == 0 {
		t.Fatal("expected non-empty choices from fallback response")
	}
	if res.Response.Choices[0].Message.Content != "Hello from Claude fallback!" {
		t.Errorf("unexpected response content: %s", res.Response.Choices[0].Message.Content)
	}
	if len(res.FailoverTraces) != 1 {
		t.Fatalf("expected 1 failover trace, got %d", len(res.FailoverTraces))
	}
	if res.FailoverTraces[0].FromProvider != "openai-primary" || res.FailoverTraces[0].ToProvider != "anthropic-fallback" {
		t.Errorf("unexpected failover trace: %+v", res.FailoverTraces[0])
	}
}

func TestRouter_TargetTimeoutFailover(t *testing.T) {
	// Slow primary server takes 300ms
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(300 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"Slow response"}}]}`))
		}
	}))
	defer slowSrv.Close()

	// Fast secondary server returns immediately
	fastSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"Fast fallback response"}}]}`))
	}))
	defer fastSrv.Close()

	cfg := &config.Config{
		Routing: config.RoutingConfig{DefaultStrategy: config.StrategyPriority},
		Providers: []config.ProviderConfig{
			{
				Name:    "slow-primary",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: slowSrv.URL,
				Enabled: true,
			},
			{
				Name:    "fast-fallback",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: fastSrv.URL,
				Enabled: true,
			},
		},
		Models: map[string]config.ModelRule{
			"gpt-4o": {
				Strategy: config.StrategyPriority,
				Targets: []config.TargetModel{
					{
						Provider:  "slow-primary",
						Model:     "gpt-4o",
						TimeoutMs: 60,
					},
					{
						Provider: "fast-fallback",
						Model:    "gpt-4o",
					},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	req := &provider.ChatRequest{
		Model: "gpt-4o",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Hello gateway"},
		},
	}

	res, err := r.Execute(context.Background(), req, "req-timeout-test")
	if err != nil {
		t.Fatalf("expected failover to succeed after timeout, got err: %v", err)
	}

	if res.Candidate.ProviderName != "fast-fallback" {
		t.Errorf("expected failover to fast-fallback, got %s", res.Candidate.ProviderName)
	}
	if len(res.Response.Choices) == 0 || res.Response.Choices[0].Message.Content != "Fast fallback response" {
		t.Errorf("unexpected response content: %+v", res.Response)
	}
}

func TestRouter_RetryBudgetExhaustion(t *testing.T) {
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":{"message":"Bad Gateway"}}`))
	}))
	defer primarySrv.Close()

	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-ok","object":"chat.completion","created":123,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"Should not be reached"},"finish_reason":"stop"}]}`))
	}))
	defer fallbackSrv.Close()

	cfg := &config.Config{
		Routing: config.RoutingConfig{
			DefaultStrategy:  config.StrategyPriority,
			RetryBudgetRatio: 0.2,
		},
		Providers: []config.ProviderConfig{
			{Name: "p1", Type: config.ProviderTypeOpenAI, BaseURL: primarySrv.URL, Enabled: true},
			{Name: "p2", Type: config.ProviderTypeOpenAI, BaseURL: fallbackSrv.URL, Enabled: true},
		},
		Models: map[string]config.ModelRule{
			"gpt-4o": {
				Targets: []config.TargetModel{
					{Provider: "p1", Model: "gpt-4o"},
					{Provider: "p2", Model: "gpt-4o"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Override retry budget with an exhausted budget (minRetries 0, no regular requests)
	budget := &RetryBudget{
		ratio:      0.0,
		minRetries: 0,
		window:     10 * time.Second,
		lastWindow: time.Now(),
	}
	r.SetRetryBudget(budget)

	req := &provider.ChatRequest{
		Model: "gpt-4o",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	_, err = r.Execute(context.Background(), req, "req-budget-test")
	if err == nil {
		t.Fatal("expected error due to retry budget exhaustion, got nil")
	}

	if !strings.Contains(err.Error(), "retry budget exhausted") {
		t.Errorf("expected 'retry budget exhausted' error message, got: %v", err)
	}
}

