package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

func TestErrorClassifier_Taxonomy(t *testing.T) {
	tests := []struct {
		name             string
		err              error
		expectedCategory ErrorCategory
		shouldTrip       bool
		shouldFailFast   bool
		isRetryable      bool
	}{
		{
			name:             "HTTP 429 Rate Limit",
			err:              &provider.HTTPError{StatusCode: 429, Provider: "test"},
			expectedCategory: ErrorCategoryTransient,
			shouldTrip:       true,
			shouldFailFast:   false,
			isRetryable:      true,
		},
		{
			name:             "HTTP 503 Service Unavailable",
			err:              &provider.HTTPError{StatusCode: 503, Provider: "test"},
			expectedCategory: ErrorCategoryTransient,
			shouldTrip:       true,
			shouldFailFast:   false,
			isRetryable:      true,
		},
		{
			name:             "HTTP 400 Bad Request",
			err:              &provider.HTTPError{StatusCode: 400, Provider: "test"},
			expectedCategory: ErrorCategoryClient,
			shouldTrip:       false,
			shouldFailFast:   true,
			isRetryable:      false,
		},
		{
			name:             "HTTP 422 Unprocessable Entity",
			err:              &provider.HTTPError{StatusCode: 422, Provider: "test"},
			expectedCategory: ErrorCategoryClient,
			shouldTrip:       false,
			shouldFailFast:   true,
			isRetryable:      false,
		},
		{
			name:             "HTTP 401 Unauthorized",
			err:              &provider.HTTPError{StatusCode: 401, Provider: "test"},
			expectedCategory: ErrorCategoryAuth,
			shouldTrip:       false,
			shouldFailFast:   false,
			isRetryable:      true,
		},
		{
			name:             "HTTP 404 Model Not Found",
			err:              &provider.HTTPError{StatusCode: 404, Provider: "test"},
			expectedCategory: ErrorCategoryNotFound,
			shouldTrip:       false,
			shouldFailFast:   false,
			isRetryable:      true,
		},
		{
			name:             "Context Canceled",
			err:              context.Canceled,
			expectedCategory: ErrorCategoryCanceled,
			shouldTrip:       false,
			shouldFailFast:   false,
			isRetryable:      false,
		},
		{
			name:             "Deadline Exceeded Timeout",
			err:              context.DeadlineExceeded,
			expectedCategory: ErrorCategoryTransient,
			shouldTrip:       true,
			shouldFailFast:   false,
			isRetryable:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := ClassifyError(tt.err)
			if cat != tt.expectedCategory {
				t.Errorf("expected category %s, got %s", tt.expectedCategory, cat)
			}
			if ShouldTripBreaker(tt.err) != tt.shouldTrip {
				t.Errorf("expected ShouldTripBreaker=%v, got %v", tt.shouldTrip, ShouldTripBreaker(tt.err))
			}
			if ShouldFailFast(tt.err) != tt.shouldFailFast {
				t.Errorf("expected ShouldFailFast=%v, got %v", tt.shouldFailFast, ShouldFailFast(tt.err))
			}
			if IsRetryable(tt.err) != tt.isRetryable {
				t.Errorf("expected IsRetryable=%v, got %v", tt.isRetryable, IsRetryable(tt.err))
			}
		})
	}
}

func TestRouter_FailFastOnClientError(t *testing.T) {
	primaryHit := 0
	secondaryHit := 0

	// 1. Primary returns 400 Bad Request (client prompt error)
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHit++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"Prompt exceeds context window"}}`))
	}))
	defer primarySrv.Close()

	// 2. Secondary server should NEVER be hit for a 400 client error!
	secondarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondaryHit++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ok"}`))
	}))
	defer secondarySrv.Close()

	cfg := &config.Config{
		Routing: config.RoutingConfig{DefaultStrategy: config.StrategyPriority},
		Providers: []config.ProviderConfig{
			{Name: "p1", Type: config.ProviderTypeOpenAI, BaseURL: primarySrv.URL, Enabled: true},
			{Name: "p2", Type: config.ProviderTypeOpenAI, BaseURL: secondarySrv.URL, Enabled: true},
		},
		Models: map[string]config.ModelRule{
			"m-test": {
				Targets: []config.TargetModel{
					{Provider: "p1", Model: "m-test"},
					{Provider: "p2", Model: "m-test"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	req := &provider.ChatRequest{Model: "m-test"}
	_, execErr := r.Execute(context.Background(), req, "req-fail-fast")

	if execErr == nil {
		t.Fatal("expected error from 400 Bad Request, got nil")
	}
	var httpErr *provider.HTTPError
	if !errors.As(execErr, &httpErr) || httpErr.StatusCode != 400 {
		t.Errorf("expected *provider.HTTPError with status 400, got %v", execErr)
	}

	if primaryHit != 1 {
		t.Errorf("expected 1 hit on primary, got %d", primaryHit)
	}
	if secondaryHit != 0 {
		t.Errorf("expected 0 hits on secondary due to fail-fast, got %d", secondaryHit)
	}
}
