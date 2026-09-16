package security

import (
	"fmt"
)

// ValidatePromptLimits verifies that prompt character length and estimated token count
// are within configured security bounds.
func ValidatePromptLimits(totalChars, estimatedTokens, maxChars, maxTokens int) error {
	if maxChars > 0 && totalChars > maxChars {
		return fmt.Errorf("prompt character length (%d) exceeds maximum allowed (%d)", totalChars, maxChars)
	}
	if maxTokens > 0 && estimatedTokens > maxTokens {
		return fmt.Errorf("estimated prompt tokens (%d) exceeds maximum allowed (%d)", estimatedTokens, maxTokens)
	}
	return nil
}
