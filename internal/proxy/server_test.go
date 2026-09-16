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
	"github.com/Sriram-Nambiar/Phosphor/internal/cache"
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

func TestServer_ModelAccessControl(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-mock",
			"choices": []any{},
		})
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	// Add an additional model to config
	srv.cfg.Models["gpt-4o-mini"] = config.ModelRule{
		Strategy: config.StrategyPriority,
		Targets: []config.TargetModel{
			{Provider: "mock-p", Model: "gpt-4o-mini"},
		},
	}

	// Client has permission ONLY for gpt-4o-mini
	srv.cfg.Auth = config.AuthConfig{
		Enabled: true,
		Keys: []config.APIKeyConfig{
			{
				Key:           "dev-client-token",
				Name:          "dev-client",
				AllowedModels: []string{"gpt-4o-mini"},
			},
		},
	}

	// 1. GET /v1/models should only list gpt-4o-mini for this client
	reqModels := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	reqModels.Header.Set("Authorization", "Bearer dev-client-token")
	rrModels := httptest.NewRecorder()
	srv.ServeHTTP(rrModels, reqModels)

	if rrModels.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rrModels.Code)
	}
	var modelList ModelListResponse
	if err := json.Unmarshal(rrModels.Body.Bytes(), &modelList); err != nil {
		t.Fatalf("failed to decode models: %v", err)
	}
	if len(modelList.Data) != 1 || modelList.Data[0].ID != "gpt-4o-mini" {
		t.Errorf("expected only gpt-4o-mini, got %+v", modelList.Data)
	}

	// 2. POST /v1/chat/completions requesting forbidden model (gpt-4o)
	reqForbidden := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	reqForbidden.Header.Set("Authorization", "Bearer dev-client-token")
	reqForbidden.Header.Set("Content-Type", "application/json")
	rrForbidden := httptest.NewRecorder()
	srv.ServeHTTP(rrForbidden, reqForbidden)

	if rrForbidden.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d: %s", rrForbidden.Code, rrForbidden.Body.String())
	}
	var errResp ErrorResponse
	if err := json.Unmarshal(rrForbidden.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error: %v", err)
	}
	if errResp.Error.Code != "model_access_denied" {
		t.Errorf("expected error code model_access_denied, got %s", errResp.Error.Code)
	}

	// 3. POST /v1/chat/completions requesting permitted model (gpt-4o-mini)
	reqPermitted := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	reqPermitted.Header.Set("Authorization", "Bearer dev-client-token")
	reqPermitted.Header.Set("Content-Type", "application/json")
	rrPermitted := httptest.NewRecorder()
	srv.ServeHTTP(rrPermitted, reqPermitted)

	if rrPermitted.Code != http.StatusOK {
		t.Errorf("expected 200 OK for permitted model, got %d: %s", rrPermitted.Code, rrPermitted.Body.String())
	}
}

func TestServer_RateLimiting(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "test"})
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	srv.cfg.Auth = config.AuthConfig{
		Enabled: true,
		Keys: []config.APIKeyConfig{
			{
				Key:       "limited-client-token",
				Name:      "limited-client",
				RateLimit: 2, // 2 RPM
			},
		},
	}

	// 1. First request -> Allowed
	req1 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req1.Header.Set("Authorization", "Bearer limited-client-token")
	rr1 := httptest.NewRecorder()
	srv.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Errorf("expected 200 OK for req 1, got %d", rr1.Code)
	}
	if rr1.Header().Get("X-RateLimit-Limit") != "2" {
		t.Errorf("expected Limit 2, got %s", rr1.Header().Get("X-RateLimit-Limit"))
	}
	if rr1.Header().Get("X-RateLimit-Remaining") != "1" {
		t.Errorf("expected Remaining 1, got %s", rr1.Header().Get("X-RateLimit-Remaining"))
	}

	// 2. Second request -> Allowed
	req2 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req2.Header.Set("Authorization", "Bearer limited-client-token")
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Errorf("expected 200 OK for req 2, got %d", rr2.Code)
	}
	if rr2.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Errorf("expected Remaining 0, got %s", rr2.Header().Get("X-RateLimit-Remaining"))
	}

	// 3. Third request immediately -> Throttled with 429
	req3 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req3.Header.Set("Authorization", "Bearer limited-client-token")
	rr3 := httptest.NewRecorder()
	srv.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 Too Many Requests for req 3, got %d", rr3.Code)
	}
	if rr3.Header().Get("Retry-After") == "" {
		t.Errorf("expected Retry-After header on 429 response")
	}
	var errResp ErrorResponse
	if err := json.Unmarshal(rr3.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error.Code != "rate_limit_exceeded" {
		t.Errorf("expected code rate_limit_exceeded, got %s", errResp.Error.Code)
	}

	// 4. Public endpoint (/health) is never throttled
	for i := 0; i < 5; i++ {
		reqH := httptest.NewRequest(http.MethodGet, "/health", nil)
		rrH := httptest.NewRecorder()
		srv.ServeHTTP(rrH, reqH)
		if rrH.Code != http.StatusOK {
			t.Errorf("expected /health to never be rate-limited, got %d on attempt %d", rrH.Code, i+1)
		}
	}
}

func TestServer_ChatCompletions_Streaming_MidStreamError(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		// 1. Send good chunk
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-err1\",\"choices\":[{\"delta\":{\"content\":\"Halfway done\"}}]}\n\n")
		flusher.Flush()

		// 2. Send mid-stream error frame
		fmt.Fprintf(w, "data: {\"error\":{\"message\":\"Midstream token generation failed\",\"type\":\"server_error\"}}\n\n")
		flusher.Flush()
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	reqBody := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"Stream error test"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK headers initially, got %d: %s", rr.Code, rr.Body.String())
	}

	bodyStr := rr.Body.String()
	if !strings.Contains(bodyStr, "Halfway done") {
		t.Errorf("expected body to contain initial chunk 'Halfway done', got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "stream_interrupted") || !strings.Contains(bodyStr, "Midstream token generation failed") {
		t.Errorf("expected body to contain SSE error event with stream_interrupted, got: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "data: [DONE]") {
		t.Errorf("expected body NOT to contain [DONE] on mid-stream error, got: %s", bodyStr)
	}

	// Verify database record has 502 status code and error message
	recent, err := dbInstance.GetRecentRequests(req.Context(), 10)
	if err != nil {
		t.Fatalf("failed to query db: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("expected 1 logged request, got %d", len(recent))
	}
	if recent[0].StatusCode != http.StatusBadGateway {
		t.Errorf("expected StatusCode %d, got %d", http.StatusBadGateway, recent[0].StatusCode)
	}
	if !strings.Contains(recent[0].ErrorMsg, "Midstream token generation failed") {
		t.Errorf("expected ErrorMsg to contain upstream failure, got: %s", recent[0].ErrorMsg)
	}
}

func TestServer_ChatCompletions_Streaming_ClientDisconnect(t *testing.T) {
	upstreamHit := make(chan struct{})
	upstreamDone := make(chan struct{})

	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		// Send one chunk
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-cancel1\",\"choices\":[{\"delta\":{\"content\":\"First chunk\"}}]}\n\n")
		flusher.Flush()
		close(upstreamHit)

		// Wait for context cancellation or timeout
		select {
		case <-r.Context().Done():
			close(upstreamDone)
			return
		case <-time.After(3 * time.Second):
			close(upstreamDone)
			return
		}
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	reqBody := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"Cancel stream test"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	// Cancel context as soon as upstream sends first chunk
	go func() {
		<-upstreamHit
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	// Wait to verify upstream also received the cancellation
	select {
	case <-upstreamDone:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive cancellation in time")
	}

	// Verify database record has 499 status code
	recent, err := dbInstance.GetRecentRequests(context.Background(), 10)
	if err != nil {
		t.Fatalf("failed to query db: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("expected 1 logged request, got %d", len(recent))
	}
	if recent[0].StatusCode != 499 {
		t.Errorf("expected StatusCode 499 for client cancel, got %d", recent[0].StatusCode)
	}
	if !strings.Contains(recent[0].ErrorMsg, "client aborted") {
		t.Errorf("expected ErrorMsg to contain 'client aborted', got: %s", recent[0].ErrorMsg)
	}
}

func TestServer_ChatCompletions_Streaming_UsageAndFinishReason(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		chunks := []string{
			`data: {"id":"chatcmpl-u1","choices":[{"index":0,"delta":{"content":"Hello world!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":15,"completion_tokens":5,"total_tokens":20}}`,
			`data: [DONE]`,
		}

		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			flusher.Flush()
		}
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	reqBody := `{"model":"gpt-4o","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}

	bodyStr := rr.Body.String()
	if !strings.Contains(bodyStr, `"prompt_tokens":15`) || !strings.Contains(bodyStr, `"completion_tokens":5`) {
		t.Errorf("expected usage chunk in stream body, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "data: [DONE]") {
		t.Errorf("expected stream to end with [DONE], got: %s", bodyStr)
	}

	// Verify database record has exact usage tokens
	recent, err := dbInstance.GetRecentRequests(context.Background(), 10)
	if err != nil {
		t.Fatalf("failed to query db: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("expected 1 logged request, got %d", len(recent))
	}
	if recent[0].PromptTokens != 15 || recent[0].CompletionTokens != 5 || recent[0].TotalTokens != 20 {
		t.Errorf("expected (15, 5, 20) tokens in DB, got (%d, %d, %d)",
			recent[0].PromptTokens, recent[0].CompletionTokens, recent[0].TotalTokens)
	}
}

func TestServer_ChatCompletions_Streaming_IdleTimeout(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		// 1. Send first chunk
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-to1\",\"choices\":[{\"delta\":{\"content\":\"Chunk before stall\"}}]}\n\n")
		flusher.Flush()

		// 2. Stall longer than idle timeout
		time.Sleep(200 * time.Millisecond)

		// 3. Send chunk after timeout (should not be processed)
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-to1\",\"choices\":[{\"delta\":{\"content\":\"Late chunk\"}}]}\n\n")
		flusher.Flush()
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	// Set short idle timeout
	srv.cfg.Server.StreamIdleTimeout = 40 * time.Millisecond

	reqBody := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"Timeout test"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK headers, got %d", rr.Code)
	}

	bodyStr := rr.Body.String()
	if !strings.Contains(bodyStr, "Chunk before stall") {
		t.Errorf("expected body to contain chunk before stall, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "stream_idle_timeout") {
		t.Errorf("expected body to contain stream_idle_timeout error event, got: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "Late chunk") {
		t.Errorf("expected body NOT to contain late chunk, got: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "data: [DONE]") {
		t.Errorf("expected body NOT to contain [DONE] after idle timeout, got: %s", bodyStr)
	}

	// Verify database record has 504 Gateway Timeout status code
	recent, err := dbInstance.GetRecentRequests(context.Background(), 10)
	if err != nil {
		t.Fatalf("failed to query db: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("expected 1 logged request, got %d", len(recent))
	}
	if recent[0].StatusCode != http.StatusGatewayTimeout {
		t.Errorf("expected StatusCode %d, got %d", http.StatusGatewayTimeout, recent[0].StatusCode)
	}
	if !strings.Contains(recent[0].ErrorMsg, "idle timeout exceeded") {
		t.Errorf("expected ErrorMsg to contain idle timeout, got: %s", recent[0].ErrorMsg)
	}
}

func TestServer_MetricsEndpoint(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"cmpl-m","choices":[{"message":{"content":"ok"}}]}`)
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	// 1. Initial GET /metrics
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /metrics, got %d: %s", rr.Code, rr.Body.String())
	}
	contentType := rr.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") || !strings.Contains(contentType, "version=0.0.4") {
		t.Errorf("unexpected metrics content type: %s", contentType)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "phosphor_up 1") {
		t.Errorf("expected phosphor_up 1 in metrics")
	}
	if !strings.Contains(body, "phosphor_circuit_breaker_state{provider=\"mock-p\"} 0") {
		t.Errorf("expected circuit breaker state in metrics: %s", body)
	}

	// 2. POST /metrics should return 405 Method Not Allowed
	reqPost := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	rrPost := httptest.NewRecorder()
	srv.ServeHTTP(rrPost, reqPost)
	if rrPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed for POST /metrics, got %d", rrPost.Code)
	}
}

func TestServer_ResponseCache(t *testing.T) {
	callCount := 0
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"cmpl-%d","choices":[{"message":{"role":"assistant","content":"hello %d"}}],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`, callCount, callCount)
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	// Enable cache on server
	srv.cfg.Cache.Enabled = true
	srv.cfg.Cache.Capacity = 100
	srv.cfg.Cache.TTL = 10 * time.Minute
	srv.cache = cache.NewLRUCache(100, 10*time.Minute)

	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"test cache"}]}`

	// Request 1: Cache MISS
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	rr1 := httptest.NewRecorder()
	srv.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusOK {
		t.Fatalf("req1 failed: %d: %s", rr1.Code, rr1.Body.String())
	}
	if rr1.Header().Get("X-Cache") != "MISS" {
		t.Errorf("expected X-Cache MISS, got %s", rr1.Header().Get("X-Cache"))
	}
	if callCount != 1 {
		t.Errorf("expected upstream callCount=1, got %d", callCount)
	}

	// Request 2: Cache HIT
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("req2 failed: %d: %s", rr2.Code, rr2.Body.String())
	}
	if rr2.Header().Get("X-Cache") != "HIT" {
		t.Errorf("expected X-Cache HIT, got %s", rr2.Header().Get("X-Cache"))
	}
	if callCount != 1 {
		t.Errorf("expected upstream callCount=1 (not called again), got %d", callCount)
	}
	if rr1.Body.String() != rr2.Body.String() {
		t.Errorf("expected identical body from cache hit")
	}

	// Request 3: Cache-Control: no-cache bypass
	req3 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req3.Header.Set("Cache-Control", "no-cache")
	rr3 := httptest.NewRecorder()
	srv.ServeHTTP(rr3, req3)

	if rr3.Code != http.StatusOK {
		t.Fatalf("req3 failed: %d: %s", rr3.Code, rr3.Body.String())
	}
	if rr3.Header().Get("X-Cache") != "MISS" {
		t.Errorf("expected X-Cache MISS when bypassed, got %s", rr3.Header().Get("X-Cache"))
	}
	if callCount != 2 {
		t.Errorf("expected upstream callCount=2, got %d", callCount)
	}
}

func TestServer_StreamingResponseCache(t *testing.T) {
	callCount := 0
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"cmpl-%d","choices":[{"message":{"role":"assistant","content":"stream hello"}}],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`, callCount)
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	srv.cfg.Cache.Enabled = true
	srv.cfg.Cache.Capacity = 100
	srv.cfg.Cache.TTL = 10 * time.Minute
	srv.cache = cache.NewLRUCache(100, 10*time.Minute)

	// Pre-populate cache via non-streaming call
	payloadNonStream := `{"model":"gpt-4o","messages":[{"role":"user","content":"stream cache test"}]}`
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payloadNonStream))
	rr1 := httptest.NewRecorder()
	srv.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("setup non-stream request failed: %d", rr1.Code)
	}
	if callCount != 1 {
		t.Fatalf("expected callCount 1, got %d", callCount)
	}

	// Now send streaming request with exact same prompt
	payloadStream := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"stream cache test"}]}`
	reqStream := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payloadStream))
	rrStream := httptest.NewRecorder()
	srv.ServeHTTP(rrStream, reqStream)

	if rrStream.Code != http.StatusOK {
		t.Fatalf("streaming request failed: %d: %s", rrStream.Code, rrStream.Body.String())
	}
	if rrStream.Header().Get("X-Cache") != "HIT" {
		t.Errorf("expected X-Cache HIT for streaming replay, got %s", rrStream.Header().Get("X-Cache"))
	}
	if callCount != 1 {
		t.Errorf("expected upstream callCount=1 (served from cache replay), got %d", callCount)
	}
	body := rrStream.Body.String()
	if !strings.Contains(body, "stream hello") {
		t.Errorf("expected stream replay to contain 'stream hello', got: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("expected stream replay to terminate with [DONE]")
	}
}

func TestServer_BudgetEnforcement(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"cmpl-budget","choices":[{"message":{"role":"assistant","content":"budget ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":10,"total_tokens":20}}`)
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	srv.cfg.Auth = config.AuthConfig{
		Enabled: true,
		Keys: []config.APIKeyConfig{
			{
				Key:  "tenant-key-1",
				Name: "tenant-corp",
				Budget: &config.BudgetConfig{
					MaxSpend:    0.10,
					SoftLimit:   0.05,
					ResetPeriod: "monthly",
				},
			},
		},
	}

	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"budget check"}]}`

	// 1. Initial request with 0 spend -> Allowed
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req1.Header.Set("Authorization", "Bearer tenant-key-1")
	rr1 := httptest.NewRecorder()
	srv.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for initial request, got %d: %s", rr1.Code, rr1.Body.String())
	}
	if rr1.Header().Get("X-Budget-Warning") != "" {
		t.Errorf("expected no budget warning initially, got %s", rr1.Header().Get("X-Budget-Warning"))
	}

	// 2. Add spend to DB to trigger soft limit ($0.06 > $0.05)
	_ = dbInstance.LogRequestSync(context.Background(), &db.RequestLog{
		ModelRequested: "gpt-4o",
		Provider:       "mock-p",
		ModelRouted:    "gpt-4o",
		EstimatedCost:  0.06,
		StatusCode:     200,
		ClientName:     "tenant-corp",
		CreatedAt:      time.Now().UTC(),
	})

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req2.Header.Set("Authorization", "Bearer tenant-key-1")
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on soft limit, got %d: %s", rr2.Code, rr2.Body.String())
	}
	if rr2.Header().Get("X-Budget-Warning") == "" {
		t.Errorf("expected X-Budget-Warning header when soft limit reached")
	}

	// 3. Add spend to DB to trigger hard limit ($0.12 > $0.10)
	_ = dbInstance.LogRequestSync(context.Background(), &db.RequestLog{
		ModelRequested: "gpt-4o",
		Provider:       "mock-p",
		ModelRouted:    "gpt-4o",
		EstimatedCost:  0.06,
		StatusCode:     200,
		ClientName:     "tenant-corp",
		CreatedAt:      time.Now().UTC(),
	})

	req3 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req3.Header.Set("Authorization", "Bearer tenant-key-1")
	rr3 := httptest.NewRecorder()
	srv.ServeHTTP(rr3, req3)

	if rr3.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 Too Many Requests when hard budget exceeded, got %d: %s", rr3.Code, rr3.Body.String())
	}
	var errResp ErrorResponse
	if err := json.Unmarshal(rr3.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error: %v", err)
	}
	if errResp.Error.Code != "budget_exceeded" {
		t.Errorf("expected error code budget_exceeded, got %s", errResp.Error.Code)
	}
}

func TestServer_AdminBudgets(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	srv.cfg.Auth = config.AuthConfig{
		Enabled: true,
		Keys: []config.APIKeyConfig{
			{
				Key:  "fintech-key",
				Name: "client-fintech",
				Budget: &config.BudgetConfig{
					MaxSpend:    50.0,
					SoftLimit:   40.0,
					ResetPeriod: "monthly",
				},
			},
			{
				Key:  "nobudget-key",
				Name: "client-unlimited",
			},
		},
	}

	_ = dbInstance.LogRequestSync(context.Background(), &db.RequestLog{
		ModelRequested: "gpt-4o",
		Provider:       "mock-p",
		ModelRouted:    "gpt-4o",
		EstimatedCost:  15.0,
		StatusCode:     200,
		ClientName:     "client-fintech",
		CreatedAt:      time.Now().UTC(),
	})

	// 1. GET /v1/admin/budgets
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/budgets", nil)
	req.Header.Set("Authorization", "Bearer fintech-key")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp AdminBudgetsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 budget status, got %d", len(resp.Data))
	}

	b := resp.Data[0]
	if b.ClientName != "client-fintech" {
		t.Errorf("expected client-fintech, got %s", b.ClientName)
	}
	if b.MaxSpend != 50.0 || b.SoftLimit != 40.0 {
		t.Errorf("expected MaxSpend 50 and SoftLimit 40, got %f / %f", b.MaxSpend, b.SoftLimit)
	}
	if b.CurrentSpend < 14.99 || b.CurrentSpend > 15.01 {
		t.Errorf("expected CurrentSpend 15.0, got %f", b.CurrentSpend)
	}
	if b.RemainingSpend < 34.99 || b.RemainingSpend > 35.01 {
		t.Errorf("expected RemainingSpend 35.0, got %f", b.RemainingSpend)
	}
	if b.SoftReached || b.HardExceeded {
		t.Errorf("expected SoftReached=false and HardExceeded=false")
	}

	// 2. Method not allowed for POST
	reqPost := httptest.NewRequest(http.MethodPost, "/v1/admin/budgets", nil)
	reqPost.Header.Set("Authorization", "Bearer fintech-key")
	rrPost := httptest.NewRecorder()
	srv.ServeHTTP(rrPost, reqPost)

	if rrPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", rrPost.Code)
	}
}

func TestServer_AdminCacheEndpoints(t *testing.T) {
	srv, dbInstance, mockUpstream := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"cmpl-cache-admin","choices":[{"message":{"role":"assistant","content":"cache ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`)
	})
	defer dbInstance.Close()
	defer mockUpstream.Close()

	srv.cfg.Cache.Enabled = true
	srv.cfg.Cache.Capacity = 200
	srv.cfg.Cache.TTL = 5 * time.Minute
	srv.cache = cache.NewLRUCache(200, 5*time.Minute)
	srv.cache.SetPersistentStore(dbInstance)

	// 1. Initial stats
	reqStats := httptest.NewRequest(http.MethodGet, "/v1/admin/cache/stats", nil)
	rrStats := httptest.NewRecorder()
	srv.ServeHTTP(rrStats, reqStats)

	if rrStats.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for cache stats, got %d", rrStats.Code)
	}
	var statsResp AdminCacheStatsResponse
	if err := json.Unmarshal(rrStats.Body.Bytes(), &statsResp); err != nil {
		t.Fatalf("failed to parse cache stats: %v", err)
	}
	if !statsResp.Enabled || statsResp.Capacity != 200 || statsResp.Size != 0 {
		t.Errorf("unexpected initial cache stats: %+v", statsResp)
	}

	// 2. Perform chat completion to generate a cache entry
	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello cache"}]}`
	reqChat1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	rrChat1 := httptest.NewRecorder()
	srv.ServeHTTP(rrChat1, reqChat1)
	if rrChat1.Code != http.StatusOK {
		t.Fatalf("chat1 failed: %d", rrChat1.Code)
	}

	// 3. Repeat chat completion to generate a cache hit
	reqChat2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	rrChat2 := httptest.NewRecorder()
	srv.ServeHTTP(rrChat2, reqChat2)
	if rrChat2.Code != http.StatusOK {
		t.Fatalf("chat2 failed: %d", rrChat2.Code)
	}

	// 4. Verify stats updated (size 1, hit 1, miss 1)
	rrStats2 := httptest.NewRecorder()
	srv.ServeHTTP(rrStats2, reqStats)
	var statsResp2 AdminCacheStatsResponse
	_ = json.Unmarshal(rrStats2.Body.Bytes(), &statsResp2)
	if statsResp2.Hits != 1 || statsResp2.Misses != 1 || statsResp2.Size != 1 {
		t.Errorf("expected 1 hit, 1 miss, 1 item; got %+v", statsResp2)
	}
	if statsResp2.HitRatio < 0.49 || statsResp2.HitRatio > 0.51 {
		t.Errorf("expected ~0.5 hit ratio, got %f", statsResp2.HitRatio)
	}

	// 5. Clear cache
	reqClear := httptest.NewRequest(http.MethodPost, "/v1/admin/cache/clear", nil)
	rrClear := httptest.NewRecorder()
	srv.ServeHTTP(rrClear, reqClear)
	if rrClear.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for cache clear, got %d", rrClear.Code)
	}
	var clearResp AdminCacheClearResponse
	if err := json.Unmarshal(rrClear.Body.Bytes(), &clearResp); err != nil {
		t.Fatalf("failed to decode clear response: %v", err)
	}
	if clearResp.Status != "ok" || clearResp.ClearedL1 != 1 {
		t.Errorf("unexpected clear response: %+v", clearResp)
	}

	// 6. Verify stats are zeroed after clear
	rrStats3 := httptest.NewRecorder()
	srv.ServeHTTP(rrStats3, reqStats)
	var statsResp3 AdminCacheStatsResponse
	_ = json.Unmarshal(rrStats3.Body.Bytes(), &statsResp3)
	if statsResp3.Size != 0 || statsResp3.Hits != 0 || statsResp3.Misses != 0 {
		t.Errorf("expected empty cache after clear, got %+v", statsResp3)
	}

	// 7. Method validations
	reqBadStats := httptest.NewRequest(http.MethodPost, "/v1/admin/cache/stats", nil)
	rrBadStats := httptest.NewRecorder()
	srv.ServeHTTP(rrBadStats, reqBadStats)
	if rrBadStats.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed for POST /v1/admin/cache/stats")
	}

	reqBadClear := httptest.NewRequest(http.MethodGet, "/v1/admin/cache/clear", nil)
	rrBadClear := httptest.NewRecorder()
	srv.ServeHTTP(rrBadClear, reqBadClear)
	if rrBadClear.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed for GET /v1/admin/cache/clear")
	}
}




