package router

import (
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

func TestRouter_CapabilityFiltering_Tools(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{DefaultStrategy: config.StrategyPriority},
		Providers: []config.ProviderConfig{
			{
				Name:    "prov-text-only",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: "http://localhost:8001",
				Enabled: true,
				Capabilities: []string{"text"},
			},
			{
				Name:    "prov-tool-agent",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: "http://localhost:8002",
				Enabled: true,
				Capabilities: []string{"tools", "json_mode"},
			},
		},
		Models: map[string]config.ModelRule{
			"model-cap": {
				Targets: []config.TargetModel{
					{Provider: "prov-text-only", Model: "model-cap"},
					{Provider: "prov-tool-agent", Model: "model-cap"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to initialize router: %v", err)
	}

	// 1. Plain text request routes to prov-text-only (first in priority)
	plainReq := &provider.ChatRequest{
		Model: "model-cap",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Hello world"},
		},
	}
	plainCands, _, err := r.ResolveCandidates(plainReq)
	if err != nil {
		t.Fatalf("failed to resolve plain candidates: %v", err)
	}
	if plainCands[0].ProviderName != "prov-text-only" {
		t.Errorf("expected plain request to hit first priority prov-text-only, got %s", plainCands[0].ProviderName)
	}

	// 2. Request with tools must skip prov-text-only and pick prov-tool-agent!
	toolReq := &provider.ChatRequest{
		Model: "model-cap",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "What is the weather?"},
		},
		Tools: []interface{}{
			map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name": "get_weather",
				},
			},
		},
	}
	toolCands, _, err := r.ResolveCandidates(toolReq)
	if err != nil {
		t.Fatalf("failed to resolve tool candidates: %v", err)
	}
	if len(toolCands) != 1 || toolCands[0].ProviderName != "prov-tool-agent" {
		t.Errorf("expected tool request to route only to prov-tool-agent, got %+v", toolCands)
	}
}

func TestRouter_CapabilityFiltering_VisionAndJSON(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{DefaultStrategy: config.StrategyPriority},
		Providers: []config.ProviderConfig{
			{
				Name:    "p1-vision",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: "http://localhost:8001",
				Enabled: true,
				Capabilities: []string{"vision"},
			},
			{
				Name:    "p2-json",
				Type:    config.ProviderTypeOpenAI,
				BaseURL: "http://localhost:8002",
				Enabled: true,
				Capabilities: []string{"json_mode"},
			},
		},
		Models: map[string]config.ModelRule{
			"omni-model": {
				Targets: []config.TargetModel{
					{Provider: "p1-vision", Model: "omni-model"},
					{Provider: "p2-json", Model: "omni-model"},
				},
			},
		},
	}

	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to initialize router: %v", err)
	}

	// Vision request with image content part
	visionReq := &provider.ChatRequest{
		Model: "omni-model",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: `[{"type":"image_url","image_url":{"url":"https://example.com/cat.jpg"}}]`},
		},
	}
	vCands, _, err := r.ResolveCandidates(visionReq)
	if err != nil {
		t.Fatalf("failed to resolve vision candidates: %v", err)
	}
	if len(vCands) != 1 || vCands[0].ProviderName != "p1-vision" {
		t.Errorf("expected vision request to route to p1-vision, got %+v", vCands)
	}

	// JSON mode request
	jsonReq := &provider.ChatRequest{
		Model: "omni-model",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: "Return JSON object with stats"},
		},
		ResponseFormat: &provider.ResponseFormat{Type: "json_object"},
	}
	jCands, _, err := r.ResolveCandidates(jsonReq)
	if err != nil {
		t.Fatalf("failed to resolve json candidates: %v", err)
	}
	if len(jCands) != 1 || jCands[0].ProviderName != "p2-json" {
		t.Errorf("expected json request to route to p2-json, got %+v", jCands)
	}

	// Unsatisfiable request requiring both vision AND json_mode
	impossibleReq := &provider.ChatRequest{
		Model: "omni-model",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: `data:image/png;base64,iVBORw0KGgo`},
		},
		ResponseFormat: &provider.ResponseFormat{Type: "json_object"},
	}
	_, _, err = r.ResolveCandidates(impossibleReq)
	if err == nil {
		t.Error("expected error when no candidate satisfies all required capabilities, got nil")
	}
}
