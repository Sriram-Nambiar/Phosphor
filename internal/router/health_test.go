package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
)

func TestHealthChecker_ProbingAndBreakerRecovery(t *testing.T) {
	// Server 1: healthy (200 OK)
	serverHealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer serverHealthy.Close()

	// Server 2: failing (503 Service Unavailable)
	serverFailing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer serverFailing.Close()

	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{
				Name:    "healthy-p",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: serverHealthy.URL,
				Enabled: true,
				Models:  []string{"m1"},
			},
			{
				Name:    "failing-p",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: serverFailing.URL,
				Enabled: true,
				Models:  []string{"m2"},
			},
		},
		CircuitBreaker: config.CircuitBreakerConfig{
			FailureThreshold: 2,
			CooldownSeconds:  1,
		},
	}

	database, err := db.New(":memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer database.Close()

	r, err := NewRouter(cfg, database)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Artificially trip the circuit breaker on healthy-p to HalfOpen
	cb, _ := r.GetCircuitBreaker("healthy-p")
	cb.state = StateHalfOpen

	hc := NewHealthChecker(r, 100*time.Millisecond)

	// Execute explicit probe
	ctx := context.Background()
	hc.ProbeAll(ctx)

	if !hc.IsHealthy("healthy-p") {
		t.Errorf("expected healthy-p to be healthy")
	}
	if hc.IsHealthy("failing-p") {
		t.Errorf("expected failing-p to be unhealthy")
	}

	// Verify breaker on healthy-p recovered to StateClosed
	if cb.GetState() != StateClosed {
		t.Errorf("expected circuit breaker on healthy-p to recover to StateClosed, got %v", cb.GetState())
	}

	// Verify ProviderStatuses reflects unreachable status
	statuses := r.ProviderStatuses()
	// Since r.healthChecker was not assigned to r.healthChecker yet, let's start it on router
	r.healthChecker = hc
	statuses = r.ProviderStatuses()
	if statuses["failing-p"] != "unreachable" {
		t.Errorf("expected failing-p status to be unreachable, got %s", statuses["failing-p"])
	}

	// Test background loop Start and Stop
	hc.Start()
	time.Sleep(50 * time.Millisecond)
	hc.Stop()
}
