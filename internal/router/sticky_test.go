package router

import (
	"context"
	"fmt"
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

func TestConsistentHash(t *testing.T) {
	// 1. Same key always maps to same index
	for i := 0; i < 50; i++ {
		idx1 := ConsistentHash("session-alpha", 4)
		idx2 := ConsistentHash("session-alpha", 4)
		if idx1 != idx2 {
			t.Fatalf("expected identical index for same key, got %d and %d", idx1, idx2)
		}
	}

	// 2. Bound constraints
	for i := 0; i < 100; i++ {
		idx := ConsistentHash(string(rune('a'+i)), 5)
		if idx < 0 || idx >= 5 {
			t.Fatalf("hash index %d out of bounds [0, 5)", idx)
		}
	}

	// 3. Single candidate or empty returns 0
	if idx := ConsistentHash("anything", 1); idx != 0 {
		t.Errorf("expected 0 for n=1, got %d", idx)
	}
	if idx := ConsistentHash("anything", 0); idx != 0 {
		t.Errorf("expected 0 for n=0, got %d", idx)
	}
	if idx := ConsistentHash("", 5); idx != 0 {
		t.Errorf("expected 0 for empty key, got %d", idx)
	}

	// 4. Ensure non-negative under wide variety of strings
	for i := 0; i < 500; i++ {
		idx := ConsistentHash(fmt.Sprintf("complex-key-session-%d-%x", i, i*999999), 7)
		if idx < 0 || idx >= 7 {
			t.Fatalf("index %d out of bounds [0, 7)", idx)
		}
	}
}

func TestStickySessionScorer_Affinity(t *testing.T) {
	candidates := []CandidateTarget{
		{ProviderName: "provider-1", Model: "m1"},
		{ProviderName: "provider-2", Model: "m2"},
		{ProviderName: "provider-3", Model: "m3"},
	}

	scorer := NewStickySessionScorer()

	// 1. Session affinity: repeat 20 times for session A
	var firstChoiceA string
	for i := 0; i < 20; i++ {
		ranked := scorer.Rank(candidates, ScoringContext{SessionID: "sess-user-12345"})
		if len(ranked) != len(candidates) {
			t.Fatalf("expected %d candidates, got %d", len(candidates), len(ranked))
		}
		if i == 0 {
			firstChoiceA = ranked[0].ProviderName
		} else if ranked[0].ProviderName != firstChoiceA {
			t.Fatalf("iteration %d: sticky routing changed from %s to %s for session A",
				i, firstChoiceA, ranked[0].ProviderName)
		}
	}

	// 2. Session affinity for session B
	var firstChoiceB string
	for i := 0; i < 20; i++ {
		ranked := scorer.Rank(candidates, ScoringContext{SessionID: "sess-user-99999"})
		if i == 0 {
			firstChoiceB = ranked[0].ProviderName
		} else if ranked[0].ProviderName != firstChoiceB {
			t.Fatalf("iteration %d: sticky routing changed from %s to %s for session B",
				i, firstChoiceB, ranked[0].ProviderName)
		}
	}

	// 3. Fallback to req.User
	reqUser := &provider.ChatRequest{User: "user-tenant-xyz"}
	rankedUser := scorer.Rank(candidates, ScoringContext{Request: reqUser})
	if len(rankedUser) != len(candidates) {
		t.Fatalf("expected full candidate list")
	}
	for i := 0; i < 10; i++ {
		repeat := scorer.Rank(candidates, ScoringContext{Request: reqUser})
		if repeat[0].ProviderName != rankedUser[0].ProviderName {
			t.Fatalf("sticky user routing changed unexpectedly")
		}
	}
}

func TestRouter_StickySessionResolution(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{Name: "p1", Type: config.ProviderTypeOpenAI, BaseURL: "https://p1.example.com", Enabled: true, Models: []string{"gpt-4o"}},
			{Name: "p2", Type: config.ProviderTypeOpenAI, BaseURL: "https://p2.example.com", Enabled: true, Models: []string{"gpt-4o"}},
			{Name: "p3", Type: config.ProviderTypeOpenAI, BaseURL: "https://p3.example.com", Enabled: true, Models: []string{"gpt-4o"}},
		},
		Models: map[string]config.ModelRule{
			"gpt-4o": {
				Strategy: config.StrategyStickySession,
				Targets: []config.TargetModel{
					{Provider: "p1", Model: "gpt-4o"},
					{Provider: "p2", Model: "gpt-4o"},
					{Provider: "p3", Model: "gpt-4o"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	req := &provider.ChatRequest{Model: "gpt-4o"}
	ctx := ContextWithSessionID(context.Background(), "persistent-session-42")

	var initialProvider string
	for i := 0; i < 10; i++ {
		cands, strat, err := r.ResolveCandidatesWithContext(ctx, req)
		if err != nil {
			t.Fatalf("resolution failed: %v", err)
		}
		if strat != config.StrategyStickySession {
			t.Fatalf("expected strategy sticky-session, got %s", strat)
		}
		if i == 0 {
			initialProvider = cands[0].ProviderName
		} else if cands[0].ProviderName != initialProvider {
			t.Fatalf("expected sticky provider %s, got %s", initialProvider, cands[0].ProviderName)
		}
	}
}
