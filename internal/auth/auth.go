package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
)

type contextKey string

const clientContextKey contextKey = "phosphor_client_info"

// ClientInfo represents an authenticated client's identity and permissions.
type ClientInfo struct {
	Key           string
	Name          string
	AllowedModels []string
	RateLimit     int
	Budget        *config.BudgetConfig
}

// WithClientInfo returns a new context with the client metadata attached.
func WithClientInfo(ctx context.Context, client ClientInfo) context.Context {
	return context.WithValue(ctx, clientContextKey, client)
}

// GetClientInfo extracts the authenticated client metadata from the context, if present.
func GetClientInfo(ctx context.Context) (ClientInfo, bool) {
	client, ok := ctx.Value(clientContextKey).(ClientInfo)
	return client, ok
}

// CanAccessModel reports whether the client has permission to access the requested model.
// If AllowedModels is empty, all models are permitted.
// If AllowedModels contains patterns (e.g. "gpt-4o", "llama-*", "*"), they are matched.
func (c *ClientInfo) CanAccessModel(model string) bool {
	if len(c.AllowedModels) == 0 {
		return true
	}
	for _, pattern := range c.AllowedModels {
		p := strings.TrimSpace(pattern)
		if p == "*" {
			return true
		}
		if strings.EqualFold(p, model) {
			return true
		}
		if strings.HasSuffix(p, "*") {
			prefix := strings.TrimSuffix(p, "*")
			if strings.HasPrefix(strings.ToLower(model), strings.ToLower(prefix)) {
				return true
			}
		}
	}
	return false
}

// HashKey computes the hexadecimal-encoded SHA-256 digest of an API key.
func HashKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
}

// ExtractAPIKey retrieves the API key from the request.
// It checks the 'Authorization: Bearer <key>' header first, followed by the 'x-api-key' header.
func ExtractAPIKey(r *http.Request) string {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			return strings.TrimSpace(authHeader[7:])
		}
		return authHeader
	}
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		return key
	}
	return ""
}

// Authenticate verifies the raw API key against the list of configured keys using constant-time comparison
// to prevent timing attacks. It supports plaintext keys, keys with "sha256:" prefix, and explicit KeyHash fields.
// Returns the matching ClientInfo if authentication succeeds.
func Authenticate(rawKey string, configuredKeys []config.APIKeyConfig) (*ClientInfo, bool) {
	if rawKey == "" {
		return nil, false
	}

	rawBytes := []byte(rawKey)
	rawHash := HashKey(rawKey)
	rawHashBytes := []byte(rawHash)

	for _, k := range configuredKeys {
		// 1. Explicit KeyHash field configured
		if k.KeyHash != "" {
			expectedHash := strings.ToLower(strings.TrimSpace(k.KeyHash))
			expectedHashBytes := []byte(expectedHash)
			if len(rawHashBytes) == len(expectedHashBytes) && subtle.ConstantTimeCompare(rawHashBytes, expectedHashBytes) == 1 {
				return &ClientInfo{
					Key:           k.KeyHash,
					Name:          k.Name,
					AllowedModels: k.AllowedModels,
					RateLimit:     k.RateLimit,
					Budget:        k.Budget,
				}, true
			}
		}

		// 2. Key prefixed with sha256:
		if strings.HasPrefix(strings.ToLower(k.Key), "sha256:") {
			expectedHash := strings.ToLower(strings.TrimSpace(k.Key[7:]))
			expectedHashBytes := []byte(expectedHash)
			if len(rawHashBytes) == len(expectedHashBytes) && subtle.ConstantTimeCompare(rawHashBytes, expectedHashBytes) == 1 {
				return &ClientInfo{
					Key:           k.Key,
					Name:          k.Name,
					AllowedModels: k.AllowedModels,
					RateLimit:     k.RateLimit,
					Budget:        k.Budget,
				}, true
			}
		}

		// 3. Plaintext key
		if k.Key != "" {
			cfgBytes := []byte(k.Key)
			if len(rawBytes) == len(cfgBytes) && subtle.ConstantTimeCompare(rawBytes, cfgBytes) == 1 {
				return &ClientInfo{
					Key:           k.Key,
					Name:          k.Name,
					AllowedModels: k.AllowedModels,
					RateLimit:     k.RateLimit,
					Budget:        k.Budget,
				}, true
			}
		}
	}

	return nil, false
}
