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

type OpenAIProvider struct {
	cfg          config.ProviderConfig
	httpClient   *http.Client
	streamClient *http.Client
}

func NewOpenAIProvider(cfg config.ProviderConfig) *OpenAIProvider {
	timeout := 30 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	return &OpenAIProvider{
		cfg:          cfg,
		httpClient:   NewHTTPClient(timeout),
		streamClient: NewStreamingHTTPClient(),
	}
}

func (p *OpenAIProvider) Name() string {
	return p.cfg.Name
}

func (p *OpenAIProvider) Type() config.ProviderType {
	return p.cfg.Type
}

func (p *OpenAIProvider) GetBaseURL() string {
	return p.cfg.BaseURL
}

func (p *OpenAIProvider) SupportsModel(model string) bool {
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

func (p *OpenAIProvider) getEndpointURL() string {
	baseURL := strings.TrimRight(p.cfg.BaseURL, "/")
	if strings.HasSuffix(baseURL, "/chat/completions") {
		return baseURL
	}
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/chat/completions"
	}
	return baseURL + "/v1/chat/completions"
}

func (p *OpenAIProvider) Send(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	reqCopy := *req
	reqCopy.Stream = false

	reqBody, err := json.Marshal(reqCopy)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := p.getEndpointURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

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

	var chatResp ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response JSON: %w", err)
	}

	return &chatResp, nil
}

func (p *OpenAIProvider) Stream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	reqCopy := *req
	reqCopy.Stream = true

	reqBody, err := json.Marshal(reqCopy)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal streaming request: %w", err)
	}

	url := p.getEndpointURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
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
		// Allocate initial buffer to prevent excessive allocations
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
			if line == "" || strings.HasPrefix(line, ":") {
				// Empty line or SSE comment
				continue
			}

			if !strings.HasPrefix(line, "data:") {
				continue
			}

			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				return
			}

			if strings.Contains(data, `"error"`) {
				var errResp struct {
					Error struct {
						Message string `json:"message"`
						Type    string `json:"type"`
						Code    any    `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal([]byte(data), &errResp); err == nil && errResp.Error.Message != "" {
					ch <- StreamChunk{Err: fmt.Errorf("upstream stream error: %s", errResp.Error.Message)}
					return
				}
			}

			var chunk StreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				ch <- StreamChunk{Err: fmt.Errorf("failed to parse SSE chunk: %w", err)}
				return
			}
			chunk.Raw = []byte(data)

			select {
			case ch <- chunk:
			case <-ctx.Done():
				ch <- StreamChunk{Err: ctx.Err()}
				return
			}
		}

		if err := scanner.Err(); err != nil && err != io.EOF {
			ch <- StreamChunk{Err: err}
		}
	}()

	return ch, nil
}
