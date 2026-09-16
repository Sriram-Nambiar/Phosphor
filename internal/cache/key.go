package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

// ComputeKey derives a deterministic SHA-256 cache key for an incoming ChatRequest.
// It normalizes messages, model name, sampling parameters, tools, and response format.
func ComputeKey(req *provider.ChatRequest, namespace string) string {
	if req == nil {
		return ""
	}

	h := sha256.New()
	if namespace != "" {
		h.Write([]byte("ns:" + strings.TrimSpace(namespace) + "|"))
	}

	// Model normalization (lowercase trimmed)
	h.Write([]byte("m:" + strings.ToLower(strings.TrimSpace(req.Model)) + "|"))

	// Messages normalization
	for i, msg := range req.Messages {
		h.Write([]byte(fmt.Sprintf("msg[%d]:r=%s:c=%s|", i, strings.ToLower(strings.TrimSpace(msg.Role)), strings.TrimSpace(msg.Content))))
		if msg.Name != "" {
			h.Write([]byte("n=" + strings.TrimSpace(msg.Name) + "|"))
		}
		if msg.ToolCallID != "" {
			h.Write([]byte("tcid=" + strings.TrimSpace(msg.ToolCallID) + "|"))
		}
	}

	// Sampling parameters
	if req.Temperature != nil {
		h.Write([]byte(fmt.Sprintf("temp:%.4f|", *req.Temperature)))
	}
	if req.TopP != nil {
		h.Write([]byte(fmt.Sprintf("topp:%.4f|", *req.TopP)))
	}
	if req.MaxTokens != nil {
		h.Write([]byte(fmt.Sprintf("maxt:%d|", *req.MaxTokens)))
	}

	// Response format
	if req.ResponseFormat != nil && req.ResponseFormat.Type != "" {
		h.Write([]byte("rf:" + strings.ToLower(strings.TrimSpace(req.ResponseFormat.Type)) + "|"))
	}

	// Tools / Functions serialization
	if req.Tools != nil {
		toolsBytes, err := json.Marshal(req.Tools)
		if err == nil {
			h.Write([]byte("tools:" + string(toolsBytes) + "|"))
		}
	}
	if req.ToolChoice != nil {
		choiceBytes, err := json.Marshal(req.ToolChoice)
		if err == nil {
			h.Write([]byte("tc:" + string(choiceBytes) + "|"))
		}
	}
	if req.Functions != nil {
		funcsBytes, err := json.Marshal(req.Functions)
		if err == nil {
			h.Write([]byte("fn:" + string(funcsBytes) + "|"))
		}
	}
	if req.Stop != nil {
		stopBytes, err := json.Marshal(req.Stop)
		if err == nil {
			h.Write([]byte("stop:" + string(stopBytes) + "|"))
		}
	}

	return "cache:prompt:" + hex.EncodeToString(h.Sum(nil))
}
