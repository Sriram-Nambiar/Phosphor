package test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/Sriram-Nambiar/Phosphor/internal/proxy"
	"github.com/Sriram-Nambiar/Phosphor/internal/router"
)

func TestGateway_ConcurrentStressAndFailover(t *testing.T) {
	var primaryHits atomic.Uint64
	var secondaryHits atomic.Uint64

	// Primary mock: fails intermittently with 429 Too Many Requests (transient failure)
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits := primaryHits.Add(1)
		if hits%2 == 1 {
			http.Error(w, `{"error":{"message":"Rate limit exceeded on primary"}}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{
			"id": "chatcmpl-primary",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gpt-4o",
			"choices": [{"index":0,"message":{"role":"assistant","content":"Primary response"},"finish_reason":"stop"}],
			"usage": {"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`)
	}))
	defer primaryServer.Close()

	// Secondary mock: consistently succeeds
	secondaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondaryHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{
			"id": "chatcmpl-secondary",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gpt-4o-secondary",
			"choices": [{"index":0,"message":{"role":"assistant","content":"Secondary response"},"finish_reason":"stop"}],
			"usage": {"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`)
	}))
	defer secondaryServer.Close()

	database, err := db.New(":memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 8080,
			CORS: config.CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"*"},
			},
		},
		Routing: config.RoutingConfig{
			DefaultStrategy: config.StrategyPriority,
			TimeoutSeconds:  5,
		},
		CircuitBreaker: config.CircuitBreakerConfig{
			FailureThreshold: 5,
			CooldownSeconds:  1,
		},
		Providers: []config.ProviderConfig{
			{
				Name:    "primary-p",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: primaryServer.URL,
				Enabled: true,
				Models:  []string{"gpt-4o"},
				Cost: config.CostConfig{
					PromptCostPer1M:     2.50,
					CompletionCostPer1M: 10.00,
				},
			},
			{
				Name:    "secondary-p",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: secondaryServer.URL,
				Enabled: true,
				Models:  []string{"gpt-4o-secondary"},
				Cost: config.CostConfig{
					PromptCostPer1M:     1.50,
					CompletionCostPer1M: 5.00,
				},
			},
		},
		Models: map[string]config.ModelRule{
			"gpt-4o": {
				Strategy: config.StrategyPriority,
				Targets: []config.TargetModel{
					{Provider: "primary-p", Model: "gpt-4o"},
					{Provider: "secondary-p", Model: "gpt-4o-secondary"},
				},
			},
		},
	}

	r, err := router.NewRouter(cfg, database)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	srv := proxy.NewServer(cfg, r, database)

	// Launch concurrent worker clients
	const numWorkers = 8
	const requestsPerWorker = 10
	var totalSuccess atomic.Uint64
	var totalErrors atomic.Uint64

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < requestsPerWorker; j++ {
				reqBody := fmt.Sprintf(`{"model":"gpt-4o","messages":[{"role":"user","content":"Stress test %d-%d"}]}`, workerID, j)
				httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
				httpReq.Header.Set("Content-Type", "application/json")

				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, httpReq)

				if rec.Code == http.StatusOK {
					totalSuccess.Add(1)
				} else {
					totalErrors.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()

	totalExpected := uint64(numWorkers * requestsPerWorker)
	if totalSuccess.Load() != totalExpected {
		t.Errorf("expected all %d requests to succeed through failover, got %d successes and %d errors",
			totalExpected, totalSuccess.Load(), totalErrors.Load())
	}

	// Verify Prometheus metrics reflect load
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	srv.ServeHTTP(metricsRec, metricsReq)

	if metricsRec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for /metrics, got %d", metricsRec.Code)
	}

	// Verify database persistence
	stats, err := database.GetAggregateStats(context.Background())
	if err != nil {
		t.Fatalf("failed to get aggregate stats: %v", err)
	}
	if stats.TotalRequests != int(totalExpected) {
		t.Errorf("expected %d total requests logged in DB, got %d", totalExpected, stats.TotalRequests)
	}
	if stats.TotalFailovers == 0 {
		t.Errorf("expected failovers to be logged in DB")
	}
}
