package budget

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/config"
)

var (
	ErrHardLimitExceeded = errors.New("assigned budget limit exceeded")
)

// ResetPeriod defines the budget accounting cycle.
type ResetPeriod string

const (
	PeriodDaily   ResetPeriod = "daily"
	PeriodWeekly  ResetPeriod = "weekly"
	PeriodMonthly ResetPeriod = "monthly"
	PeriodTotal   ResetPeriod = "total"
)

// IsValidPeriod checks if a budget reset period is recognized.
func IsValidPeriod(period string) bool {
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "daily", "weekly", "monthly", "total", "":
		return true
	default:
		return false
	}
}

// WindowStart calculates the beginning timestamp of the current budget cycle in UTC.
func WindowStart(period string, now time.Time) time.Time {
	utc := now.UTC()
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "daily":
		return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	case "weekly":
		weekday := int(utc.Weekday())
		if weekday == 0 {
			weekday = 7 // Sunday -> 7
		}
		daysToSubtract := weekday - 1 // Monday is 1, subtract 0
		startOfWeek := utc.AddDate(0, 0, -daysToSubtract)
		return time.Date(startOfWeek.Year(), startOfWeek.Month(), startOfWeek.Day(), 0, 0, 0, 0, time.UTC)
	case "monthly":
		return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
	case "total", "":
		return time.Time{} // Zero time covers all records
	default:
		return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
}

// CheckResult represents the outcome of a budget evaluation.
type CheckResult struct {
	Allowed     bool
	SoftReached bool
	Current     float64
	MaxSpend    float64
	SoftLimit   float64
	Period      string
}

// Evaluate evaluates a client's current spend against their configured budget.
func Evaluate(cfg *config.BudgetConfig, currentSpend float64) CheckResult {
	if cfg == nil || cfg.MaxSpend <= 0 {
		return CheckResult{Allowed: true}
	}

	res := CheckResult{
		Allowed:   true,
		Current:   currentSpend,
		MaxSpend:  cfg.MaxSpend,
		SoftLimit: cfg.SoftLimit,
		Period:    cfg.ResetPeriod,
	}

	if cfg.SoftLimit > 0 && currentSpend >= cfg.SoftLimit {
		res.SoftReached = true
	}

	if currentSpend >= cfg.MaxSpend {
		res.Allowed = false
	}

	return res
}

// FormatBudgetError generates an OpenAI-compatible error message.
func FormatBudgetError(res CheckResult) string {
	return fmt.Sprintf("You have exceeded your assigned %s budget limit ($%.2f / $%.2f)", res.Period, res.Current, res.MaxSpend)
}
