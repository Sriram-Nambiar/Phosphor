package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
)

func TestOpenAIProvider_Send_Success(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gpt-4o-mini",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello world!"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 9, "completion_tokens": 12, "total_tokens": 21}
		}`)
	}))
	defer mockServer.Close()

	cfg := config.ProviderConfig{
		Name:    "mock-openai",
		Type:    config.ProviderTypeOpenAI,
		BaseURL: mockServer.URL,
		APIKey:  "test-key",
		Models:  []string{"gpt-4o-mini"},
	}

	p := NewOpenAIProvider(cfg)
	resp, err := p.Send(context.Background(), &ChatRequest{
		Model: "gpt-4o-mini",
		Messages: []ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "chatcmpl-123" {
		t.Errorf("expected ID chatcmpl-123, got %s", resp.ID)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "Hello world!" {
		t.Errorf("unexpected content: %+v", resp.Choices)
	}
	if resp.Usage.TotalTokens != 21 {
		t.Errorf("expected 21 tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestOpenAIProvider_Send_RateLimit(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Rate limit reached"}}`, http.StatusTooManyRequests)
	}))
	defer mockServer.Close()

	cfg := config.ProviderConfig{
		Name:    "mock-openai",
		Type:    config.ProviderTypeOpenAI,
		BaseURL: mockServer.URL,
	}

	p := NewOpenAIProvider(cfg)
	_, err := p.Send(context.Background(), &ChatRequest{
		Model: "gpt-4o",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsRateLimit(err) {
		t.Errorf("expected IsRateLimit true, got error: %v", err)
	}
}

func TestOpenAIProvider_Stream(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		chunks := []string{
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"role":"assistant","content":"Hello"}}]}`,
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":" there!"}}]}`,
			`data: [DONE]`,
		}

		for _, chunk := range chunks {
			fmt.Fprintf(w, "%s\n\n", chunk)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer mockServer.Close()

	cfg := config.ProviderConfig{
		Name:    "mock-openai",
		Type:    config.ProviderTypeOpenAI,
		BaseURL: mockServer.URL,
	}

	p := NewOpenAIProvider(cfg)
	ch, err := p.Stream(context.Background(), &ChatRequest{
		Model:  "gpt-4o",
		Stream: true,
	})
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	var contents []string
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			contents = append(contents, chunk.Choices[0].Delta.Content)
		}
	}

	if len(contents) != 2 || contents[0] != "Hello" || contents[1] != " there!" {
		t.Errorf("unexpected streamed chunks: %+v", contents)
	}
}

func TestAnthropicProvider_Send(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "anthropic-key" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{
			"id": "msg_01X9",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Greetings from Claude"}],
			"model": "claude-3-5-sonnet",
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 15, "output_tokens": 8}
		}`)
	}))
	defer mockServer.Close()

	cfg := config.ProviderConfig{
		Name:    "mock-anthropic",
		Type:    config.ProviderTypeAnthropic,
		BaseURL: mockServer.URL,
		APIKey:  "anthropic-key",
	}

	p := NewAnthropicProvider(cfg)
	resp, err := p.Send(context.Background(), &ChatRequest{
		Model: "claude-3-5-sonnet",
		Messages: []ChatMessage{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("Anthropic Send failed: %v", err)
	}

	if resp.ID != "msg_01X9" {
		t.Errorf("expected ID msg_01X9, got %s", resp.ID)
	}
	if resp.Choices[0].Message.Content != "Greetings from Claude" {
		t.Errorf("unexpected content: %s", resp.Choices[0].Message.Content)
	}
	if resp.Usage.PromptTokens != 15 || resp.Usage.CompletionTokens != 8 {
		t.Errorf("usage mismatch: %+v", resp.Usage)
	}
}

func TestOllamaProvider_SendAndStream(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// If streaming
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)
		stream, _ := req["stream"].(bool)

		if stream {
			flusher := w.(http.Flusher)
			fmt.Fprintln(w, `{"model":"llama3.2","message":{"role":"assistant","content":"Hi"},"done":false}`)
			flusher.Flush()
			fmt.Fprintln(w, `{"model":"llama3.2","message":{"role":"assistant","content":" there"},"done":true,"prompt_eval_count":10,"eval_count":5}`)
			flusher.Flush()
		} else {
			fmt.Fprintln(w, `{
				"model": "llama3.2",
				"message": {"role": "assistant", "content": "Local Ollama response"},
				"done": true,
				"prompt_eval_count": 12,
				"eval_count": 6
			}`)
		}
	}))
	defer mockServer.Close()

	cfg := config.ProviderConfig{
		Name:    "mock-ollama",
		Type:    config.ProviderTypeOllama,
		BaseURL: mockServer.URL,
	}

	p := NewOllamaProvider(cfg)

	// Test Send
	resp, err := p.Send(context.Background(), &ChatRequest{
		Model: "llama3.2",
		Messages: []ChatMessage{
			{Role: "user", Content: "Ping"},
		},
	})
	if err != nil {
		t.Fatalf("Ollama Send failed: %v", err)
	}
	if resp.Choices[0].Message.Content != "Local Ollama response" {
		t.Errorf("unexpected content: %s", resp.Choices[0].Message.Content)
	}
	if resp.Usage.TotalTokens != 18 {
		t.Errorf("expected 18 tokens, got %d", resp.Usage.TotalTokens)
	}

	// Test Stream
	ch, err := p.Stream(context.Background(), &ChatRequest{
		Model:  "llama3.2",
		Stream: true,
	})
	if err != nil {
		t.Fatalf("Ollama Stream failed: %v", err)
	}

	var chunks []string
	var finalUsage *Usage
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			chunks = append(chunks, chunk.Choices[0].Delta.Content)
		}
		if chunk.Usage != nil {
			finalUsage = chunk.Usage
		}
	}

	if len(chunks) != 2 || chunks[0] != "Hi" || chunks[1] != " there" {
		t.Errorf("unexpected chunks: %+v", chunks)
	}
	if finalUsage == nil || finalUsage.TotalTokens != 15 {
		t.Errorf("expected final usage 15, got %+v", finalUsage)
	}
}

func TestFormatAndWriteSSEChunk(t *testing.T) {
	chunk := StreamChunk{
		ID:      "chatcmpl-test",
		Object:  "chat.completion.chunk",
		Created: 1234567890,
		Model:   "gpt-4o",
		Choices: []StreamChoice{
			{
				Index: 0,
				Delta: StreamDelta{
					Content: "Hello world!",
				},
				FinishReason: "stop",
			},
		},
	}

	// 1. Test FormatSSEChunk
	formatted, err := FormatSSEChunk(chunk)
	if err != nil {
		t.Fatalf("FormatSSEChunk failed: %v", err)
	}
	expectedPrefix := "data: {"
	if !strings.HasPrefix(string(formatted), expectedPrefix) || !strings.HasSuffix(string(formatted), "\n\n") {
		t.Errorf("unexpected SSE chunk format: %s", string(formatted))
	}

	// 2. Test WriteSSEChunk
	var buf bytes.Buffer
	if err := WriteSSEChunk(&buf, chunk); err != nil {
		t.Fatalf("WriteSSEChunk failed: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), formatted) {
		t.Errorf("expected WriteSSEChunk and FormatSSEChunk to match:\nWrite:  %q\nFormat: %q", buf.String(), string(formatted))
	}
}

func BenchmarkFormatSSEChunk(b *testing.B) {
	chunk := StreamChunk{
		ID:      "chatcmpl-bench",
		Object:  "chat.completion.chunk",
		Created: 1234567890,
		Model:   "gpt-4o",
		Choices: []StreamChoice{
			{
				Index: 0,
				Delta: StreamDelta{
					Content: "Streaming token benchmark payload",
				},
			},
		},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = FormatSSEChunk(chunk)
	}
}

func BenchmarkWriteSSEChunk(b *testing.B) {
	chunk := StreamChunk{
		ID:      "chatcmpl-bench",
		Object:  "chat.completion.chunk",
		Created: 1234567890,
		Model:   "gpt-4o",
		Choices: []StreamChoice{
			{
				Index: 0,
				Delta: StreamDelta{
					Content: "Streaming token benchmark payload",
				},
			},
		},
	}
	w := io.Discard
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = WriteSSEChunk(w, chunk)
	}
}

func TestChatResponse_EnsureUsage(t *testing.T) {
	// Case 1: Empty usage gets populated
	resp := &ChatResponse{
		Choices: []ChatChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: "Hello world! How can I help you today?",
				},
			},
		},
	}

	resp.EnsureUsage(15)

	if resp.Usage.PromptTokens != 15 {
		t.Errorf("expected prompt tokens 15, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens <= 0 {
		t.Errorf("expected positive completion tokens, got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != resp.Usage.PromptTokens+resp.Usage.CompletionTokens {
		t.Errorf("expected total tokens sum, got %d", resp.Usage.TotalTokens)
	}

	// Case 2: Pre-existing usage is preserved untouched
	respWithUsage := &ChatResponse{
		Choices: []ChatChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: "Hello",
				},
			},
		},
		Usage: Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	respWithUsage.EnsureUsage(25)

	if respWithUsage.Usage.PromptTokens != 100 || respWithUsage.Usage.CompletionTokens != 50 || respWithUsage.Usage.TotalTokens != 150 {
		t.Errorf("expected existing usage to be preserved, got %+v", respWithUsage.Usage)
	}
}

func TestParseRetryAfter(t *testing.T) {
	// Delta seconds
	if d := ParseRetryAfter("5"); d != 5*time.Second {
		t.Errorf("expected 5s, got %v", d)
	}
	if d := ParseRetryAfter("1.5"); d != 1500*time.Millisecond {
		t.Errorf("expected 1.5s, got %v", d)
	}

	// Empty and invalid
	if d := ParseRetryAfter(""); d != 0 {
		t.Errorf("expected 0 for empty string, got %v", d)
	}
	if d := ParseRetryAfter("invalid-value"); d != 0 {
		t.Errorf("expected 0 for invalid value, got %v", d)
	}
	if d := ParseRetryAfter("-10"); d != 0 {
		t.Errorf("expected 0 for negative seconds, got %v", d)
	}

	// Future HTTP Date
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	dFuture := ParseRetryAfter(future)
	if dFuture <= 0 || dFuture > 12*time.Second {
		t.Errorf("expected around 10s for HTTP-date, got %v", dFuture)
	}

	// Past HTTP Date
	past := time.Now().Add(-10 * time.Second).UTC().Format(http.TimeFormat)
	if dPast := ParseRetryAfter(past); dPast != 0 {
		t.Errorf("expected 0 for past HTTP date, got %v", dPast)
	}
}

func TestExtractRetryAfter(t *testing.T) {
	// Nil error
	if d := ExtractRetryAfter(nil); d != 0 {
		t.Errorf("expected 0 for nil, got %v", d)
	}

	// Non-HTTPError
	if d := ExtractRetryAfter(errors.New("generic error")); d != 0 {
		t.Errorf("expected 0 for generic error, got %v", d)
	}

	// HTTPError with explicit RetryAfter
	err1 := &HTTPError{StatusCode: 429, RetryAfter: 3 * time.Second}
	if d := ExtractRetryAfter(err1); d != 3*time.Second {
		t.Errorf("expected 3s from RetryAfter field, got %v", d)
	}

	// HTTPError with Header
	hdr := make(http.Header)
	hdr.Set("Retry-After", "7")
	err2 := &HTTPError{StatusCode: 429, Header: hdr}
	if d := ExtractRetryAfter(err2); d != 7*time.Second {
		t.Errorf("expected 7s from Header, got %v", d)
	}
}


