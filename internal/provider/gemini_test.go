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

func TestGeminiProvider_Send(t *testing.T) {
	var receivedKey string
	var receivedBody geminiRequest

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("x-goog-api-key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedBody)

		if !strings.Contains(r.URL.Path, "models/gemini-2.0-flash:generateContent") {
			t.Errorf("unexpected URL path: %s", r.URL.Path)
		}

		resp := geminiResponse{
			Candidates: []geminiCandidate{
				{
					Content: geminiContent{
						Role: "model",
						Parts: []geminiPart{
							{Text: "Hello from Gemini!"},
						},
					},
					FinishReason: "STOP",
					Index:        0,
				},
			},
			UsageMetadata: geminiUsageMetadata{
				PromptTokenCount:     12,
				CandidatesTokenCount: 8,
				TotalTokenCount:      20,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	p := NewGeminiProvider(config.ProviderConfig{
		Name:    "google-gemini",
		Type:    config.ProviderTypeGemini,
		BaseURL: mockServer.URL,
		APIKey:  "secret-gemini-key",
		Models:  []string{"gemini-2.0-flash"},
	})

	if !p.SupportsModel("gemini-2.0-flash") {
		t.Errorf("expected model to be supported")
	}

	temp := 0.7
	maxTokens := 500
	resp, err := p.Send(context.Background(), &ChatRequest{
		Model: "gemini-2.0-flash",
		Messages: []ChatMessage{
			{Role: "system", Content: "Be concise"},
			{Role: "user", Content: "Hi"},
		},
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	})

	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if receivedKey != "secret-gemini-key" {
		t.Errorf("expected API key 'secret-gemini-key', got %s", receivedKey)
	}

	if receivedBody.SystemInstruction == nil || len(receivedBody.SystemInstruction.Parts) == 0 {
		t.Errorf("expected system instruction to be populated, got %+v", receivedBody.SystemInstruction)
	} else if receivedBody.SystemInstruction.Parts[0].Text != "Be concise" {
		t.Errorf("expected system instruction 'Be concise', got %s", receivedBody.SystemInstruction.Parts[0].Text)
	}

	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "Hello from Gemini!" {
		t.Errorf("unexpected choice: %+v", resp.Choices)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %s", resp.Choices[0].FinishReason)
	}
	if resp.Usage.TotalTokens != 20 || resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 8 {
		t.Errorf("unexpected usage: %+v", resp.Usage)
	}
}

func TestGeminiProvider_Stream(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Errorf("unexpected streaming path: %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			`data: {"candidates":[{"content":{"parts":[{"text":"Streamed "}]}}]}`,
			`data: {"candidates":[{"content":{"parts":[{"text":"Gemini"}]},"finishReason":"STOP"}]}`,
			`data: [DONE]`,
		}

		for _, chunk := range chunks {
			fmt.Fprintf(w, "%s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer mockServer.Close()

	p := NewGeminiProvider(config.ProviderConfig{
		Name:    "google-gemini",
		Type:    config.ProviderTypeGemini,
		BaseURL: mockServer.URL,
		APIKey:  "test-key",
	})

	streamChan, err := p.Stream(context.Background(), &ChatRequest{
		Model: "gemini-2.0-flash",
		Messages: []ChatMessage{
			{Role: "user", Content: "Stream test"},
		},
	})
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	var accumulated strings.Builder
	var lastFinishReason string

	for chunk := range streamChan {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if len(chunk.Choices) > 0 {
			accumulated.WriteString(chunk.Choices[0].Delta.Content)
			if chunk.Choices[0].FinishReason != "" {
				lastFinishReason = chunk.Choices[0].FinishReason
			}
		}
	}

	if accumulated.String() != "Streamed Gemini" {
		t.Errorf("expected accumulated 'Streamed Gemini', got %q", accumulated.String())
	}
	if lastFinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", lastFinishReason)
	}
}

func TestGeminiProvider_ErrorResponse(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    400,
				"message": "Invalid argument: model not found",
				"status":  "INVALID_ARGUMENT",
			},
		})
	}))
	defer mockServer.Close()

	p := NewGeminiProvider(config.ProviderConfig{
		Name:    "google-gemini",
		Type:    config.ProviderTypeGemini,
		BaseURL: mockServer.URL,
	})

	_, err := p.Send(context.Background(), &ChatRequest{
		Model: "nonexistent",
		Messages: []ChatMessage{
			{Role: "user", Content: "test"},
		},
	})

	if err == nil {
		t.Fatal("expected error on 400 Bad Request, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected error to mention 400, got %v", err)
	}
}

func TestGeminiProvider_EndpointNormalization(t *testing.T) {
	tests := []struct {
		baseURL  string
		model    string
		stream   bool
		expected string
	}{
		{"", "gemini-2.0-flash", false, "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent"},
		{"https://generativelanguage.googleapis.com/v1beta/", "gemini-2.0-flash", false, "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent"},
		{"https://generativelanguage.googleapis.com/v1beta/openai", "gemini-2.0-flash", false, "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent"},
		{"https://generativelanguage.googleapis.com/v1beta/openai/", "models/gemini-2.0-flash", true, "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse"},
	}

	for _, tt := range tests {
		p := NewGeminiProvider(config.ProviderConfig{BaseURL: tt.baseURL})
		if got := p.getEndpointURL(tt.model, tt.stream); got != tt.expected {
			t.Errorf("baseURL %q: expected %q, got %q", tt.baseURL, tt.expected, got)
		}
	}
}
