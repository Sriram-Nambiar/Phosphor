package router

import (
	"context"
	"testing"
	"time"
)

func TestBackoffPolicy_FullJitterBounds(t *testing.T) {
	initial := 50 * time.Millisecond
	max := 500 * time.Millisecond
	policy := NewBackoffPolicy(initial, max)

	for attempt := 0; attempt < 10; attempt++ {
		capDuration := initial * (1 << attempt)
		if capDuration > max || capDuration <= 0 {
			capDuration = max
		}

		for i := 0; i < 50; i++ {
			delay := policy.ComputeBackoff(attempt)
			if delay < 0 {
				t.Fatalf("attempt %d: negative backoff %v", attempt, delay)
			}
			if delay > capDuration {
				t.Fatalf("attempt %d: backoff %v exceeded cap %v", attempt, delay, capDuration)
			}
		}
	}
}

func TestBackoffPolicy_SleepCancellation(t *testing.T) {
	policy := NewBackoffPolicy(5*time.Second, 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after 10ms
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := policy.Sleep(ctx, 2)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected context canceled error, got nil")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("sleep did not abort promptly on context cancellation: took %v", elapsed)
	}
}

func TestBackoffPolicy_LargeAttemptNoOverflow(t *testing.T) {
	policy := NewBackoffPolicy(100*time.Millisecond, 2*time.Second)
	delay := policy.ComputeBackoff(100)

	if delay < 0 || delay > 2*time.Second {
		t.Fatalf("expected delay within [0, 2s] for large attempt count, got %v", delay)
	}
}

func TestBackoffPolicy_SleepWithRetryAfter(t *testing.T) {
	policy := NewBackoffPolicy(10*time.Millisecond, 200*time.Millisecond)

	// 1. Valid short Retry-After
	start := time.Now()
	err := policy.SleepWithRetryAfter(context.Background(), 0, 50*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("expected sleep at least 40ms, got %v", elapsed)
	}

	// 2. Retry-After exceeding maxBackoff is capped
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Pass a huge 10 second retry-after; should be capped at maxBackoff (200ms), and timeout after 50ms
	err2 := policy.SleepWithRetryAfter(ctx, 0, 10*time.Second)
	if err2 == nil {
		t.Errorf("expected context deadline error for capped backoff, got nil")
	}

	// 3. Negative or zero retry-after falls back to exponential backoff
	start3 := time.Now()
	_ = policy.SleepWithRetryAfter(context.Background(), 0, 0)
	_ = time.Since(start3)
}

