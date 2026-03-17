package minheap

import (
	"container/heap"
	"sync"
	"time"
)

// -------------------------------------------------------
// Min-Heap of expiry entries (ordered by expiresAt ASC)
// -------------------------------------------------------

type heapEntry struct {
	key       string
	expiresAt time.Time
	index     int // maintained by heap.Interface for O(log n) updates
}

type expiryHeap []*heapEntry

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }
func (h expiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *expiryHeap) Push(x any) {
	entry := x.(*heapEntry)
	entry.index = len(*h)
	*h = append(*h, entry)
}

func (h *expiryHeap) Pop() any {
	old := *h
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil   // avoid memory leak
	entry.index = -1 // mark as removed
	*h = old[:n-1]
	return entry
}

// -------------------------------------------------------
// Cache entry stored in the map
// -------------------------------------------------------

type cacheEntry struct {
	value     any
	expiresAt time.Time
	heapNode  *heapEntry // pointer back to heap node for O(log n) re-heap on update
}

// -------------------------------------------------------
// TTL Cache
// -------------------------------------------------------

type TTLCache struct {
	mu       sync.RWMutex
	items    map[string]*cacheEntry
	k        int
	h        expiryHeap
	ttl      time.Duration
	stopChan chan struct{}
}

func NewTTLCache(k int, ttl, cleanupInterval time.Duration) *TTLCache {
	c := &TTLCache{
		items:    make(map[string]*cacheEntry),
		h:        expiryHeap{},
		k:        k,
		ttl:      ttl,
		stopChan: make(chan struct{}),
	}
	heap.Init(&c.h)
	go c.startCleanup(cleanupInterval)
	return c
}

// Set inserts or updates a key. On update the heap node is fixed in O(log n).
func (c *TTLCache) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	newExpiry := time.Now().Add(c.ttl)

	if existing, ok := c.items[key]; ok {
		// Update value and refresh expiry in-place — O(log n) heap fix
		existing.value = value
		existing.expiresAt = newExpiry
		existing.heapNode.expiresAt = newExpiry
		heap.Fix(&c.h, existing.heapNode.index)
		return
	}

	// New entry: push onto heap and record cross-pointer
	node := &heapEntry{key: key, expiresAt: newExpiry}
	heap.Push(&c.h, node)
	c.items[key] = &cacheEntry{
		value:     value,
		expiresAt: newExpiry,
		heapNode:  node,
	}
}

// Get returns the value if present and not expired.
func (c *TTLCache) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.items[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.value, true
}

// Delete removes an entry immediately.
func (c *TTLCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeKey(key)
}

// removeKey removes key from both map and heap. Must be called with mu held.
func (c *TTLCache) removeKey(key string) {
	entry, ok := c.items[key]
	if !ok {
		return
	}
	heap.Remove(&c.h, entry.heapNode.index)
	delete(c.items, key)
}

// deleteExpired pops at most k expired entries from the heap.
// Holding the lock only long enough to drain the k oldest entries
// avoids a full map scan and caps the critical section.
func (c *TTLCache) deleteExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	removed := 0

	for c.h.Len() > 0 && removed < c.k {
		top := c.h[0] // peek — O(1)
		if now.Before(top.expiresAt) {
			break // everything else expires later — heap guarantee
		}
		heap.Pop(&c.h)
		delete(c.items, top.key)
		removed++
	}
}

// startCleanup ticks and evicts at most k expired entries per cycle.
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

// Stop shuts down the background goroutine gracefully.
func (c *TTLCache) Stop() {
	close(c.stopChan)
}

// Len returns the number of items currently in the cache (including not-yet-cleaned expired ones).
func (c *TTLCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

//**How the heap changes the design:**

//Set(key)  →  heap.Push  or  heap.Fix     O(log n)
//Get(key)  →  map lookup + expiry check   O(1)
//deleteExpired(k) →  peek + pop k times   O(k log n)  ← no full scan
