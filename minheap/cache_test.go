package minheap_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/outofboxer/go-cache-ttl/minheap"
)

// -------------------------------------------------------
// Demo
// -------------------------------------------------------

func TestTTLCache_MinHeap(t *testing.T) {
	cache := minheap.NewTTLCache(100, 2*time.Second, 500*time.Millisecond)
	defer cache.Stop()

	// Populate
	for i := range 5 {
		key := fmt.Sprintf("key:%d", i)
		cache.Set(key, i*10)
	}
	fmt.Println("After Set — size:", cache.Len()) // 5

	// Refresh key:0 so its TTL resets
	cache.Set("key:0", 999)

	// Immediate read
	if v, ok := cache.Get("key:1"); ok {
		fmt.Println("key:1 =", v) // 10
	}

	// Wait for expiry + cleanup cycle
	time.Sleep(3 * time.Second)
	fmt.Println("After TTL — size:", cache.Len()) // 0

	if _, ok := cache.Get("key:1"); !ok {
		fmt.Println("key:1 expired") // expired
	}
}
