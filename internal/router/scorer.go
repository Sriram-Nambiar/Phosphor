package router

import (
	"sort"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

// ScoringContext provides contextual metadata for candidate scoring and ranking.
type ScoringContext struct {
	Request         *provider.ChatRequest
	EstimatedTokens int
	LatencyTracker  *LatencyTracker
}

// CandidateScorer defines the interface for ranking candidates based on routing strategies.
type CandidateScorer interface {
	Rank(candidates []CandidateTarget, ctx ScoringContext) []CandidateTarget
}

// PriorityScorer preserves the explicit priority order defined in the configuration.
type PriorityScorer struct{}

func (s *PriorityScorer) Rank(candidates []CandidateTarget, ctx ScoringContext) []CandidateTarget {
	ranked := make([]CandidateTarget, len(candidates))
	copy(ranked, candidates)
	return ranked
}

// LeastCostScorer ranks candidates in ascending order of estimated request cost.
type LeastCostScorer struct{}

func (s *LeastCostScorer) Rank(candidates []CandidateTarget, ctx ScoringContext) []CandidateTarget {
	ranked := make([]CandidateTarget, len(candidates))
	copy(ranked, candidates)
	tokens := ctx.EstimatedTokens
	if tokens <= 0 {
		tokens = 1000
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		costI := EstimateRequestCost(tokens, ranked[i].Cost)
		costJ := EstimateRequestCost(tokens, ranked[j].Cost)
		return costI < costJ
	})
	return ranked
}

// LowestLatencyScorer ranks candidates in ascending order of exponential moving average TTFT.
type LowestLatencyScorer struct{}

func (s *LowestLatencyScorer) Rank(candidates []CandidateTarget, ctx ScoringContext) []CandidateTarget {
	ranked := make([]CandidateTarget, len(candidates))
	copy(ranked, candidates)
	if ctx.LatencyTracker == nil {
		return ranked
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		ttftI := ctx.LatencyTracker.GetTTFT(ranked[i].ProviderName, ranked[i].Model)
		ttftJ := ctx.LatencyTracker.GetTTFT(ranked[j].ProviderName, ranked[j].Model)
		return ttftI < ttftJ
	})
	return ranked
}

// DefaultScorerRegistry returns standard scorers for built-in strategies.
func DefaultScorerRegistry() map[config.RoutingStrategy]CandidateScorer {
	wrr := NewWeightedRoundRobinScorer()
	composite := NewCompositeScorer(0.5, 0.5)
	return map[config.RoutingStrategy]CandidateScorer{
		config.StrategyPriority:           &PriorityScorer{},
		config.StrategyLeastCost:          &LeastCostScorer{},
		config.StrategyLowestLatency:      &LowestLatencyScorer{},
		config.StrategyRoundRobin:         wrr,
		config.StrategyWeightedRoundRobin: wrr,
		config.StrategyComposite:          composite,
		"balanced":                        composite,
		"cost-latency":                    composite,
		"round_robin":                     wrr,
		"weighted_round_robin":            wrr,
	}
}
