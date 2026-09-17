package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type RoutingStrategy string

const (
	StrategyPriority           RoutingStrategy = "priority"
	StrategyLeastCost          RoutingStrategy = "least-cost"
	StrategyLowestLatency      RoutingStrategy = "lowest-latency"
	StrategyRoundRobin         RoutingStrategy = "round-robin"
	StrategyWeightedRoundRobin RoutingStrategy = "weighted-round-robin"
	StrategyComposite          RoutingStrategy = "composite"
	StrategyStickySession      RoutingStrategy = "sticky-session"
)

type ProviderType string

const (
	ProviderTypeOpenAI    ProviderType = "openai"
	ProviderTypeAnthropic ProviderType = "anthropic"
	ProviderTypeOllama    ProviderType = "ollama"
	ProviderTypeGroq      ProviderType = "groq"
	ProviderTypeGemini    ProviderType = "gemini"
	ProviderTypeCohere    ProviderType = "cohere"
)

type Config struct {
	Server         ServerConfig         `mapstructure:"server" yaml:"server"`
	Auth           AuthConfig           `mapstructure:"auth" yaml:"auth"`
	Security       SecurityConfig       `mapstructure:"security" yaml:"security"`
	Database       DatabaseConfig       `mapstructure:"database" yaml:"database"`
	Routing        RoutingConfig        `mapstructure:"routing" yaml:"routing"`
	CircuitBreaker CircuitBreakerConfig `mapstructure:"circuit_breaker" yaml:"circuit_breaker"`
	Cache          CacheConfig          `mapstructure:"cache" yaml:"cache"`
	ModelAliases   map[string]string    `mapstructure:"model_aliases,omitempty" yaml:"model_aliases,omitempty"`
	Providers      []ProviderConfig     `mapstructure:"providers" yaml:"providers"`
	Models         map[string]ModelRule `mapstructure:"models" yaml:"models"`
}

type SecurityConfig struct {
	EnablePromptGuard bool     `mapstructure:"enable_prompt_guard" yaml:"enable_prompt_guard"`
	BlockThreshold    float64  `mapstructure:"block_threshold" yaml:"block_threshold"`
	AllowedIPs        []string `mapstructure:"allowed_ips,omitempty" yaml:"allowed_ips,omitempty"`
	BlockedIPs        []string `mapstructure:"blocked_ips,omitempty" yaml:"blocked_ips,omitempty"`
	MaxPromptTokens   int      `mapstructure:"max_prompt_tokens,omitempty" yaml:"max_prompt_tokens,omitempty"`
	MaxPromptChars    int      `mapstructure:"max_prompt_chars,omitempty" yaml:"max_prompt_chars,omitempty"`
}

type AuthConfig struct {
	Enabled bool           `mapstructure:"enabled" yaml:"enabled"`
	Keys    []APIKeyConfig `mapstructure:"keys" yaml:"keys"`
}

type BudgetConfig struct {
	MaxSpend    float64 `mapstructure:"max_spend" yaml:"max_spend"`
	SoftLimit   float64 `mapstructure:"soft_limit,omitempty" yaml:"soft_limit,omitempty"`
	ResetPeriod string  `mapstructure:"reset_period,omitempty" yaml:"reset_period,omitempty"`
}

type APIKeyConfig struct {
	Key           string        `mapstructure:"key" yaml:"key"`
	KeyHash       string        `mapstructure:"key_hash" yaml:"key_hash"`
	Name          string        `mapstructure:"name" yaml:"name"`
	AllowedModels []string      `mapstructure:"allowed_models" yaml:"allowed_models"`
	RateLimit     int           `mapstructure:"rate_limit" yaml:"rate_limit"`
	Budget        *BudgetConfig `mapstructure:"budget,omitempty" yaml:"budget,omitempty"`
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
	StreamIdleTimeout   time.Duration `mapstructure:"stream_idle_timeout" yaml:"stream_idle_timeout"`
	MaxRequestBodyBytes int64         `mapstructure:"max_request_body_bytes" yaml:"max_request_body_bytes"`
	CORS                CORSConfig    `mapstructure:"cors" yaml:"cors"`
}

type DatabaseConfig struct {
	Path string `mapstructure:"path" yaml:"path"`
}

type RoutingConfig struct {
	DefaultStrategy  RoutingStrategy `mapstructure:"default_strategy" yaml:"default_strategy"`
	TimeoutSeconds   int             `mapstructure:"timeout_seconds" yaml:"timeout_seconds"`
	CostWeight       float64         `mapstructure:"cost_weight" yaml:"cost_weight"`
	LatencyWeight    float64         `mapstructure:"latency_weight" yaml:"latency_weight"`
	DefaultFallbacks []string        `mapstructure:"default_fallbacks,omitempty" yaml:"default_fallbacks,omitempty"`
	MaxRetries       int             `mapstructure:"max_retries" yaml:"max_retries"`
	InitialBackoffMs int             `mapstructure:"initial_backoff_ms" yaml:"initial_backoff_ms"`
	MaxBackoffMs     int             `mapstructure:"max_backoff_ms" yaml:"max_backoff_ms"`
	RetryBudgetRatio float64         `mapstructure:"retry_budget_ratio,omitempty" yaml:"retry_budget_ratio,omitempty"`
}

type CircuitBreakerConfig struct {
	FailureThreshold int `mapstructure:"failure_threshold" yaml:"failure_threshold"`
	CooldownSeconds  int `mapstructure:"cooldown_seconds" yaml:"cooldown_seconds"`
}

type CacheConfig struct {
	Enabled  bool          `mapstructure:"enabled" yaml:"enabled"`
	Capacity int           `mapstructure:"capacity" yaml:"capacity"`
	TTL      time.Duration `mapstructure:"ttl" yaml:"ttl"`
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
	MaxConcurrency int          `mapstructure:"max_concurrency" yaml:"max_concurrency"`
	Models         []string     `mapstructure:"models" yaml:"models"`
	Capabilities   []string     `mapstructure:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Cost           CostConfig   `mapstructure:"cost" yaml:"cost"`
}

type TargetModel struct {
	Provider       string      `mapstructure:"provider" yaml:"provider"`
	Model          string      `mapstructure:"model" yaml:"model"`
	Cost           *CostConfig `mapstructure:"cost,omitempty" yaml:"cost,omitempty"`
	Weight         int         `mapstructure:"weight,omitempty" yaml:"weight,omitempty"`
	Capabilities   []string    `mapstructure:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	TimeoutSeconds int         `mapstructure:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
	TimeoutMs      int         `mapstructure:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
}

type ModelRule struct {
	Strategy  RoutingStrategy `mapstructure:"strategy" yaml:"strategy"`
	Targets   []TargetModel   `mapstructure:"targets" yaml:"targets"`
	Fallbacks []string        `mapstructure:"fallbacks,omitempty" yaml:"fallbacks,omitempty"`
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
			StreamIdleTimeout:   30 * time.Second,
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
		Security: SecurityConfig{
			EnablePromptGuard: false,
			BlockThreshold:    0.7,
		},
		Database: DatabaseConfig{
			Path: dbPath,
		},
		Routing: RoutingConfig{
			DefaultStrategy:  StrategyPriority,
			TimeoutSeconds:   30,
			CostWeight:       0.5,
			LatencyWeight:    0.5,
			MaxRetries:       2,
			InitialBackoffMs: 100,
			MaxBackoffMs:     2000,
		},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold: 3,
			CooldownSeconds:  30,
		},
		Cache: CacheConfig{
			Enabled:  false,
			Capacity: 1000,
			TTL:      5 * time.Minute,
		},
		ModelAliases: make(map[string]string),
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
	v.SetDefault("security.enable_prompt_guard", false)
	v.SetDefault("security.block_threshold", 0.7)
	v.SetDefault("database.path", dbPath)
	v.SetDefault("routing.default_strategy", "priority")
	v.SetDefault("routing.timeout_seconds", 30)
	v.SetDefault("routing.cost_weight", 0.5)
	v.SetDefault("routing.latency_weight", 0.5)
	v.SetDefault("routing.max_retries", 2)
	v.SetDefault("routing.initial_backoff_ms", 100)
	v.SetDefault("routing.max_backoff_ms", 2000)
	v.SetDefault("circuit_breaker.failure_threshold", 3)
	v.SetDefault("circuit_breaker.cooldown_seconds", 30)
	v.SetDefault("cache.enabled", false)
	v.SetDefault("cache.capacity", 1000)
	v.SetDefault("cache.ttl", "5m")
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
	if c.Server.StreamIdleTimeout < 0 {
		errs = append(errs, "server.stream_idle_timeout cannot be negative")
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
			if strings.TrimSpace(k.Key) == "" && strings.TrimSpace(k.KeyHash) == "" {
				errs = append(errs, fmt.Sprintf("auth.keys[%d] must specify either key or key_hash", i))
			}
			if k.Budget != nil {
				if k.Budget.MaxSpend < 0 {
					errs = append(errs, fmt.Sprintf("auth.keys[%d].budget.max_spend cannot be negative", i))
				}
				if k.Budget.SoftLimit < 0 {
					errs = append(errs, fmt.Sprintf("auth.keys[%d].budget.soft_limit cannot be negative", i))
				}
				if k.Budget.SoftLimit > k.Budget.MaxSpend && k.Budget.MaxSpend > 0 {
					errs = append(errs, fmt.Sprintf("auth.keys[%d].budget.soft_limit cannot exceed max_spend", i))
				}
				switch strings.ToLower(strings.TrimSpace(k.Budget.ResetPeriod)) {
				case "daily", "weekly", "monthly", "total", "":
				default:
					errs = append(errs, fmt.Sprintf("auth.keys[%d].budget.reset_period '%s' is invalid (must be daily, weekly, monthly, or total)", i, k.Budget.ResetPeriod))
				}
			}
		}
	}

	if strings.TrimSpace(c.Database.Path) == "" {
		errs = append(errs, "database.path must not be empty")
	}

	switch c.Routing.DefaultStrategy {
	case StrategyPriority, StrategyLeastCost, StrategyLowestLatency, StrategyRoundRobin, StrategyWeightedRoundRobin, StrategyComposite, "balanced", "cost-latency", "round_robin", "weighted_round_robin", "":
		// Valid strategy or default
	default:
		errs = append(errs, fmt.Sprintf("invalid routing.default_strategy '%s'", c.Routing.DefaultStrategy))
	}

	if c.Routing.CostWeight < 0 {
		errs = append(errs, "routing.cost_weight cannot be negative")
	}
	if c.Routing.LatencyWeight < 0 {
		errs = append(errs, "routing.latency_weight cannot be negative")
	}
	if (c.Routing.DefaultStrategy == StrategyComposite || c.Routing.DefaultStrategy == "balanced" || c.Routing.DefaultStrategy == "cost-latency") && c.Routing.CostWeight <= 0 && c.Routing.LatencyWeight <= 0 {
		errs = append(errs, "at least one of routing.cost_weight or routing.latency_weight must be positive for composite strategy")
	}
	if c.Routing.TimeoutSeconds < 0 {
		errs = append(errs, "routing.timeout_seconds cannot be negative")
	}
	if c.Routing.MaxRetries < 0 {
		errs = append(errs, "routing.max_retries cannot be negative")
	}
	if c.Routing.InitialBackoffMs < 0 {
		errs = append(errs, "routing.initial_backoff_ms cannot be negative")
	}
	if c.Routing.MaxBackoffMs < 0 {
		errs = append(errs, "routing.max_backoff_ms cannot be negative")
	}
	if c.Routing.InitialBackoffMs > c.Routing.MaxBackoffMs && c.Routing.MaxBackoffMs > 0 {
		errs = append(errs, "routing.initial_backoff_ms cannot exceed routing.max_backoff_ms")
	}
	if c.Routing.RetryBudgetRatio < 0 || c.Routing.RetryBudgetRatio > 1.0 {
		errs = append(errs, "routing.retry_budget_ratio must be between 0.0 and 1.0")
	}

	if c.CircuitBreaker.FailureThreshold < 0 {
		errs = append(errs, "circuit_breaker.failure_threshold cannot be negative")
	}
	if c.CircuitBreaker.CooldownSeconds < 0 {
		errs = append(errs, "circuit_breaker.cooldown_seconds cannot be negative")
	}

	if c.Cache.Enabled {
		if c.Cache.Capacity < 0 {
			errs = append(errs, "cache.capacity cannot be negative")
		}
		if c.Cache.TTL < 0 {
			errs = append(errs, "cache.ttl cannot be negative")
		}
	}

	if c.Security.EnablePromptGuard {
		if c.Security.BlockThreshold < 0 || c.Security.BlockThreshold > 1.0 {
			errs = append(errs, "security.block_threshold must be between 0.0 and 1.0")
		}
	}
	if c.Security.MaxPromptTokens < 0 {
		errs = append(errs, "security.max_prompt_tokens cannot be negative")
	}
	if c.Security.MaxPromptChars < 0 {
		errs = append(errs, "security.max_prompt_chars cannot be negative")
	}

	for _, ipStr := range c.Security.AllowedIPs {
		s := strings.TrimSpace(ipStr)
		if s == "" || strings.EqualFold(s, "localhost") {
			continue
		}
		if _, _, err := net.ParseCIDR(s); err != nil && net.ParseIP(s) == nil {
			errs = append(errs, fmt.Sprintf("invalid IP or CIDR in security.allowed_ips: '%s'", ipStr))
		}
	}
	for _, ipStr := range c.Security.BlockedIPs {
		s := strings.TrimSpace(ipStr)
		if s == "" || strings.EqualFold(s, "localhost") {
			continue
		}
		if _, _, err := net.ParseCIDR(s); err != nil && net.ParseIP(s) == nil {
			errs = append(errs, fmt.Sprintf("invalid IP or CIDR in security.blocked_ips: '%s'", ipStr))
		}
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
			if p.MaxConcurrency < 0 {
				errs = append(errs, fmt.Sprintf("providers[%s].max_concurrency cannot be negative", p.Name))
			}
			if p.TimeoutSeconds < 0 {
				errs = append(errs, fmt.Sprintf("providers[%s].timeout_seconds cannot be negative", p.Name))
			}
		}
	}

	for mName, rule := range c.Models {
		switch rule.Strategy {
		case StrategyPriority, StrategyLeastCost, StrategyLowestLatency, StrategyRoundRobin, StrategyWeightedRoundRobin, StrategyComposite, StrategyStickySession, "sticky_session", "balanced", "cost-latency", "round_robin", "weighted_round_robin", "":
			// Valid strategy
		default:
			errs = append(errs, fmt.Sprintf("models['%s'].strategy '%s' is invalid", mName, rule.Strategy))
		}

		if len(rule.Targets) == 0 {
			errs = append(errs, fmt.Sprintf("models['%s'] must define at least one target", mName))
		}
		for tIdx, tgt := range rule.Targets {
			if !providerNames[tgt.Provider] {
				errs = append(errs, fmt.Sprintf("models['%s'].targets[%d] references non-existent provider '%s'", mName, tIdx, tgt.Provider))
			}
			if tgt.Weight < 0 {
				errs = append(errs, fmt.Sprintf("models['%s'].targets[%d].weight cannot be negative", mName, tIdx))
			}
			if tgt.TimeoutSeconds < 0 {
				errs = append(errs, fmt.Sprintf("models['%s'].targets[%d].timeout_seconds cannot be negative", mName, tIdx))
			}
			if tgt.TimeoutMs < 0 {
				errs = append(errs, fmt.Sprintf("models['%s'].targets[%d].timeout_ms cannot be negative", mName, tIdx))
			}
		}
	}

	for alias, target := range c.ModelAliases {
		if strings.TrimSpace(alias) == "" {
			errs = append(errs, "model_aliases contains an empty alias key")
		}
		if strings.TrimSpace(target) == "" {
			errs = append(errs, fmt.Sprintf("model_aliases['%s'] target model cannot be empty", alias))
		}
		if alias == target {
			errs = append(errs, fmt.Sprintf("model_aliases['%s'] cannot point to itself", alias))
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
		cfg.Auth.Keys[i].KeyHash = expandEnv(cfg.Auth.Keys[i].KeyHash)
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

	for alias, target := range cfg.ModelAliases {
		cfg.ModelAliases[alias] = expandEnv(target)
	}
}

// ExpandEnv expands environment variables with support for default values,
// e.g. ${PORT:-8080} and ${HOST:-127.0.0.1}.
func ExpandEnv(s string) string {
	if s == "" {
		return ""
	}
	return os.Expand(s, func(v string) string {
		if idx := strings.Index(v, ":-"); idx != -1 {
			name := v[:idx]
			defaultVal := v[idx+2:]
			val, exists := os.LookupEnv(name)
			if exists && val != "" {
				return val
			}
			return defaultVal
		}
		return os.Getenv(v)
	})
}

func expandEnv(s string) string {
	return ExpandEnv(s)
}

// ResolveModelAlias resolves a model alias to its target canonical model name.
// If an alias chain exists (e.g. fast -> gpt-4o-mini -> gpt-4o-mini-2024-07-18), it resolves recursively up to 5 hops to prevent cycles.
func (c *Config) ResolveModelAlias(model string) string {
	if c.ModelAliases == nil {
		return model
	}
	current := model
	visited := make(map[string]bool)
	for i := 0; i < 5; i++ {
		target, ok := c.ModelAliases[current]
		if !ok || target == "" || visited[target] {
			break
		}
		visited[current] = true
		current = target
	}
	return current
}
