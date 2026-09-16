package cache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeduplicator_ConcurrentIdenticalCalls(t *testing.T) {
	d := NewDeduplicator()
	var execCount atomic.Int32

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	results := make([][]byte, numGoroutines)
	sharedFlags := make([]bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			val, shared, err := d.Do("test-key", func() ([]byte, error) {
				execCount.Add(1)
				time.Sleep(50 * time.Millisecond) // Simulate upstream latency
				return []byte("expensive-llm-response"), nil
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			results[idx] = val
			sharedFlags[idx] = shared
		}(i)
	}

	wg.Wait()

	if count := execCount.Load(); count != 1 {
		t.Fatalf("expected exactly 1 execution for 10 concurrent requests, got %d", count)
	}

	sharedCount := 0
	for i := 0; i < numGoroutines; i++ {
		if string(results[i]) != "expensive-llm-response" {
			t.Errorf("goroutine %d got wrong response: %s", i, string(results[i]))
		}
		if sharedFlags[i] {
			sharedCount++
		}
	}

	if sharedCount == 0 {
		t.Errorf("expected at least some callers to have shared=true, got 0")
	}
}

func TestDeduplicator_SequentialCalls(t *testing.T) {
	d := NewDeduplicator()
	var execCount atomic.Int32

	for i := 0; i < 3; i++ {
		val, shared, err := d.Do("seq-key", func() ([]byte, error) {
			execCount.Add(1)
			return []byte("result"), nil
		})
		if err != nil {
			t.Fatalf("call %d failed: %v", i, err)
		}
		if string(val) != "result" {
			t.Errorf("call %d got wrong value: %s", i, string(val))
		}
		if shared {
			t.Errorf("call %d should not be shared", i)
		}
	}

	if count := execCount.Load(); count != 3 {
		t.Fatalf("expected 3 executions for 3 sequential calls, got %d", count)
	}
}

func TestDeduplicator_ErrorPropagation(t *testing.T) {
	d := NewDeduplicator()
	expectedErr := errors.New("upstream connection reset")

	var wg sync.WaitGroup
	wg.Add(3)

	for i := 0; i < 3; i++ {
		go func() {
			defer wg.Done()
			_, _, err := d.Do("err-key", func() ([]byte, error) {
				time.Sleep(20 * time.Millisecond)
				return nil, expectedErr
			})
			if !errors.Is(err, expectedErr) {
				t.Errorf("expected %v, got %v", expectedErr, err)
			}
		}()
	}

	wg.Wait()
}
