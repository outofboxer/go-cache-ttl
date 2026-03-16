package simple

import (
	"sync"
	"time"
)

type cacheEntry struct {
	value     any
	expiresAt time.Time
}

type TTLCache struct {
	mu       sync.RWMutex
	items    map[string]cacheEntry
	ttl      time.Duration
	stopChan chan struct{}
}

func NewTTLCache(ttl, cleanupInterval time.Duration) *TTLCache {
	c := &TTLCache{
		items:    make(map[string]cacheEntry),
		ttl:      ttl,
		stopChan: make(chan struct{}),
	}
	go c.startCleanup(cleanupInterval) // 👈 background goroutine required
	return c
}

// Set adds or updates an entry with a fresh TTL
func (c *TTLCache) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// Get retrieves a value, returning false if missing or expired
func (c *TTLCache) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.items[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.value, true
}

// startCleanup runs a ticker that purges expired entries
func (c *TTLCache) startCleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.deleteExpired()
		case <-c.stopChan:
			return
		}
	}
}

func (c *TTLCache) deleteExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, v := range c.items {
		if now.After(v.expiresAt) {
			delete(c.items, k)
		}
	}
}

// Stop shuts down the background cleanup goroutine
func (c *TTLCache) Stop() {
	close(c.stopChan)
}
