package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Routing.DefaultStrategy != StrategyPriority {
		t.Errorf("expected default strategy priority, got %s", cfg.Routing.DefaultStrategy)
	}
	if len(cfg.Providers) == 0 {
		t.Error("expected default providers to not be empty")
	}
}

func TestLoadConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	yamlContent := `
server:
  host: "0.0.0.0"
  port: 9000
database:
  path: ":memory:"
routing:
  default_strategy: "least-cost"
providers:
  - name: "mock"
    type: "openai"
    base_url: "https://example.com"
    api_key: "${TEST_API_KEY}"
    enabled: true
    cost:
      prompt_cost_per_1m: 1.00
      completion_cost_per_1m: 2.00
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	t.Setenv("TEST_API_KEY", "secret-test-key")

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Server.Port != 9000 {
		t.Errorf("expected port 9000, got %d", cfg.Server.Port)
	}
	if cfg.Routing.DefaultStrategy != StrategyLeastCost {
		t.Errorf("expected least-cost strategy, got %s", cfg.Routing.DefaultStrategy)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(cfg.Providers))
	}
	if cfg.Providers[0].APIKey != "secret-test-key" {
		t.Errorf("expected API key secret-test-key, got %s", cfg.Providers[0].APIKey)
	}
}

func TestConfig_Validation(t *testing.T) {
	// 1. Default config should validate without errors
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected DefaultConfig to be valid, got: %v", err)
	}

	// 2. Invalid port
	badPortCfg := *cfg
	badPortCfg.Server.Port = 70000
	if err := badPortCfg.Validate(); err == nil {
		t.Error("expected error for invalid port 70000, got nil")
	}

	// 3. Empty host
	badHostCfg := *cfg
	badHostCfg.Server.Host = ""
	if err := badHostCfg.Validate(); err == nil {
		t.Error("expected error for empty host, got nil")
	}

	// 4. Invalid provider URL
	badURLCfg := *cfg
	badURLCfg.Providers = []ProviderConfig{
		{
			Name:    "bad-url",
			Type:    ProviderTypeOpenAI,
			BaseURL: "not-a-valid-url",
			Enabled: true,
		},
	}
	if err := badURLCfg.Validate(); err == nil {
		t.Error("expected error for invalid URL, got nil")
	}

	// 5. Target model referencing missing provider
	missingProvCfg := *cfg
	missingProvCfg.Models = map[string]ModelRule{
		"test": {
			Targets: []TargetModel{
				{Provider: "nonexistent", Model: "m1"},
			},
		},
	}
	if err := missingProvCfg.Validate(); err == nil {
		t.Error("expected error for non-existent target provider, got nil")
	}
}
