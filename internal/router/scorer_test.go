package router

import (
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

func TestCandidateScorer_Priority(t *testing.T) {
	candidates := []CandidateTarget{
		{ProviderName: "p1", Model: "m1"},
		{ProviderName: "p2", Model: "m2"},
		{ProviderName: "p3", Model: "m3"},
	}

	scorer := &PriorityScorer{}
	ranked := scorer.Rank(candidates, ScoringContext{})

	if len(ranked) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(ranked))
	}
	if ranked[0].ProviderName != "p1" || ranked[1].ProviderName != "p2" || ranked[2].ProviderName != "p3" {
		t.Errorf("priority scorer altered candidate order: %+v", ranked)
	}
}

func TestCandidateScorer_LeastCost(t *testing.T) {
	candidates := []CandidateTarget{
		{
			ProviderName: "expensive",
			Model:        "m1",
			Cost:         config.CostConfig{PromptCostPer1M: 30.0},
		},
		{
			ProviderName: "cheap",
			Model:        "m2",
			Cost:         config.CostConfig{PromptCostPer1M: 2.0},
		},
		{
			ProviderName: "medium",
			Model:        "m3",
			Cost:         config.CostConfig{PromptCostPer1M: 10.0},
		},
	}

	scorer := &LeastCostScorer{}
	ranked := scorer.Rank(candidates, ScoringContext{EstimatedTokens: 1000})

	if ranked[0].ProviderName != "cheap" {
		t.Errorf("expected cheap to be ranked first, got %s", ranked[0].ProviderName)
	}
	if ranked[1].ProviderName != "medium" {
		t.Errorf("expected medium to be ranked second, got %s", ranked[1].ProviderName)
	}
	if ranked[2].ProviderName != "expensive" {
		t.Errorf("expected expensive to be ranked third, got %s", ranked[2].ProviderName)
	}
}

func TestCandidateScorer_LowestLatency(t *testing.T) {
	tracker := NewLatencyTracker(0.2)
	tracker.Record("slow", "m1", 500.0, 1000.0)
	tracker.Record("fast", "m2", 50.0, 100.0)
	tracker.Record("medium", "m3", 200.0, 400.0)

	candidates := []CandidateTarget{
		{ProviderName: "slow", Model: "m1"},
		{ProviderName: "fast", Model: "m2"},
		{ProviderName: "medium", Model: "m3"},
	}

	scorer := &LowestLatencyScorer{}
	ranked := scorer.Rank(candidates, ScoringContext{LatencyTracker: tracker})

	if ranked[0].ProviderName != "fast" {
		t.Errorf("expected fast to be ranked first, got %s", ranked[0].ProviderName)
	}
	if ranked[1].ProviderName != "medium" {
		t.Errorf("expected medium to be ranked second, got %s", ranked[1].ProviderName)
	}
	if ranked[2].ProviderName != "slow" {
		t.Errorf("expected slow to be ranked third, got %s", ranked[2].ProviderName)
	}
}

// ReverseScorer is a custom scorer for testing RegisterScorer
type ReverseScorer struct{}

func (s *ReverseScorer) Rank(candidates []CandidateTarget, ctx ScoringContext) []CandidateTarget {
	ranked := make([]CandidateTarget, len(candidates))
	for i, c := range candidates {
		ranked[len(candidates)-1-i] = c
	}
	return ranked
}

func TestRouter_CustomScorerRegistration(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{DefaultStrategy: "custom_reverse"},
		Providers: []config.ProviderConfig{
			{Name: "p1", Type: config.ProviderTypeOpenAI, BaseURL: "http://localhost:8001", Enabled: true},
			{Name: "p2", Type: config.ProviderTypeOpenAI, BaseURL: "http://localhost:8002", Enabled: true},
		},
		Models: map[string]config.ModelRule{
			"test-model": {
				Strategy: "custom_reverse",
				Targets: []config.TargetModel{
					{Provider: "p1", Model: "test-model"},
					{Provider: "p2", Model: "test-model"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	r.RegisterScorer("custom_reverse", &ReverseScorer{})

	candidates, strat, err := r.ResolveCandidates(&provider.ChatRequest{Model: "test-model"})
	if err != nil {
		t.Fatalf("failed to resolve candidates: %v", err)
	}

	if strat != "custom_reverse" {
		t.Errorf("expected strategy custom_reverse, got %s", strat)
	}
	if len(candidates) != 2 || candidates[0].ProviderName != "p2" || candidates[1].ProviderName != "p1" {
		t.Errorf("expected candidates reversed [p2, p1], got %+v", candidates)
	}
}
