package cache

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// PersistentStore represents an optional secondary persistent storage layer (e.g. SQLite).
type PersistentStore interface {
	GetCachedResponse(ctx context.Context, key string) ([]byte, bool, error)
	SetCachedResponse(ctx context.Context, key string, value []byte, ttl time.Duration) error
	DeleteCachedResponse(ctx context.Context, key string) error
}

// CacheItem represents a stored item in the LRU cache with an expiration time.
type CacheItem struct {
	Key       string
	Value     []byte
	ExpiresAt time.Time
	element   *list.Element
}

// IsExpired returns true if the item has passed its TTL expiration.
func (i *CacheItem) IsExpired(now time.Time) bool {
	if i.ExpiresAt.IsZero() {
		return false
	}
	return now.After(i.ExpiresAt)
}

// LRUCache is a thread-safe in-memory LRU cache with per-item TTL expiration and optional L2 persistence.
type LRUCache struct {
	mu              sync.RWMutex
	capacity        int
	defaultTTL      time.Duration
	items           map[string]*CacheItem
	evictList       *list.List
	hits            int64
	misses          int64
	persistentStore PersistentStore
}

// NewLRUCache creates a new thread-safe LRU cache with maximum capacity and default TTL.
func NewLRUCache(capacity int, defaultTTL time.Duration) *LRUCache {
	if capacity <= 0 {
		capacity = 1000
	}
	return &LRUCache{
		capacity:   capacity,
		defaultTTL: defaultTTL,
		items:      make(map[string]*CacheItem),
		evictList:  list.New(),
	}
}

// SetPersistentStore configures an optional secondary persistent storage backend.
func (c *LRUCache) SetPersistentStore(store PersistentStore) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.persistentStore = store
}

// Get retrieves an item by key. If the item is expired, it is purged.
// If missing from in-memory cache, the optional persistentStore is queried as L2.
func (c *LRUCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, exists := c.items[key]
	if !exists {
		if c.persistentStore != nil {
			if val, found, err := c.persistentStore.GetCachedResponse(context.Background(), key); err == nil && found {
				c.setMemoryItem(key, val, c.defaultTTL)
				c.hits++
				copied := make([]byte, len(val))
				copy(copied, val)
				return copied, true
			}
		}
		c.misses++
		return nil, false
	}

	now := time.Now()
	if item.IsExpired(now) {
		c.removeElement(item.element)
		c.misses++
		return nil, false
	}

	c.evictList.MoveToFront(item.element)
	c.hits++
	// Return a defensive copy to prevent external mutation
	copied := make([]byte, len(item.Value))
	copy(copied, item.Value)
	return copied, true
}

// Set stores an item with default TTL. If ttl <= 0, defaultTTL is used.
// It writes to in-memory L1 cache and persists to L2 if persistentStore is configured.
func (c *LRUCache) Set(key string, value []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ttl <= 0 {
		ttl = c.defaultTTL
	}

	// Defensive copy
	copied := make([]byte, len(value))
	copy(copied, value)

	c.setMemoryItem(key, copied, ttl)

	if c.persistentStore != nil {
		go func(k string, v []byte, t time.Duration) {
			_ = c.persistentStore.SetCachedResponse(context.Background(), k, v, t)
		}(key, copied, ttl)
	}
}

func (c *LRUCache) setMemoryItem(key string, value []byte, ttl time.Duration) {
	now := time.Now()
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}

	if item, exists := c.items[key]; exists {
		item.Value = value
		item.ExpiresAt = expiresAt
		c.evictList.MoveToFront(item.element)
		return
	}

	// Evict oldest if at capacity
	for c.evictList.Len() >= c.capacity {
		c.removeOldest()
	}

	item := &CacheItem{
		Key:       key,
		Value:     value,
		ExpiresAt: expiresAt,
	}
	element := c.evictList.PushFront(item)
	item.element = element
	c.items[key] = item
}

// Delete removes an item by key from both L1 and L2.
func (c *LRUCache) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.persistentStore != nil {
		go func(k string) {
			_ = c.persistentStore.DeleteCachedResponse(context.Background(), k)
		}(key)
	}

	if item, exists := c.items[key]; exists {
		c.removeElement(item.element)
		return true
	}
	return false
}

// Len returns the current number of items in cache.
func (c *LRUCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.evictList.Len()
}

// Capacity returns the maximum configured capacity of the cache.
func (c *LRUCache) Capacity() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capacity
}

// Stats returns hit, miss, and size metrics.
func (c *LRUCache) Stats() (hits, misses int64, size int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses, c.evictList.Len()
}

// Clear flushes all in-memory cache entries and resets hit/miss counters.
func (c *LRUCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*CacheItem)
	c.evictList.Init()
	c.hits = 0
	c.misses = 0
}

// PurgeExpired evicts all expired items from the cache.
func (c *LRUCache) PurgeExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	purged := 0
	for elem := c.evictList.Back(); elem != nil; {
		prev := elem.Prev()
		item := elem.Value.(*CacheItem)
		if item.IsExpired(now) {
			c.removeElement(elem)
			purged++
		}
		elem = prev
	}
	return purged
}

func (c *LRUCache) removeElement(elem *list.Element) {
	c.evictList.Remove(elem)
	item := elem.Value.(*CacheItem)
	delete(c.items, item.Key)
}

func (c *LRUCache) removeOldest() {
	elem := c.evictList.Back()
	if elem != nil {
		c.removeElement(elem)
	}
}
