package router

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// RoundRobinScorer implements lock-free atomic round-robin candidate distribution.
type RoundRobinScorer struct {
	counter atomic.Uint64
}

// NewRoundRobinScorer initializes a lock-free atomic round-robin scorer.
func NewRoundRobinScorer() *RoundRobinScorer {
	return &RoundRobinScorer{}
}

// Rank orders candidates in a rotating circular sequence using an atomic counter.
func (s *RoundRobinScorer) Rank(candidates []CandidateTarget, ctx ScoringContext) []CandidateTarget {
	n := len(candidates)
	if n <= 1 {
		ranked := make([]CandidateTarget, n)
		copy(ranked, candidates)
		return ranked
	}

	idx := int(s.counter.Add(1)-1) % n
	ranked := make([]CandidateTarget, n)
	for i := 0; i < n; i++ {
		ranked[i] = candidates[(idx+i)%n]
	}
	return ranked
}

// WeightedRoundRobinScorer implements the Nginx smooth weighted round-robin (SWRR) algorithm.
// It evenly interleaves candidates across multiple requests based on assigned weights without clumping.
type WeightedRoundRobinScorer struct {
	mu     sync.Mutex
	states map[string]map[string]int // poolKey -> (candidateKey -> currentWeight)
}

// NewWeightedRoundRobinScorer initializes a thread-safe smooth weighted round-robin scorer.
func NewWeightedRoundRobinScorer() *WeightedRoundRobinScorer {
	return &WeightedRoundRobinScorer{
		states: make(map[string]map[string]int),
	}
}

func candidateKey(c CandidateTarget) string {
	return fmt.Sprintf("%s::%s", c.ProviderName, c.Model)
}

func poolKey(candidates []CandidateTarget) string {
	keys := make([]string, len(candidates))
	for i, c := range candidates {
		keys[i] = candidateKey(c)
	}
	return strings.Join(keys, "|")
}

// Rank orders candidates using the smooth weighted round-robin algorithm.
func (s *WeightedRoundRobinScorer) Rank(candidates []CandidateTarget, ctx ScoringContext) []CandidateTarget {
	if len(candidates) <= 1 {
		ranked := make([]CandidateTarget, len(candidates))
		copy(ranked, candidates)
		return ranked
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	pKey := poolKey(candidates)
	cw, exists := s.states[pKey]
	if !exists {
		cw = make(map[string]int)
		s.states[pKey] = cw
	}

	totalWeight := 0
	for _, c := range candidates {
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
		cw[candidateKey(c)] += w
	}

	ranked := make([]CandidateTarget, 0, len(candidates))
	available := make([]CandidateTarget, len(candidates))
	copy(available, candidates)

	// Pick primary candidate with maximum current weight
	bestIdx := 0
	bestVal := -1 << 31
	for i, c := range available {
		val := cw[candidateKey(c)]
		if val > bestVal {
			bestVal = val
			bestIdx = i
		}
	}

	best := available[bestIdx]
	cw[candidateKey(best)] -= totalWeight
	ranked = append(ranked, best)

	// Remove selected candidate from available pool
	available = append(available[:bestIdx], available[bestIdx+1:]...)

	// Rank remaining candidates for failover sequence in descending order of currentWeight
	for len(available) > 0 {
		maxIdx := 0
		maxVal := -1 << 31
		for i, c := range available {
			val := cw[candidateKey(c)]
			if val > maxVal {
				maxVal = val
				maxIdx = i
			}
		}
		ranked = append(ranked, available[maxIdx])
		available = append(available[:maxIdx], available[maxIdx+1:]...)
	}

	return ranked
}
