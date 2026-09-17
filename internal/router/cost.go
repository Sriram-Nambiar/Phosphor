package router

import (
	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

// EstimatePromptTokens estimates the token count for a chat request.
// Standard heuristic: ~4 characters per token + 4 formatting tokens per message.
func EstimatePromptTokens(req *provider.ChatRequest) int {
	if req == nil {
		return 0
	}

	totalChars := 0
	for _, msg := range req.Messages {
		totalChars += len(msg.Role) + len(msg.Content)
		// Account for framing tokens per message
		totalChars += 16
	}

	estimated := totalChars / 4
	if estimated < 1 && len(req.Messages) > 0 {
		return 1
	}
	return estimated
}

// CalculateCost computes dollar cost based on prompt and completion token counts and cost rates per 1M tokens.
func CalculateCost(promptTokens, completionTokens int, costCfg config.CostConfig) float64 {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	promptCost := (float64(promptTokens) / 1_000_000.0) * costCfg.PromptCostPer1M
	completionCost := (float64(completionTokens) / 1_000_000.0) * costCfg.CompletionCostPer1M
	return promptCost + completionCost
}

// EstimateRequestCost estimates the dollar cost for a request based on prompt token count.
func EstimateRequestCost(estimatedPromptTokens int, costCfg config.CostConfig) float64 {
	if estimatedPromptTokens < 0 {
		estimatedPromptTokens = 0
	}
	// Assume an average completion of ~50% prompt tokens for estimation purposes
	estCompletion := estimatedPromptTokens / 2
	return CalculateCost(estimatedPromptTokens, estCompletion, costCfg)
}
