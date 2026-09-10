package router

import (
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

func TestCompositeScorer_PureCostWeight(t *testing.T) {
	scorer := NewCompositeScorer(1.0, 0.0)

	tracker := NewLatencyTracker(0.2)
	tracker.Record("fast-expensive", "m1", 20.0, 50.0)
	tracker.Record("slow-cheap", "m2", 500.0, 1000.0)

	candidates := []CandidateTarget{
		{
			ProviderName: "fast-expensive",
			Model:        "m1",
			Cost:         config.CostConfig{PromptCostPer1M: 50.0},
		},
		{
			ProviderName: "slow-cheap",
			Model:        "m2",
			Cost:         config.CostConfig{PromptCostPer1M: 2.0},
		},
	}

	ranked := scorer.Rank(candidates, ScoringContext{
		EstimatedTokens: 1000,
		LatencyTracker:  tracker,
	})

	if ranked[0].ProviderName != "slow-cheap" {
		t.Errorf("pure cost weighting expected slow-cheap first, got %s", ranked[0].ProviderName)
	}
}

func TestCompositeScorer_PureLatencyWeight(t *testing.T) {
	scorer := NewCompositeScorer(0.0, 1.0)

	tracker := NewLatencyTracker(0.2)
	tracker.Record("fast-expensive", "m1", 20.0, 50.0)
	tracker.Record("slow-cheap", "m2", 500.0, 1000.0)

	candidates := []CandidateTarget{
		{
			ProviderName: "slow-cheap",
			Model:        "m2",
			Cost:         config.CostConfig{PromptCostPer1M: 2.0},
		},
		{
			ProviderName: "fast-expensive",
			Model:        "m1",
			Cost:         config.CostConfig{PromptCostPer1M: 50.0},
		},
	}

	ranked := scorer.Rank(candidates, ScoringContext{
		EstimatedTokens: 1000,
		LatencyTracker:  tracker,
	})

	if ranked[0].ProviderName != "fast-expensive" {
		t.Errorf("pure latency weighting expected fast-expensive first, got %s", ranked[0].ProviderName)
	}
}

func TestCompositeScorer_Balanced(t *testing.T) {
	// Candidate A: very expensive (50), very fast (10ms) -> normCost=1.0, normLat=0.0 -> score = 0.5*1.0 + 0.5*0.0 = 0.50
	// Candidate B: very cheap (2), very slow (1000ms)    -> normCost=0.0, normLat=1.0 -> score = 0.5*0.0 + 0.5*1.0 = 0.50
	// Candidate C: cheap (5), fast (50ms)                -> normCost=0.06, normLat=0.04 -> score = 0.05 (best!)
	scorer := NewCompositeScorer(0.5, 0.5)

	tracker := NewLatencyTracker(0.2)
	tracker.Record("prov-a", "m", 10.0, 20.0)
	tracker.Record("prov-b", "m", 1000.0, 1500.0)
	tracker.Record("prov-c", "m", 50.0, 80.0)

	candidates := []CandidateTarget{
		{ProviderName: "prov-a", Model: "m", Cost: config.CostConfig{PromptCostPer1M: 50.0}},
		{ProviderName: "prov-b", Model: "m", Cost: config.CostConfig{PromptCostPer1M: 2.0}},
		{ProviderName: "prov-c", Model: "m", Cost: config.CostConfig{PromptCostPer1M: 5.0}},
	}

	ranked := scorer.Rank(candidates, ScoringContext{
		EstimatedTokens: 1000,
		LatencyTracker:  tracker,
	})

	if ranked[0].ProviderName != "prov-c" {
		t.Errorf("balanced composite scoring expected prov-c first, got %s", ranked[0].ProviderName)
	}
}

func TestRouter_CompositeStrategyIntegration(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			DefaultStrategy: config.StrategyComposite,
			CostWeight:      0.8,
			LatencyWeight:   0.2,
		},
		Providers: []config.ProviderConfig{
			{
				Name:    "p-exp",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: "http://localhost:8001",
				Enabled: true,
				Cost:    config.CostConfig{PromptCostPer1M: 50.0},
			},
			{
				Name:    "p-cheap",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: "http://localhost:8002",
				Enabled: true,
				Cost:    config.CostConfig{PromptCostPer1M: 1.0},
			},
		},
		Models: map[string]config.ModelRule{
			"model-comp": {
				Strategy: config.StrategyComposite,
				Targets: []config.TargetModel{
					{Provider: "p-exp", Model: "model-comp"},
					{Provider: "p-cheap", Model: "model-comp"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Cost weight is 0.8 so cheap should be preferred
	cands, strat, err := r.ResolveCandidates(&provider.ChatRequest{Model: "model-comp"})
	if err != nil {
		t.Fatalf("failed to resolve candidates: %v", err)
	}

	if strat != config.StrategyComposite {
		t.Errorf("expected strategy composite, got %s", strat)
	}
	if len(cands) != 2 || cands[0].ProviderName != "p-cheap" {
		t.Errorf("expected p-cheap to rank first, got %s", cands[0].ProviderName)
	}
}
