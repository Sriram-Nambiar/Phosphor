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

// GeminiProvider implements the Provider interface for Google's native Gemini API.
type GeminiProvider struct {
	cfg          config.ProviderConfig
	httpClient   *http.Client
	streamClient *http.Client
}

// NewGeminiProvider creates a new Gemini native API provider instance.
func NewGeminiProvider(cfg config.ProviderConfig) *GeminiProvider {
	timeout := 30 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	return &GeminiProvider{
		cfg:          cfg,
		httpClient:   NewHTTPClient(timeout),
		streamClient: NewStreamingHTTPClient(),
	}
}

func (p *GeminiProvider) Name() string {
	return p.cfg.Name
}

func (p *GeminiProvider) Type() config.ProviderType {
	return p.cfg.Type
}

func (p *GeminiProvider) GetBaseURL() string {
	return p.cfg.BaseURL
}

func (p *GeminiProvider) SupportsModel(model string) bool {
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

func (p *GeminiProvider) getEndpointURL(model string, stream bool) string {
	baseURL := strings.TrimRight(p.cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta"
	}

	cleanModel := strings.TrimPrefix(model, "models/")
	action := "generateContent"
	if stream {
		action = "streamGenerateContent?alt=sse"
	}

	return fmt.Sprintf("%s/models/%s:%s", baseURL, cleanModel, action)
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

type geminiRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
	Index        int           `json:"index"`
}

type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate   `json:"candidates"`
	UsageMetadata geminiUsageMetadata `json:"usageMetadata"`
	Error         *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

func (p *GeminiProvider) convertRequest(req *ChatRequest) geminiRequest {
	var gReq geminiRequest
	var systemParts []geminiPart

	for _, m := range req.Messages {
		switch strings.ToLower(m.Role) {
		case "system":
			systemParts = append(systemParts, geminiPart{Text: m.Content})
		case "assistant":
			gReq.Contents = append(gReq.Contents, geminiContent{
				Role:  "model",
				Parts: []geminiPart{{Text: m.Content}},
			})
		default: // "user" and others
			gReq.Contents = append(gReq.Contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: m.Content}},
			})
		}
	}

	if len(systemParts) > 0 {
		gReq.SystemInstruction = &geminiContent{
			Parts: systemParts,
		}
	}

	var genCfg geminiGenerationConfig
	hasGenCfg := false

	if req.Temperature != nil {
		genCfg.Temperature = req.Temperature
		hasGenCfg = true
	}
	if req.TopP != nil {
		genCfg.TopP = req.TopP
		hasGenCfg = true
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		genCfg.MaxOutputTokens = *req.MaxTokens
		hasGenCfg = true
	}

	if req.Stop != nil {
		switch s := req.Stop.(type) {
		case string:
			if s != "" {
				genCfg.StopSequences = []string{s}
				hasGenCfg = true
			}
		case []interface{}:
			var stops []string
			for _, item := range s {
				if str, ok := item.(string); ok && str != "" {
					stops = append(stops, str)
				}
			}
			if len(stops) > 0 {
				genCfg.StopSequences = stops
				hasGenCfg = true
			}
		case []string:
			if len(s) > 0 {
				genCfg.StopSequences = s
				hasGenCfg = true
			}
		}
	}

	if hasGenCfg {
		gReq.GenerationConfig = &genCfg
	}

	return gReq
}

func (p *GeminiProvider) mapFinishReason(reason string) string {
	switch strings.ToUpper(reason) {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	default:
		return "stop"
	}
}

func (p *GeminiProvider) Send(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	gemReq := p.convertRequest(req)
	bodyBytes, err := json.Marshal(gemReq)
	if err != nil {
		return nil, fmt.Errorf("failed to encode gemini request: %w", err)
	}

	endpoint := p.getEndpointURL(req.Model, false)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("x-goog-api-key", p.cfg.APIKey)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini request failed for %s: %w", p.cfg.Name, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read gemini response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       string(respBody),
			Provider:   p.cfg.Name,
			Header:     resp.Header,
			RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	var gemResp geminiResponse
	if err := json.Unmarshal(respBody, &gemResp); err != nil {
		return nil, fmt.Errorf("failed to parse gemini response: %w", err)
	}

	if gemResp.Error != nil {
		return nil, fmt.Errorf("gemini api error: %s (code %d)", gemResp.Error.Message, gemResp.Error.Code)
	}

	var contentBuilder strings.Builder
	finishReason := "stop"

	if len(gemResp.Candidates) > 0 {
		cand := gemResp.Candidates[0]
		for _, part := range cand.Content.Parts {
			contentBuilder.WriteString(part.Text)
		}
		if cand.FinishReason != "" {
			finishReason = p.mapFinishReason(cand.FinishReason)
		}
	}

	return &ChatResponse{
		ID:      "chatcmpl-" + uuid.New().String(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []ChatChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: contentBuilder.String(),
				},
				FinishReason: finishReason,
			},
		},
		Usage: Usage{
			PromptTokens:     gemResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: gemResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gemResp.UsageMetadata.TotalTokenCount,
		},
	}, nil
}

func (p *GeminiProvider) Stream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	gemReq := p.convertRequest(req)
	bodyBytes, err := json.Marshal(gemReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal gemini request: %w", err)
	}

	endpoint := p.getEndpointURL(req.Model, true)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create streaming request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("x-goog-api-key", p.cfg.APIKey)
	}

	resp, err := p.streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini streaming request failed for %s: %w", p.cfg.Name, err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		header := resp.Header
		resp.Body.Close()
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       string(body),
			Provider:   p.cfg.Name,
			Header:     header,
			RetryAfter: ParseRetryAfter(header.Get("Retry-After")),
		}
	}

	chunkChan := make(chan StreamChunk, 32)
	reqID := "chatcmpl-" + uuid.New().String()

	go func() {
		defer resp.Body.Close()
		defer close(chunkChan)

		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF && !strings.Contains(err.Error(), "canceled") {
					chunkChan <- StreamChunk{
						Err: fmt.Errorf("gemini stream read error: %w", err),
					}
				}
				return
			}

			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}

			dataPayload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataPayload == "" || dataPayload == "[DONE]" {
				continue
			}

			var gemResp geminiResponse
			if err := json.Unmarshal([]byte(dataPayload), &gemResp); err != nil {
				continue
			}

			if len(gemResp.Candidates) > 0 {
				cand := gemResp.Candidates[0]
				var deltaText strings.Builder
				for _, part := range cand.Content.Parts {
					deltaText.WriteString(part.Text)
				}

				finishReason := ""
				if cand.FinishReason != "" && cand.FinishReason != "UNSPECIFIED" {
					finishReason = p.mapFinishReason(cand.FinishReason)
				}

				chunkChan <- StreamChunk{
					ID:      reqID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []StreamChoice{
						{
							Index: 0,
							Delta: StreamDelta{
								Role:    "assistant",
								Content: deltaText.String(),
							},
							FinishReason: finishReason,
						},
					},
				}
			}
		}
	}()

	return chunkChan, nil
}
