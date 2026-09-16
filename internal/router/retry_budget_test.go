package router

import (
	"testing"
)

func TestRetryBudget_Limits(t *testing.T) {
	// 20% ratio, min 2 retries
	rb := NewRetryBudget(0.2, 2)

	// Can use minimum 2 retries immediately
	if !rb.CanRetry() {
		t.Error("expected retry 1 to be allowed")
	}
	if !rb.CanRetry() {
		t.Error("expected retry 2 to be allowed")
	}

	// 3rd retry should be denied because no regular requests recorded yet
	if rb.CanRetry() {
		t.Error("expected retry 3 to be denied")
	}

	// Record 10 regular requests -> adds 10 * 0.2 = 2 more allowed retries
	for i := 0; i < 10; i++ {
		rb.RecordRequest()
	}

	// Retry 3 and 4 should now succeed
	if !rb.CanRetry() {
		t.Error("expected retry 3 to be allowed after regular requests")
	}
	if !rb.CanRetry() {
		t.Error("expected retry 4 to be allowed after regular requests")
	}

	// Retry 5 should fail
	if rb.CanRetry() {
		t.Error("expected retry 5 to be denied")
	}
}

func TestRetryBudget_NilSafe(t *testing.T) {
	var rb *RetryBudget
	rb.RecordRequest()
	if !rb.CanRetry() {
		t.Error("expected nil RetryBudget to permit retries")
	}
	reg, ret, allowed := rb.Stats()
	if reg != 0 || ret != 0 || allowed != 0 {
		t.Errorf("expected zero stats for nil budget")
	}
}
