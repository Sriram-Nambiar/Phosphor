package security

import (
	"strings"
	"testing"
)

func TestRedactText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
		excludes []string
	}{
		{
			name:     "OpenAI key in error message",
			input:    "failed request: 401 Unauthorized for sk-proj-1234567890abcdef123456",
			contains: "[REDACTED]",
			excludes: []string{"sk-proj-1234567890abcdef123456"},
		},
		{
			name:     "Anthropic key",
			input:    "request with sk-ant-api03-abcdef123456789 failed",
			contains: "[REDACTED]",
			excludes: []string{"sk-ant-api03-abcdef123456789"},
		},
		{
			name:     "Bearer token",
			input:    "Authorization: Bearer mySecretToken12345",
			contains: "Bearer [REDACTED]",
			excludes: []string{"mySecretToken12345"},
		},
		{
			name:     "Phosphor key",
			input:    "client token ph_abcdef1234567890 rejected",
			contains: "[REDACTED]",
			excludes: []string{"ph_abcdef1234567890"},
		},
		{
			name:     "Query param secret",
			input:    "http://example.com/v1?api_key=secretKey123&other=val",
			contains: "api_key=[REDACTED]",
			excludes: []string{"secretKey123"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := RedactText(tc.input)
			if !strings.Contains(output, tc.contains) {
				t.Errorf("expected output to contain %q, got %q", tc.contains, output)
			}
			for _, ex := range tc.excludes {
				if strings.Contains(output, ex) {
					t.Errorf("expected output to NOT contain %q, got %q", ex, output)
				}
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	// 1. URL with userinfo password
	urlWithPass := "https://admin:mySuperSecretPassword@api.example.com/v1/chat"
	redactedPass := RedactURL(urlWithPass)
	if strings.Contains(redactedPass, "mySuperSecretPassword") {
		t.Errorf("expected password to be redacted, got %s", redactedPass)
	}
	if !strings.Contains(redactedPass, "admin:[REDACTED]@") && !strings.Contains(redactedPass, "admin:%5BREDACTED%5D@") {
		t.Errorf("expected redacted userinfo in url, got %s", redactedPass)
	}

	// 2. URL with sensitive query parameter
	urlWithQuery := "https://api.example.com/v1/models?token=secret-token-xyz&format=json"
	redactedQuery := RedactURL(urlWithQuery)
	if strings.Contains(redactedQuery, "secret-token-xyz") {
		t.Errorf("expected query secret to be redacted, got %s", redactedQuery)
	}
	if !strings.Contains(redactedQuery, "token=%5BREDACTED%5D") && !strings.Contains(redactedQuery, "token=[REDACTED]") {
		t.Errorf("expected redacted token parameter, got %s", redactedQuery)
	}
}

func TestRedactHeaders(t *testing.T) {
	headers := map[string][]string{
		"Authorization": {"Bearer top-secret-token"},
		"x-api-key":     {"raw-secret-key"},
		"Content-Type":  {"application/json"},
	}

	sanitized := RedactHeaders(headers)

	if sanitized["Authorization"][0] != "[REDACTED]" {
		t.Errorf("expected Authorization to be [REDACTED], got %v", sanitized["Authorization"])
	}
	if sanitized["x-api-key"][0] != "[REDACTED]" {
		t.Errorf("expected x-api-key to be [REDACTED], got %v", sanitized["x-api-key"])
	}
	if sanitized["Content-Type"][0] != "application/json" {
		t.Errorf("expected Content-Type to be preserved, got %v", sanitized["Content-Type"])
	}
}
