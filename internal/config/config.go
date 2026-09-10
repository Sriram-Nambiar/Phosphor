package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type RoutingStrategy string

const (
	StrategyPriority      RoutingStrategy = "priority"
	StrategyLeastCost     RoutingStrategy = "least-cost"
	StrategyLowestLatency RoutingStrategy = "lowest-latency"
)

type ProviderType string

const (
	ProviderTypeOpenAI    ProviderType = "openai"
	ProviderTypeAnthropic ProviderType = "anthropic"
	ProviderTypeOllama    ProviderType = "ollama"
	ProviderTypeGroq      ProviderType = "groq"
	ProviderTypeGemini    ProviderType = "gemini"
)

type Config struct {
	Server         ServerConfig         `mapstructure:"server" yaml:"server"`
	Database       DatabaseConfig       `mapstructure:"database" yaml:"database"`
	Routing        RoutingConfig        `mapstructure:"routing" yaml:"routing"`
	CircuitBreaker CircuitBreakerConfig `mapstructure:"circuit_breaker" yaml:"circuit_breaker"`
	Providers      []ProviderConfig     `mapstructure:"providers" yaml:"providers"`
	Models         map[string]ModelRule `mapstructure:"models" yaml:"models"`
}

type ServerConfig struct {
	Host         string        `mapstructure:"host" yaml:"host"`
	Port         int           `mapstructure:"port" yaml:"port"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout" yaml:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout" yaml:"write_timeout"`
}

type DatabaseConfig struct {
	Path string `mapstructure:"path" yaml:"path"`
}

type RoutingConfig struct {
	DefaultStrategy RoutingStrategy `mapstructure:"default_strategy" yaml:"default_strategy"`
	TimeoutSeconds  int             `mapstructure:"timeout_seconds" yaml:"timeout_seconds"`
}

type CircuitBreakerConfig struct {
	FailureThreshold int `mapstructure:"failure_threshold" yaml:"failure_threshold"`
	CooldownSeconds  int `mapstructure:"cooldown_seconds" yaml:"cooldown_seconds"`
}

type CostConfig struct {
	PromptCostPer1M     float64 `mapstructure:"prompt_cost_per_1m" yaml:"prompt_cost_per_1m"`
	CompletionCostPer1M float64 `mapstructure:"completion_cost_per_1m" yaml:"completion_cost_per_1m"`
}

type ProviderConfig struct {
	Name           string       `mapstructure:"name" yaml:"name"`
	Type           ProviderType `mapstructure:"type" yaml:"type"`
	BaseURL        string       `mapstructure:"base_url" yaml:"base_url"`
	APIKey         string       `mapstructure:"api_key" yaml:"api_key"`
	Enabled        bool         `mapstructure:"enabled" yaml:"enabled"`
	TimeoutSeconds int          `mapstructure:"timeout_seconds" yaml:"timeout_seconds"`
	Models         []string     `mapstructure:"models" yaml:"models"`
	Cost           CostConfig   `mapstructure:"cost" yaml:"cost"`
}

type TargetModel struct {
	Provider string      `mapstructure:"provider" yaml:"provider"`
	Model    string      `mapstructure:"model" yaml:"model"`
	Cost     *CostConfig `mapstructure:"cost,omitempty" yaml:"cost,omitempty"`
}

type ModelRule struct {
	Strategy RoutingStrategy `mapstructure:"strategy" yaml:"strategy"`
	Targets  []TargetModel   `mapstructure:"targets" yaml:"targets"`
}

// DefaultConfig returns a sane default configuration.
func DefaultConfig() *Config {
	homeDir, err := os.UserHomeDir()
	dbPath := "phosphor.db"
	if err == nil {
		dbPath = filepath.Join(homeDir, ".phosphor", "phosphor.db")
	}

	return &Config{
		Server: ServerConfig{
			Host:         "127.0.0.1",
			Port:         8080,
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Database: DatabaseConfig{
			Path: dbPath,
		},
		Routing: RoutingConfig{
			DefaultStrategy: StrategyPriority,
			TimeoutSeconds:  30,
		},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold: 3,
			CooldownSeconds:  30,
		},
		Providers: []ProviderConfig{
			{
				Name:           "openai",
				Type:           ProviderTypeOpenAI,
				BaseURL:        "https://api.openai.com/v1",
				APIKey:         "${OPENAI_API_KEY}",
				Enabled:        true,
				TimeoutSeconds: 30,
				Models:         []string{"gpt-4o", "gpt-4o-mini"},
				Cost: CostConfig{
					PromptCostPer1M:     2.50,
					CompletionCostPer1M: 10.00,
				},
			},
			{
				Name:           "groq",
				Type:           ProviderTypeGroq,
				BaseURL:        "https://api.groq.com/openai/v1",
				APIKey:         "${GROQ_API_KEY}",
				Enabled:        true,
				TimeoutSeconds: 15,
				Models:         []string{"llama-3.3-70b-versatile", "llama-3.1-8b-instant"},
				Cost: CostConfig{
					PromptCostPer1M:     0.59,
					CompletionCostPer1M: 0.79,
				},
			},
			{
				Name:           "ollama",
				Type:           ProviderTypeOllama,
				BaseURL:        "http://localhost:11434",
				APIKey:         "",
				Enabled:        true,
				TimeoutSeconds: 60,
				Models:         []string{"llama3.2", "mistral"},
				Cost: CostConfig{
					PromptCostPer1M:     0.0,
					CompletionCostPer1M: 0.0,
				},
			},
		},
		Models: map[string]ModelRule{
			"default": {
				Strategy: StrategyPriority,
				Targets: []TargetModel{
					{Provider: "openai", Model: "gpt-4o-mini"},
					{Provider: "groq", Model: "llama-3.3-70b-versatile"},
					{Provider: "ollama", Model: "llama3.2"},
				},
			},
			"gpt-4o": {
				Strategy: StrategyPriority,
				Targets: []TargetModel{
					{Provider: "openai", Model: "gpt-4o"},
					{Provider: "groq", Model: "llama-3.3-70b-versatile"},
				},
			},
		},
	}
}

// LoadConfig loads configuration from a file path or searches default locations.
func LoadConfig(configPath string) (*Config, error) {
	v := viper.New()

	v.SetEnvPrefix("PHOSPHOR")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	cfg := DefaultConfig()

	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("./config")
		if homeDir, err := os.UserHomeDir(); err == nil {
			v.AddConfigPath(filepath.Join(homeDir, ".phosphor"))
		}
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok && !os.IsNotExist(err) && configPath != "" {
			return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
		}
		// Fall back to default config if not found
		return cfg, nil
	}

	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	// Resolve environment variables in API keys and base URLs
	resolveEnvVars(cfg)

	return cfg, nil
}

func resolveEnvVars(cfg *Config) {
	for i := range cfg.Providers {
		cfg.Providers[i].APIKey = expandEnv(cfg.Providers[i].APIKey)
		cfg.Providers[i].BaseURL = expandEnv(cfg.Providers[i].BaseURL)
	}
}

func expandEnv(s string) string {
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		envName := strings.TrimSuffix(strings.TrimPrefix(s, "${"), "}")
		if val, ok := os.LookupEnv(envName); ok {
			return val
		}
		return ""
	}
	if strings.HasPrefix(s, "$") {
		envName := strings.TrimPrefix(s, "$")
		if val, ok := os.LookupEnv(envName); ok {
			return val
		}
		return ""
	}
	return s
}
