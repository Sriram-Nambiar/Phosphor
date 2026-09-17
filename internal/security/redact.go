package security

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	// Matches Authorization Bearer tokens: "Bearer sk-...", "bearer eyJ..."
	bearerRegex = regexp.MustCompile(`(?i)(bearer\s+)([a-zA-Z0-9_\-\.]{6,})`)

	// Matches common LLM vendor API key formats:
	// - OpenAI: sk-..., sk-proj-...
	// - Anthropic: sk-ant-...
	// - Groq: gsk_...
	// - Phosphor: ph_...
	// - Generic tokens
	apiKeyRegex = regexp.MustCompile(`\b(sk-[a-zA-Z0-9_\-]{8,}|sk-ant-[a-zA-Z0-9_\-]{8,}|ph_[a-zA-Z0-9]{8,}|gsk_[a-zA-Z0-9_\-]{8,})\b`)

	// Matches query parameters or JSON-like fields containing secrets
	querySecretRegex = regexp.MustCompile(`(?i)([?&](?:api_?key|token|secret|password|auth)=)([^&]+)`)
)

// RedactText scrubs API keys, Bearer tokens, and sensitive query parameters from any text string.
func RedactText(s string) string {
	if s == "" {
		return ""
	}
	s = bearerRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = apiKeyRegex.ReplaceAllString(s, "[REDACTED]")
	s = querySecretRegex.ReplaceAllString(s, "${1}[REDACTED]")
	return s
}

// RedactURL removes user credentials (passwords) and sensitive query parameters from a URL.
func RedactURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return RedactText(rawURL)
	}

	if parsed.User != nil {
		if _, hasPass := parsed.User.Password(); hasPass {
			parsed.User = url.UserPassword(parsed.User.Username(), "[REDACTED]")
		}
	}

	if parsed.RawQuery != "" {
		q := parsed.Query()
		for k := range q {
			lower := strings.ToLower(k)
			if lower == "key" || lower == "api_key" || lower == "apikey" || lower == "token" || lower == "secret" || lower == "password" {
				q.Set(k, "[REDACTED]")
			}
		}
		parsed.RawQuery = q.Encode()
	}

	return parsed.String()
}

// RedactHeaders returns a sanitized copy of HTTP headers with sensitive keys redacted.
func RedactHeaders(headers map[string][]string) map[string][]string {
	sanitized := make(map[string][]string, len(headers))
	for k, v := range headers {
		lower := strings.ToLower(k)
		if lower == "authorization" || lower == "x-api-key" || lower == "proxy-authorization" || lower == "cookie" || lower == "set-cookie" {
			sanitized[k] = []string{"[REDACTED]"}
		} else {
			sanitized[k] = v
		}
	}
	return sanitized
}

// RedactAPIKey masks a secret key, preserving only leading and trailing characters if long enough.
func RedactAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
		return key
	}
	runes := []rune(key)
	if len(runes) <= 8 {
		return "[REDACTED]"
	}
	return string(runes[:4]) + "..." + string(runes[len(runes)-4:])
}

