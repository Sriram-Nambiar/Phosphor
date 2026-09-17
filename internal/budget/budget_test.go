package budget

import (
	"testing"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
)

func TestWindowStart(t *testing.T) {
	now := time.Date(2026, 9, 16, 14, 30, 0, 0, time.UTC)

	daily := WindowStart("daily", now)
	if daily != time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC) {
		t.Errorf("unexpected daily start: %v", daily)
	}

	monthly := WindowStart("monthly", now)
	if monthly != time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) {
		t.Errorf("unexpected monthly start: %v", monthly)
	}

	total := WindowStart("total", now)
	if !total.IsZero() {
		t.Errorf("expected zero time for total, got %v", total)
	}

	// 2026-09-16 is a Wednesday. Monday of that week is 2026-09-14.
	weekly := WindowStart("weekly", now)
	if weekly != time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC) {
		t.Errorf("unexpected weekly start: %v", weekly)
	}

	if !IsValidPeriod("daily") || !IsValidPeriod("weekly") || !IsValidPeriod("monthly") || !IsValidPeriod("total") {
		t.Error("expected daily, weekly, monthly, total to be valid periods")
	}
	if IsValidPeriod("hourly") || IsValidPeriod("yearly") {
		t.Error("expected hourly, yearly to be invalid periods")
	}
}

func TestEvaluate_Limits(t *testing.T) {
	cfg := &config.BudgetConfig{
		MaxSpend:    50.0,
		SoftLimit:   40.0,
		ResetPeriod: "monthly",
	}

	// 1. Below limits
	res1 := Evaluate(cfg, 25.0)
	if !res1.Allowed || res1.SoftReached {
		t.Errorf("expected allowed=true, soft=false, got %+v", res1)
	}

	// 2. Exceeds soft limit
	res2 := Evaluate(cfg, 42.0)
	if !res2.Allowed || !res2.SoftReached {
		t.Errorf("expected allowed=true, soft=true, got %+v", res2)
	}

	// 3. Exceeds hard limit
	res3 := Evaluate(cfg, 50.50)
	if res3.Allowed {
		t.Errorf("expected allowed=false, got %+v", res3)
	}

	errMsg := FormatBudgetError(res3)
	if errMsg == "" {
		t.Errorf("expected non-empty error message")
	}

	// 4. Nil config
	resNil := Evaluate(nil, 100.0)
	if !resNil.Allowed {
		t.Errorf("expected allowed for nil config")
	}
}
