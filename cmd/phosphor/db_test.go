package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sriram-Nambiar/Phosphor/internal/db"
)

func TestDBBackupCommand_Online(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/db/backup" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "ok",
			"destination": "test_online_backup.db",
			"size_bytes":  4096,
		})
	}))
	defer srv.Close()

	dbURL = srv.URL
	dbPathFlag = ""
	dbDestFlag = ""
	defer func() {
		dbURL = ""
		dbPathFlag = ""
		dbDestFlag = ""
	}()

	if err := runDBBackup(nil, []string{"test_online_backup.db"}); err != nil {
		t.Fatalf("expected runDBBackup online to succeed: %v", err)
	}
}

func TestDBBackupCommand_OfflineDirect(t *testing.T) {
	tmpDir := t.TempDir()
	srcDBPath := filepath.Join(tmpDir, "source.db")
	destBackupPath := filepath.Join(tmpDir, "backup.db")

	database, err := db.New(srcDBPath)
	if err != nil {
		t.Fatalf("failed to create source db: %v", err)
	}
	_ = database.LogRequest(context.Background(), &db.RequestLog{
		ID:             "req-backup-cli",
		ModelRequested: "gpt-4o",
		StatusCode:     200,
	})
	_ = database.Flush(context.Background())
	_ = database.Close()

	dbURL = "http://127.0.0.1:59999" // unreachable gateway triggers offline mode
	dbPathFlag = srcDBPath
	dbDestFlag = destBackupPath
	defer func() {
		dbURL = ""
		dbPathFlag = ""
		dbDestFlag = ""
	}()

	if err := runDBBackup(nil, nil); err != nil {
		t.Fatalf("expected offline runDBBackup to succeed: %v", err)
	}

	fi, err := os.Stat(destBackupPath)
	if err != nil {
		t.Fatalf("backup file does not exist: %v", err)
	}
	if fi.Size() == 0 {
		t.Errorf("expected non-empty backup file")
	}
}

func TestDBVacuumCommand_Online(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/db/vacuum" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "ok",
			"size_bytes": 8192,
		})
	}))
	defer srv.Close()

	dbURL = srv.URL
	dbPathFlag = ""
	defer func() {
		dbURL = ""
		dbPathFlag = ""
	}()

	if err := runDBVacuum(nil, nil); err != nil {
		t.Fatalf("expected runDBVacuum online to succeed: %v", err)
	}
}

func TestDBVacuumCommand_OfflineDirect(t *testing.T) {
	tmpDir := t.TempDir()
	srcDBPath := filepath.Join(tmpDir, "source.db")

	database, err := db.New(srcDBPath)
	if err != nil {
		t.Fatalf("failed to create source db: %v", err)
	}
	_ = database.Close()

	dbURL = "http://127.0.0.1:59999" // unreachable
	dbPathFlag = srcDBPath
	defer func() {
		dbURL = ""
		dbPathFlag = ""
	}()

	if err := runDBVacuum(nil, nil); err != nil {
		t.Fatalf("expected offline runDBVacuum to succeed: %v", err)
	}
}
