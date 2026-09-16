package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLRUCache_BasicOperations(t *testing.T) {
	c := NewLRUCache(3, 10*time.Minute)

	c.Set("k1", []byte("v1"), 0)
	c.Set("k2", []byte("v2"), 0)
	c.Set("k3", []byte("v3"), 0)

	val, found := c.Get("k1")
	if !found || string(val) != "v1" {
		t.Fatalf("expected v1, got %s, found=%v", string(val), found)
	}

	// Adding k4 should evict k2 (since k1 was accessed, k2 is oldest)
	c.Set("k4", []byte("v4"), 0)

	if _, found := c.Get("k2"); found {
		t.Fatalf("expected k2 to be evicted")
	}

	if val, found := c.Get("k4"); !found || string(val) != "v4" {
		t.Fatalf("expected v4, got %s", string(val))
	}

	if c.Len() != 3 {
		t.Fatalf("expected len 3, got %d", c.Len())
	}
}

func TestLRUCache_TTLExpiration(t *testing.T) {
	c := NewLRUCache(10, 50*time.Millisecond)

	c.Set("temp", []byte("data"), 30*time.Millisecond)

	val, found := c.Get("temp")
	if !found || string(val) != "data" {
		t.Fatalf("expected temp to be found immediately")
	}

	time.Sleep(50 * time.Millisecond)

	if _, found := c.Get("temp"); found {
		t.Fatalf("expected temp to be expired")
	}

	hits, misses, size := c.Stats()
	if hits != 1 || misses != 1 || size != 0 {
		t.Fatalf("unexpected stats: hits=%d, misses=%d, size=%d", hits, misses, size)
	}
}

func TestLRUCache_PurgeExpired(t *testing.T) {
	c := NewLRUCache(10, 10*time.Minute)

	c.Set("k1", []byte("v1"), 10*time.Millisecond)
	c.Set("k2", []byte("v2"), 1*time.Hour)
	c.Set("k3", []byte("v3"), 10*time.Millisecond)

	time.Sleep(25 * time.Millisecond)

	purged := c.PurgeExpired()
	if purged != 2 {
		t.Fatalf("expected 2 items purged, got %d", purged)
	}

	if c.Len() != 1 {
		t.Fatalf("expected 1 item left, got %d", c.Len())
	}
}

func TestLRUCache_ConcurrentAccess(t *testing.T) {
	c := NewLRUCache(100, 1*time.Minute)
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				key := fmt.Sprintf("k-%d-%d", workerID, j%10)
				c.Set(key, []byte("data"), 0)
				c.Get(key)
			}
		}(i)
	}

	wg.Wait()
	if c.Len() > 100 {
		t.Fatalf("cache exceeded capacity: %d", c.Len())
	}
}
