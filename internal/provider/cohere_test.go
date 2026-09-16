package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
)

func TestCohereProvider_Send_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-cohere-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content-type: %s", r.Header.Get("Content-Type"))
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		var req cohereRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		if req.Model != "command-r-plus" {
			t.Errorf("expected model command-r-plus, got %s", req.Model)
		}
		if len(req.Messages) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(req.Messages))
		}
		if req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
			t.Errorf("unexpected message roles: %+v", req.Messages)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{
			"id": "cohere-chat-12345",
			"finish_reason": "COMPLETE",
			"message": {
				"role": "assistant",
				"content": [
					{
						"type": "text",
						"text": "Hello! I am Cohere Command R Plus."
					}
				]
			},
			"usage": {
				"tokens": {
					"input_tokens": 18,
					"output_tokens": 9
				}
			}
		}`)
	}))
	defer server.Close()

	p := NewCohereProvider(config.ProviderConfig{
		Name:    "cohere-test",
		Type:    config.ProviderTypeCohere,
		BaseURL: server.URL,
		APIKey:  "test-cohere-key",
	})

	if p.Name() != "cohere-test" {
		t.Errorf("expected name 'cohere-test', got %s", p.Name())
	}
	if p.Type() != config.ProviderTypeCohere {
		t.Errorf("expected type 'cohere', got %s", p.Type())
	}

	req := &ChatRequest{
		Model: "command-r-plus",
		Messages: []ChatMessage{
			{Role: "system", Content: "You are a helpful assistant"},
			{Role: "user", Content: "Hello world"},
		},
	}

	resp, err := p.Send(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected Send error: %v", err)
	}

	if resp.ID != "cohere-chat-12345" {
		t.Errorf("expected ID 'cohere-chat-12345', got %s", resp.ID)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	choice := resp.Choices[0]
	if choice.Message.Content != "Hello! I am Cohere Command R Plus." {
		t.Errorf("unexpected content: %q", choice.Message.Content)
	}
	if choice.FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", choice.FinishReason)
	}
	if resp.Usage.PromptTokens != 18 || resp.Usage.CompletionTokens != 9 || resp.Usage.TotalTokens != 27 {
		t.Errorf("unexpected usage counts: %+v", resp.Usage)
	}
}

func TestCohereProvider_Stream_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		// Chunk 1: content-delta
		fmt.Fprintf(w, "data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"Hello \"}}}}\n\n")
		flusher.Flush()

		// Chunk 2: content-delta
		fmt.Fprintf(w, "data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"from Cohere!\"}}}}\n\n")
		flusher.Flush()

		// Chunk 3: message-end
		fmt.Fprintf(w, "data: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"COMPLETE\",\"usage\":{\"tokens\":{\"input_tokens\":10,\"output_tokens\":5}}}}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := NewCohereProvider(config.ProviderConfig{
		Name:    "cohere-stream-test",
		Type:    config.ProviderTypeCohere,
		BaseURL: server.URL,
	})

	req := &ChatRequest{
		Model: "command-r",
		Messages: []ChatMessage{
			{Role: "user", Content: "Stream test"},
		},
	}

	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected Stream error: %v", err)
	}

	var chunks []StreamChunk
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream chunk error: %v", c.Err)
		}
		chunks = append(chunks, c)
	}

	if len(chunks) != 3 {
		t.Fatalf("expected 3 stream chunks, got %d", len(chunks))
	}

	if chunks[0].Choices[0].Delta.Content != "Hello " {
		t.Errorf("expected 'Hello ', got %q", chunks[0].Choices[0].Delta.Content)
	}
	if chunks[1].Choices[0].Delta.Content != "from Cohere!" {
		t.Errorf("expected 'from Cohere!', got %q", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", chunks[2].Choices[0].FinishReason)
	}
	if chunks[2].Usage.TotalTokens != 15 {
		t.Errorf("expected 15 total tokens, got %d", chunks[2].Usage.TotalTokens)
	}
}

func TestCohereProvider_ErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"message":"You have exceeded your rate limit."}`))
	}))
	defer server.Close()

	p := NewCohereProvider(config.ProviderConfig{
		Name:    "cohere-err-test",
		Type:    config.ProviderTypeCohere,
		BaseURL: server.URL,
	})

	_, err := p.Send(context.Background(), &ChatRequest{Model: "command"})
	if err == nil {
		t.Fatal("expected Send error on 429, got nil")
	}

	if !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "exceeded your rate limit") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestCohereProvider_FactoryCreation(t *testing.T) {
	cfg := config.ProviderConfig{
		Name:    "cohere-prod",
		Type:    config.ProviderTypeCohere,
		BaseURL: "https://api.cohere.com/v2/chat",
		Models:  []string{"command-r-plus", "command-r"},
	}

	prov, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create Cohere provider via factory: %v", err)
	}

	if prov.Type() != config.ProviderTypeCohere {
		t.Errorf("expected provider type cohere, got %s", prov.Type())
	}
	if !prov.SupportsModel("command-r-plus") {
		t.Error("expected model command-r-plus to be supported")
	}
	if prov.SupportsModel("gpt-4o") {
		t.Error("expected model gpt-4o to NOT be supported")
	}
}
