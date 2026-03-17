# Naive TTL Cache in Go
### Simple design, cheap steady-state operations, expensive cleanup

This document analyzes the naive TTL cache implementation from `naive/cache.go`.

---

## Implementation

The cache stores each item directly in a Go map and periodically scans the entire map to remove expired entries.

```go
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
```

Core behaviors:

- `Set()` inserts or overwrites a map entry
- `Get()` checks expiration at read time
- expired items remain in memory until background cleanup runs
- `deleteExpired()` scans the full map under the write lock

That last point defines the entire performance profile of this design.

---

## Complexity

| Operation | Complexity |
|---|---|
| `Get` | `O(1)` average |
| `Set` | `O(1)` average |
| `deleteExpired` | `O(n)` |
| `Stop` | `O(1)` |

The cache is fast on ordinary reads and writes, but expiration cleanup cost grows linearly with the total number of cached items.

---

## Why This Version Is Attractive

This implementation has real advantages:

- minimal code
- easy to reason about
- no secondary index to maintain
- cheap updates to existing keys
- good concurrent-read behavior in steady state because it uses `sync.RWMutex`

An update is especially cheap here: overwrite the map value and refresh `expiresAt`. There is no heap fix-up, no delete-reinsert, and no extra structure to synchronize.

For small and medium caches, this simplicity is often worth a lot.

---

## Memory Shape

The in-struct metadata is small:

```go
type cacheEntry struct {
    value     any       // 16 bytes on 64-bit Go
    expiresAt time.Time // 24 bytes
}
```

That makes the struct itself roughly:

- `40 bytes` on a 64-bit Go runtime

But total per-entry memory is larger because the cache also pays for:

- map bucket overhead
- string key header
- key bytes
- allocator size classes
- map growth slack
- the concrete value stored behind `any`

If you assume:

- average key size: `16 bytes`
- average value payload: `1024 bytes`

then a realistic rough planning number is:

- about `1.1 KB` to `1.2 KB` per entry total

That estimate is good enough for capacity planning, even though the exact number varies by workload and Go runtime details.

---

## Capacity on a 32 GiB Node

Using a conservative 75% memory budget for the cache:

```text
32 GiB × 0.75 = 24 GiB usable
24 GiB / 1.1 KB ≈ 21M entries
```

So a reasonable planning number is:

- `~20M to 21M` entries on a `32 GiB` machine with `1 KB` values

This is only a memory-capacity estimate. It does not mean the cache will behave well operationally at that size.

---

## The Real Cost: Full-Map Cleanup

The cleanup path is the weakness:

```go
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
```

Properties of this cleanup strategy:

- it acquires the write lock
- it blocks all readers and writers
- it scans every entry
- it does the same scan even if very few items are expired

This means cleanup cost depends on **cache size**, not on **number of expired items**.

That is the central scalability limitation of the naive design.

---

## Concurrency Behavior

This implementation uses `sync.RWMutex`, which gives it a useful steady-state property:

- many `Get()` calls can run concurrently
- `Set()` requires exclusive access
- cleanup blocks everything because it takes the write lock

So in read-heavy workloads, the naive version can actually outperform more sophisticated designs during normal operation.

The problem appears when cleanup starts. At that moment:

- all readers stop
- all writers stop
- the cache remains unavailable until the scan finishes

In other words, average-case concurrency is decent, but tail latency is dominated by cleanup pauses.

---

## Stale Data and Memory Retention

Expired entries are not removed immediately on `Get()`. They are only observed as expired and treated as misses.

That means dead entries can remain in memory until the next cleanup pass.

Assume:

- `TTL = 60s`
- `~21M` entries
- roughly uniform expiration distribution

Then the average expiry rate is:

```text
21M / 60s ≈ 350K expired entries per second
```

If cleanup runs every `45s`, then just before the next sweep the cache may contain roughly:

```text
350K × 45 ≈ 15.75M expired entries
```

At about `1.1 KB` each, that is roughly:

- `~17 GB` of stale memory

That is a peak-before-sweep estimate, not a constant-state average, but it shows the trade-off clearly: the cache retains expired data to keep `Set()` simple.

---

## Operational Impact at Large Scale

For small caches, a full scan is cheap enough.

For very large caches, the picture changes:

- cleanup time grows with total entry count
- the write lock is held for the whole scan
- latency spikes become proportional to cleanup duration
- the more often cleanup runs, the lower the stale-memory backlog, but the more often the cache pauses

This creates a tension between:

- memory freshness
- latency stability
- cleanup frequency

You can reclaim memory faster by sweeping more often, but every sweep still blocks the entire cache.

---

## Performance Framing

It is tempting to describe this cache with nanosecond-level estimates for `Get()` and `Set()`, but the more honest framing is this:

### Reads
- usually fast
- average-case `O(1)`
- concurrent readers scale reasonably well between cleanup cycles

### Writes
- usually fast
- average-case `O(1)`
- updates are especially cheap

### Expiration
- the expensive part
- `O(n)` under exclusive lock
- becomes the dominant cost at large cache sizes

So the naive implementation often looks excellent in microbenchmarks and disappointing in tail-latency charts.

---

## When This Design Works Well

This cache is a good fit when:

- the cache is not extremely large
- occasional cleanup pauses are acceptable
- the workload is read-heavy with modest churn
- implementation simplicity matters more than perfect expiration behavior

It is a poor fit when:

- the cache holds tens of millions of items
- tight latency SLOs matter
- expired data must be reclaimed quickly
- cleanup pauses are operationally unacceptable

---

## Final Takeaway

The naive TTL cache is a strong baseline because it is:

- simple
- compact
- easy to maintain
- fast for ordinary reads and writes

Its weakness is not lookup speed. Its weakness is expiration strategy.

Because cleanup scans the entire map under the write lock, the cost of expiration grows with total cache size rather than with the number of expired items. That makes the design easy to implement, but hard to scale cleanly once the cache becomes very large.
