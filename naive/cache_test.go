package simple_test

import (
	"fmt"
	"testing"
	"time"

	simple "github.com/outofboxer/go-cache-ttl/naive"
)

// ----------------------------
// Demo
// ----------------------------
func TestTTLCache_Simple(t *testing.T) {
	cache := simple.NewTTLCache(2*time.Second, 1*time.Second)
	defer cache.Stop()

	cache.Set("session:abc", "user-42")

	if v, ok := cache.Get("session:abc"); ok {
		fmt.Println("Found:", v) // Found: user-42
	}

	time.Sleep(3 * time.Second)

	if _, ok := cache.Get("session:abc"); !ok {
		fmt.Println("Expired and cleaned up") // Expired and cleaned up
	}
}
