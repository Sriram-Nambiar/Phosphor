package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/auth"
	"github.com/Sriram-Nambiar/Phosphor/internal/budget"
	"github.com/Sriram-Nambiar/Phosphor/internal/cache"
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
	cfg            *config.Config
	router         *router.Router
	database       *db.DB
	cache          *cache.LRUCache
	dedup          *cache.Deduplicator
	detector       *security.PromptDetector
	ipFilter       *security.IPFilter
	securityBlocks atomic.Uint64
	rateLimiter    *security.ClientRateLimiter
	server         *http.Server
	mux            *http.ServeMux
	handler        http.Handler
}

func NewServer(cfg *config.Config, r *router.Router, database *db.DB) *Server {
	var c *cache.LRUCache
	if cfg.Cache.Enabled {
		c = cache.NewLRUCache(cfg.Cache.Capacity, cfg.Cache.TTL)
		if database != nil {
			c.SetPersistentStore(database)
		}
	}

	var detector *security.PromptDetector
	if cfg.Security.EnablePromptGuard {
		detector = security.NewPromptDetector(cfg.Security.BlockThreshold)
	}

	var ipFilter *security.IPFilter
	if len(cfg.Security.AllowedIPs) > 0 || len(cfg.Security.BlockedIPs) > 0 {
		if filter, err := security.NewIPFilter(cfg.Security.AllowedIPs, cfg.Security.BlockedIPs); err == nil {
			ipFilter = filter
		}
	}

	s := &Server{
		cfg:         cfg,
		router:      r,
		database:    database,
		cache:       c,
		dedup:       cache.NewDeduplicator(),
		detector:    detector,
		ipFilter:    ipFilter,
		rateLimiter: security.NewClientRateLimiter(0),
		mux:         http.NewServeMux(),
	}

	s.routes()
	s.handler = s.requestIDMiddleware(s.corsMiddleware(s.ipFilterMiddleware(s.authMiddleware(s.rateLimitMiddleware(s.mux)))))

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
	s.mux.HandleFunc("/metrics", s.handleMetrics)
	s.mux.HandleFunc("/v1/models", s.handleModels)
	s.mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	s.mux.HandleFunc("/v1/admin/budgets", s.handleAdminBudgets)
	s.mux.HandleFunc("/v1/admin/cache/stats", s.handleAdminCacheStats)
	s.mux.HandleFunc("/v1/admin/cache/clear", s.handleAdminCacheClear)
	s.mux.HandleFunc("/v1/admin/db/vacuum", s.handleAdminDBVacuum)
	s.mux.HandleFunc("/v1/admin/db/backup", s.handleAdminDBBackup)
	s.mux.HandleFunc("/openapi.json", s.handleOpenAPI)
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
			if origin != "" && matchOriginPattern(o, origin) {
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

// matchOriginPattern evaluates whether an incoming Origin matches an allowed origin pattern.
// Supports exact matches ("https://app.example.com"), subdomains ("https://*.example.com"),
// and wildcard ports ("http://localhost:*").
func matchOriginPattern(pattern, origin string) bool {
	if pattern == "*" {
		return true
	}
	if origin == "" {
		return false
	}
	pattern = strings.ToLower(pattern)
	origin = strings.ToLower(origin)
	if pattern == origin {
		return true
	}

	if !strings.Contains(pattern, "*") {
		return false
	}

	parts := strings.Split(pattern, "*")
	if len(parts) == 2 {
		prefix, suffix := parts[0], parts[1]
		return strings.HasPrefix(origin, prefix) && strings.HasSuffix(origin, suffix) && len(origin) >= len(prefix)+len(suffix)
	}

	escaped := regexp.QuoteMeta(pattern)
	regexPattern := "^" + strings.ReplaceAll(escaped, `\*`, `.*`) + "$"
	matched, _ := regexp.MatchString(regexPattern, origin)
	return matched
}

func (s *Server) ipFilterMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.ipFilter != nil && s.ipFilter.HasRules() {
			clientIP := security.GetClientIP(r)
			if !s.ipFilter.IsAllowed(clientIP) {
				s.securityBlocks.Add(1)
				writeOpenAIError(w, http.StatusForbidden,
					fmt.Sprintf("Access denied: IP %s is not permitted to access this gateway", clientIP),
					"permission_denied", "ip_forbidden")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Public endpoints that don't require authentication
		if r.URL.Path == "/health" || r.URL.Path == "/ready" || r.URL.Path == "/metrics" || r.URL.Path == "/openapi.json" {
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
		// Public health, readiness, and metrics endpoints are never rate-limited
		if r.URL.Path == "/health" || r.URL.Path == "/ready" || r.URL.Path == "/metrics" {
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
	err := s.server.Shutdown(ctx)
	if s.database != nil {
		_ = s.database.Flush(ctx)
	}
	return err
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	status := "healthy"
	if s.database != nil && s.database.IsDegraded() {
		status = "degraded"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    status,
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
		} else if s.database.IsDegraded() {
			dbStatus = fmt.Sprintf("degraded: recurring write failures (%s)", s.database.LastWriteError())
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
	} else if s.database != nil && s.database.IsDegraded() {
		statusText = "degraded"
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

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only GET is supported.", "invalid_request_error", "method_not_allowed")
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var buf strings.Builder

	// 1. Process and gateway status
	buf.WriteString("# HELP phosphor_up Whether the Phosphor gateway is up and running.\n")
	buf.WriteString("# TYPE phosphor_up gauge\n")
	buf.WriteString("phosphor_up 1\n\n")

	// 2. Telemetry queue stats and DB degradation
	queueDepth := 0
	var droppedLogs uint64
	dbDegraded := 0
	var dbWriteErrors uint64 = 0
	if s.database != nil {
		queueDepth = s.database.QueueDepth()
		droppedLogs = s.database.DroppedLogsCount()
		if s.database.IsDegraded() {
			dbDegraded = 1
		}
		dbWriteErrors = s.database.WriteErrorsCount()
	}
	buf.WriteString("# HELP phosphor_telemetry_queue_depth Current number of items queued in async telemetry queue.\n")
	buf.WriteString("# TYPE phosphor_telemetry_queue_depth gauge\n")
	fmt.Fprintf(&buf, "phosphor_telemetry_queue_depth %d\n\n", queueDepth)

	buf.WriteString("# HELP phosphor_telemetry_dropped_total Total number of dropped telemetry events due to backpressure.\n")
	buf.WriteString("# TYPE phosphor_telemetry_dropped_total counter\n")
	fmt.Fprintf(&buf, "phosphor_telemetry_dropped_total %d\n\n", droppedLogs)

	buf.WriteString("# HELP phosphor_db_degraded SQLite database degraded mode indicator (1=degraded, 0=healthy).\n")
	buf.WriteString("# TYPE phosphor_db_degraded gauge\n")
	fmt.Fprintf(&buf, "phosphor_db_degraded %d\n\n", dbDegraded)

	buf.WriteString("# HELP phosphor_db_write_errors_total Total number of database write errors encountered.\n")
	buf.WriteString("# TYPE phosphor_db_write_errors_total counter\n")
	fmt.Fprintf(&buf, "phosphor_db_write_errors_total %d\n\n", dbWriteErrors)

	// 3. Circuit breaker metrics
	buf.WriteString("# HELP phosphor_circuit_breaker_state Current state of provider circuit breaker (0=closed, 1=half-open, 2=open).\n")
	buf.WriteString("# TYPE phosphor_circuit_breaker_state gauge\n")
	for name, cb := range s.router.GetCircuitBreakers() {
		stateVal := 0
		switch cb.GetState() {
		case router.StateClosed:
			stateVal = 0
		case router.StateHalfOpen:
			stateVal = 1
		case router.StateOpen:
			stateVal = 2
		}
		fmt.Fprintf(&buf, "phosphor_circuit_breaker_state{provider=%q} %d\n", name, stateVal)
	}
	buf.WriteString("\n")

	// 4. Provider latency and TTFT metrics from database
	if s.database != nil {
		metrics, err := s.database.GetProviderMetrics(r.Context())
		if err == nil && len(metrics) > 0 {
			buf.WriteString("# HELP phosphor_provider_latency_ms Exponential moving average latency in milliseconds.\n")
			buf.WriteString("# TYPE phosphor_provider_latency_ms gauge\n")
			for _, m := range metrics {
				fmt.Fprintf(&buf, "phosphor_provider_latency_ms{provider=%q,model=%q} %.2f\n", m.Provider, m.Model, m.EMALatencyMs)
			}
			buf.WriteString("\n")

			buf.WriteString("# HELP phosphor_provider_ttft_ms Exponential moving average time to first token in milliseconds.\n")
			buf.WriteString("# TYPE phosphor_provider_ttft_ms gauge\n")
			for _, m := range metrics {
				fmt.Fprintf(&buf, "phosphor_provider_ttft_ms{provider=%q,model=%q} %.2f\n", m.Provider, m.Model, m.EMATTFTMs)
			}
			buf.WriteString("\n")

			buf.WriteString("# HELP phosphor_provider_requests_total Total requests routed to provider.\n")
			buf.WriteString("# TYPE phosphor_provider_requests_total counter\n")
			for _, m := range metrics {
				fmt.Fprintf(&buf, "phosphor_provider_requests_total{provider=%q,model=%q} %d\n", m.Provider, m.Model, m.TotalRequests)
			}
			buf.WriteString("\n")

			buf.WriteString("# HELP phosphor_provider_failures_total Total failed requests for provider.\n")
			buf.WriteString("# TYPE phosphor_provider_failures_total counter\n")
			for _, m := range metrics {
				fmt.Fprintf(&buf, "phosphor_provider_failures_total{provider=%q,model=%q} %d\n", m.Provider, m.Model, m.TotalFailures)
			}
			buf.WriteString("\n")
		}

		// 5. Aggregate stats
		stats, err := s.database.GetAggregateStats(r.Context())
		if err == nil && stats != nil {
			buf.WriteString("# HELP phosphor_requests_total Total requests processed by Phosphor.\n")
			buf.WriteString("# TYPE phosphor_requests_total counter\n")
			fmt.Fprintf(&buf, "phosphor_requests_total %d\n\n", stats.TotalRequests)

			buf.WriteString("# HELP phosphor_prompt_tokens_total Total prompt tokens processed.\n")
			buf.WriteString("# TYPE phosphor_prompt_tokens_total counter\n")
			fmt.Fprintf(&buf, "phosphor_prompt_tokens_total %d\n\n", stats.TotalPromptTok)

			buf.WriteString("# HELP phosphor_completion_tokens_total Total completion tokens generated.\n")
			buf.WriteString("# TYPE phosphor_completion_tokens_total counter\n")
			fmt.Fprintf(&buf, "phosphor_completion_tokens_total %d\n\n", stats.TotalCompTok)

			buf.WriteString("# HELP phosphor_tokens_total Total tokens processed.\n")
			buf.WriteString("# TYPE phosphor_tokens_total counter\n")
			fmt.Fprintf(&buf, "phosphor_tokens_total %d\n\n", stats.TotalTokens)

			buf.WriteString("# HELP phosphor_cost_dollars_total Total estimated cost in USD.\n")
			buf.WriteString("# TYPE phosphor_cost_dollars_total counter\n")
			fmt.Fprintf(&buf, "phosphor_cost_dollars_total %.6f\n\n", stats.TotalCost)

			buf.WriteString("# HELP phosphor_failovers_total Total failovers between providers.\n")
			buf.WriteString("# TYPE phosphor_failovers_total counter\n")
			fmt.Fprintf(&buf, "phosphor_failovers_total %d\n\n", stats.TotalFailovers)
		}

		// 6. Latency percentiles (p50, p90, p99)
		pStats, err := s.database.GetLatencyPercentiles(r.Context())
		if err == nil && pStats.Count > 0 {
			buf.WriteString("# HELP phosphor_latency_percentile_ms Request latency percentiles across all models.\n")
			buf.WriteString("# TYPE phosphor_latency_percentile_ms gauge\n")
			fmt.Fprintf(&buf, "phosphor_latency_percentile_ms{quantile=\"0.5\"} %.2f\n", pStats.P50)
			fmt.Fprintf(&buf, "phosphor_latency_percentile_ms{quantile=\"0.9\"} %.2f\n", pStats.P90)
			fmt.Fprintf(&buf, "phosphor_latency_percentile_ms{quantile=\"0.99\"} %.2f\n\n", pStats.P99)
		}

		provPercentiles, err := s.database.GetProviderLatencyPercentiles(r.Context())
		if err == nil && len(provPercentiles) > 0 {
			buf.WriteString("# HELP phosphor_provider_latency_percentile_ms Provider request latency percentiles.\n")
			buf.WriteString("# TYPE phosphor_provider_latency_percentile_ms gauge\n")
			for prov, pp := range provPercentiles {
				fmt.Fprintf(&buf, "phosphor_provider_latency_percentile_ms{provider=%q,quantile=\"0.5\"} %.2f\n", prov, pp.P50)
				fmt.Fprintf(&buf, "phosphor_provider_latency_percentile_ms{provider=%q,quantile=\"0.9\"} %.2f\n", prov, pp.P90)
				fmt.Fprintf(&buf, "phosphor_provider_latency_percentile_ms{provider=%q,quantile=\"0.99\"} %.2f\n", prov, pp.P99)
			}
			buf.WriteString("\n")
		}
	}

	if s.cache != nil {
		hits, misses, size := s.cache.Stats()
		buf.WriteString("# HELP phosphor_cache_hits_total Total response cache hits.\n")
		buf.WriteString("# TYPE phosphor_cache_hits_total counter\n")
		fmt.Fprintf(&buf, "phosphor_cache_hits_total %d\n\n", hits)

		buf.WriteString("# HELP phosphor_cache_misses_total Total response cache misses.\n")
		buf.WriteString("# TYPE phosphor_cache_misses_total counter\n")
		fmt.Fprintf(&buf, "phosphor_cache_misses_total %d\n\n", misses)

		buf.WriteString("# HELP phosphor_cache_entries Current entries in response cache.\n")
		buf.WriteString("# TYPE phosphor_cache_entries gauge\n")
		fmt.Fprintf(&buf, "phosphor_cache_entries %d\n\n", size)
	}

	buf.WriteString("# HELP phosphor_security_blocks_total Total requests blocked by security guardrails.\n")
	buf.WriteString("# TYPE phosphor_security_blocks_total counter\n")
	fmt.Fprintf(&buf, "phosphor_security_blocks_total %d\n\n", s.securityBlocks.Load())

	_, _ = w.Write([]byte(buf.String()))
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

	// Collect aliases configured under model_aliases:
	for alias := range s.cfg.ModelAliases {
		modelMap[alias] = true
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

type ClientBudgetStatus struct {
	ClientName     string  `json:"client_name"`
	MaxSpend       float64 `json:"max_spend"`
	SoftLimit      float64 `json:"soft_limit"`
	CurrentSpend   float64 `json:"current_spend"`
	ResetPeriod    string  `json:"reset_period"`
	WindowStart    string  `json:"window_start,omitempty"`
	RemainingSpend float64 `json:"remaining_spend"`
	SoftReached    bool    `json:"soft_reached"`
	HardExceeded   bool    `json:"hard_exceeded"`
}

type AdminBudgetsResponse struct {
	Object string               `json:"object"`
	Data   []ClientBudgetStatus `json:"data"`
}

func (s *Server) handleAdminBudgets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only GET is supported.", "invalid_request_error", "method_not_allowed")
		return
	}

	var results []ClientBudgetStatus
	now := time.Now().UTC()

	for _, k := range s.cfg.Auth.Keys {
		if k.Budget == nil || k.Budget.MaxSpend <= 0 {
			continue
		}
		var currentSpend float64
		windowStart := budget.WindowStart(k.Budget.ResetPeriod, now)
		if s.database != nil {
			spend, err := s.database.GetClientSpend(r.Context(), k.Name, windowStart)
			if err == nil {
				currentSpend = spend
			}
		}

		eval := budget.Evaluate(k.Budget, currentSpend)
		remaining := k.Budget.MaxSpend - currentSpend
		if remaining < 0 {
			remaining = 0
		}

		windowStr := ""
		if !windowStart.IsZero() {
			windowStr = windowStart.Format(time.RFC3339)
		}

		results = append(results, ClientBudgetStatus{
			ClientName:     k.Name,
			MaxSpend:       k.Budget.MaxSpend,
			SoftLimit:      k.Budget.SoftLimit,
			CurrentSpend:   currentSpend,
			ResetPeriod:    k.Budget.ResetPeriod,
			WindowStart:    windowStr,
			RemainingSpend: remaining,
			SoftReached:    eval.SoftReached,
			HardExceeded:   !eval.Allowed,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(AdminBudgetsResponse{
		Object: "list",
		Data:   results,
	})
}

type AdminCacheStatsResponse struct {
	Enabled    bool    `json:"enabled"`
	Capacity   int     `json:"capacity,omitempty"`
	Size       int     `json:"size,omitempty"`
	Hits       int64   `json:"hits,omitempty"`
	Misses     int64   `json:"misses,omitempty"`
	HitRatio   float64 `json:"hit_ratio,omitempty"`
	TTLSeconds int     `json:"ttl_seconds,omitempty"`
}

func (s *Server) handleAdminCacheStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only GET is supported.", "invalid_request_error", "method_not_allowed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if s.cache == nil {
		_ = json.NewEncoder(w).Encode(AdminCacheStatsResponse{
			Enabled: false,
		})
		return
	}

	hits, misses, size := s.cache.Stats()
	var ratio float64
	total := hits + misses
	if total > 0 {
		ratio = float64(hits) / float64(total)
	}

	_ = json.NewEncoder(w).Encode(AdminCacheStatsResponse{
		Enabled:    true,
		Capacity:   s.cache.Capacity(),
		Size:       size,
		Hits:       hits,
		Misses:     misses,
		HitRatio:   ratio,
		TTLSeconds: int(s.cfg.Cache.TTL.Seconds()),
	})
}

type AdminCacheClearResponse struct {
	Status        string `json:"status"`
	ClearedL1     int    `json:"cleared_l1"`
	ClearedL2Rows int64  `json:"cleared_l2_rows"`
}

func (s *Server) handleAdminCacheClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only POST is supported.", "invalid_request_error", "method_not_allowed")
		return
	}

	clearedL1 := 0
	if s.cache != nil {
		clearedL1 = s.cache.Len()
		s.cache.Clear()
	}

	var clearedL2 int64
	if s.database != nil {
		if rows, err := s.database.ClearCache(r.Context()); err == nil {
			clearedL2 = rows
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(AdminCacheClearResponse{
		Status:        "ok",
		ClearedL1:     clearedL1,
		ClearedL2Rows: clearedL2,
	})
}

type AdminDBVacuumResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	SizeBytes int64  `json:"size_bytes"`
}

func (s *Server) handleAdminDBVacuum(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only POST is supported.", "invalid_request_error", "method_not_allowed")
		return
	}

	if s.database == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "Database is not configured or disabled.", "database_error", "db_unavailable")
		return
	}

	if err := s.database.Vacuum(r.Context()); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to vacuum database: %v", err), "database_error", "vacuum_failed")
		return
	}

	size, _ := s.database.FileSize()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(AdminDBVacuumResponse{
		Status:    "ok",
		Message:   "database vacuumed and optimized",
		SizeBytes: size,
	})
}

type AdminDBBackupRequest struct {
	Destination string `json:"destination"`
}

type AdminDBBackupResponse struct {
	Status      string `json:"status"`
	Destination string `json:"destination"`
	SizeBytes   int64  `json:"size_bytes"`
}

func (s *Server) handleAdminDBBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only POST is supported.", "invalid_request_error", "method_not_allowed")
		return
	}

	if s.database == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "Database is not configured or disabled.", "database_error", "db_unavailable")
		return
	}

	var req AdminDBBackupRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	dest := strings.TrimSpace(req.Destination)
	if dest == "" {
		dest = fmt.Sprintf("phosphor_backup_%s.db", time.Now().UTC().Format("20060102_150405"))
	}

	if err := s.database.Backup(r.Context(), dest); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to backup database: %v", err), "database_error", "backup_failed")
		return
	}

	var size int64
	if fi, err := os.Stat(dest); err == nil {
		size = fi.Size()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(AdminDBBackupResponse{
		Status:      "ok",
		Destination: dest,
		SizeBytes:   size,
	})
}


func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only POST is supported.", "invalid_request_error", "method_not_allowed")
		return
	}

	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = strings.TrimSpace(r.Header.Get("X-Trace-ID"))
	}
	sessionID := strings.TrimSpace(r.Header.Get("X-Session-ID"))

	if correlationID != "" {
		w.Header().Set("X-Correlation-ID", correlationID)
	}
	if sessionID != "" {
		w.Header().Set("X-Session-ID", sessionID)
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

	requestedModel := chatReq.Model
	if headerModel := strings.TrimSpace(r.Header.Get("X-Phosphor-Model")); headerModel != "" {
		requestedModel = headerModel
		chatReq.Model = headerModel
	} else if headerModel := strings.TrimSpace(r.Header.Get("X-Routing-Model")); headerModel != "" {
		requestedModel = headerModel
		chatReq.Model = headerModel
	}

	canonicalModel := s.cfg.ResolveModelAlias(requestedModel)
	w.Header().Set("X-Phosphor-Model", canonicalModel)

	if client, ok := auth.GetClientInfo(r.Context()); ok {
		if !client.CanAccessModel(requestedModel) && !client.CanAccessModel(canonicalModel) {
			writeOpenAIError(w, http.StatusForbidden, fmt.Sprintf("Your API key does not have permission to access model '%s'", chatReq.Model), "permission_error", "model_access_denied")
			return
		}

		// Enforce tenant/client budget if configured
		if client.Budget != nil && client.Budget.MaxSpend > 0 && s.database != nil {
			start := budget.WindowStart(client.Budget.ResetPeriod, time.Now())
			currentSpend, err := s.database.GetClientSpend(r.Context(), client.Name, start)
			if err == nil {
				res := budget.Evaluate(client.Budget, currentSpend)
				if !res.Allowed {
					writeOpenAIError(w, http.StatusTooManyRequests, budget.FormatBudgetError(res), "insufficient_quota", "budget_exceeded")
					return
				}
				if res.SoftReached {
					w.Header().Set("X-Budget-Warning", fmt.Sprintf("Approaching budget limit ($%.2f / $%.2f)", res.Current, res.MaxSpend))
				}
			}
		}
	}

	requestID := GetRequestID(r.Context())

	// Security Guardrails: Scan input for prompt injection and jailbreaks
	if s.detector != nil {
		var combinedText strings.Builder
		for _, m := range chatReq.Messages {
			combinedText.WriteString(m.Content)
			combinedText.WriteString(" ")
		}
		scan := s.detector.Scan(combinedText.String())
		w.Header().Set("X-Security-Risk", fmt.Sprintf("%.2f", scan.RiskScore))

		if !scan.Safe {
			s.securityBlocks.Add(1)
			reason := "prompt injection pattern detected"
			if len(scan.Matches) > 0 {
				reason = scan.Matches[0].Description
			}
			if s.database != nil {
				var clientName string
				if client, ok := auth.GetClientInfo(r.Context()); ok {
					clientName = client.Name
				}
				logCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = s.database.LogRequest(logCtx, &db.RequestLog{
					ID:             requestID,
					CreatedAt:      time.Now().UTC(),
					ModelRequested: chatReq.Model,
					Provider:       "security_guardrail",
					ModelRouted:    canonicalModel,
					StatusCode:     http.StatusBadRequest,
					Stream:         chatReq.Stream,
					ErrorMsg:       fmt.Sprintf("Blocked by security guardrail: %s", reason),
					ClientName:     clientName,
				})
			}
			writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("Request rejected by security guardrails: %s", reason), "invalid_request_error", "prompt_injection_detected")
			return
		}
	}

	chatReq.Model = canonicalModel

	if chatReq.Stream {
		s.handleStreamingCompletions(w, r, &chatReq, requestID)
	} else {
		s.handleNonStreamingCompletions(w, r, &chatReq, requestID)
	}
}

func (s *Server) handleNonStreamingCompletions(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, requestID string) {
	ctx := r.Context()
	cacheKey := cache.ComputeKey(req, "")
	etag := `"` + cacheKey + `"`
	bypassCache := strings.Contains(strings.ToLower(r.Header.Get("Cache-Control")), "no-cache")

	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = strings.TrimSpace(r.Header.Get("X-Trace-ID"))
	}
	sessionID := strings.TrimSpace(r.Header.Get("X-Session-ID"))

	// 1. Check response cache if enabled and not bypassed via Cache-Control: no-cache
	if s.cache != nil && !bypassCache {
		if cachedBytes, hit := s.cache.Get(cacheKey); hit {
			w.Header().Set("ETag", etag)
			clientETag := strings.TrimSpace(r.Header.Get("If-None-Match"))
			if clientETag != "" && (clientETag == "*" || clientETag == etag || strings.Trim(clientETag, `"`) == cacheKey) {
				w.Header().Set("X-Cache", "HIT")
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(cachedBytes)
			return
		}
	}

	executeUpstream := func() ([]byte, error) {
		start := time.Now()
		result, err := s.router.Execute(ctx, req, requestID)
		latencyMs := float64(time.Since(start).Milliseconds())

		if err != nil {
			statusCode := http.StatusBadGateway
			if provider.IsRateLimit(err) {
				statusCode = http.StatusTooManyRequests
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
					CorrelationID:  correlationID,
					SessionID:      sessionID,
				})
			}
			return nil, err
		}

		resp := result.Response
		if resp.Usage.TotalTokens <= 0 {
			resp.EnsureUsage(router.EstimatePromptTokens(req))
		}
		promptTokens := resp.Usage.PromptTokens
		compTokens := resp.Usage.CompletionTokens
		totalTokens := resp.Usage.TotalTokens

		cost := router.CalculateCost(promptTokens, compTokens, result.Candidate.Cost)

		if s.database != nil {
			var clientName string
			if client, ok := auth.GetClientInfo(r.Context()); ok {
				clientName = client.Name
			}
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
				ClientName:       clientName,
				CorrelationID:    correlationID,
				SessionID:        sessionID,
			})
		}

		respBytes, err := json.Marshal(resp)
		if err != nil {
			return nil, err
		}
		if s.cache != nil && cacheKey != "" {
			s.cache.Set(cacheKey, respBytes, s.cfg.Cache.TTL)
		}
		return respBytes, nil
	}

	var respBytes []byte
	var shared bool
	var err error

	if s.dedup != nil && !bypassCache {
		respBytes, shared, err = s.dedup.Do(cacheKey, executeUpstream)
	} else {
		respBytes, err = executeUpstream()
	}

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
		writeOpenAIError(w, statusCode, security.RedactText(err.Error()), errType, errCode)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	if shared {
		w.Header().Set("X-Deduplicated", "true")
		if s.cache != nil {
			w.Header().Set("X-Cache", "HIT")
		}
	} else if s.cache != nil {
		w.Header().Set("X-Cache", "MISS")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBytes)
}

func (s *Server) handleStreamingCompletions(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, requestID string) {
	ctx := r.Context()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "Streaming unsupported by underlying transport", "api_error", "streaming_unsupported")
		return
	}

	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = strings.TrimSpace(r.Header.Get("X-Trace-ID"))
	}
	sessionID := strings.TrimSpace(r.Header.Get("X-Session-ID"))

	var cacheKey string
	if s.cache != nil && !strings.Contains(strings.ToLower(r.Header.Get("Cache-Control")), "no-cache") {
		cacheKey = cache.ComputeKey(req, "")
		if cachedBytes, hit := s.cache.Get(cacheKey); hit {
			var cachedResp provider.ChatResponse
			if err := json.Unmarshal(cachedBytes, &cachedResp); err == nil {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.Header().Set("X-Accel-Buffering", "no")
				w.Header().Set("X-Cache", "HIT")
				w.WriteHeader(http.StatusOK)
				flusher.Flush()

				content := ""
				if len(cachedResp.Choices) > 0 {
					content = cachedResp.Choices[0].Message.Content
				}

				// Replay role
				chunk1 := provider.StreamChunk{
					ID:      requestID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []provider.StreamChoice{
						{Index: 0, Delta: provider.StreamDelta{Role: "assistant"}},
					},
				}
				_ = provider.WriteSSEChunk(w, chunk1)
				flusher.Flush()

				// Replay content
				chunk2 := provider.StreamChunk{
					ID:      requestID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []provider.StreamChoice{
						{Index: 0, Delta: provider.StreamDelta{Content: content}, FinishReason: "stop"},
					},
				}
				_ = provider.WriteSSEChunk(w, chunk2)
				flusher.Flush()

				// Usage chunk if requested
				if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
					chunk3 := provider.StreamChunk{
						ID:      requestID,
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   req.Model,
						Choices: []provider.StreamChoice{},
						Usage:   &cachedResp.Usage,
					}
					_ = provider.WriteSSEChunk(w, chunk3)
					flusher.Flush()
				}

				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				flusher.Flush()
				return
			}
		}
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
				CorrelationID:  correlationID,
				SessionID:      sessionID,
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
	if s.cache != nil {
		w.Header().Set("X-Cache", "MISS")
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var accumulatedContent strings.Builder

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
				accumulatedContent.WriteString(chunk.Choices[0].Delta.Content)
				if chunk.Choices[0].FinishReason != "" {
					lastFinishReason = chunk.Choices[0].FinishReason
				}
			}
			if chunk.Usage != nil {
				finalUsage = chunk.Usage
			}

			if err := provider.WriteSSEChunk(w, chunk); err != nil {
				clientAborted = true
				break streamLoop
			}
			flusher.Flush()
		}
	}

	totalLatencyMs := float64(time.Since(start).Microseconds()) / 1000.0

	// Compute token usage
	promptTokens := router.EstimatePromptTokens(req)
	compTokens := completionChars / 4
	if compTokens < 1 && completionChars > 0 {
		compTokens = 1
	}
	if finalUsage != nil && finalUsage.TotalTokens > 0 {
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
		if err := provider.WriteSSEChunk(w, usageChunk); err == nil {
			flusher.Flush()
		}
	}

	if streamErr == nil && !clientAborted {
		log.Printf("[Phosphor] Stream completed for request %s (chunks: %d, finish_reason: %s, total_tokens: %d)\n",
			requestID, chunkCount, lastFinishReason, totalTokens)
		// Terminate SSE stream normally
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()

		// Cache successful streaming completion for future requests
		if s.cache != nil && cacheKey != "" {
			cachedObj := provider.ChatResponse{
				ID:      requestID,
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   streamResult.Candidate.Model,
				Choices: []provider.ChatChoice{
					{
						Index: 0,
						Message: provider.ChatMessage{
							Role:    "assistant",
							Content: accumulatedContent.String(),
						},
						FinishReason: "stop",
					},
				},
				Usage: provider.Usage{
					PromptTokens:     promptTokens,
					CompletionTokens: compTokens,
					TotalTokens:      totalTokens,
				},
			}
			if data, err := json.Marshal(cachedObj); err == nil {
				s.cache.Set(cacheKey, data, s.cfg.Cache.TTL)
			}
		}
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
		var clientName string
		if client, ok := auth.GetClientInfo(r.Context()); ok {
			clientName = client.Name
		}
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
			ClientName:       clientName,
			CorrelationID:    correlationID,
			SessionID:        sessionID,
		})
	}
}
