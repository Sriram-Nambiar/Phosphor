package router

import (
	"sync"
	"time"
)

// RetryBudget limits the rate of retries/failovers relative to total requests to prevent cascading failures.
type RetryBudget struct {
	mu             sync.Mutex
	ratio          float64
	minRetries     int
	regularCount   int
	retryCount     int
	window         time.Duration
	lastWindow     time.Time
}

// NewRetryBudget creates a new retry budget tracker.
// ratio: percentage of total requests that may be retried (e.g. 0.20 = 20%).
// minRetries: minimum retries permitted per window (ensuring low-traffic systems don't choke on a single retry).
func NewRetryBudget(ratio float64, minRetries int) *RetryBudget {
	if ratio <= 0 {
		return nil
	}
	if minRetries <= 0 {
		minRetries = 5
	}
	return &RetryBudget{
		ratio:      ratio,
		minRetries: minRetries,
		window:     10 * time.Second,
		lastWindow: time.Now(),
	}
}

// RecordRequest records an initial (non-retry) request attempt.
func (b *RetryBudget) RecordRequest() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.advanceWindow(time.Now())
	b.regularCount++
}

// CanRetry checks whether another retry is allowed under the current budget.
// If allowed, it automatically records the retry and returns true.
func (b *RetryBudget) CanRetry() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.advanceWindow(now)

	allowedRetries := int(float64(b.regularCount)*b.ratio) + b.minRetries
	if b.retryCount < allowedRetries {
		b.retryCount++
		return true
	}
	return false
}

// Stats returns current counts for debugging and metrics.
func (b *RetryBudget) Stats() (regular int, retries int, allowed int) {
	if b == nil {
		return 0, 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	allowed = int(float64(b.regularCount)*b.ratio) + b.minRetries
	return b.regularCount, b.retryCount, allowed
}

func (b *RetryBudget) advanceWindow(now time.Time) {
	if now.Sub(b.lastWindow) >= b.window {
		b.regularCount = 0
		b.retryCount = 0
		b.lastWindow = now
	}
}
