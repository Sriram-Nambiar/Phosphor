package router

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
	"github.com/Sriram-Nambiar/Phosphor/internal/security"
)

type CandidateTarget struct {
	ProviderName string
	Model        string
	Cost         config.CostConfig
	Client       provider.Provider
	Breaker      *CircuitBreaker
	BreakerState CircuitState
	Weight       int
	Capabilities []string
}

// SupportsCapability checks if the candidate target supports the required capability.
// If capabilities are empty (unannotated), it returns true for backward compatibility.
func (c CandidateTarget) SupportsCapability(capName string) bool {
	if len(c.Capabilities) == 0 {
		return true
	}
	target := strings.ToLower(strings.TrimSpace(capName))
	for _, declared := range c.Capabilities {
		d := strings.ToLower(strings.TrimSpace(declared))
		if d == target {
			return true
		}
		if (target == "tools" && (d == "function_calling" || d == "functions")) ||
			(target == "vision" && (d == "multimodal" || d == "image")) ||
			(target == "json_mode" && (d == "json" || d == "json_object")) {
			return true
		}
	}
	return false
}

type Router struct {
	cfg            *config.Config
	database       *db.DB
	providers      map[string]provider.Provider
	breakers       map[string]*CircuitBreaker
	semaphores     map[string]chan struct{}
	latencyTracker *LatencyTracker
	scorers        map[config.RoutingStrategy]CandidateScorer
	mu             sync.RWMutex
}

func NewRouter(cfg *config.Config, database *db.DB) (*Router, error) {
	r := &Router{
		cfg:            cfg,
		database:       database,
		providers:      make(map[string]provider.Provider),
		breakers:       make(map[string]*CircuitBreaker),
		semaphores:     make(map[string]chan struct{}),
		latencyTracker: NewLatencyTracker(0.2),
		scorers:        DefaultScorerRegistry(),
	}

	// Configure custom composite scorer if specific weights are defined
	if cfg.Routing.CostWeight > 0 || cfg.Routing.LatencyWeight > 0 {
		comp := NewCompositeScorer(cfg.Routing.CostWeight, cfg.Routing.LatencyWeight)
		r.scorers[config.StrategyComposite] = comp
		r.scorers["composite"] = comp
		r.scorers["balanced"] = comp
		r.scorers["cost-latency"] = comp
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

	// Instantiate providers, circuit breakers, and concurrency semaphores
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
		if pCfg.MaxConcurrency > 0 {
			r.semaphores[pCfg.Name] = make(chan struct{}, pCfg.MaxConcurrency)
		}
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

// RegisterScorer registers a candidate scorer for a routing strategy.
func (r *Router) RegisterScorer(strategy config.RoutingStrategy, s CandidateScorer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scorers[strategy] = s
}

// GetScorer retrieves the candidate scorer for a routing strategy, falling back to PriorityScorer.
func (r *Router) GetScorer(strategy config.RoutingStrategy) CandidateScorer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.scorers[strategy]; ok && s != nil {
		return s
	}
	return &PriorityScorer{}
}

// ProviderStatuses returns a snapshot map of provider names and their availability states.
func (r *Router) ProviderStatuses() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	statuses := make(map[string]string, len(r.providers))
	for name := range r.providers {
		cb, ok := r.breakers[name]
		if ok && cb != nil && !cb.Allow() {
			statuses[name] = "circuit_open"
		} else if sem, ok := r.semaphores[name]; ok && sem != nil && len(sem) >= cap(sem) {
			statuses[name] = "busy"
		} else {
			statuses[name] = "available"
		}
	}
	return statuses
}

// TryAcquireConcurrency attempts to acquire an active execution slot for a provider.
// Returns a release closure and true if acquired (or if unconstrained); nil and false if busy.
func (r *Router) TryAcquireConcurrency(provider string) (func(), bool) {
	r.mu.RLock()
	sem, exists := r.semaphores[provider]
	r.mu.RUnlock()

	if !exists || sem == nil {
		return func() {}, true
	}

	select {
	case sem <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-sem
			})
		}, true
	default:
		return nil, false
	}
}

// GetConcurrency returns the currently active slots and maximum capacity for a provider.
func (r *Router) GetConcurrency(provider string) (active int, max int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sem, exists := r.semaphores[provider]
	if !exists || sem == nil {
		return 0, 0
	}
	return len(sem), cap(sem)
}

func (r *Router) findProviderConfig(name string) (config.ProviderConfig, bool) {
	for _, p := range r.cfg.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return config.ProviderConfig{}, false
}

// buildCandidatesForTargets converts target models to candidate targets, populating cost, circuit breakers, and capabilities.
func (r *Router) buildCandidatesForTargets(targets []config.TargetModel) []CandidateTarget {
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

		weight := tm.Weight
		if weight <= 0 {
			weight = 1
		}

		caps := tm.Capabilities
		if len(caps) == 0 {
			caps = pCfg.Capabilities
		}

		var bState CircuitState = StateClosed
		if cb != nil {
			bState = cb.GetState()
		}

		candidates = append(candidates, CandidateTarget{
			ProviderName: tm.Provider,
			Model:        tm.Model,
			Cost:         costCfg,
			Client:       p,
			Breaker:      cb,
			BreakerState: bState,
			Weight:       weight,
			Capabilities: caps,
		})
	}
	return candidates
}

// filterAndRankCandidates filters candidates by capabilities and ranks them using the strategy scorer with breaker cooldown penalty.
func (r *Router) filterAndRankCandidates(candidates []CandidateTarget, req *provider.ChatRequest, strategy config.RoutingStrategy) []CandidateTarget {
	reqVision := req.RequiresVision()
	reqTools := req.RequiresTools()
	reqJSON := req.RequiresJSONMode()

	var capable []CandidateTarget
	for _, c := range candidates {
		if reqVision && !c.SupportsCapability("vision") {
			continue
		}
		if reqTools && !c.SupportsCapability("tools") {
			continue
		}
		if reqJSON && !c.SupportsCapability("json_mode") {
			continue
		}
		capable = append(capable, c)
	}
	if len(capable) == 0 {
		return nil
	}

	var closedCandidates []CandidateTarget
	var halfOpenCandidates []CandidateTarget
	var openCandidates []CandidateTarget

	for _, c := range capable {
		if c.Breaker == nil {
			closedCandidates = append(closedCandidates, c)
		} else {
			state := c.Breaker.GetState()
			c.BreakerState = state
			switch state {
			case StateClosed:
				closedCandidates = append(closedCandidates, c)
			case StateHalfOpen:
				halfOpenCandidates = append(halfOpenCandidates, c)
			case StateOpen:
				openCandidates = append(openCandidates, c)
			default:
				closedCandidates = append(closedCandidates, c)
			}
		}
	}

	estTokens := EstimatePromptTokens(req)
	scorer := r.GetScorer(strategy)
	sCtx := ScoringContext{
		Request:         req,
		EstimatedTokens: estTokens,
		LatencyTracker:  r.latencyTracker,
	}

	var ranked []CandidateTarget
	if len(closedCandidates) > 0 {
		ranked = append(ranked, scorer.Rank(closedCandidates, sCtx)...)
	}
	if len(halfOpenCandidates) > 0 {
		ranked = append(ranked, scorer.Rank(halfOpenCandidates, sCtx)...)
	}
	if len(ranked) == 0 && len(openCandidates) > 0 {
		ranked = append(ranked, scorer.Rank(openCandidates, sCtx)...)
	}
	return ranked
}

// ResolveCandidates builds and ranks eligible candidates for a given requested model, including cross-family fallbacks.
func (r *Router) ResolveCandidates(req *provider.ChatRequest) ([]CandidateTarget, config.RoutingStrategy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	model := req.Model
	var targets []config.TargetModel
	var fallbacks []string
	var strategy config.RoutingStrategy = r.cfg.Routing.DefaultStrategy
	if strategy == "" {
		strategy = config.StrategyPriority
	}

	// 1. Check explicit model configuration rule
	if rule, exists := r.cfg.Models[model]; exists {
		targets = rule.Targets
		fallbacks = rule.Fallbacks
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
				fallbacks = defRule.Fallbacks
				if defRule.Strategy != "" {
					strategy = defRule.Strategy
				}
			}
		}
	}

	if len(fallbacks) == 0 {
		fallbacks = r.cfg.Routing.DefaultFallbacks
	}

	primaryCandidates := r.buildCandidatesForTargets(targets)
	rankedPrimary := r.filterAndRankCandidates(primaryCandidates, req, strategy)

	seen := make(map[string]bool)
	var finalCandidates []CandidateTarget

	for _, c := range rankedPrimary {
		key := candidateKey(c)
		seen[key] = true
		finalCandidates = append(finalCandidates, c)
	}

	// Resolve fallback model groups if configured
	for _, fbModel := range fallbacks {
		if fbModel == model {
			continue
		}
		var fbTargets []config.TargetModel
		if fbRule, ok := r.cfg.Models[fbModel]; ok {
			fbTargets = fbRule.Targets
		} else {
			for name, p := range r.providers {
				if p.SupportsModel(fbModel) {
					fbTargets = append(fbTargets, config.TargetModel{
						Provider: name,
						Model:    fbModel,
					})
				}
			}
		}
		fbCandidates := r.buildCandidatesForTargets(fbTargets)
		rankedFB := r.filterAndRankCandidates(fbCandidates, req, strategy)
		for _, c := range rankedFB {
			key := candidateKey(c)
			if !seen[key] {
				seen[key] = true
				finalCandidates = append(finalCandidates, c)
			}
		}
	}

	if len(finalCandidates) == 0 {
		if len(primaryCandidates) == 0 && len(fallbacks) == 0 {
			return nil, strategy, fmt.Errorf("no provider targets available for model: %s", model)
		}
		return nil, strategy, fmt.Errorf("no available provider targets for model '%s' (including %d fallback groups) satisfy requirements", model, len(fallbacks))
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
		release, acquired := r.TryAcquireConcurrency(cand.ProviderName)
		if !acquired {
			if i+1 < len(candidates) {
				nextCandidate := candidates[i+1]
				trace := db.FailoverTrace{
					RequestID:    requestID,
					Timestamp:    time.Now().UTC(),
					FromProvider: cand.ProviderName,
					ToProvider:   nextCandidate.ProviderName,
					Reason:       fmt.Sprintf("concurrency limit reached for provider %s", cand.ProviderName),
					LatencyMs:    0,
				}
				traces = append(traces, trace)
				if r.database != nil {
					_ = r.database.LogFailover(ctx, &trace)
				}
			}
			lastErr = fmt.Errorf("concurrency limit reached for provider %s", cand.ProviderName)
			continue
		}

		targetReq := *req
		targetReq.Model = cand.Model

		attemptStart := time.Now()
		resp, attemptErr := cand.Client.Send(ctx, &targetReq)
		attemptLatency := float64(time.Since(attemptStart).Milliseconds())
		release()

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
				Reason:       security.RedactText(attemptErr.Error()),
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
		release, acquired := r.TryAcquireConcurrency(cand.ProviderName)
		if !acquired {
			if i+1 < len(candidates) {
				nextCandidate := candidates[i+1]
				trace := db.FailoverTrace{
					RequestID:    requestID,
					Timestamp:    time.Now().UTC(),
					FromProvider: cand.ProviderName,
					ToProvider:   nextCandidate.ProviderName,
					Reason:       fmt.Sprintf("concurrency limit reached for provider %s", cand.ProviderName),
					LatencyMs:    0,
				}
				traces = append(traces, trace)
				if r.database != nil {
					_ = r.database.LogFailover(ctx, &trace)
				}
			}
			lastErr = fmt.Errorf("concurrency limit reached for provider %s", cand.ProviderName)
			continue
		}

		targetReq := *req
		targetReq.Model = cand.Model

		attemptStart := time.Now()
		streamChan, attemptErr := cand.Client.Stream(ctx, &targetReq)
		attemptLatency := float64(time.Since(attemptStart).Milliseconds())

		if attemptErr == nil {
			wrappedChan := make(chan provider.StreamChunk)
			go func() {
				defer release()
				for chunk := range streamChan {
					wrappedChan <- chunk
				}
				close(wrappedChan)
			}()

			return &StreamResult{
				StreamChan:     wrappedChan,
				Candidate:      cand,
				FailoverTraces: traces,
			}, nil
		}

		release()

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
				Reason:       security.RedactText(attemptErr.Error()),
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
