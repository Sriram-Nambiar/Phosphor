package test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
	"github.com/Sriram-Nambiar/Phosphor/internal/db"
	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
	"github.com/Sriram-Nambiar/Phosphor/internal/proxy"
	"github.com/Sriram-Nambiar/Phosphor/internal/router"
)

func TestE2E_GatewayFailover_NonStreamingAndStreaming(t *testing.T) {
	// 1. Primary mock upstream: fails with 429 Too Many Requests
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Rate limit exceeded on primary"}}`, http.StatusTooManyRequests)
	}))
	defer primaryServer.Close()

	// 2. Secondary mock upstream: succeeds with 200 OK (both non-streaming and streaming)
	secondaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		isStream, _ := req["stream"].(bool)

		if isStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)

			chunks := []string{
				`data: {"id":"chatcmpl-stream-2","choices":[{"delta":{"role":"assistant","content":"Streamed from "}}]}`,
				`data: {"id":"chatcmpl-stream-2","choices":[{"delta":{"content":"secondary!"}}]}`,
				`data: [DONE]`,
			}
			for _, chunk := range chunks {
				fmt.Fprintf(w, "%s\n\n", chunk)
				flusher.Flush()
				time.Sleep(10 * time.Millisecond)
			}
		} else {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{
				"id": "chatcmpl-e2e-succ",
				"object": "chat.completion",
				"created": 1677652288,
				"model": "secondary-model",
				"choices": [{
					"index": 0,
					"message": {"role": "assistant", "content": "Hello from secondary provider!"},
					"finish_reason": "stop"
				}],
				"usage": {"prompt_tokens": 15, "completion_tokens": 10, "total_tokens": 25}
			}`)
		}
	}))
	defer secondaryServer.Close()

	// 3. Configure Phosphor gateway with pure-Go SQLite DB
	database, err := db.New(":memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 8080,
		},
		Routing: config.RoutingConfig{
			DefaultStrategy: config.StrategyPriority,
			TimeoutSeconds:  10,
		},
		CircuitBreaker: config.CircuitBreakerConfig{
			FailureThreshold: 2,
			CooldownSeconds:  30,
		},
		Providers: []config.ProviderConfig{
			{
				Name:           "primary-provider",
				Type:           config.ProviderTypeOpenAI,
				BaseURL:        primaryServer.URL,
				Enabled:        true,
				TimeoutSeconds: 5,
				Cost: config.CostConfig{
					PromptCostPer1M:     2.50,
					CompletionCostPer1M: 10.00,
				},
			},
			{
				Name:           "secondary-provider",
				Type:           config.ProviderTypeOpenAI,
				BaseURL:        secondaryServer.URL,
				Enabled:        true,
				TimeoutSeconds: 5,
				Cost: config.CostConfig{
					PromptCostPer1M:     0.50,
					CompletionCostPer1M: 1.50,
				},
			},
		},
		Models: map[string]config.ModelRule{
			"resilient-chat": {
				Strategy: config.StrategyPriority,
				Targets: []config.TargetModel{
					{Provider: "primary-provider", Model: "primary-model"},
					{Provider: "secondary-provider", Model: "secondary-model"},
				},
			},
		},
	}

	r, err := router.NewRouter(cfg, database)
	if err != nil {
		t.Fatalf("failed to init router: %v", err)
	}

	server := proxy.NewServer(cfg, r, database)
	gatewayServer := httptest.NewServer(server)
	defer gatewayServer.Close()

	client := &http.Client{Timeout: 10 * time.Second}

	// 4. Test Non-Streaming failover
	t.Run("NonStreaming_Failover", func(t *testing.T) {
		reqPayload := `{"model":"resilient-chat","messages":[{"role":"user","content":"Hello Phosphor"}]}`
		resp, err := client.Post(gatewayServer.URL+"/v1/chat/completions", "application/json", bytes.NewReader([]byte(reqPayload)))
		if err != nil {
			t.Fatalf("gateway request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected HTTP 200 OK from gateway, got %d", resp.StatusCode)
		}

		var chatResp provider.ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
			t.Fatalf("failed to decode response JSON: %v", err)
		}

		if chatResp.Choices[0].Message.Content != "Hello from secondary provider!" {
			t.Errorf("expected response from secondary provider, got: %s", chatResp.Choices[0].Message.Content)
		}

		// Verify database recorded the failover
		failovers, err := database.GetRecentFailovers(context.Background(), 10)
		if err != nil {
			t.Fatalf("failed to query failovers: %v", err)
		}
		if len(failovers) != 1 {
			t.Fatalf("expected 1 failover logged, got %d", len(failovers))
		}
		if failovers[0].FromProvider != "primary-provider" || failovers[0].ToProvider != "secondary-provider" {
			t.Errorf("unexpected failover: %+v", failovers[0])
		}
	})

	// 5. Test Streaming failover
	t.Run("Streaming_Failover", func(t *testing.T) {
		reqPayload := `{"model":"resilient-chat","stream":true,"messages":[{"role":"user","content":"Stream this"}]}`
		resp, err := client.Post(gatewayServer.URL+"/v1/chat/completions", "application/json", bytes.NewReader([]byte(reqPayload)))
		if err != nil {
			t.Fatalf("gateway streaming request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected HTTP 200 OK from gateway, got %d", resp.StatusCode)
		}

		contentType := resp.Header.Get("Content-Type")
		if !strings.Contains(contentType, "text/event-stream") {
			t.Errorf("expected text/event-stream content type, got %s", contentType)
		}

		scanner := bufio.NewScanner(resp.Body)
		var receivedText strings.Builder
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					break
				}
				var chunk provider.StreamChunk
				if err := json.Unmarshal([]byte(data), &chunk); err == nil {
					if len(chunk.Choices) > 0 {
						receivedText.WriteString(chunk.Choices[0].Delta.Content)
					}
				}
			}
		}

		if receivedText.String() != "Streamed from secondary!" {
			t.Errorf("expected 'Streamed from secondary!', got '%s'", receivedText.String())
		}
	})

	time.Sleep(100 * time.Millisecond)

	// 6. Verify aggregate stats in DB
	stats, err := database.GetAggregateStats(context.Background())
	if err != nil {
		t.Fatalf("failed to query aggregate stats: %v", err)
	}
	if stats.TotalRequests != 2 {
		t.Errorf("expected 2 total requests in DB, got %d", stats.TotalRequests)
	}
	if stats.TotalFailovers != 2 {
		t.Errorf("expected 2 total failovers logged (one per test), got %d", stats.TotalFailovers)
	}
}
