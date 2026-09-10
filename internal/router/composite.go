package router

import (
	"math"
	"sort"
)

// CompositeScorer ranks candidates using a combined multi-objective cost and latency score.
// Score = (wCost * NormalizedCost) + (wLatency * NormalizedLatency).
// Lower score is ranked first.
type CompositeScorer struct {
	CostWeight    float64
	LatencyWeight float64
}

// NewCompositeScorer creates a new multi-objective composite scorer.
func NewCompositeScorer(costWeight, latencyWeight float64) *CompositeScorer {
	if costWeight <= 0 && latencyWeight <= 0 {
		costWeight = 0.5
		latencyWeight = 0.5
	}
	totalW := costWeight + latencyWeight
	if totalW > 0 {
		costWeight /= totalW
		latencyWeight /= totalW
	}
	return &CompositeScorer{
		CostWeight:    costWeight,
		LatencyWeight: latencyWeight,
	}
}

// Rank scores each candidate using min-max normalized cost and latency and ranks them in ascending score order.
func (s *CompositeScorer) Rank(candidates []CandidateTarget, ctx ScoringContext) []CandidateTarget {
	if len(candidates) <= 1 {
		ranked := make([]CandidateTarget, len(candidates))
		copy(ranked, candidates)
		return ranked
	}

	costs := make([]float64, len(candidates))
	latencies := make([]float64, len(candidates))

	minCost, maxCost := math.MaxFloat64, -math.MaxFloat64
	minLat, maxLat := math.MaxFloat64, -math.MaxFloat64

	tokens := ctx.EstimatedTokens
	if tokens <= 0 {
		tokens = 1000
	}

	for i, c := range candidates {
		// Estimated cost for prompt tokens
		cVal := EstimateRequestCost(tokens, c.Cost)
		costs[i] = cVal
		if cVal < minCost {
			minCost = cVal
		}
		if cVal > maxCost {
			maxCost = cVal
		}

		// Historical TTFT EMA latency
		lVal := 100.0 // default fallback TTFT in ms if no history
		if ctx.LatencyTracker != nil {
			tracked := ctx.LatencyTracker.GetTTFT(c.ProviderName, c.Model)
			if tracked > 0 {
				lVal = tracked
			}
		}
		latencies[i] = lVal
		if lVal < minLat {
			minLat = lVal
		}
		if lVal > maxLat {
			maxLat = lVal
		}
	}

	costRange := maxCost - minCost
	latRange := maxLat - minLat

	type scoredCandidate struct {
		candidate CandidateTarget
		score     float64
	}

	scored := make([]scoredCandidate, len(candidates))
	for i, c := range candidates {
		normCost := 0.0
		if costRange > 1e-9 {
			normCost = (costs[i] - minCost) / costRange
		}

		normLat := 0.0
		if latRange > 1e-9 {
			normLat = (latencies[i] - minLat) / latRange
		}

		score := (s.CostWeight * normCost) + (s.LatencyWeight * normLat)
		scored[i] = scoredCandidate{
			candidate: c,
			score:     score,
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score < scored[j].score
	})

	ranked := make([]CandidateTarget, len(candidates))
	for i, sc := range scored {
		ranked[i] = sc.candidate
	}
	return ranked
}
