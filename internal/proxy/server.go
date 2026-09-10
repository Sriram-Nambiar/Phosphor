package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/auth"
	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
	"github.com/Sriram-Nambiar/Phosphor/internal/router"
	"github.com/Sriram-Nambiar/Phosphor/internal/security"
	"github.com/google/uuid"
)

type OpenAIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param,omitempty"`
	Code    string  `json:"code,omitempty"`
}

type ErrorResponse struct {
	Error OpenAIError `json:"error"`
}

func writeOpenAIError(w http.ResponseWriter, statusCode int, message, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: OpenAIError{
			Message: message,
			Type:    errType,
			Code:    code,
		},
	})
}

type contextKey string

const (
	RequestIDKey contextKey = "request_id"
)

func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(RequestIDKey).(string); ok && id != "" {
		return id
	}
	return "chatcmpl-" + uuid.New().String()
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, RequestIDKey, id)
}

type Server struct {
	cfg         *config.Config
	router      *router.Router
	database    *db.DB
	rateLimiter *security.ClientRateLimiter
	server      *http.Server
	mux         *http.ServeMux
	handler     http.Handler
}

func NewServer(cfg *config.Config, r *router.Router, database *db.DB) *Server {
	s := &Server{
		cfg:         cfg,
		router:      r,
		database:    database,
		rateLimiter: security.NewClientRateLimiter(0),
		mux:         http.NewServeMux(),
	}

	s.routes()
	s.handler = s.requestIDMiddleware(s.corsMiddleware(s.authMiddleware(s.rateLimitMiddleware(s.mux))))

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	s.server = &http.Server{
		Addr:         addr,
		Handler:      s.handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/ready", s.handleReady)
	s.mux.HandleFunc("/v1/models", s.handleModels)
	s.mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
}

func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if reqID == "" {
			reqID = "chatcmpl-" + uuid.New().String()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := WithRequestID(r.Context(), reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cors := s.cfg.Server.CORS
		if !cors.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		origin := r.Header.Get("Origin")
		allowedOrigin := ""
		for _, o := range cors.AllowedOrigins {
			if o == "*" {
				allowedOrigin = "*"
				break
			}
			if origin != "" && strings.EqualFold(o, origin) {
				allowedOrigin = origin
				break
			}
		}

		if allowedOrigin != "" {
			if allowedOrigin == "*" && cors.AllowCredentials && origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
				if allowedOrigin != "*" {
					w.Header().Set("Vary", "Origin")
				}
			}
		}

		if cors.AllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		if len(cors.AllowedMethods) > 0 {
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(cors.AllowedMethods, ", "))
		}
		if len(cors.AllowedHeaders) > 0 {
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(cors.AllowedHeaders, ", "))
		}
		if cors.MaxAgeSeconds > 0 {
			w.Header().Set("Access-Control-Max-Age", strconv.Itoa(cors.MaxAgeSeconds))
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Public endpoints that don't require authentication
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			next.ServeHTTP(w, r)
			return
		}

		if !s.cfg.Auth.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		rawKey := auth.ExtractAPIKey(r)
		if rawKey == "" {
			writeOpenAIError(w, http.StatusUnauthorized,
				"You didn't provide an API key. You need to provide your API key in an Authorization header using Bearer auth (i.e. Authorization: Bearer YOUR_KEY), or as the x-api-key header.",
				"invalid_request_error", "invalid_api_key")
			return
		}

		client, ok := auth.Authenticate(rawKey, s.cfg.Auth.Keys)
		if !ok {
			writeOpenAIError(w, http.StatusUnauthorized,
				"Incorrect API key provided.",
				"invalid_request_error", "invalid_api_key")
			return
		}

		ctx := auth.WithClientInfo(r.Context(), *client)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Public health and readiness endpoints are never rate-limited
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			next.ServeHTTP(w, r)
			return
		}

		clientKey := ""
		limitRPM := 0
		if client, ok := auth.GetClientInfo(r.Context()); ok {
			clientKey = client.Key
			if clientKey == "" {
				clientKey = client.Name
			}
			limitRPM = client.RateLimit
		} else {
			clientKey = security.GetClientIP(r)
		}

		bucket := s.rateLimiter.GetBucket(clientKey, limitRPM)
		if bucket == nil {
			next.ServeHTTP(w, r)
			return
		}

		allowed, remaining, retryAfter := bucket.Allow()
		security.SetRateLimitHeaders(w, limitRPM, remaining, retryAfter)

		if !allowed {
			writeOpenAIError(w, http.StatusTooManyRequests,
				"Rate limit reached for your account. Please back off and retry.",
				"rate_limit_error", "rate_limit_exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) Start() error {
	log.Printf("[Phosphor] Listening on %s\n", s.server.Addr)
	return s.server.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    "healthy",
		"service":   "phosphor",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only GET is supported.", "invalid_request_error", "method_not_allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	var issues []string
	dbStatus := "ok"
	if s.database != nil {
		if err := s.database.Ping(ctx); err != nil {
			dbStatus = "unhealthy: " + err.Error()
			issues = append(issues, "database ping failed: "+err.Error())
		}
	} else {
		dbStatus = "disabled"
	}

	providerStatuses := make(map[string]string)
	if s.router != nil {
		providerStatuses = s.router.ProviderStatuses()
	}

	if len(providerStatuses) == 0 {
		issues = append(issues, "no active providers configured")
	} else {
		hasAvailable := false
		for _, state := range providerStatuses {
			if state == "available" {
				hasAvailable = true
				break
			}
		}
		if !hasAvailable {
			issues = append(issues, "all provider circuit breakers are open")
		}
	}

	isReady := len(issues) == 0
	statusCode := http.StatusOK
	statusText := "ready"
	if !isReady {
		statusCode = http.StatusServiceUnavailable
		statusText = "not_ready"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	resp := map[string]any{
		"status":    statusText,
		"database":  dbStatus,
		"providers": providerStatuses,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if len(issues) > 0 {
		resp["errors"] = issues
	}
	_ = json.NewEncoder(w).Encode(resp)
}

type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelListResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only GET is supported.", "invalid_request_error", "method_not_allowed")
		return
	}

	modelMap := make(map[string]bool)

	// Collect models defined under models:
	for modelName := range s.cfg.Models {
		modelMap[modelName] = true
	}

	// Collect models configured under providers:
	for _, p := range s.cfg.Providers {
		for _, m := range p.Models {
			modelMap[m] = true
		}
	}

	client, hasClient := auth.GetClientInfo(r.Context())

	var models []ModelInfo
	now := time.Now().Unix()
	for m := range modelMap {
		if hasClient && !client.CanAccessModel(m) {
			continue
		}
		models = append(models, ModelInfo{
			ID:      m,
			Object:  "model",
			Created: now,
			OwnedBy: "phosphor",
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ModelListResponse{
		Object: "list",
		Data:   models,
	})
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only POST is supported.", "invalid_request_error", "method_not_allowed")
		return
	}

	maxBytes := s.cfg.Server.MaxRequestBodyBytes
	if maxBytes <= 0 {
		maxBytes = 4 * 1024 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("Request body exceeds maximum allowed limit of %d bytes", maxBytes), "invalid_request_error", "payload_too_large")
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "Failed to read request body", "invalid_request_error", "read_error")
		return
	}

	var chatReq provider.ChatRequest
	if err := json.Unmarshal(bodyBytes, &chatReq); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("Invalid JSON: %s", err.Error()), "invalid_request_error", "invalid_json")
		return
	}

	if client, ok := auth.GetClientInfo(r.Context()); ok {
		if !client.CanAccessModel(chatReq.Model) {
			writeOpenAIError(w, http.StatusForbidden, fmt.Sprintf("Your API key does not have permission to access model '%s'", chatReq.Model), "permission_error", "model_access_denied")
			return
		}
	}

	requestID := GetRequestID(r.Context())

	if chatReq.Stream {
		s.handleStreamingCompletions(w, r.Context(), &chatReq, requestID)
	} else {
		s.handleNonStreamingCompletions(w, r.Context(), &chatReq, requestID)
	}
}

func (s *Server) handleNonStreamingCompletions(w http.ResponseWriter, ctx context.Context, req *provider.ChatRequest, requestID string) {
	start := time.Now()
	result, err := s.router.Execute(ctx, req, requestID)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		statusCode := http.StatusBadGateway
		errType := "api_error"
		errCode := "gateway_error"
		if provider.IsRateLimit(err) {
			statusCode = http.StatusTooManyRequests
			errType = "rate_limit_error"
			errCode = "rate_limit_exceeded"
		} else if provider.IsServerError(err) {
			statusCode = http.StatusBadGateway
			errType = "api_error"
			errCode = "upstream_service_unavailable"
		}

		if s.database != nil {
			logCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.database.LogRequest(logCtx, &db.RequestLog{
				ID:             requestID,
				CreatedAt:      time.Now().UTC(),
				ModelRequested: req.Model,
				Provider:       "none",
				ModelRouted:    req.Model,
				StatusCode:     statusCode,
				LatencyMs:      latencyMs,
				Stream:         false,
				ErrorMsg:       security.RedactText(err.Error()),
			})
		}

		writeOpenAIError(w, statusCode, security.RedactText(err.Error()), errType, errCode)
		return
	}

	resp := result.Response
	promptTokens := resp.Usage.PromptTokens
	compTokens := resp.Usage.CompletionTokens
	totalTokens := resp.Usage.TotalTokens
	if promptTokens == 0 {
		promptTokens = router.EstimatePromptTokens(req)
		if len(resp.Choices) > 0 {
			compTokens = len(resp.Choices[0].Message.Content) / 4
		}
		totalTokens = promptTokens + compTokens
	}

	cost := router.CalculateCost(promptTokens, compTokens, result.Candidate.Cost)

	if s.database != nil {
		logCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.database.LogRequest(logCtx, &db.RequestLog{
			ID:               requestID,
			CreatedAt:        time.Now().UTC(),
			ModelRequested:   req.Model,
			Provider:         result.Candidate.ProviderName,
			ModelRouted:      result.Candidate.Model,
			PromptTokens:     promptTokens,
			CompletionTokens: compTokens,
			TotalTokens:      totalTokens,
			EstimatedCost:    cost,
			LatencyMs:        latencyMs,
			TTFTMs:           0,
			StatusCode:       http.StatusOK,
			Stream:           false,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleStreamingCompletions(w http.ResponseWriter, ctx context.Context, req *provider.ChatRequest, requestID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "Streaming unsupported by underlying transport", "api_error", "streaming_unsupported")
		return
	}

	start := time.Now()
	streamResult, err := s.router.ExecuteStream(ctx, req, requestID)
	if err != nil {
		latencyMs := float64(time.Since(start).Milliseconds())
		statusCode := http.StatusBadGateway
		errType := "api_error"
		errCode := "gateway_error"
		if provider.IsRateLimit(err) {
			statusCode = http.StatusTooManyRequests
			errType = "rate_limit_error"
			errCode = "rate_limit_exceeded"
		} else if provider.IsServerError(err) {
			statusCode = http.StatusBadGateway
			errType = "api_error"
			errCode = "upstream_service_unavailable"
		}

		if s.database != nil {
			logCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.database.LogRequest(logCtx, &db.RequestLog{
				ID:             requestID,
				CreatedAt:      time.Now().UTC(),
				ModelRequested: req.Model,
				Provider:       "none",
				ModelRouted:    req.Model,
				StatusCode:     statusCode,
				LatencyMs:      latencyMs,
				Stream:         true,
				ErrorMsg:       security.RedactText(err.Error()),
			})
		}

		writeOpenAIError(w, statusCode, security.RedactText(err.Error()), errType, errCode)
		return
	}

	// Set SSE response headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	firstChunk := true
	var ttftMs float64
	var completionChars int
	var finalUsage *provider.Usage

	var streamErr error
	var clientAborted bool

	var lastFinishReason string
	var chunkCount int

	idleTimeout := s.cfg.Server.StreamIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 30 * time.Second
	}
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

streamLoop:
	for {
		select {
		case <-ctx.Done():
			clientAborted = true
			log.Printf("[Phosphor] Client aborted stream connection for request %s\n", requestID)
			break streamLoop

		case <-idleTimer.C:
			streamErr = fmt.Errorf("streaming idle timeout exceeded (%v)", idleTimeout)
			log.Printf("[Phosphor] %s for request %s\n", streamErr.Error(), requestID)

			errPayload, _ := json.Marshal(map[string]any{
				"error": map[string]any{
					"message": security.RedactText(streamErr.Error()),
					"type":    "timeout_error",
					"code":    "stream_idle_timeout",
				},
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", errPayload)
			flusher.Flush()
			break streamLoop

		case chunk, ok := <-streamResult.StreamChan:
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleTimeout)

			if !ok {
				break streamLoop
			}

			if chunk.Err != nil {
				streamErr = chunk.Err
				log.Printf("[Phosphor] Streaming chunk error: %s\n", security.RedactText(chunk.Err.Error()))

				errPayload, _ := json.Marshal(map[string]any{
					"error": map[string]any{
						"message": security.RedactText(chunk.Err.Error()),
						"type":    "upstream_error",
						"code":    "stream_interrupted",
					},
				})
				_, _ = fmt.Fprintf(w, "data: %s\n\n", errPayload)
				flusher.Flush()
				break streamLoop
			}

			chunkCount++
			if firstChunk {
				firstChunk = false
				ttftMs = float64(time.Since(start).Microseconds()) / 1000.0
				if ttftMs <= 0 {
					ttftMs = 0.001
				}
				s.router.GetLatencyTracker().Record(streamResult.Candidate.ProviderName, streamResult.Candidate.Model, ttftMs, 0)
				if s.database != nil {
					_ = s.database.UpdateLatencyEMA(ctx, streamResult.Candidate.ProviderName, streamResult.Candidate.Model, ttftMs, 0, 0.2, false)
				}
			}

			if len(chunk.Choices) > 0 {
				completionChars += len(chunk.Choices[0].Delta.Content)
				if chunk.Choices[0].FinishReason != "" {
					lastFinishReason = chunk.Choices[0].FinishReason
				}
			}
			if chunk.Usage != nil {
				finalUsage = chunk.Usage
			}

			sseData, err := provider.FormatSSEChunk(chunk)
			if err == nil {
				if _, err := w.Write(sseData); err != nil {
					clientAborted = true
					break streamLoop
				}
				flusher.Flush()
			}
		}
	}

	totalLatencyMs := float64(time.Since(start).Microseconds()) / 1000.0

	// Compute token usage
	promptTokens := router.EstimatePromptTokens(req)
	compTokens := completionChars / 4
	if compTokens < 1 {
		compTokens = 1
	}
	if finalUsage != nil {
		promptTokens = finalUsage.PromptTokens
		compTokens = finalUsage.CompletionTokens
	}
	totalTokens := promptTokens + compTokens
	cost := router.CalculateCost(promptTokens, compTokens, streamResult.Candidate.Cost)

	// If client requested stream_options.include_usage, emit terminal usage chunk
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage && streamErr == nil && !clientAborted {
		usageChunk := provider.StreamChunk{
			ID:      streamResult.Candidate.Model,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   streamResult.Candidate.Model,
			Choices: []provider.StreamChoice{},
			Usage: &provider.Usage{
				PromptTokens:     promptTokens,
				CompletionTokens: compTokens,
				TotalTokens:      totalTokens,
			},
		}
		if sseData, err := provider.FormatSSEChunk(usageChunk); err == nil {
			_, _ = w.Write(sseData)
			flusher.Flush()
		}
	}

	if streamErr == nil && !clientAborted {
		log.Printf("[Phosphor] Stream completed for request %s (chunks: %d, finish_reason: %s, total_tokens: %d)\n",
			requestID, chunkCount, lastFinishReason, totalTokens)
		// Terminate SSE stream normally
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}

	statusCode := http.StatusOK
	var errorMsg string
	if clientAborted {
		statusCode = 499 // Client Closed Request
		errorMsg = "client aborted stream connection"
	} else if streamErr != nil {
		if strings.Contains(streamErr.Error(), "timeout") {
			statusCode = http.StatusGatewayTimeout
		} else {
			statusCode = http.StatusBadGateway
		}
		errorMsg = security.RedactText(streamErr.Error())
	}

	// Log completed or failed stream telemetry
	if s.database != nil {
		logCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.database.LogRequest(logCtx, &db.RequestLog{
			ID:               requestID,
			CreatedAt:        time.Now().UTC(),
			ModelRequested:   req.Model,
			Provider:         streamResult.Candidate.ProviderName,
			ModelRouted:      streamResult.Candidate.Model,
			PromptTokens:     promptTokens,
			CompletionTokens: compTokens,
			TotalTokens:      totalTokens,
			EstimatedCost:    cost,
			LatencyMs:        totalLatencyMs,
			TTFTMs:           ttftMs,
			StatusCode:       statusCode,
			Stream:           true,
			ErrorMsg:         errorMsg,
		})
	}
}
