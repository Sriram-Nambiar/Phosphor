package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/auth"
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
			CORS: config.CORSConfig{
				Enabled:          true,
				AllowedOrigins:   []string{"*"},
				AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
				AllowedHeaders:   []string{"Content-Type", "Authorization", "x-api-key", "X-Request-ID"},
				AllowCredentials: true,
				MaxAgeSeconds:    86400,
			},
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

func TestServer_CORS(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	// 1. Default Preflight OPTIONS
	reqOpt := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	reqOpt.Header.Set("Origin", "https://client.example.com")
	rrOpt := httptest.NewRecorder()
	srv.ServeHTTP(rrOpt, reqOpt)

	if rrOpt.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content for OPTIONS, got %d", rrOpt.Code)
	}
	if rrOpt.Header().Get("Access-Control-Allow-Origin") != "https://client.example.com" {
		t.Errorf("expected Access-Control-Allow-Origin to match origin, got %s", rrOpt.Header().Get("Access-Control-Allow-Origin"))
	}
	if rrOpt.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("expected Access-Control-Allow-Credentials to be true")
	}
	if !strings.Contains(rrOpt.Header().Get("Access-Control-Allow-Methods"), "POST") {
		t.Errorf("expected POST in allowed methods, got %s", rrOpt.Header().Get("Access-Control-Allow-Methods"))
	}
	if rrOpt.Header().Get("Access-Control-Max-Age") != "86400" {
		t.Errorf("expected Max-Age 86400, got %s", rrOpt.Header().Get("Access-Control-Max-Age"))
	}

	// 2. Specific origin filtering
	srv.cfg.Server.CORS.AllowedOrigins = []string{"https://allowed.com"}
	srv.cfg.Server.CORS.AllowCredentials = false

	// Request from allowed origin
	reqAllowed := httptest.NewRequest(http.MethodGet, "/health", nil)
	reqAllowed.Header.Set("Origin", "https://allowed.com")
	rrAllowed := httptest.NewRecorder()
	srv.ServeHTTP(rrAllowed, reqAllowed)
	if rrAllowed.Header().Get("Access-Control-Allow-Origin") != "https://allowed.com" {
		t.Errorf("expected allowed origin to be reflected, got %s", rrAllowed.Header().Get("Access-Control-Allow-Origin"))
	}

	// Request from forbidden origin
	reqForbidden := httptest.NewRequest(http.MethodGet, "/health", nil)
	reqForbidden.Header.Set("Origin", "https://malicious.com")
	rrForbidden := httptest.NewRecorder()
	srv.ServeHTTP(rrForbidden, reqForbidden)
	if rrForbidden.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected no allow origin header for forbidden origin, got %s", rrForbidden.Header().Get("Access-Control-Allow-Origin"))
	}

	// 3. Disabled CORS
	srv.cfg.Server.CORS.Enabled = false
	reqDisabled := httptest.NewRequest(http.MethodOptions, "/health", nil)
	reqDisabled.Header.Set("Origin", "https://allowed.com")
	rrDisabled := httptest.NewRecorder()
	srv.ServeHTTP(rrDisabled, reqDisabled)
	if rrDisabled.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected no CORS headers when CORS disabled")
	}
}

func TestServer_Readiness(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	// 1. Initial healthy state
	reqReady := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rrReady := httptest.NewRecorder()
	srv.ServeHTTP(rrReady, reqReady)

	if rrReady.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for ready check, got %d: %s", rrReady.Code, rrReady.Body.String())
	}

	var readyResp map[string]any
	if err := json.Unmarshal(rrReady.Body.Bytes(), &readyResp); err != nil {
		t.Fatalf("failed to decode ready response: %v", err)
	}
	if readyResp["status"] != "ready" {
		t.Errorf("expected status ready, got %v", readyResp["status"])
	}
	if readyResp["database"] != "ok" {
		t.Errorf("expected database ok, got %v", readyResp["database"])
	}

	// 2. Method Not Allowed
	reqPost := httptest.NewRequest(http.MethodPost, "/ready", nil)
	rrPost := httptest.NewRecorder()
	srv.ServeHTTP(rrPost, reqPost)
	if rrPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", rrPost.Code)
	}

	// 3. Unhealthy when all circuits tripped
	cb, ok := srv.router.GetCircuitBreaker("mock-p")
	if !ok {
		t.Fatal("mock-p circuit breaker not found")
	}
	for i := 0; i < 5; i++ {
		cb.RecordFailure(context.DeadlineExceeded)
	}

	rrTripped := httptest.NewRecorder()
	srv.ServeHTTP(rrTripped, reqReady)
	if rrTripped.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 Service Unavailable when circuits tripped, got %d", rrTripped.Code)
	}

	var trippedResp map[string]any
	if err := json.Unmarshal(rrTripped.Body.Bytes(), &trippedResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if trippedResp["status"] != "not_ready" {
		t.Errorf("expected status not_ready, got %v", trippedResp["status"])
	}
}

func TestServer_GracefulShutdown(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	srv.server.Addr = "127.0.0.1:0"

	errChan := make(chan error, 1)
	go func() {
		err := srv.Start()
		if err != nil && err != http.ErrServerClosed {
			errChan <- err
		} else {
			errChan <- nil
		}
	}()

	// Give server a short moment to listen
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("expected clean shutdown, got %v", err)
	}

	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("server exited with unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server to stop")
	}
}

func TestServer_AuthMiddleware(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	srv.cfg.Auth = config.AuthConfig{
		Enabled: true,
		Keys: []config.APIKeyConfig{
			{
				Key:  "secret-test-key-123",
				Name: "test-client",
			},
		},
	}

	// 1. Health and ready are public without auth
	reqHealth := httptest.NewRequest(http.MethodGet, "/health", nil)
	rrHealth := httptest.NewRecorder()
	srv.ServeHTTP(rrHealth, reqHealth)
	if rrHealth.Code != http.StatusOK {
		t.Errorf("expected 200 OK for /health without auth, got %d", rrHealth.Code)
	}

	reqReady := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rrReady := httptest.NewRecorder()
	srv.ServeHTTP(rrReady, reqReady)
	if rrReady.Code != http.StatusOK {
		t.Errorf("expected 200 OK for /ready without auth, got %d", rrReady.Code)
	}

	// 2. Missing API key on protected endpoint (/v1/models)
	reqNoAuth := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rrNoAuth := httptest.NewRecorder()
	srv.ServeHTTP(rrNoAuth, reqNoAuth)
	if rrNoAuth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for missing API key, got %d", rrNoAuth.Code)
	}
	var errResp ErrorResponse
	if err := json.Unmarshal(rrNoAuth.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error.Code != "invalid_api_key" {
		t.Errorf("expected error code invalid_api_key, got %s", errResp.Error.Code)
	}

	// 3. Incorrect API key
	reqBadAuth := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	reqBadAuth.Header.Set("Authorization", "Bearer wrong-key")
	rrBadAuth := httptest.NewRecorder()
	srv.ServeHTTP(rrBadAuth, reqBadAuth)
	if rrBadAuth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for incorrect key, got %d", rrBadAuth.Code)
	}

	// 4. Valid API key via Bearer token
	reqValidBearer := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	reqValidBearer.Header.Set("Authorization", "Bearer secret-test-key-123")
	rrValidBearer := httptest.NewRecorder()
	srv.ServeHTTP(rrValidBearer, reqValidBearer)
	if rrValidBearer.Code != http.StatusOK {
		t.Errorf("expected 200 OK with valid bearer key, got %d", rrValidBearer.Code)
	}

	// 5. Valid API key via x-api-key header
	reqValidXKey := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	reqValidXKey.Header.Set("x-api-key", "secret-test-key-123")
	rrValidXKey := httptest.NewRecorder()
	srv.ServeHTTP(rrValidXKey, reqValidXKey)
	if rrValidXKey.Code != http.StatusOK {
		t.Errorf("expected 200 OK with valid x-api-key header, got %d", rrValidXKey.Code)
	}

	// 6. Valid API key configured via SHA-256 KeyHash
	srv.cfg.Auth.Keys = append(srv.cfg.Auth.Keys, config.APIKeyConfig{
		KeyHash: auth.HashKey("hashed-secret-token-456"),
		Name:    "hashed-client",
	})
	reqHashed := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	reqHashed.Header.Set("Authorization", "Bearer hashed-secret-token-456")
	rrHashed := httptest.NewRecorder()
	srv.ServeHTTP(rrHashed, reqHashed)
	if rrHashed.Code != http.StatusOK {
		t.Errorf("expected 200 OK with hashed secret token, got %d", rrHashed.Code)
	}
}



