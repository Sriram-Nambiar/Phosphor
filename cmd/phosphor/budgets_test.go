package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBudgetsCommand_HTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/budgets" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(adminBudgetsResponse{
			Object: "list",
			Data: []clientBudgetStatus{
				{
					ClientName:     "team-alpha",
					MaxSpend:       100.0,
					SoftLimit:      80.0,
					CurrentSpend:   45.25,
					ResetPeriod:    "monthly",
					RemainingSpend: 54.75,
					SoftReached:    false,
					HardExceeded:   false,
				},
				{
					ClientName:     "team-beta",
					MaxSpend:       50.0,
					SoftLimit:      40.0,
					CurrentSpend:   52.10,
					ResetPeriod:    "monthly",
					RemainingSpend: 0.0,
					SoftReached:    true,
					HardExceeded:   true,
				},
			},
		})
	}))
	defer srv.Close()

	budgetsURL = srv.URL

	if err := runBudgets(nil, nil); err != nil {
		t.Fatalf("expected runBudgets to succeed: %v", err)
	}
}
