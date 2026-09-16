package provider

import (
	"fmt"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
)

// NewProvider instantiates the proper Provider implementation based on configuration.
func NewProvider(cfg config.ProviderConfig) (Provider, error) {
	switch cfg.Type {
	case config.ProviderTypeOpenAI, config.ProviderTypeGroq:
		return NewOpenAIProvider(cfg), nil
	case config.ProviderTypeGemini:
		return NewGeminiProvider(cfg), nil
	case config.ProviderTypeAnthropic:
		return NewAnthropicProvider(cfg), nil
	case config.ProviderTypeOllama:
		return NewOllamaProvider(cfg), nil
	default:
		// Default to OpenAI-compatible if unrecognized
		if cfg.BaseURL != "" {
			return NewOpenAIProvider(cfg), nil
		}
		return nil, fmt.Errorf("unsupported provider type: %s", cfg.Type)
	}
}
