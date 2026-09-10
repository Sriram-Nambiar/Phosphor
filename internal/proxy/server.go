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
	"strings"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
	"github.com/Sriram-Nambiar/Phosphor/internal/router"
	"github.com/google/uuid"
)

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
	cfg      *config.Config
	router   *router.Router
	database *db.DB
	server   *http.Server
	mux      *http.ServeMux
	handler  http.Handler
}

func NewServer(cfg *config.Config, r *router.Router, database *db.DB) *Server {
	s := &Server{
		cfg:      cfg,
		router:   r,
		database: database,
		mux:      http.NewServeMux(),
	}

	s.routes()
	s.handler = s.requestIDMiddleware(s.corsMiddleware(s.mux))

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
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, X-Request-ID")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
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
		http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
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

	var models []ModelInfo
	now := time.Now().Unix()
	for m := range modelMap {
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
		http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
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
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{
					"message": fmt.Sprintf("Request body exceeds maximum allowed limit of %d bytes", maxBytes),
					"type":    "invalid_request_error",
					"code":    "payload_too_large",
				},
			})
			return
		}
		http.Error(w, `{"error":{"message":"Failed to read request body"}}`, http.StatusBadRequest)
		return
	}

	var chatReq provider.ChatRequest
	if err := json.Unmarshal(bodyBytes, &chatReq); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"Invalid JSON: %s"}}`, err.Error()), http.StatusBadRequest)
		return
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
		if provider.IsRateLimit(err) {
			statusCode = http.StatusTooManyRequests
		}

		if s.database != nil {
			_ = s.database.LogRequest(ctx, &db.RequestLog{
				ID:             requestID,
				CreatedAt:      time.Now().UTC(),
				ModelRequested: req.Model,
				Provider:       "none",
				ModelRouted:    req.Model,
				StatusCode:     statusCode,
				LatencyMs:      latencyMs,
				Stream:         false,
				ErrorMsg:       err.Error(),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"message": err.Error(),
				"type":    "phosphor_gateway_error",
			},
		})
		return
	}

	resp := result.Response
	promptTokens := resp.Usage.PromptTokens
	compTokens := resp.Usage.CompletionTokens
	totalTokens := resp.Usage.TotalTokens
	if promptTokens == 0 {
		promptTokens = router.EstimatePromptTokens(req)
		compTokens = len(resp.Choices[0].Message.Content) / 4
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
		http.Error(w, `{"error":{"message":"Streaming unsupported by underlying transport"}}`, http.StatusInternalServerError)
		return
	}

	start := time.Now()
	streamResult, err := s.router.ExecuteStream(ctx, req, requestID)
	if err != nil {
		latencyMs := float64(time.Since(start).Milliseconds())
		statusCode := http.StatusBadGateway
		if provider.IsRateLimit(err) {
			statusCode = http.StatusTooManyRequests
		}

		if s.database != nil {
			_ = s.database.LogRequest(ctx, &db.RequestLog{
				ID:             requestID,
				CreatedAt:      time.Now().UTC(),
				ModelRequested: req.Model,
				Provider:       "none",
				ModelRouted:    req.Model,
				StatusCode:     statusCode,
				LatencyMs:      latencyMs,
				Stream:         true,
				ErrorMsg:       err.Error(),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"message": err.Error(),
				"type":    "phosphor_gateway_error",
			},
		})
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

	for chunk := range streamResult.StreamChan {
		if chunk.Err != nil {
			log.Printf("[Phosphor] Streaming chunk error: %v\n", chunk.Err)
			break
		}

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
		}
		if chunk.Usage != nil {
			finalUsage = chunk.Usage
		}

		sseData, err := provider.FormatSSEChunk(chunk)
		if err == nil {
			_, _ = w.Write(sseData)
			flusher.Flush()
		}
	}

	// Terminate SSE stream
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()

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

	// Log completed stream telemetry
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
			StatusCode:       http.StatusOK,
			Stream:           true,
		})
	}
}
