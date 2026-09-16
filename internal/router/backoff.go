package router

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// BackoffPolicy computes exponential backoff with full jitter.
// sleep = random_between(0, min(maxBackoff, initialBackoff * 2^attempt))
type BackoffPolicy struct {
	mu             sync.Mutex
	rng            *rand.Rand
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// NewBackoffPolicy creates a new BackoffPolicy with initial and maximum backoff bounds.
func NewBackoffPolicy(initialBackoff, maxBackoff time.Duration) *BackoffPolicy {
	if initialBackoff <= 0 {
		initialBackoff = 100 * time.Millisecond
	}
	if maxBackoff <= 0 {
		maxBackoff = 2000 * time.Millisecond
	}
	if maxBackoff < initialBackoff {
		maxBackoff = initialBackoff
	}
	return &BackoffPolicy{
		rng:            rand.New(rand.NewSource(time.Now().UnixNano())),
		initialBackoff: initialBackoff,
		maxBackoff:     maxBackoff,
	}
}

// ComputeBackoff calculates jittered backoff duration for a given attempt index.
func (b *BackoffPolicy) ComputeBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 30 {
		attempt = 30
	}

	capDuration := b.initialBackoff * (1 << attempt)
	if capDuration > b.maxBackoff || capDuration <= 0 {
		capDuration = b.maxBackoff
	}

	b.mu.Lock()
	randomFactor := b.rng.Float64()
	b.mu.Unlock()

	jittered := time.Duration(float64(capDuration) * randomFactor)
	return jittered
}

// Sleep pauses execution for the computed jittered backoff duration, or aborts early if context is canceled.
func (b *BackoffPolicy) Sleep(ctx context.Context, attempt int) error {
	return b.SleepWithRetryAfter(ctx, attempt, 0)
}

// SleepWithRetryAfter pauses execution for either the specified retryAfter duration (if positive)
// or the computed exponential backoff duration. It caps the duration at maxBackoff to prevent hung requests.
func (b *BackoffPolicy) SleepWithRetryAfter(ctx context.Context, attempt int, retryAfter time.Duration) error {
	delay := retryAfter
	if delay <= 0 {
		delay = b.ComputeBackoff(attempt)
	} else if delay > b.maxBackoff {
		delay = b.maxBackoff
	}

	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

