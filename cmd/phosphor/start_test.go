package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
)

func TestFormatStartupBanner(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 8080,
		},
		Database: config.DatabaseConfig{
			Path: "test.db",
		},
		Routing: config.RoutingConfig{
			DefaultStrategy: config.StrategyPriority,
		},
		Providers: []config.ProviderConfig{
			{Name: "openai", Enabled: true},
			{Name: "anthropic", Enabled: true},
		},
		Auth: config.AuthConfig{
			Enabled: true,
			Keys: []config.APIKeyConfig{
				{Key: "key-1", Name: "team-1"},
			},
		},
		Cache: config.CacheConfig{
			Enabled:  true,
			TTL:      10 * time.Minute,
			Capacity: 1000,
		},
		Security: config.SecurityConfig{
			EnablePromptGuard: true,
			BlockThreshold:    0.85,
		},
	}

	banner := FormatStartupBanner(cfg)

	expectedSubstrings := []string{
		"PHOSPHOR LLM GATEWAY",
		"http://127.0.0.1:8080",
		"test.db",
		"priority",
		"openai",
		"anthropic",
		"Enabled (1 API keys)",
		"Enabled (TTL 10m0s, Cap 1000)",
		"Enabled (threshold 0.85)",
		"/v1/chat/completions",
		"/v1/models",
		"/v1/admin/budgets",
		"/v1/admin/cache/stats",
		"/v1/admin/db/vacuum",
	}

	for _, sub := range expectedSubstrings {
		if !strings.Contains(banner, sub) {
			t.Errorf("expected banner to contain %q, banner was:\n%s", sub, banner)
		}
	}
}
