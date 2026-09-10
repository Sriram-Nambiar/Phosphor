package router

import (
	"sync"
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

func TestSmoothWeightedRoundRobin_Uniform(t *testing.T) {
	scorer := NewWeightedRoundRobinScorer()

	candidates := []CandidateTarget{
		{ProviderName: "p1", Model: "m1", Weight: 1},
		{ProviderName: "p2", Model: "m2", Weight: 1},
		{ProviderName: "p3", Model: "m3", Weight: 1},
	}

	expectedWinners := []string{"p1", "p2", "p3", "p1", "p2", "p3"}
	for i, expected := range expectedWinners {
		ranked := scorer.Rank(candidates, ScoringContext{})
		if len(ranked) != 3 {
			t.Fatalf("iteration %d: expected 3 ranked candidates, got %d", i, len(ranked))
		}
		if ranked[0].ProviderName != expected {
			t.Errorf("iteration %d: expected primary winner %s, got %s", i, expected, ranked[0].ProviderName)
		}
	}
}

func TestSmoothWeightedRoundRobin_Weighted(t *testing.T) {
	scorer := NewWeightedRoundRobinScorer()

	candidates := []CandidateTarget{
		{ProviderName: "fast-a", Model: "gpt-4o", Weight: 3},
		{ProviderName: "slow-b", Model: "gpt-4o", Weight: 1},
	}

	counts := make(map[string]int)
	iterations := 40
	for i := 0; i < iterations; i++ {
		ranked := scorer.Rank(candidates, ScoringContext{})
		if len(ranked) != 2 {
			t.Fatalf("expected 2 candidates, got %d", len(ranked))
		}
		counts[ranked[0].ProviderName]++
	}

	// For weights 3:1 over 40 iterations, fast-a must win 30 times, slow-b 10 times
	if counts["fast-a"] != 30 {
		t.Errorf("expected 30 picks for fast-a, got %d", counts["fast-a"])
	}
	if counts["slow-b"] != 10 {
		t.Errorf("expected 10 picks for slow-b, got %d", counts["slow-b"])
	}
}

func TestSmoothWeightedRoundRobin_Concurrency(t *testing.T) {
	scorer := NewWeightedRoundRobinScorer()

	candidates := []CandidateTarget{
		{ProviderName: "p1", Model: "m1", Weight: 2},
		{ProviderName: "p2", Model: "m2", Weight: 1},
	}

	var wg sync.WaitGroup
	workers := 50
	reqsPerWorker := 20

	counts := make([]map[string]int, workers)
	for i := 0; i < workers; i++ {
		counts[i] = make(map[string]int)
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < reqsPerWorker; j++ {
				ranked := scorer.Rank(candidates, ScoringContext{})
				counts[workerID][ranked[0].ProviderName]++
			}
		}(i)
	}

	wg.Wait()

	totalP1 := 0
	totalP2 := 0
	for _, c := range counts {
		totalP1 += c["p1"]
		totalP2 += c["p2"]
	}

	total := totalP1 + totalP2
	if total != workers*reqsPerWorker {
		t.Fatalf("expected total %d, got %d", workers*reqsPerWorker, total)
	}
	// Ratio should be close to 2:1
	if totalP1 <= totalP2 {
		t.Errorf("expected p1 (%d) to receive roughly double of p2 (%d)", totalP1, totalP2)
	}
}

func TestRouter_WeightedRoundRobinStrategyIntegration(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{DefaultStrategy: config.StrategyWeightedRoundRobin},
		Providers: []config.ProviderConfig{
			{Name: "p1", Type: config.ProviderTypeOpenAI, BaseURL: "http://localhost:8001", Enabled: true},
			{Name: "p2", Type: config.ProviderTypeOpenAI, BaseURL: "http://localhost:8002", Enabled: true},
		},
		Models: map[string]config.ModelRule{
			"gpt-test": {
				Strategy: config.StrategyWeightedRoundRobin,
				Targets: []config.TargetModel{
					{Provider: "p1", Model: "gpt-test", Weight: 2},
					{Provider: "p2", Model: "gpt-test", Weight: 1},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	picks := make(map[string]int)
	for i := 0; i < 30; i++ {
		cands, strat, err := r.ResolveCandidates(&provider.ChatRequest{Model: "gpt-test"})
		if err != nil {
			t.Fatalf("failed to resolve candidates: %v", err)
		}
		if strat != config.StrategyWeightedRoundRobin {
			t.Errorf("expected strategy weighted-round-robin, got %s", strat)
		}
		picks[cands[0].ProviderName]++
	}

	if picks["p1"] != 20 || picks["p2"] != 10 {
		t.Errorf("expected 20 picks for p1 and 10 for p2, got: %+v", picks)
	}
}
