package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPingCommand_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":    "ready",
			"database":  "ok",
			"providers": map[string]string{"openai": "available", "groq": "available"},
		})
	}))
	defer srv.Close()

	pingURL = srv.URL
	pingCount = 2
	pingInterval = 10 * time.Millisecond
	pingTimeout = 1 * time.Second
	pingJSON = false
	defer func() {
		pingURL = ""
		pingCount = 1
		pingInterval = 1 * time.Second
		pingTimeout = 2 * time.Second
		pingJSON = false
	}()

	if err := runPing(nil, nil); err != nil {
		t.Fatalf("expected runPing to succeed: %v", err)
	}
}

func TestPingCommand_JSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "degraded",
			"database": "degraded: write errors",
		})
	}))
	defer srv.Close()

	pingURL = srv.URL
	pingCount = 1
	pingInterval = 0
	pingTimeout = 1 * time.Second
	pingJSON = true
	defer func() {
		pingURL = ""
		pingCount = 1
		pingJSON = false
	}()

	if err := runPing(nil, nil); err != nil {
		t.Fatalf("expected runPing JSON to succeed: %v", err)
	}
}

func TestPingCommand_Unreachable(t *testing.T) {
	pingURL = "http://127.0.0.1:1" // unreachable port
	pingCount = 1
	pingTimeout = 500 * time.Millisecond
	defer func() {
		pingURL = ""
		pingCount = 1
		pingTimeout = 2 * time.Second
	}()

	err := runPing(nil, nil)
	if err == nil {
		t.Fatal("expected runPing to return error for unreachable host, got nil")
	}
}

func TestPingCommand_TimeoutFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	defer srv.Close()

	pingURL = srv.URL
	pingCount = 1
	pingTimeout = -1 * time.Second // Should fall back to 2s
	pingInterval = -1 * time.Second // Should fall back to 0
	defer func() {
		pingURL = ""
		pingCount = 1
		pingTimeout = 2 * time.Second
		pingInterval = 1 * time.Second
	}()

	if err := runPing(nil, nil); err != nil {
		t.Fatalf("expected runPing with negative timeout to succeed with fallback: %v", err)
	}
}

func TestPingCommand_ComputeSummary(t *testing.T) {
	results := []PingResult{
		{Seq: 1, StatusCode: 200, LatencyMs: 10.0},
		{Seq: 2, StatusCode: 200, LatencyMs: 30.0},
		{Seq: 3, StatusCode: 200, LatencyMs: 20.0},
	}
	latencies := []float64{10.0, 30.0, 20.0}

	summary := computePingSummary("http://localhost:8080", results, latencies)

	if summary.Transmitted != 3 {
		t.Errorf("expected 3 transmitted, got %d", summary.Transmitted)
	}
	if summary.Received != 3 {
		t.Errorf("expected 3 received, got %d", summary.Received)
	}
	if summary.LossPercent != 0.0 {
		t.Errorf("expected 0%% loss, got %.1f", summary.LossPercent)
	}
	if summary.MinMs != 10.0 {
		t.Errorf("expected min 10ms, got %.2f", summary.MinMs)
	}
	if summary.MaxMs != 30.0 {
		t.Errorf("expected max 30ms, got %.2f", summary.MaxMs)
	}
	if summary.AvgMs != 20.0 {
		t.Errorf("expected avg 20ms, got %.2f", summary.AvgMs)
	}
}
