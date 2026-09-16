package security

import (
	"testing"
)

func TestValidatePromptLimits(t *testing.T) {
	// 1. Within limits
	if err := ValidatePromptLimits(100, 25, 200, 50); err != nil {
		t.Fatalf("expected within limits to succeed, got: %v", err)
	}

	// 2. Disabled limits (0 values)
	if err := ValidatePromptLimits(10000, 2500, 0, 0); err != nil {
		t.Fatalf("expected disabled limits to succeed, got: %v", err)
	}

	// 3. Exceeds maxChars
	if err := ValidatePromptLimits(250, 50, 200, 100); err == nil {
		t.Fatalf("expected error when totalChars > maxChars, got nil")
	}

	// 4. Exceeds maxTokens
	if err := ValidatePromptLimits(100, 120, 200, 100); err == nil {
		t.Fatalf("expected error when estimatedTokens > maxTokens, got nil")
	}
}
