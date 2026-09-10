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

type OllamaProvider struct {
	cfg        config.ProviderConfig
	httpClient *http.Client
}

func NewOllamaProvider(cfg config.ProviderConfig) *OllamaProvider {
	timeout := 60 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	return &OllamaProvider{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *OllamaProvider) Name() string {
	return p.cfg.Name
}

func (p *OllamaProvider) Type() config.ProviderType {
	return p.cfg.Type
}

func (p *OllamaProvider) GetBaseURL() string {
	return p.cfg.BaseURL
}

func (p *OllamaProvider) SupportsModel(model string) bool {
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

func (p *OllamaProvider) getEndpointURL() string {
	baseURL := strings.TrimRight(p.cfg.BaseURL, "/")
	if strings.HasSuffix(baseURL, "/api/chat") {
		return baseURL
	}
	return baseURL + "/api/chat"
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaChatResponse struct {
	Model           string        `json:"model"`
	CreatedAt       string        `json:"created_at"`
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	TotalDuration   int64         `json:"total_duration"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
}

func (p *OllamaProvider) Send(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	var msgs []ollamaMessage
	for _, m := range req.Messages {
		msgs = append(msgs, ollamaMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	opts := make(map[string]any)
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		opts["num_predict"] = *req.MaxTokens
	}

	oReq := ollamaChatRequest{
		Model:    req.Model,
		Messages: msgs,
		Stream:   false,
		Options:  opts,
	}

	reqBody, err := json.Marshal(oReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ollama request: %w", err)
	}

	url := p.getEndpointURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read ollama response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       string(bodyBytes),
			Provider:   p.cfg.Name,
		}
	}

	var oResp ollamaChatResponse
	if err := json.Unmarshal(bodyBytes, &oResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ollama response: %w", err)
	}

	return &ChatResponse{
		ID:      "chatcmpl-" + uuid.New().String(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   oResp.Model,
		Choices: []ChatChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    oResp.Message.Role,
					Content: oResp.Message.Content,
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     oResp.PromptEvalCount,
			CompletionTokens: oResp.EvalCount,
			TotalTokens:      oResp.PromptEvalCount + oResp.EvalCount,
		},
	}, nil
}

func (p *OllamaProvider) Stream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	var msgs []ollamaMessage
	for _, m := range req.Messages {
		msgs = append(msgs, ollamaMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	opts := make(map[string]any)
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		opts["num_predict"] = *req.MaxTokens
	}

	oReq := ollamaChatRequest{
		Model:    req.Model,
		Messages: msgs,
		Stream:   true,
		Options:  opts,
	}

	reqBody, err := json.Marshal(oReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ollama stream request: %w", err)
	}

	url := p.getEndpointURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama stream request failed: %w", err)
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
	streamID := "chatcmpl-" + uuid.New().String()

	go func() {
		defer resp.Body.Close()
		defer close(ch)

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Err: ctx.Err()}
				return
			default:
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var oResp ollamaChatResponse
			if err := json.Unmarshal([]byte(line), &oResp); err != nil {
				continue
			}

			finishReason := ""
			var usage *Usage
			if oResp.Done {
				finishReason = "stop"
				usage = &Usage{
					PromptTokens:     oResp.PromptEvalCount,
					CompletionTokens: oResp.EvalCount,
					TotalTokens:      oResp.PromptEvalCount + oResp.EvalCount,
				}
			}

			chunk := StreamChunk{
				ID:      streamID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   oResp.Model,
				Choices: []StreamChoice{
					{
						Index: 0,
						Delta: StreamDelta{
							Role:    oResp.Message.Role,
							Content: oResp.Message.Content,
						},
						FinishReason: finishReason,
					},
				},
				Usage: usage,
			}

			select {
			case ch <- chunk:
			case <-ctx.Done():
				ch <- StreamChunk{Err: ctx.Err()}
				return
			}

			if oResp.Done {
				return
			}
		}

		if err := scanner.Err(); err != nil && err != io.EOF {
			ch <- StreamChunk{Err: err}
		}
	}()

	return ch, nil
}
