package auth

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
)

func TestExtractAPIKey(t *testing.T) {
	// 1. Bearer token in Authorization
	req1 := httptest.NewRequest("GET", "/", nil)
	req1.Header.Set("Authorization", "Bearer sk-test-12345")
	if key := ExtractAPIKey(req1); key != "sk-test-12345" {
		t.Errorf("expected sk-test-12345, got %s", key)
	}

	// 2. Direct token in Authorization
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Authorization", "sk-direct-key")
	if key := ExtractAPIKey(req2); key != "sk-direct-key" {
		t.Errorf("expected sk-direct-key, got %s", key)
	}

	// 3. x-api-key header
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.Header.Set("x-api-key", "sk-header-key")
	if key := ExtractAPIKey(req3); key != "sk-header-key" {
		t.Errorf("expected sk-header-key, got %s", key)
	}

	// 4. Missing headers
	req4 := httptest.NewRequest("GET", "/", nil)
	if key := ExtractAPIKey(req4); key != "" {
		t.Errorf("expected empty string, got %s", key)
	}
}

func TestAuthenticate(t *testing.T) {
	keys := []config.APIKeyConfig{
		{
			Key:           "ph-secret-token-1",
			Name:          "prod-team",
			AllowedModels: []string{"gpt-4o"},
			RateLimit:     100,
		},
		{
			Key:           "ph-secret-token-2",
			Name:          "dev-team",
			AllowedModels: []string{"gpt-4o-mini"},
			RateLimit:     20,
		},
	}

	// 1. Success match 1
	info, ok := Authenticate("ph-secret-token-1", keys)
	if !ok || info == nil {
		t.Fatal("expected successful authentication for token 1")
	}
	if info.Name != "prod-team" {
		t.Errorf("expected prod-team, got %s", info.Name)
	}

	// 2. Success match 2
	info2, ok := Authenticate("ph-secret-token-2", keys)
	if !ok || info2 == nil {
		t.Fatal("expected successful authentication for token 2")
	}
	if info2.Name != "dev-team" {
		t.Errorf("expected dev-team, got %s", info2.Name)
	}

	// 3. Wrong key
	_, okWrong := Authenticate("wrong-key", keys)
	if okWrong {
		t.Error("expected authentication to fail for wrong key")
	}

	// 4. Empty key
	_, okEmpty := Authenticate("", keys)
	if okEmpty {
		t.Error("expected authentication to fail for empty key")
	}
}

func TestClientContext(t *testing.T) {
	client := ClientInfo{
		Key:           "test-key",
		Name:          "service-a",
		AllowedModels: []string{"gpt-4o"},
		RateLimit:     50,
	}

	ctx := context.Background()
	_, found := GetClientInfo(ctx)
	if found {
		t.Error("expected no client info in empty context")
	}

	ctxWithClient := WithClientInfo(ctx, client)
	retrieved, found := GetClientInfo(ctxWithClient)
	if !found {
		t.Fatal("expected client info to be found in context")
	}
	if retrieved.Name != "service-a" {
		t.Errorf("expected service-a, got %s", retrieved.Name)
	}
	if retrieved.RateLimit != 50 {
		t.Errorf("expected rate limit 50, got %d", retrieved.RateLimit)
	}
}
