package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigCheck_ValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "valid_config.yaml")

	content := `
server:
  host: "127.0.0.1"
  port: 8080
database:
  path: "test.db"
providers:
  - name: "openai-test"
    type: "openai"
    base_url: "https://api.openai.com/v1"
    enabled: true
    models: ["gpt-4o"]
models:
  default:
    strategy: "priority"
    targets:
      - provider: "openai-test"
        model: "gpt-4o"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	if err := runConfigCheck(nil, []string{configPath}); err != nil {
		t.Fatalf("expected runConfigCheck to succeed for valid config, got: %v", err)
	}
}

func TestConfigCheck_InvalidFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid_config.yaml")

	// Invalid config: enabled provider with invalid base_url scheme (ftp)
	content := `
server:
  host: "127.0.0.1"
  port: 8080
providers:
  - name: "bad-provider"
    type: "openai"
    base_url: "ftp://invalid-url"
    enabled: true
models:
  default:
    strategy: "priority"
    targets:
      - provider: "bad-provider"
        model: "gpt-4o"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	if err := runConfigCheck(nil, []string{configPath}); err == nil {
		t.Fatalf("expected runConfigCheck to fail for invalid provider URL, got nil")
	}
}

func TestConfigCheck_NonExistentFile(t *testing.T) {
	err := runConfigCheck(nil, []string{"non_existent_config_9999.yaml"})
	if err == nil {
		t.Fatalf("expected error for non-existent config file, got nil")
	}
}

func TestConfigView_Success(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "view_config.yaml")

	content := `
server:
  host: "127.0.0.1"
  port: 8080
auth:
  enabled: true
  keys:
    - name: "client-a"
      key: "sk-phosphor-secret-abcdef123456"
providers:
  - name: "openai-test"
    type: "openai"
    base_url: "https://api.openai.com/v1"
    api_key: "sk-openai-top-secret-99999"
    enabled: true
models:
  default:
    strategy: "priority"
    targets:
      - provider: "openai-test"
        model: "gpt-4o"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	if err := runConfigView(nil, []string{configPath}); err != nil {
		t.Fatalf("expected runConfigView to succeed, got: %v", err)
	}
}
