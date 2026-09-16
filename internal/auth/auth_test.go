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

func TestAuthenticate_SHA256HashedKeys(t *testing.T) {
	rawSecret := "super-secure-token-xyz"
	hash := HashKey(rawSecret)

	keys := []config.APIKeyConfig{
		{
			KeyHash: hash,
			Name:    "secure-service",
		},
		{
			Key:  "sha256:" + hash,
			Name: "prefixed-service",
		},
	}

	// 1. Match via KeyHash
	info1, ok1 := Authenticate(rawSecret, []config.APIKeyConfig{keys[0]})
	if !ok1 || info1 == nil || info1.Name != "secure-service" {
		t.Errorf("expected match on KeyHash, got ok=%v, info=%+v", ok1, info1)
	}

	// 2. Match via sha256: prefix in Key
	info2, ok2 := Authenticate(rawSecret, []config.APIKeyConfig{keys[1]})
	if !ok2 || info2 == nil || info2.Name != "prefixed-service" {
		t.Errorf("expected match on sha256: prefix, got ok=%v, info=%+v", ok2, info2)
	}

	// 3. Failed match with wrong raw secret
	_, ok3 := Authenticate("invalid-token", keys)
	if ok3 {
		t.Error("expected failure for wrong token against hashed keys")
	}
}

func TestClientInfo_CanAccessModel(t *testing.T) {
	// 1. Unrestricted access when AllowedModels is empty
	unrestricted := ClientInfo{}
	if !unrestricted.CanAccessModel("gpt-4o") {
		t.Error("expected unrestricted client to access gpt-4o")
	}

	// 2. Specific exact models
	restricted := ClientInfo{
		AllowedModels: []string{"gpt-4o-mini", "llama-3.2"},
	}
	if !restricted.CanAccessModel("gpt-4o-mini") {
		t.Error("expected access to gpt-4o-mini")
	}
	if restricted.CanAccessModel("gpt-4o") {
		t.Error("expected restriction on gpt-4o")
	}

	// 3. Wildcard prefix pattern
	wildcard := ClientInfo{
		AllowedModels: []string{"gpt-*", "claude-*"},
	}
	if !wildcard.CanAccessModel("gpt-4o") {
		t.Error("expected access to gpt-4o via gpt-*")
	}
	if !wildcard.CanAccessModel("gpt-4o-mini") {
		t.Error("expected access to gpt-4o-mini via gpt-*")
	}
	if wildcard.CanAccessModel("llama-3.2") {
		t.Error("expected restriction on llama-3.2")
	}

	// 4. Global wildcard *
	allAccess := ClientInfo{
		AllowedModels: []string{"*"},
	}
	if !allAccess.CanAccessModel("any-model") {
		t.Error("expected * wildcard to access any model")
	}
}

func TestAuthenticate_WithBudget(t *testing.T) {
	budgetCfg := &config.BudgetConfig{
		MaxSpend:    100.0,
		SoftLimit:   80.0,
		ResetPeriod: "monthly",
	}

	keys := []config.APIKeyConfig{
		{
			Key:    "sk-budgeted-key",
			Name:   "enterprise-tenant",
			Budget: budgetCfg,
		},
	}

	info, ok := Authenticate("sk-budgeted-key", keys)
	if !ok || info == nil {
		t.Fatal("expected successful authentication")
	}
	if info.Budget == nil {
		t.Fatal("expected non-nil budget on client info")
	}
	if info.Budget.MaxSpend != 100.0 || info.Budget.ResetPeriod != "monthly" {
		t.Errorf("budget config mismatch: %+v", info.Budget)
	}
}


