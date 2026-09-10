package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
	"github.com/Sriram-Nambiar/Phosphor/internal/router"
)

func setupTestServer(t *testing.T, upstreamHandler http.HandlerFunc) (*Server, *db.DB, *httptest.Server) {
	mockUpstream := httptest.NewServer(upstreamHandler)

	database, err := db.New(":memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 8080,
		},
		Routing: config.RoutingConfig{
			DefaultStrategy: config.StrategyPriority,
		},
		CircuitBreaker: config.CircuitBreakerConfig{
			FailureThreshold: 3,
			CooldownSeconds:  30,
		},
		Providers: []config.ProviderConfig{
			{
				Name:    "mock-p",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: mockUpstream.URL,
				Enabled: true,
				Models:  []string{"gpt-4o"},
				Cost: config.CostConfig{
					PromptCostPer1M:     2.0,
					CompletionCostPer1M: 10.0,
				},
			},
		},
		Models: map[string]config.ModelRule{
			"gpt-4o": {
				Strategy: config.StrategyPriority,
				Targets: []config.TargetModel{
					{Provider: "mock-p", Model: "gpt-4o"},
				},
			},
		},
	}

	r, err := router.NewRouter(cfg, database)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	server := NewServer(cfg, r, database)
	return server, database, mockUpstream
}

func TestServer_HealthAndModels(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	// 1. Health check
	reqHealth := httptest.NewRequest(http.MethodGet, "/health", nil)
	rrHealth := httptest.NewRecorder()
	srv.ServeHTTP(rrHealth, reqHealth)

	if rrHealth.Code != http.StatusOK {
		t.Errorf("health endpoint returned %d", rrHealth.Code)
	}

	// 2. Models list
	reqModels := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rrModels := httptest.NewRecorder()
	srv.ServeHTTP(rrModels, reqModels)

	if rrModels.Code != http.StatusOK {
		t.Errorf("models endpoint returned %d", rrModels.Code)
	}

	var modelList ModelListResponse
	if err := json.NewDecoder(rrModels.Body).Decode(&modelList); err != nil {
		t.Fatalf("failed to decode models response: %v", err)
	}
	if len(modelList.Data) == 0 || modelList.Data[0].ID != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %+v", modelList.Data)
	}
}

func TestServer_ChatCompletions_NonStreaming(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{
			"id": "chatcmpl-mock-99",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gpt-4o",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Phosphor response"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20}
		}`)
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp provider.ChatResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Choices[0].Message.Content != "Phosphor response" {
		t.Errorf("unexpected content: %s", resp.Choices[0].Message.Content)
	}

	// Verify database record
	stats, err := dbInstance.GetAggregateStats(req.Context())
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if stats.TotalRequests != 1 {
		t.Errorf("expected 1 request logged in DB, got %d", stats.TotalRequests)
	}
}

func TestServer_ChatCompletions_Streaming(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		chunks := []string{
			`data: {"id":"chatcmpl-s1","choices":[{"delta":{"role":"assistant","content":"Streamed "}}]}`,
			`data: {"id":"chatcmpl-s1","choices":[{"delta":{"content":"chunks"}}]}`,
			`data: [DONE]`,
		}

		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			flusher.Flush()
			time.Sleep(15 * time.Millisecond)
		}
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	reqBody := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"Stream test"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}

	contentType := rr.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("expected text/event-stream, got %s", contentType)
	}

	bodyStr := rr.Body.String()
	if !strings.Contains(bodyStr, "Streamed ") || !strings.Contains(bodyStr, "data: [DONE]") {
		t.Errorf("missing expected streamed tokens in body: %s", bodyStr)
	}

	// Verify scanner can parse lines
	scanner := bufio.NewScanner(strings.NewReader(bodyStr))
	var dataLinesCount int
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			dataLinesCount++
		}
	}
	if dataLinesCount < 3 {
		t.Errorf("expected at least 3 data lines, got %d", dataLinesCount)
	}

	// Verify database record has TTFT and stream = true
	recent, err := dbInstance.GetRecentRequests(req.Context(), 10)
	if err != nil {
		t.Fatalf("failed to query db: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("expected 1 logged request, got %d", len(recent))
	}
	if !recent[0].Stream {
		t.Error("expected logged request Stream to be true")
	}
	if recent[0].TTFTMs <= 0 {
		t.Errorf("expected positive TTFT recorded, got %f", recent[0].TTFTMs)
	}
}

func TestServer_RequestBodySizeLimit(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	// Set 50 byte limit for test
	srv.cfg.Server.MaxRequestBodyBytes = 50

	largeBody := strings.Repeat("A", 100)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected HTTP 413, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestServer_RequestIDMiddleware(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"up-1","object":"chat.completion","choices":[{"message":{"content":"ok"}}]}`)
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	// 1. Custom client X-Request-ID
	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "custom-client-req-99")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	respReqID := rr.Header().Get("X-Request-ID")
	if respReqID != "custom-client-req-99" {
		t.Errorf("expected X-Request-ID custom-client-req-99, got %s", respReqID)
	}

	recent, err := dbInstance.GetRecentRequests(req.Context(), 1)
	if err != nil || len(recent) == 0 {
		t.Fatalf("failed to get request from db: %v", err)
	}
	if recent[0].ID != "custom-client-req-99" {
		t.Errorf("expected DB request ID custom-client-req-99, got %s", recent[0].ID)
	}

	// 2. Auto-generated X-Request-ID
	req2 := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)

	respReqID2 := rr2.Header().Get("X-Request-ID")
	if !strings.HasPrefix(respReqID2, "chatcmpl-") {
		t.Errorf("expected auto-generated request ID starting with chatcmpl-, got %s", respReqID2)
	}
}

func TestServer_StandardizedOpenAIErrors(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	// 1. Method Not Allowed
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", rr.Code)
	}

	var errResp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to unmarshal standardized error response: %v", err)
	}
	if errResp.Error.Type != "invalid_request_error" {
		t.Errorf("expected error type invalid_request_error, got %s", errResp.Error.Type)
	}
	if errResp.Error.Code != "method_not_allowed" {
		t.Errorf("expected error code method_not_allowed, got %s", errResp.Error.Code)
	}

	// 2. Invalid JSON
	reqBadJSON := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{invalid-json"))
	reqBadJSON.Header.Set("Content-Type", "application/json")
	rrBadJSON := httptest.NewRecorder()
	srv.ServeHTTP(rrBadJSON, reqBadJSON)

	if rrBadJSON.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rrBadJSON.Code)
	}
	var errBadJSON ErrorResponse
	if err := json.Unmarshal(rrBadJSON.Body.Bytes(), &errBadJSON); err != nil {
		t.Fatalf("failed to unmarshal JSON error response: %v", err)
	}
	if errBadJSON.Error.Code != "invalid_json" {
		t.Errorf("expected code invalid_json, got %s", errBadJSON.Error.Code)
	}
}
