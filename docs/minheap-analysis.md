# Min-Heap TTL Cache in Go
### More metadata, more write-path work, far better expiration control

This document analyzes the heap-based TTL cache implementation from `minheap/cache.go`.

---

## Implementation

The optimized version keeps two structures in sync:

1. a map for ordinary key lookups
2. a min-heap ordered by expiration time

```go
type heapEntry struct {
key       string
expiresAt time.Time
index     int
}

type cacheEntry struct {
value     any
expiresAt time.Time
heapNode  *heapEntry
}

type TTLCache struct {
mu       sync.Mutex
items    map[string]*cacheEntry
k        int
h        expiryHeap
ttl      time.Duration
stopChan chan struct{}
}
```

Core behaviors:

- `Set()` inserts into the map and the heap
- updating an existing key refreshes the heap entry with `heap.Fix`
- `Get()` checks expiration from the map entry
- cleanup repeatedly pops the earliest-expiring heap entries
- cleanup stops as soon as the heap root is not expired
- cleanup removes at most `k` expired items per tick, where `k` is configurable

This is a fundamentally different expiration strategy from the naive version.

---

## Complexity

| Operation | Complexity |
|---|---|
| `Get` | `O(1)` average |
| `Set` new | `O(log n)` |
| `Set` update | `O(log n)` |
| `Delete` | `O(log n)` |
| `deleteExpired(k)` | `O(k log n)` |
| `Len` | `O(1)` |

This is the core algorithmic win:

- naive cleanup: `O(n)` over the entire cache
- heap cleanup: `O(k log n)` over only the expired prefix

In other words, the heap changes expiration work from **full scan** to **ordered incremental drain**.

---

## What the Heap Optimizes

The heap does **not** make the ordinary write path cheaper.

In fact, it makes writes more expensive because every insert or TTL refresh must maintain heap order.

What it improves is expiration cleanup:

### Naive version
- scan all items
- remove the expired ones you find
- cost depends on total cache size

### Heap version
- inspect the oldest expiration first
- stop immediately if that item is still alive
- remove expired items in time order
- cost depends mostly on how many expired items are actually drained

That is why the heap version scales better for large TTL caches.

---

## Memory Cost

The heap-based cache pays more metadata per entry than the naive one.

Additional costs include:

- `cacheEntry` grows from about `40` to about `48` bytes
- each item gets a separate `heapEntry` object, about `48` bytes
- the heap slice stores one pointer per item
- the map stores `*cacheEntry` instead of inline `cacheEntry`

So compared with the naive version:

- metadata overhead is higher
- capacity is lower by some percentage
- the smaller the payload, the more visible this overhead becomes

With `1 KB` values, the extra metadata is usually acceptable.
With very small values, the metadata tax becomes much harder to justify.

A reasonable planning conclusion is:

- the heap design gives up some memory density to gain much better expiration behavior

---

## The Main Operational Requirement

Main operational requirement:

- cleanup throughput must be at least as high as expiration throughput

So the practical question is this:

- "what values of `k` and `cleanupInterval` are sufficient for this workload?"

Assume:

- `~21M` items
- `TTL = 60s`

Then the average expiration rate is:

```text
21M / 60s ≈ 350K expired items per second
```

To keep up, cleanup must satisfy:

```text
k / interval >= 350K evictions per second
```

Examples:

- `k=100`, `interval=100ms` => `1,000 evictions/sec`
- `k=1_000`, `interval=100ms` => `10,000 evictions/sec`
- `k=10_000`, `interval=100ms` => `100,000 evictions/sec`
- `k=50_000`, `interval=100ms` => `500,000 evictions/sec`

So for this example workload:

- the first three settings fall behind
- `k=50_000` with `100ms` cleanup interval is large enough to keep up

The real caveat is configuration here:

- too-small `k` causes stale-entry backlog growth
- too-large `k` increases per-tick lock hold time
- too-long cleanup interval increases stale-memory peaks
- too-short cleanup interval increases scheduler and timer overhead

---

## Concurrency Trade-Off

That has a major consequence:

- cleanup also blocks all other operations while it holds the lock

So compared with the naive version:

- cleanup behavior is much better

---

## Stale Data Behavior

The heap reduces stale data only if cleanup throughput is high enough.


The heap makes stale-data accumulation controllable, when (that's important) cleanup capacity exceeds expiry rate

---

## Write-Path Trade-Off

Compared with the naive version:

### New insert
- slower, because it updates the heap

### Existing-key update
- also slower, because it must call `heap.Fix`

### Delete
- supported explicitly and efficiently through `heap.Remove`

This is an important correction to a common intuition: the heap version is not “faster overall.” It is faster specifically at expiration management.

---

## Operational Picture

The optimized version is best understood as a rebalancing of costs.

It moves the design from this profile:

- cheap writes
- expensive global cleanup

to this one:

- more expensive writes
- incremental expiration cleanup

That is usually the right trade-off for large caches, because global scans are much harder to scale than `log n` maintenance on the write path.

---

## When This Design Works Well

This version is a good fit when:

- the cache is large
- expiration behavior matters
- long cleanup pauses are unacceptable
- the workload can tolerate extra write-path overhead
- cleanup capacity is provisioned correctly

It is a weaker fit when:

- values are tiny and metadata overhead matters a lot
- the workload is heavily read-concurrent
- the implementation is not sharded
- `k` and cleanup interval are tuned too conservatively for the churn rate

---

## Final Takeaway

The min-heap cache is a better expiration algorithm than the naive cache.

Its strengths are:

- no full-cache expiration scan
- cleanup tied to expired items instead of total cache size
- incremental and predictable eviction behavior

Its costs are:

- more metadata per entry
- slower `Set()`, especially updates
- eviction throughput still has to be tuned correctly for the workload

So the right conclusion is not that the heap version is simply “faster.” The right conclusion is:

> the heap version spends more work on each write so that expiration stops being a full-cache event

That is usually the better engineering trade-off for large TTL caches, especially once eviction capacity and concurrency strategy are tuned properly.
