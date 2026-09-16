package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/db"
)

func TestLogsCommand_ClientFilter(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_logs.db")

	database, err := db.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}

	ctx := context.Background()
	_ = database.LogRequestSync(ctx, &db.RequestLog{
		ModelRequested: "gpt-4o",
		Provider:       "openai",
		ModelRouted:    "gpt-4o",
		ClientName:     "team-frontend",
		CreatedAt:      time.Now().UTC(),
		StatusCode:     200,
		TotalTokens:    120,
	})
	_ = database.LogRequestSync(ctx, &db.RequestLog{
		ModelRequested: "claude-3-5-sonnet",
		Provider:       "anthropic",
		ModelRouted:    "claude-3-5-sonnet",
		ClientName:     "team-backend",
		CreatedAt:      time.Now().UTC(),
		StatusCode:     200,
		TotalTokens:    250,
	})
	database.Close()

	logsDBPath = dbPath
	logsClient = "team-frontend"
	logsLimit = 10
	logsFailoversOnly = false
	defer func() {
		logsDBPath = ""
		logsClient = ""
		logsLimit = 20
	}()

	if err := runLogs(nil, nil); err != nil {
		t.Fatalf("runLogs failed with client filter: %v", err)
	}
}

func TestLogsCommand_FailoversOnly(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_failovers.db")

	database, err := db.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}

	ctx := context.Background()
	_ = database.LogFailoverSync(ctx, &db.FailoverTrace{
		RequestID:    "req-test-failover",
		FromProvider: "openai",
		ToProvider:   "anthropic",
		Reason:       "HTTP 503 Outage",
		LatencyMs:    125.0,
		Timestamp:    time.Now().UTC(),
	})
	database.Close()

	logsDBPath = dbPath
	logsFailoversOnly = true
	logsLimit = 5
	defer func() {
		logsDBPath = ""
		logsFailoversOnly = false
		logsLimit = 20
	}()

	if err := runLogs(nil, nil); err != nil {
		t.Fatalf("runLogs failed with failovers-only: %v", err)
	}
}
