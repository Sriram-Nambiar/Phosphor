package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCacheCommands_HTTP(t *testing.T) {
	clearedCalled := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/admin/cache/stats":
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			_ = json.NewEncoder(w).Encode(cacheStatsResponse{
				Enabled:    true,
				Capacity:   1000,
				Size:       42,
				Hits:       150,
				Misses:     25,
				HitRatio:   0.857,
				TTLSeconds: 3600,
			})
		case "/v1/admin/cache/clear":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			clearedCalled = true
			_ = json.NewEncoder(w).Encode(cacheClearResponse{
				Status:        "ok",
				ClearedL1:     42,
				ClearedL2Rows: 100,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cacheURL = srv.URL

	// 1. Test stats command
	if err := runCacheStats(nil, nil); err != nil {
		t.Fatalf("expected runCacheStats to succeed: %v", err)
	}

	// 2. Test clear command
	if err := runCacheClear(nil, nil); err != nil {
		t.Fatalf("expected runCacheClear to succeed: %v", err)
	}
	if !clearedCalled {
		t.Errorf("expected clear endpoint to be called")
	}
}
