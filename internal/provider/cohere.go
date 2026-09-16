package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/google/uuid"
)

// CohereProvider implements the Provider interface for Cohere's native Chat API (v2).
type CohereProvider struct {
	cfg          config.ProviderConfig
	httpClient   *http.Client
	streamClient *http.Client
}

// NewCohereProvider creates a new Cohere native API provider instance.
func NewCohereProvider(cfg config.ProviderConfig) *CohereProvider {
	timeout := 30 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	return &CohereProvider{
		cfg:          cfg,
		httpClient:   NewHTTPClient(timeout),
		streamClient: NewStreamingHTTPClient(),
	}
}

func (p *CohereProvider) Name() string {
	return p.cfg.Name
}

func (p *CohereProvider) Type() config.ProviderType {
	return config.ProviderTypeCohere
}

func (p *CohereProvider) GetBaseURL() string {
	return p.cfg.BaseURL
}

func (p *CohereProvider) SupportsModel(model string) bool {
	if len(p.cfg.Models) == 0 {
		return true
	}
	for _, m := range p.cfg.Models {
		if strings.EqualFold(m, model) {
			return true
		}
	}
	return false
}

func (p *CohereProvider) getEndpointURL() string {
	baseURL := strings.TrimRight(p.cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.cohere.com/v2/chat"
		return baseURL
	}
	if strings.HasSuffix(baseURL, "/v2/chat") || strings.HasSuffix(baseURL, "/chat") {
		return baseURL
	}
	if strings.HasSuffix(baseURL, "/v2") {
		return baseURL + "/chat"
	}
	return baseURL + "/v2/chat"
}

type cohereMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type cohereRequest struct {
	Model         string          `json:"model"`
	Messages      []cohereMessage `json:"messages"`
	Stream        bool            `json:"stream,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	MaxTokens     *int            `json:"max_tokens,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
}

type cohereContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type cohereResponse struct {
	ID           string `json:"id"`
	FinishReason string `json:"finish_reason"`
	Message      struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Text  string `json:"text,omitempty"` // Fallback for v1 endpoints
	Usage *struct {
		BilledUnits struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"billed_units"`
		Tokens struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"tokens"`
	} `json:"usage"`
}

func (p *CohereProvider) Send(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	url := p.getEndpointURL()

	cohereReq := cohereRequest{
		Model:       req.Model,
		Stream:      false,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
	}

	if req.Stop != nil {
		switch v := req.Stop.(type) {
		case string:
			cohereReq.StopSequences = []string{v}
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					cohereReq.StopSequences = append(cohereReq.StopSequences, s)
				}
			}
		case []string:
			cohereReq.StopSequences = v
		}
	}

	for _, m := range req.Messages {
		role := m.Role
		if role == "" {
			role = "user"
		}
		cohereReq.Messages = append(cohereReq.Messages, cohereMessage{
			Role:    role,
			Content: m.Content,
		})
	}

	bodyBytes, err := json.Marshal(cohereReq)
	if err != nil {
		return nil, fmt.Errorf("failed to encode cohere request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("cohere request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read cohere response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBytes, &errResp)
		msg := errResp.Message
		if msg == "" {
			msg = string(respBytes)
		}
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       msg,
			Provider:   p.cfg.Name,
			Header:     resp.Header,
			RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	var cohereResp cohereResponse
	if err := json.Unmarshal(respBytes, &cohereResp); err != nil {
		return nil, fmt.Errorf("failed to parse cohere response: %w", err)
	}

	// Extract generated text
	var textContent strings.Builder
	for _, block := range cohereResp.Message.Content {
		textContent.WriteString(block.Text)
	}
	if textContent.Len() == 0 && cohereResp.Text != "" {
		textContent.WriteString(cohereResp.Text)
	}

	// Map finish reason
	finishReason := "stop"
	switch strings.ToUpper(cohereResp.FinishReason) {
	case "COMPLETE":
		finishReason = "stop"
	case "MAX_TOKENS":
		finishReason = "length"
	case "ERROR":
		finishReason = "error"
	default:
		if cohereResp.FinishReason != "" {
			finishReason = strings.ToLower(cohereResp.FinishReason)
		}
	}

	// Map usage
	var usage Usage
	if cohereResp.Usage != nil {
		if cohereResp.Usage.Tokens.InputTokens > 0 || cohereResp.Usage.Tokens.OutputTokens > 0 {
			usage.PromptTokens = cohereResp.Usage.Tokens.InputTokens
			usage.CompletionTokens = cohereResp.Usage.Tokens.OutputTokens
		} else {
			usage.PromptTokens = cohereResp.Usage.BilledUnits.InputTokens
			usage.CompletionTokens = cohereResp.Usage.BilledUnits.OutputTokens
		}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	respID := cohereResp.ID
	if respID == "" {
		respID = "chatcmpl-" + uuid.New().String()
	}

	return &ChatResponse{
		ID:      respID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []ChatChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: textContent.String(),
				},
				FinishReason: finishReason,
			},
		},
		Usage: usage,
	}, nil
}

func (p *CohereProvider) Stream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	url := p.getEndpointURL()

	cohereReq := cohereRequest{
		Model:       req.Model,
		Stream:      true,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
	}

	if req.Stop != nil {
		switch v := req.Stop.(type) {
		case string:
			cohereReq.StopSequences = []string{v}
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					cohereReq.StopSequences = append(cohereReq.StopSequences, s)
				}
			}
		case []string:
			cohereReq.StopSequences = v
		}
	}

	for _, m := range req.Messages {
		role := m.Role
		if role == "" {
			role = "user"
		}
		cohereReq.Messages = append(cohereReq.Messages, cohereMessage{
			Role:    role,
			Content: m.Content,
		})
	}

	bodyBytes, err := json.Marshal(cohereReq)
	if err != nil {
		return nil, fmt.Errorf("failed to encode cohere streaming request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("cohere stream request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBytes, _ := io.ReadAll(resp.Body)
		var errResp struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBytes, &errResp)
		msg := errResp.Message
		if msg == "" {
			msg = string(respBytes)
		}
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       msg,
			Provider:   p.cfg.Name,
			Header:     resp.Header,
			RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	chunkChan := make(chan StreamChunk)

	go func() {
		defer resp.Body.Close()
		defer close(chunkChan)

		reader := bufio.NewReader(resp.Body)
		chunkID := "chatcmpl-" + uuid.New().String()

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err != io.EOF && ctx.Err() == nil {
					chunkChan <- StreamChunk{
						Err: fmt.Errorf("error reading stream chunk: %w", err),
					}
				}
				return
			}

			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}

			// Look for "data: " or direct JSON
			data := trimmed
			if bytes.HasPrefix(data, []byte("data: ")) {
				data = bytes.TrimPrefix(data, []byte("data: "))
			}
			if bytes.Equal(data, []byte("[DONE]")) {
				return
			}

			var event struct {
				Type  string `json:"type"`
				Delta *struct {
					Message *struct {
						Content *struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"message"`
					FinishReason string `json:"finish_reason"`
					Usage        *struct {
						BilledUnits struct {
							InputTokens  int `json:"input_tokens"`
							OutputTokens int `json:"output_tokens"`
						} `json:"billed_units"`
						Tokens struct {
							InputTokens  int `json:"input_tokens"`
							OutputTokens int `json:"output_tokens"`
						} `json:"tokens"`
					} `json:"usage"`
				} `json:"delta"`
				// v1 fallback events
				EventType string `json:"event_type"`
				Text      string `json:"text"`
			}

			if err := json.Unmarshal(data, &event); err != nil {
				continue
			}

			// Handle v2 content-delta
			if event.Type == "content-delta" && event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.Content != nil {
				text := event.Delta.Message.Content.Text
				if text != "" {
					chunkChan <- StreamChunk{
						ID:      chunkID,
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   req.Model,
						Choices: []StreamChoice{
							{
								Index: 0,
								Delta: StreamDelta{
									Role:    "assistant",
									Content: text,
								},
							},
						},
					}
				}
			} else if event.Type == "message-end" && event.Delta != nil {
				// Handle end of stream
				finishReason := "stop"
				if strings.ToUpper(event.Delta.FinishReason) == "MAX_TOKENS" {
					finishReason = "length"
				}

				var usage *Usage
				if event.Delta.Usage != nil {
					u := Usage{
						PromptTokens:     event.Delta.Usage.Tokens.InputTokens,
						CompletionTokens: event.Delta.Usage.Tokens.OutputTokens,
					}
					if u.PromptTokens == 0 && u.CompletionTokens == 0 {
						u.PromptTokens = event.Delta.Usage.BilledUnits.InputTokens
						u.CompletionTokens = event.Delta.Usage.BilledUnits.OutputTokens
					}
					u.TotalTokens = u.PromptTokens + u.CompletionTokens
					usage = &u
				}

				chunkChan <- StreamChunk{
					ID:      chunkID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []StreamChoice{
						{
							Index:        0,
							FinishReason: finishReason,
						},
					},
					Usage: usage,
				}
				return
			} else if event.EventType == "text-generation" && event.Text != "" {
				// v1 fallback
				chunkChan <- StreamChunk{
					ID:      chunkID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []StreamChoice{
						{
							Index: 0,
							Delta: StreamDelta{
								Role:    "assistant",
								Content: event.Text,
							},
						},
					},
				}
			} else if event.EventType == "stream-end" {
				return
			}
		}
	}()

	return chunkChan, nil
}
