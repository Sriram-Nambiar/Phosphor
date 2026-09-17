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
	if cfg.Server.ShutdownTimeout <= 0 {
		t.Errorf("expected positive shutdown timeout, got %v", cfg.Server.ShutdownTimeout)
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

	// 3b. Negative shutdown timeout
	badShutdownCfg := *cfg
	badShutdownCfg.Server.ShutdownTimeout = -1
	if err := badShutdownCfg.Validate(); err == nil {
		t.Error("expected error for negative shutdown timeout, got nil")
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

func TestConfig_DeepEnvExpansion(t *testing.T) {
	t.Setenv("TEST_HOST", "0.0.0.0")
	t.Setenv("TEST_DB_NAME", "custom_telemetry.db")
	t.Setenv("UPSTREAM_PORT", "11434")
	t.Setenv("UPSTREAM_HOST", "127.0.0.1")

	cfg := &Config{
		Server: ServerConfig{
			Host: "${TEST_HOST}",
			Port: 8080,
		},
		Database: DatabaseConfig{
			Path: "/tmp/${TEST_DB_NAME}",
		},
		Routing: RoutingConfig{
			DefaultStrategy: StrategyPriority,
		},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold: 3,
			CooldownSeconds:  30,
		},
		Providers: []ProviderConfig{
			{
				Name:    "ollama-local",
				Type:    ProviderTypeOllama,
				BaseURL: "http://${UPSTREAM_HOST}:${UPSTREAM_PORT}",
				Enabled: true,
				Models:  []string{"llama-${UPSTREAM_PORT}"},
			},
		},
		Models: map[string]ModelRule{
			"default": {
				Strategy: StrategyPriority,
				Targets: []TargetModel{
					{Provider: "ollama-local", Model: "llama-${UPSTREAM_PORT}"},
				},
			},
		},
	}

	resolveEnvVars(cfg)

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected host 0.0.0.0, got %s", cfg.Server.Host)
	}
	if cfg.Database.Path != "/tmp/custom_telemetry.db" {
		t.Errorf("expected db path /tmp/custom_telemetry.db, got %s", cfg.Database.Path)
	}
	if cfg.Providers[0].BaseURL != "http://127.0.0.1:11434" {
		t.Errorf("expected BaseURL http://127.0.0.1:11434, got %s", cfg.Providers[0].BaseURL)
	}
	if cfg.Providers[0].Models[0] != "llama-11434" {
		t.Errorf("expected model llama-11434, got %s", cfg.Providers[0].Models[0])
	}
	if cfg.Models["default"].Targets[0].Model != "llama-11434" {
		t.Errorf("expected target model llama-11434, got %s", cfg.Models["default"].Targets[0].Model)
	}
}

func TestConfig_ResolveModelAlias(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelAliases = map[string]string{
		"fast":  "gpt-4o-mini",
		"smart": "gpt-4o",
		"best":  "smart", // Chained: best -> smart -> gpt-4o
		"loop1": "loop2", // Cycle: loop1 -> loop2 -> loop1
		"loop2": "loop1",
	}

	// 1. Direct alias
	if resolved := cfg.ResolveModelAlias("fast"); resolved != "gpt-4o-mini" {
		t.Errorf("expected gpt-4o-mini, got %s", resolved)
	}

	// 2. Chained alias
	if resolved := cfg.ResolveModelAlias("best"); resolved != "gpt-4o" {
		t.Errorf("expected gpt-4o, got %s", resolved)
	}

	// 3. No alias
	if resolved := cfg.ResolveModelAlias("claude-3-5-sonnet"); resolved != "claude-3-5-sonnet" {
		t.Errorf("expected claude-3-5-sonnet, got %s", resolved)
	}

	// 4. Cycle termination (must not hang)
	resolvedLoop := cfg.ResolveModelAlias("loop1")
	if resolvedLoop != "loop2" && resolvedLoop != "loop1" {
		t.Errorf("expected cycle termination, got %s", resolvedLoop)
	}
}

func TestConfig_ValidateModelAliases(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelAliases = map[string]string{
		"":     "gpt-4o",
		"fast": "",
		"self": "self",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for bad model_aliases")
	}
	valErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if len(valErr.Errors) < 3 {
		t.Errorf("expected at least 3 errors, got %d: %v", len(valErr.Errors), valErr.Errors)
	}
}

func TestConfig_ExpandEnvWithDefaults(t *testing.T) {
	t.Setenv("PHOSPHOR_EXISTING", "my-secret-key")

	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "${PHOSPHOR_EXISTING:-default-key}",
			expected: "my-secret-key",
		},
		{
			input:    "${PHOSPHOR_NON_EXISTENT:-fallback-port}",
			expected: "fallback-port",
		},
		{
			input:    "http://${PHOSPHOR_NON_EXISTENT:-127.0.0.1}:${PHOSPHOR_NON_EXISTENT_PORT:-8080}/v1",
			expected: "http://127.0.0.1:8080/v1",
		},
		{
			input:    "${PHOSPHOR_EXISTING}",
			expected: "my-secret-key",
		},
		{
			input:    "",
			expected: "",
		},
	}

	for _, tc := range tests {
		result := ExpandEnv(tc.input)
		if result != tc.expected {
			t.Errorf("ExpandEnv(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

func TestConfig_TimeoutValidation(t *testing.T) {
	cfg := DefaultConfig()

	// Negative routing timeout
	cfg.Routing.TimeoutSeconds = -5
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative routing.timeout_seconds, got nil")
	}
	cfg.Routing.TimeoutSeconds = 30

	// Negative provider timeout
	cfg.Providers[0].TimeoutSeconds = -10
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative provider.timeout_seconds, got nil")
	}
}

func TestConfig_BackoffValidation(t *testing.T) {
	cfg := DefaultConfig()

	// Initial backoff greater than max backoff
	cfg.Routing.InitialBackoffMs = 5000
	cfg.Routing.MaxBackoffMs = 1000
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when initial_backoff_ms exceeds max_backoff_ms, got nil")
	}
}
