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
)

type AnthropicProvider struct {
	cfg        config.ProviderConfig
	httpClient *http.Client
}

func NewAnthropicProvider(cfg config.ProviderConfig) *AnthropicProvider {
	timeout := 30 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	return &AnthropicProvider{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *AnthropicProvider) Name() string {
	return p.cfg.Name
}

func (p *AnthropicProvider) Type() config.ProviderType {
	return p.cfg.Type
}

func (p *AnthropicProvider) GetBaseURL() string {
	return p.cfg.BaseURL
}

func (p *AnthropicProvider) SupportsModel(model string) bool {
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

func (p *AnthropicProvider) getEndpointURL() string {
	baseURL := strings.TrimRight(p.cfg.BaseURL, "/")
	if strings.HasSuffix(baseURL, "/messages") {
		return baseURL
	}
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/messages"
	}
	return baseURL + "/v1/messages"
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (p *AnthropicProvider) convertRequest(req *ChatRequest) *anthropicRequest {
	var systemParts []string
	var msgs []anthropicMessage

	for _, m := range req.Messages {
		if strings.EqualFold(m.Role, "system") {
			systemParts = append(systemParts, m.Content)
		} else {
			role := strings.ToLower(m.Role)
			if role != "user" && role != "assistant" {
				role = "user"
			}
			msgs = append(msgs, anthropicMessage{
				Role:    role,
				Content: m.Content,
			})
		}
	}

	maxTokens := 4096
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	return &anthropicRequest{
		Model:       req.Model,
		System:      strings.Join(systemParts, "\n\n"),
		Messages:    msgs,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
	}
}

func (p *AnthropicProvider) Send(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	antReq := p.convertRequest(req)
	antReq.Stream = false

	reqBody, err := json.Marshal(antReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal anthropic request: %w", err)
	}

	url := p.getEndpointURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request failed for %s: %w", p.cfg.Name, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       string(bodyBytes),
			Provider:   p.cfg.Name,
		}
	}

	var antResp anthropicResponse
	if err := json.Unmarshal(bodyBytes, &antResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal anthropic response JSON: %w", err)
	}

	var fullContent strings.Builder
	for _, block := range antResp.Content {
		if block.Type == "text" {
			fullContent.WriteString(block.Text)
		}
	}

	finishReason := "stop"
	if antResp.StopReason == "max_tokens" {
		finishReason = "length"
	}

	return &ChatResponse{
		ID:      antResp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   antResp.Model,
		Choices: []ChatChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: fullContent.String(),
				},
				FinishReason: finishReason,
			},
		},
		Usage: Usage{
			PromptTokens:     antResp.Usage.InputTokens,
			CompletionTokens: antResp.Usage.OutputTokens,
			TotalTokens:      antResp.Usage.InputTokens + antResp.Usage.OutputTokens,
		},
	}, nil
}

func (p *AnthropicProvider) Stream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	antReq := p.convertRequest(req)
	antReq.Stream = true

	reqBody, err := json.Marshal(antReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal streaming anthropic request: %w", err)
	}

	url := p.getEndpointURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", p.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("streaming request failed for %s: %w", p.cfg.Name, err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       string(bodyBytes),
			Provider:   p.cfg.Name,
		}
	}

	ch := make(chan StreamChunk, 32)

	go func() {
		defer resp.Body.Close()
		defer close(ch)

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		var messageID string
		var modelName string

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Err: ctx.Err()}
				return
			default:
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				var eventMap map[string]interface{}
				if err := json.Unmarshal([]byte(data), &eventMap); err != nil {
					continue
				}

				eventType, _ := eventMap["type"].(string)
				switch eventType {
				case "message_start":
					if msgObj, ok := eventMap["message"].(map[string]interface{}); ok {
						if id, ok := msgObj["id"].(string); ok {
							messageID = id
						}
						if m, ok := msgObj["model"].(string); ok {
							modelName = m
						}
					}
					// Send initial role chunk
					ch <- StreamChunk{
						ID:      messageID,
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   modelName,
						Choices: []StreamChoice{
							{
								Index: 0,
								Delta: StreamDelta{
									Role: "assistant",
								},
							},
						},
					}

				case "content_block_delta":
					if deltaObj, ok := eventMap["delta"].(map[string]interface{}); ok {
						if deltaType, ok := deltaObj["type"].(string); ok && deltaType == "text_delta" {
							if text, ok := deltaObj["text"].(string); ok {
								ch <- StreamChunk{
									ID:      messageID,
									Object:  "chat.completion.chunk",
									Created: time.Now().Unix(),
									Model:   modelName,
									Choices: []StreamChoice{
										{
											Index: 0,
											Delta: StreamDelta{
												Content: text,
											},
										},
									},
								}
							}
						}
					}

				case "message_delta":
					finishReason := "stop"
					if deltaObj, ok := eventMap["delta"].(map[string]interface{}); ok {
						if stopReason, ok := deltaObj["stop_reason"].(string); ok && stopReason == "max_tokens" {
							finishReason = "length"
						}
					}
					ch <- StreamChunk{
						ID:      messageID,
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   modelName,
						Choices: []StreamChoice{
							{
								Index:        0,
								FinishReason: finishReason,
							},
						},
					}

				case "message_stop":
					return
				}
			}
		}

		if err := scanner.Err(); err != nil && err != io.EOF {
			ch <- StreamChunk{Err: err}
		}
	}()

	return ch, nil
}
