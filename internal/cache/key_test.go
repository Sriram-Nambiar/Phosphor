package cache

import (
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

func TestComputeKey_Determinism(t *testing.T) {
	temp := 0.7
	maxTokens := 150

	req1 := &provider.ChatRequest{
		Model: "gpt-4o",
		Messages: []provider.ChatMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello world!"},
		},
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	}

	req2 := &provider.ChatRequest{
		Model: "  GPT-4O  ",
		Messages: []provider.ChatMessage{
			{Role: "SYSTEM", Content: " You are helpful. "},
			{Role: "user", Content: "Hello world!"},
		},
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	}

	key1 := ComputeKey(req1, "tenant-a")
	key2 := ComputeKey(req2, "tenant-a")

	if key1 != key2 {
		t.Fatalf("expected identical keys for normalized requests, got:\nkey1=%s\nkey2=%s", key1, key2)
	}

	// Different namespace should differ
	keyTenantB := ComputeKey(req1, "tenant-b")
	if key1 == keyTenantB {
		t.Fatalf("expected different keys for different namespaces")
	}

	// Different temperature should differ
	tempOther := 0.8
	req3 := &provider.ChatRequest{
		Model:       "gpt-4o",
		Messages:    req1.Messages,
		Temperature: &tempOther,
		MaxTokens:   &maxTokens,
	}
	key3 := ComputeKey(req3, "tenant-a")
	if key1 == key3 {
		t.Fatalf("expected different keys for different temperatures")
	}
}

func TestComputeKey_EmptyNil(t *testing.T) {
	if ComputeKey(nil, "") != "" {
		t.Fatalf("expected empty key for nil request")
	}
}
