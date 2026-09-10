package config

import (
	"fmt"
	"net/url"
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
	Auth           AuthConfig           `mapstructure:"auth" yaml:"auth"`
	Database       DatabaseConfig       `mapstructure:"database" yaml:"database"`
	Routing        RoutingConfig        `mapstructure:"routing" yaml:"routing"`
	CircuitBreaker CircuitBreakerConfig `mapstructure:"circuit_breaker" yaml:"circuit_breaker"`
	Providers      []ProviderConfig     `mapstructure:"providers" yaml:"providers"`
	Models         map[string]ModelRule `mapstructure:"models" yaml:"models"`
}

type AuthConfig struct {
	Enabled bool           `mapstructure:"enabled" yaml:"enabled"`
	Keys    []APIKeyConfig `mapstructure:"keys" yaml:"keys"`
}

type APIKeyConfig struct {
	Key           string   `mapstructure:"key" yaml:"key"`
	Name          string   `mapstructure:"name" yaml:"name"`
	AllowedModels []string `mapstructure:"allowed_models" yaml:"allowed_models"`
	RateLimit     int      `mapstructure:"rate_limit" yaml:"rate_limit"`
}

type CORSConfig struct {
	Enabled          bool     `mapstructure:"enabled" yaml:"enabled"`
	AllowedOrigins   []string `mapstructure:"allowed_origins" yaml:"allowed_origins"`
	AllowedMethods   []string `mapstructure:"allowed_methods" yaml:"allowed_methods"`
	AllowedHeaders   []string `mapstructure:"allowed_headers" yaml:"allowed_headers"`
	AllowCredentials bool     `mapstructure:"allow_credentials" yaml:"allow_credentials"`
	MaxAgeSeconds    int      `mapstructure:"max_age_seconds" yaml:"max_age_seconds"`
}

type ServerConfig struct {
	Host                string        `mapstructure:"host" yaml:"host"`
	Port                int           `mapstructure:"port" yaml:"port"`
	ReadTimeout         time.Duration `mapstructure:"read_timeout" yaml:"read_timeout"`
	WriteTimeout        time.Duration `mapstructure:"write_timeout" yaml:"write_timeout"`
	ShutdownTimeout     time.Duration `mapstructure:"shutdown_timeout" yaml:"shutdown_timeout"`
	MaxRequestBodyBytes int64         `mapstructure:"max_request_body_bytes" yaml:"max_request_body_bytes"`
	CORS                CORSConfig    `mapstructure:"cors" yaml:"cors"`
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
			Host:                "127.0.0.1",
			Port:                8080,
			ReadTimeout:         60 * time.Second,
			WriteTimeout:        120 * time.Second,
			ShutdownTimeout:     15 * time.Second,
			MaxRequestBodyBytes: 4 * 1024 * 1024,
			CORS: CORSConfig{
				Enabled:          true,
				AllowedOrigins:   []string{"*"},
				AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
				AllowedHeaders:   []string{"Content-Type", "Authorization", "x-api-key", "X-Request-ID"},
				AllowCredentials: true,
				MaxAgeSeconds:    86400,
			},
		},
		Auth: AuthConfig{
			Enabled: false,
			Keys:    []APIKeyConfig{},
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

	setDefaults(v)

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

	hasConfigFile := true
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok && !os.IsNotExist(err) && configPath != "" {
			return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
		}
		hasConfigFile = false
	}

	if !hasConfigFile {
		// No config file found; return default configuration directly
		cfg := DefaultConfig()
		resolveEnvVars(cfg)
		return cfg, cfg.Validate()
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	// If providers were specified in file but no models rule, auto-generate default rule for them
	if len(cfg.Models) == 0 && len(cfg.Providers) > 0 {
		var targets []TargetModel
		for _, p := range cfg.Providers {
			m := "default"
			if len(p.Models) > 0 {
				m = p.Models[0]
			}
			targets = append(targets, TargetModel{
				Provider: p.Name,
				Model:    m,
			})
		}
		cfg.Models = map[string]ModelRule{
			"default": {
				Strategy: cfg.Routing.DefaultStrategy,
				Targets:  targets,
			},
		}
	}

	// Resolve environment variables in API keys and base URLs
	resolveEnvVars(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func setDefaults(v *viper.Viper) {
	homeDir, err := os.UserHomeDir()
	dbPath := "phosphor.db"
	if err == nil {
		dbPath = filepath.Join(homeDir, ".phosphor", "phosphor.db")
	}

	v.SetDefault("server.host", "127.0.0.1")
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_timeout", 60*time.Second)
	v.SetDefault("server.write_timeout", 120*time.Second)
	v.SetDefault("server.shutdown_timeout", 15*time.Second)
	v.SetDefault("server.max_request_body_bytes", int64(4*1024*1024))
	v.SetDefault("server.cors.enabled", true)
	v.SetDefault("server.cors.allowed_origins", []string{"*"})
	v.SetDefault("server.cors.allowed_methods", []string{"GET", "POST", "OPTIONS"})
	v.SetDefault("server.cors.allowed_headers", []string{"Content-Type", "Authorization", "x-api-key", "X-Request-ID"})
	v.SetDefault("server.cors.allow_credentials", true)
	v.SetDefault("server.cors.max_age_seconds", 86400)
	v.SetDefault("auth.enabled", false)
	v.SetDefault("database.path", dbPath)
	v.SetDefault("routing.default_strategy", "priority")
	v.SetDefault("routing.timeout_seconds", 30)
	v.SetDefault("circuit_breaker.failure_threshold", 3)
	v.SetDefault("circuit_breaker.cooldown_seconds", 30)
}

// ValidationError records all configuration violations found during validation.
type ValidationError struct {
	Errors []string
}

func (ve *ValidationError) Error() string {
	return fmt.Sprintf("configuration validation failed with %d error(s):\n - %s", len(ve.Errors), strings.Join(ve.Errors, "\n - "))
}

// Validate performs strict validation on the configuration.
func (c *Config) Validate() error {
	var errs []string

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Sprintf("server.port must be between 1 and 65535 (got %d)", c.Server.Port))
	}
	if strings.TrimSpace(c.Server.Host) == "" {
		errs = append(errs, "server.host must not be empty")
	}
	if c.Server.ReadTimeout < 0 {
		errs = append(errs, "server.read_timeout cannot be negative")
	}
	if c.Server.WriteTimeout < 0 {
		errs = append(errs, "server.write_timeout cannot be negative")
	}
	if c.Server.ShutdownTimeout < 0 {
		errs = append(errs, "server.shutdown_timeout cannot be negative")
	}
	if c.Server.MaxRequestBodyBytes < 0 {
		errs = append(errs, "server.max_request_body_bytes cannot be negative")
	}
	if c.Server.CORS.MaxAgeSeconds < 0 {
		errs = append(errs, "server.cors.max_age_seconds cannot be negative")
	}

	if c.Auth.Enabled {
		if len(c.Auth.Keys) == 0 {
			errs = append(errs, "auth is enabled but no api keys are configured in auth.keys")
		}
		for i, k := range c.Auth.Keys {
			if strings.TrimSpace(k.Key) == "" {
				errs = append(errs, fmt.Sprintf("auth.keys[%d].key must not be empty", i))
			}
		}
	}

	if strings.TrimSpace(c.Database.Path) == "" {
		errs = append(errs, "database.path must not be empty")
	}

	switch c.Routing.DefaultStrategy {
	case StrategyPriority, StrategyLeastCost, StrategyLowestLatency, "":
		// Valid strategy or default
	default:
		errs = append(errs, fmt.Sprintf("invalid routing.default_strategy '%s'", c.Routing.DefaultStrategy))
	}

	if c.CircuitBreaker.FailureThreshold < 0 {
		errs = append(errs, "circuit_breaker.failure_threshold cannot be negative")
	}
	if c.CircuitBreaker.CooldownSeconds < 0 {
		errs = append(errs, "circuit_breaker.cooldown_seconds cannot be negative")
	}

	if len(c.Providers) == 0 {
		errs = append(errs, "at least one provider must be configured")
	}

	providerNames := make(map[string]bool)
	for i, p := range c.Providers {
		if strings.TrimSpace(p.Name) == "" {
			errs = append(errs, fmt.Sprintf("providers[%d].name must not be empty", i))
		} else if providerNames[p.Name] {
			errs = append(errs, fmt.Sprintf("duplicate provider name '%s'", p.Name))
		} else {
			providerNames[p.Name] = true
		}

		if p.Enabled {
			if strings.TrimSpace(p.BaseURL) == "" {
				errs = append(errs, fmt.Sprintf("providers[%s].base_url must not be empty", p.Name))
			} else {
				parsedURL, err := url.Parse(p.BaseURL)
				if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
					errs = append(errs, fmt.Sprintf("providers[%s].base_url '%s' is not a valid http/https URL", p.Name, p.BaseURL))
				}
			}
			if p.Cost.PromptCostPer1M < 0 || p.Cost.CompletionCostPer1M < 0 {
				errs = append(errs, fmt.Sprintf("providers[%s].cost cannot have negative pricing", p.Name))
			}
		}
	}

	for mName, rule := range c.Models {
		if len(rule.Targets) == 0 {
			errs = append(errs, fmt.Sprintf("models['%s'] must define at least one target", mName))
		}
		for _, tgt := range rule.Targets {
			if !providerNames[tgt.Provider] {
				errs = append(errs, fmt.Sprintf("models['%s'] references non-existent provider '%s'", mName, tgt.Provider))
			}
		}
	}

	if len(errs) > 0 {
		return &ValidationError{Errors: errs}
	}
	return nil
}

func resolveEnvVars(cfg *Config) {
	cfg.Server.Host = expandEnv(cfg.Server.Host)
	for i := range cfg.Server.CORS.AllowedOrigins {
		cfg.Server.CORS.AllowedOrigins[i] = expandEnv(cfg.Server.CORS.AllowedOrigins[i])
	}
	for i := range cfg.Server.CORS.AllowedMethods {
		cfg.Server.CORS.AllowedMethods[i] = expandEnv(cfg.Server.CORS.AllowedMethods[i])
	}
	for i := range cfg.Server.CORS.AllowedHeaders {
		cfg.Server.CORS.AllowedHeaders[i] = expandEnv(cfg.Server.CORS.AllowedHeaders[i])
	}

	for i := range cfg.Auth.Keys {
		cfg.Auth.Keys[i].Key = expandEnv(cfg.Auth.Keys[i].Key)
		cfg.Auth.Keys[i].Name = expandEnv(cfg.Auth.Keys[i].Name)
		for j := range cfg.Auth.Keys[i].AllowedModels {
			cfg.Auth.Keys[i].AllowedModels[j] = expandEnv(cfg.Auth.Keys[i].AllowedModels[j])
		}
	}

	cfg.Database.Path = expandEnv(cfg.Database.Path)

	for i := range cfg.Providers {
		cfg.Providers[i].Name = expandEnv(cfg.Providers[i].Name)
		cfg.Providers[i].APIKey = expandEnv(cfg.Providers[i].APIKey)
		cfg.Providers[i].BaseURL = expandEnv(cfg.Providers[i].BaseURL)
		for j := range cfg.Providers[i].Models {
			cfg.Providers[i].Models[j] = expandEnv(cfg.Providers[i].Models[j])
		}
	}

	for mName, rule := range cfg.Models {
		for i := range rule.Targets {
			rule.Targets[i].Provider = expandEnv(rule.Targets[i].Provider)
			rule.Targets[i].Model = expandEnv(rule.Targets[i].Model)
		}
		cfg.Models[mName] = rule
	}
}

func expandEnv(s string) string {
	if s == "" {
		return ""
	}
	return os.ExpandEnv(s)
}
