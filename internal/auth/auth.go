package auth

import (
	"context"
	"crypto/subtle"
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
// to prevent timing attacks. Returns the matching ClientInfo if authentication succeeds.
func Authenticate(rawKey string, configuredKeys []config.APIKeyConfig) (*ClientInfo, bool) {
	if rawKey == "" {
		return nil, false
	}

	rawBytes := []byte(rawKey)
	for _, k := range configuredKeys {
		cfgBytes := []byte(k.Key)
		// subtle.ConstantTimeCompare requires slices of equal length to match
		if len(rawBytes) == len(cfgBytes) && subtle.ConstantTimeCompare(rawBytes, cfgBytes) == 1 {
			return &ClientInfo{
				Key:           k.Key,
				Name:          k.Name,
				AllowedModels: k.AllowedModels,
				RateLimit:     k.RateLimit,
			}, true
		}
	}

	return nil, false
}
