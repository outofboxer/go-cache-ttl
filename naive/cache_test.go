package simple

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

// ----------------------------
// Demo
// ----------------------------
func TestTTLCache_Simple(t *testing.T) {
	cache := NewTTLCache(2*time.Second, 1*time.Second)
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

func TestCacheEntrySize(t *testing.T) {
	var entry cacheEntry

	totalSize := unsafe.Sizeof(entry)
	valueSize := unsafe.Sizeof(entry.value)
	expiresAtSize := unsafe.Sizeof(entry.expiresAt)

	t.Logf("GOOS=%s GOARCH=%s", runtime.GOOS, runtime.GOARCH)
	t.Logf("cacheEntry size = %d bytes", totalSize)
	t.Logf("cacheEntry.value size = %d bytes", valueSize)
	t.Logf("cacheEntry.expiresAt size = %d bytes", expiresAtSize)

	typ := reflect.TypeOf(entry)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		t.Logf(
			"field=%s offset=%d size=%d align=%d type=%s",
			f.Name,
			f.Offset,
			f.Type.Size(),
			f.Type.Align(),
			f.Type.String(),
		)
	}

	// On 64-bit platforms:
	// - any/interface{} is typically 16 bytes
	// - time.Time is typically 24 bytes
	// Total: 40 bytes
	if runtime.GOARCH == "amd64" {
		const want = uintptr(40)
		if totalSize != want {
			t.Fatalf("unexpected cacheEntry size: got %d, want %d", totalSize, want)
		}
	}
}

func TestKnownFieldSizes(t *testing.T) {
	var v any
	var ts time.Time

	t.Logf("any size = %d bytes", unsafe.Sizeof(v))
	t.Logf("time.Time size = %d bytes", unsafe.Sizeof(ts))
}
