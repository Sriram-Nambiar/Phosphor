package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBreakerResetCommand_Online(t *testing.T) {
	var receivedProvider string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/circuit-breakers/reset" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedProvider = req["provider"]

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"message":  "circuit breaker reset",
			"provider": receivedProvider,
		})
	}))
	defer srv.Close()

	breakerURL = srv.URL
	breakerKeyFlag = "test-key"
	defer func() {
		breakerURL = ""
		breakerKeyFlag = ""
	}()

	// 1. Reset specific provider
	if err := runBreakerReset(nil, []string{"openai"}); err != nil {
		t.Fatalf("expected runBreakerReset to succeed: %v", err)
	}
	if receivedProvider != "openai" {
		t.Errorf("expected received provider 'openai', got %s", receivedProvider)
	}

	// 2. Reset default ("all")
	if err := runBreakerReset(nil, nil); err != nil {
		t.Fatalf("expected runBreakerReset with no args to succeed: %v", err)
	}
	if receivedProvider != "all" {
		t.Errorf("expected received provider 'all', got %s", receivedProvider)
	}
}

func TestBreakerResetCommand_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"provider_not_found"}`))
	}))
	defer srv.Close()

	breakerURL = srv.URL
	defer func() {
		breakerURL = ""
	}()

	err := runBreakerReset(nil, []string{"missing-p"})
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("expected error to mention HTTP 404, got: %v", err)
	}
}
