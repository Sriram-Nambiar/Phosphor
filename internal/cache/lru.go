package cache

import (
	"container/list"
	"sync"
	"time"
)

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

// LRUCache is a thread-safe in-memory LRU cache with per-item TTL expiration.
type LRUCache struct {
	mu         sync.RWMutex
	capacity   int
	defaultTTL time.Duration
	items      map[string]*CacheItem
	evictList  *list.List
	hits       int64
	misses     int64
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

// Get retrieves an item by key. If the item is expired, it is purged and returns nil, false.
func (c *LRUCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, exists := c.items[key]
	if !exists {
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
func (c *LRUCache) Set(key string, value []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ttl <= 0 {
		ttl = c.defaultTTL
	}

	now := time.Now()
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}

	// Defensive copy
	copied := make([]byte, len(value))
	copy(copied, value)

	if item, exists := c.items[key]; exists {
		item.Value = copied
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
		Value:     copied,
		ExpiresAt: expiresAt,
	}
	element := c.evictList.PushFront(item)
	item.element = element
	c.items[key] = item
}

// Delete removes an item by key.
func (c *LRUCache) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

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

// Stats returns hit, miss, and size metrics.
func (c *LRUCache) Stats() (hits, misses int64, size int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses, c.evictList.Len()
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
