package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
)

type ChatMessage struct {
	Role       string      `json:"role"`
	Content    string      `json:"content"`
	Name       string      `json:"name,omitempty"`
	ToolCalls  interface{} `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	TopP        *float64      `json:"top_p,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Stop        interface{}   `json:"stop,omitempty"`
}

type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   Usage        `json:"usage"`
}

type StreamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        StreamDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type StreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
	Raw     []byte         `json:"-"`
	Err     error          `json:"-"`
}

// Provider represents an upstream LLM provider client.
type Provider interface {
	Name() string
	Type() config.ProviderType
	Send(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	Stream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error)
	SupportsModel(model string) bool
	GetBaseURL() string
}

// HTTPError represents an error with an HTTP status code returned by an upstream provider.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
	Provider   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("upstream %s returned HTTP %d: %s", e.Provider, e.StatusCode, e.Body)
}

// IsRateLimit checks if an error represents an HTTP 429 rate limit.
func IsRateLimit(err error) bool {
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr.StatusCode == 429
	}
	return false
}

// IsServerError checks if an error represents an HTTP 5xx server error.
func IsServerError(err error) bool {
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr.StatusCode >= 500 && httpErr.StatusCode <= 599
	}
	return false
}

// FormatSSEChunk converts a StreamChunk into standard SSE data line: "data: {json}\n\n"
func FormatSSEChunk(chunk StreamChunk) ([]byte, error) {
	data, err := json.Marshal(chunk)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("data: %s\n\n", data)), nil
}
