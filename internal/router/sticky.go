package router

import (
	"hash/fnv"
)

// ConsistentHash maps a key string to an index in [0, n) using 32-bit FNV-1a.
func ConsistentHash(key string, n int) int {
	if n <= 1 || key == "" {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(n))
}

// StickySessionScorer ranks candidates deterministically based on session ID consistent hashing.
// Consecutive requests sharing the same session ID will prioritize the same candidate, preserving
// upstream KV cache affinity and reducing cold start latencies.
type StickySessionScorer struct{}

// NewStickySessionScorer initializes a new StickySessionScorer.
func NewStickySessionScorer() *StickySessionScorer {
	return &StickySessionScorer{}
}

// Rank orders candidates cyclically starting with the candidate selected by consistent hashing.
func (s *StickySessionScorer) Rank(candidates []CandidateTarget, ctx ScoringContext) []CandidateTarget {
	n := len(candidates)
	if n <= 1 {
		return candidates
	}

	sessionKey := ctx.SessionID
	if sessionKey == "" && ctx.Request != nil && ctx.Request.User != "" {
		sessionKey = ctx.Request.User
	}
	if sessionKey == "" && ctx.Request != nil && len(ctx.Request.Messages) > 0 {
		sessionKey = ctx.Request.Messages[0].Content
	}

	if sessionKey == "" {
		ranked := make([]CandidateTarget, n)
		copy(ranked, candidates)
		return ranked
	}

	primaryIdx := ConsistentHash(sessionKey, n)

	ranked := make([]CandidateTarget, 0, n)
	for i := 0; i < n; i++ {
		idx := (primaryIdx + i) % n
		ranked = append(ranked, candidates[idx])
	}
	return ranked
}
