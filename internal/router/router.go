package router

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

type CandidateTarget struct {
	ProviderName string
	Model        string
	Cost         config.CostConfig
	Client       provider.Provider
	Breaker      *CircuitBreaker
}

type Router struct {
	cfg            *config.Config
	database       *db.DB
	providers      map[string]provider.Provider
	breakers       map[string]*CircuitBreaker
	latencyTracker *LatencyTracker
	mu             sync.RWMutex
}

func NewRouter(cfg *config.Config, database *db.DB) (*Router, error) {
	r := &Router{
		cfg:            cfg,
		database:       database,
		providers:      make(map[string]provider.Provider),
		breakers:       make(map[string]*CircuitBreaker),
		latencyTracker: NewLatencyTracker(0.2),
	}

	// Initialize circuit breaker settings from config
	threshold := cfg.CircuitBreaker.FailureThreshold
	if threshold <= 0 {
		threshold = 3
	}
	cooldown := time.Duration(cfg.CircuitBreaker.CooldownSeconds) * time.Second
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}

	// Instantiate providers and circuit breakers
	for _, pCfg := range cfg.Providers {
		if !pCfg.Enabled {
			continue
		}
		p, err := provider.NewProvider(pCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize provider %s: %w", pCfg.Name, err)
		}
		r.providers[pCfg.Name] = p
		r.breakers[pCfg.Name] = NewCircuitBreaker(threshold, cooldown)
	}

	// Preload latency tracker from database if available
	if database != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = r.latencyTracker.LoadFromDB(ctx, database)
	}

	return r, nil
}

// GetProvider returns a provider by name.
func (r *Router) GetProvider(name string) (provider.Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// GetCircuitBreaker returns the circuit breaker for a provider.
func (r *Router) GetCircuitBreaker(name string) (*CircuitBreaker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cb, ok := r.breakers[name]
	return cb, ok
}

// GetLatencyTracker returns the router's latency tracker.
func (r *Router) GetLatencyTracker() *LatencyTracker {
	return r.latencyTracker
}

func (r *Router) findProviderConfig(name string) (config.ProviderConfig, bool) {
	for _, p := range r.cfg.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return config.ProviderConfig{}, false
}

// ResolveCandidates builds and ranks eligible candidates for a given requested model.
func (r *Router) ResolveCandidates(req *provider.ChatRequest) ([]CandidateTarget, config.RoutingStrategy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	model := req.Model
	var targets []config.TargetModel
	var strategy config.RoutingStrategy = r.cfg.Routing.DefaultStrategy
	if strategy == "" {
		strategy = config.StrategyPriority
	}

	// 1. Check explicit model configuration rule
	if rule, exists := r.cfg.Models[model]; exists {
		targets = rule.Targets
		if rule.Strategy != "" {
			strategy = rule.Strategy
		}
	} else {
		// 2. Check providers supporting this model directly
		for name, p := range r.providers {
			if p.SupportsModel(model) {
				targets = append(targets, config.TargetModel{
					Provider: name,
					Model:    model,
				})
			}
		}

		// 3. Fall back to default model rule if no direct support
		if len(targets) == 0 {
			if defRule, ok := r.cfg.Models["default"]; ok {
				targets = defRule.Targets
				if defRule.Strategy != "" {
					strategy = defRule.Strategy
				}
			}
		}
	}

	if len(targets) == 0 {
		return nil, strategy, fmt.Errorf("no provider targets available for model: %s", model)
	}

	// Build candidate list
	var candidates []CandidateTarget
	for _, tm := range targets {
		p, hasProv := r.providers[tm.Provider]
		if !hasProv {
			continue
		}

		cb := r.breakers[tm.Provider]
		pCfg, _ := r.findProviderConfig(tm.Provider)

		costCfg := pCfg.Cost
		if tm.Cost != nil {
			costCfg = *tm.Cost
		}

		candidates = append(candidates, CandidateTarget{
			ProviderName: tm.Provider,
			Model:        tm.Model,
			Cost:         costCfg,
			Client:       p,
			Breaker:      cb,
		})
	}

	if len(candidates) == 0 {
		return nil, strategy, fmt.Errorf("no active providers configured for model: %s", model)
	}

	// Filter by circuit breaker
	var openBreakerCandidates []CandidateTarget
	var eligibleCandidates []CandidateTarget

	for _, c := range candidates {
		if c.Breaker != nil && !c.Breaker.Allow() {
			openBreakerCandidates = append(openBreakerCandidates, c)
		} else {
			eligibleCandidates = append(eligibleCandidates, c)
		}
	}

	// If all candidates are tripped, fallback to all candidates to probe rather than hard-failing
	finalCandidates := eligibleCandidates
	if len(finalCandidates) == 0 {
		finalCandidates = openBreakerCandidates
	}

	// Sort according to strategy
	estTokens := EstimatePromptTokens(req)
	switch strategy {
	case config.StrategyLeastCost:
		sort.SliceStable(finalCandidates, func(i, j int) bool {
			costI := EstimateRequestCost(estTokens, finalCandidates[i].Cost)
			costJ := EstimateRequestCost(estTokens, finalCandidates[j].Cost)
			return costI < costJ
		})

	case config.StrategyLowestLatency:
		sort.SliceStable(finalCandidates, func(i, j int) bool {
			ttftI := r.latencyTracker.GetTTFT(finalCandidates[i].ProviderName, finalCandidates[i].Model)
			ttftJ := r.latencyTracker.GetTTFT(finalCandidates[j].ProviderName, finalCandidates[j].Model)
			return ttftI < ttftJ
		})

	case config.StrategyPriority:
		// Preserves target array order
	}

	return finalCandidates, strategy, nil
}

type ExecutionResult struct {
	Response       *provider.ChatResponse
	Candidate      CandidateTarget
	TotalLatencyMs float64
	FailoverTraces []db.FailoverTrace
}

// Execute attempts to complete a non-streaming chat request with automatic failover.
func (r *Router) Execute(ctx context.Context, req *provider.ChatRequest, requestID string) (*ExecutionResult, error) {
	candidates, _, err := r.ResolveCandidates(req)
	if err != nil {
		return nil, err
	}

	var traces []db.FailoverTrace
	var lastErr error

	for i, cand := range candidates {
		targetReq := *req
		targetReq.Model = cand.Model

		attemptStart := time.Now()
		resp, attemptErr := cand.Client.Send(ctx, &targetReq)
		attemptLatency := float64(time.Since(attemptStart).Milliseconds())

		if attemptErr == nil {
			// Success!
			if cand.Breaker != nil {
				cand.Breaker.RecordSuccess()
			}
			r.latencyTracker.Record(cand.ProviderName, cand.Model, 0, attemptLatency)
			if r.database != nil {
				_ = r.database.UpdateLatencyEMA(ctx, cand.ProviderName, cand.Model, 0, attemptLatency, 0.2, false)
			}

			return &ExecutionResult{
				Response:       resp,
				Candidate:      cand,
				TotalLatencyMs: attemptLatency,
				FailoverTraces: traces,
			}, nil
		}

		// Handle attempt failure
		lastErr = attemptErr
		if cand.Breaker != nil {
			cand.Breaker.RecordFailure(attemptErr)
		}
		if r.database != nil {
			_ = r.database.UpdateLatencyEMA(ctx, cand.ProviderName, cand.Model, 0, attemptLatency, 0.2, true)
		}

		// If there is another candidate to fail over to
		if i+1 < len(candidates) {
			nextCandidate := candidates[i+1]
			trace := db.FailoverTrace{
				RequestID:    requestID,
				Timestamp:    time.Now().UTC(),
				FromProvider: cand.ProviderName,
				ToProvider:   nextCandidate.ProviderName,
				Reason:       attemptErr.Error(),
				LatencyMs:    attemptLatency,
			}
			traces = append(traces, trace)
			if r.database != nil {
				_ = r.database.LogFailover(ctx, &trace)
			}
		}
	}

	return &ExecutionResult{
		FailoverTraces: traces,
	}, fmt.Errorf("all providers failed for model %s: %w", req.Model, lastErr)
}

type StreamResult struct {
	StreamChan     <-chan provider.StreamChunk
	Candidate      CandidateTarget
	FailoverTraces []db.FailoverTrace
}

// ExecuteStream attempts to establish a streaming connection with automatic failover on initial connection failure.
func (r *Router) ExecuteStream(ctx context.Context, req *provider.ChatRequest, requestID string) (*StreamResult, error) {
	candidates, _, err := r.ResolveCandidates(req)
	if err != nil {
		return nil, err
	}

	var traces []db.FailoverTrace
	var lastErr error

	for i, cand := range candidates {
		targetReq := *req
		targetReq.Model = cand.Model

		attemptStart := time.Now()
		streamChan, attemptErr := cand.Client.Stream(ctx, &targetReq)
		attemptLatency := float64(time.Since(attemptStart).Milliseconds())

		if attemptErr == nil {
			return &StreamResult{
				StreamChan:     streamChan,
				Candidate:      cand,
				FailoverTraces: traces,
			}, nil
		}

		// Initial connection failed
		lastErr = attemptErr
		if cand.Breaker != nil {
			cand.Breaker.RecordFailure(attemptErr)
		}
		if r.database != nil {
			_ = r.database.UpdateLatencyEMA(ctx, cand.ProviderName, cand.Model, 0, attemptLatency, 0.2, true)
		}

		if i+1 < len(candidates) {
			nextCandidate := candidates[i+1]
			trace := db.FailoverTrace{
				RequestID:    requestID,
				Timestamp:    time.Now().UTC(),
				FromProvider: cand.ProviderName,
				ToProvider:   nextCandidate.ProviderName,
				Reason:       attemptErr.Error(),
				LatencyMs:    attemptLatency,
			}
			traces = append(traces, trace)
			if r.database != nil {
				_ = r.database.LogFailover(ctx, &trace)
			}
		}
	}

	return nil, fmt.Errorf("all streaming providers failed for model %s: %w", req.Model, lastErr)
}
