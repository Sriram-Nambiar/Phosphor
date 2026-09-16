package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAPICommand_ExportFile(t *testing.T) {
	tmpDir := t.TempDir()
	outFile := filepath.Join(tmpDir, "spec.json")

	openapiOutput = outFile
	openapiPretty = true
	defer func() {
		openapiOutput = ""
	}()

	if err := runOpenAPI(nil, nil); err != nil {
		t.Fatalf("expected runOpenAPI to succeed: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read exported spec: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("exported spec is not valid JSON: %v", err)
	}

	if parsed["openapi"] != "3.1.0" {
		t.Errorf("expected openapi 3.1.0, got %v", parsed["openapi"])
	}
}
