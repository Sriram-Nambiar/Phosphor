package cache

import (
	"sync"
)

// call represents an active or completed in-flight request.
type call struct {
	wg  sync.WaitGroup
	val []byte
	err error
}

// Deduplicator suppresses duplicate execution of concurrent identical requests.
// When multiple goroutines invoke Do with the exact same key concurrently,
// only one executes the provided function; the others wait and share the result.
type Deduplicator struct {
	mu sync.Mutex
	m  map[string]*call
}

// NewDeduplicator creates a new thread-safe Deduplicator.
func NewDeduplicator() *Deduplicator {
	return &Deduplicator{
		m: make(map[string]*call),
	}
}

// Do executes and returns the results of the given function, making sure that
// only one execution is in-flight for a given key at a time.
// If a duplicate arrives, the duplicate caller waits for the original to complete
// and receives the identical results.
// shared indicates whether val was shared across multiple concurrent callers.
func (g *Deduplicator) Do(key string, fn func() ([]byte, error)) (val []byte, shared bool, err error) {
	g.mu.Lock()
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, true, c.err
	}

	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()
	}()

	c.val, c.err = fn()
	c.wg.Done()

	return c.val, false, c.err
}
