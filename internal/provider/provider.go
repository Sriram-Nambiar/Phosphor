package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
)

type ChatMessage struct {
	Role       string      `json:"role"`
	Content    string      `json:"content"`
	Name       string      `json:"name,omitempty"`
	ToolCalls  interface{} `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

type ResponseFormat struct {
	Type string `json:"type,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []ChatMessage   `json:"messages"`
	Stream         bool            `json:"stream,omitempty"`
	StreamOptions  *StreamOptions  `json:"stream_options,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	TopP           *float64        `json:"top_p,omitempty"`
	MaxTokens      *int            `json:"max_tokens,omitempty"`
	Stop           interface{}     `json:"stop,omitempty"`
	Tools          interface{}     `json:"tools,omitempty"`
	ToolChoice     interface{}     `json:"tool_choice,omitempty"`
	Functions      interface{}     `json:"functions,omitempty"`
	FunctionCall   interface{}     `json:"function_call,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	User           string          `json:"user,omitempty"`
}

// RequiresTools returns true if the request specifies tools, functions, or tool choice.
func (r *ChatRequest) RequiresTools() bool {
	if r == nil {
		return false
	}
	if r.Tools != nil {
		if s, ok := r.Tools.([]interface{}); ok && len(s) == 0 {
			// empty tools array
		} else {
			return true
		}
	}
	if r.Functions != nil {
		if s, ok := r.Functions.([]interface{}); ok && len(s) == 0 {
			// empty functions array
		} else {
			return true
		}
	}
	return r.ToolChoice != nil || r.FunctionCall != nil
}

// RequiresJSONMode returns true if the request specifies json_object response formatting.
func (r *ChatRequest) RequiresJSONMode() bool {
	if r == nil || r.ResponseFormat == nil {
		return false
	}
	return r.ResponseFormat.Type == "json_object"
}

// RequiresVision returns true if the request contains image content parts or URLs.
func (r *ChatRequest) RequiresVision() bool {
	if r == nil {
		return false
	}
	for _, m := range r.Messages {
		if strings.Contains(m.Content, "data:image/") ||
			strings.Contains(m.Content, "\"type\":\"image_url\"") ||
			strings.Contains(m.Content, "\"image_url\"") {
			return true
		}
	}
	return false
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

// EnsureUsage populates missing or zero token usage in the response using heuristic estimation.
func (resp *ChatResponse) EnsureUsage(promptTokens int) {
	if resp == nil {
		return
	}
	if resp.Usage.PromptTokens <= 0 {
		resp.Usage.PromptTokens = promptTokens
	}
	if resp.Usage.CompletionTokens <= 0 {
		totalChars := 0
		for _, c := range resp.Choices {
			totalChars += len(c.Message.Content)
		}
		estComp := totalChars / 4
		if estComp < 1 && totalChars > 0 {
			estComp = 1
		}
		resp.Usage.CompletionTokens = estComp
	}
	if resp.Usage.TotalTokens <= 0 {
		resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	}
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
	Header     http.Header
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("upstream %s returned HTTP %d: %s", e.Provider, e.StatusCode, e.Body)
}

// ParseRetryAfter parses standard HTTP Retry-After header string.
// It supports both delta-seconds (e.g. "5", "1.5") and HTTP-date RFC1123 format.
func ParseRetryAfter(val string) time.Duration {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(val, 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	if t, err := http.ParseTime(val); err == nil {
		diff := time.Until(t)
		if diff > 0 {
			return diff
		}
	}
	return 0
}

// ExtractRetryAfter inspects an error and returns the upstream Retry-After duration if available.
func ExtractRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.RetryAfter > 0 {
			return httpErr.RetryAfter
		}
		if httpErr.Header != nil {
			return ParseRetryAfter(httpErr.Header.Get("Retry-After"))
		}
	}
	return 0
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

var sseBufferPool = sync.Pool{
	New: func() any {
		return bytes.NewBuffer(make([]byte, 0, 512))
	},
}

var sseDataPrefix = []byte("data: ")

// WriteSSEChunk serializes and writes an SSE event directly to w using a pooled buffer,
// avoiding heap allocations and redundant slice copies.
func WriteSSEChunk(w io.Writer, chunk StreamChunk) error {
	buf := sseBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer sseBufferPool.Put(buf)

	buf.Write(sseDataPrefix)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(chunk); err != nil {
		return err
	}
	buf.WriteByte('\n')

	_, err := w.Write(buf.Bytes())
	return err
}

// FormatSSEChunk converts a StreamChunk into standard SSE data line: "data: {json}\n\n" using buffer pooling.
func FormatSSEChunk(chunk StreamChunk) ([]byte, error) {
	buf := sseBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer sseBufferPool.Put(buf)

	buf.Write(sseDataPrefix)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(chunk); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')

	res := make([]byte, buf.Len())
	copy(res, buf.Bytes())
	return res, nil
}

