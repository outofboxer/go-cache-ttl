# Naive TTL Cache — Performance Analysis
### 1 Goroutine / 8 vCPU AWS (c6i.2xlarge) / 32 GB RAM / 1 KB Values

---

## AWS Instance Baseline

| Property | Value |
|---|---|
| vCPUs | 8 (c6i.2xlarge / m6i.2xlarge) |
| L1 cache | 48 KB per core |
| L2 cache | 1.25 MB per core |
| L3 cache | 30–40 MB shared |
| Memory bandwidth | ~50 GB/s (DDR4-3200) |
| Core clock | ~3.5 GHz base / ~3.9 GHz boost |

---

## Memory Layout Per Entry

```
type cacheEntry struct {
    value     any       → 16 bytes (interface header) + 1024 bytes (payload)
    expiresAt time.Time → 24 bytes
}

map[string]*cacheEntry overhead:
├── map bucket overhead  →    8 bytes
├── string key header    →   16 bytes
├── string key data      →   16 bytes avg
└── *cacheEntry pointer  →    8 bytes

cacheEntry struct        →   40 bytes  (without payload)
value payload            → 1024 bytes

Total per entry          → 1,112 bytes  ≈ 1.1 KB
```

No `heapEntry` overhead — the unoptimized version has no heap.

---

## Capacity at 32 GB

```
32 GB × 0.75 usable  =  24 GB
24 GB / 1,112 bytes  =  ~21.5M entries
```

Working number: **~21M entries**.

---

## Operation Latencies — Single Goroutine

With 1 goroutine there is no mutex contention. Lock cost is a single uncontested atomic CAS (~5–8 ns).

### Get

```
sync.Mutex.Lock()   (uncontested CAS)  →   5–8 ns
map bucket hash + lookup               →  30–50 ns
*cacheEntry pointer deref              →  10–40 ns  (L1 hit → L3 miss depending on recency)
time.Now()                             →   8–10 ns
expiresAt compare                      →   2 ns
sync.Mutex.Unlock()                    →   3–5 ns
────────────────────────────────────────────────────
total best  (L1 hot)                   →  ~58 ns
total avg   (L2/L3)                    →  ~80 ns
total worst (cold L3 miss)             → ~115 ns
```

### Set — New Key

```
sync.Mutex.Lock()                      →   5–8 ns
time.Now() + add TTL                   →  10 ns
map hash + bucket find                 →  30–50 ns
go runtime malloc ~1 KB                →  50–100 ns
map insert + possible rehash           →  30–80 ns
sync.Mutex.Unlock()                    →   3–5 ns
────────────────────────────────────────────────────
total best                             → ~128 ns
total avg                              → ~215 ns
total worst (rehash triggered)         → ~300 ns
```

### Set — Existing Key Update

```
sync.Mutex.Lock()                      →   5–8 ns
map lookup                             →  30–50 ns
overwrite value + expiresAt            →  10–15 ns
sync.Mutex.Unlock()                    →   3–5 ns
────────────────────────────────────────────────────
total best                             →  ~48 ns
total avg                              →  ~78 ns
```

> Update is deceptively fast here — there is no heap to fix. The cost is deferred entirely to cleanup time, where the full map scan must process every entry regardless of whether it has expired.

---

## Throughput — Single Goroutine

| Operation | Best | Avg | Worst |
|---|---|---|---|
| Get | ~17M/s | ~12.5M/s | ~8.7M/s |
| Set new | ~7.8M/s | ~4.6M/s | ~3.3M/s |
| Set update | ~20M/s | ~12.8M/s | — |
| 80/20 read/write (new keys) | ~14M/s | ~10M/s | ~7M/s |
| 80/20 read/write (updates) | ~17M/s | ~12.5M/s | — |

---

## Cleanup Cycle — The Full Cost

### Why Iteration Is Slow at Scale

```
Individual Get:
  accesses 1 specific key → likely warm in L1/L2 if recently used
  pointer deref            → ~10–15 ns (L1 hit)

Cleanup range iteration:
  walks ALL map buckets sequentially
  21M entries × pointer deref to random heap addresses
  working set = 21M × 40 bytes (struct header) = ~840 MB
  far exceeds L3 cache (30–40 MB)
  almost every deref is a cold L3 miss → 40–80 ns each
  prefetcher cannot help: pointers scatter across heap
```

### Scan Time at 21M Entries

```
Per-entry cost breakdown during range:
  map bucket pointer walk    →  15–20 ns
  *cacheEntry deref (L3 miss)→  40–80 ns
  time.Now().After() compare →   2–5 ns
  delete(map, key) amortized →  20–40 ns  (only for expired entries)
  ────────────────────────────────────────
  avg per entry              →  ~80–145 ns

21M × 80 ns   =  1.68 s  (optimistic)
21M × 100 ns  =  2.10 s  (realistic)
21M × 145 ns  =  3.05 s  (pessimistic, fully cold L3)
```

Realistic lock hold per cleanup cycle: **~2.1 seconds**.

---

## Effective Uptime at Various Cleanup Intervals

With 1 goroutine, no operations are served during the scan — the goroutine is fully blocked inside `deleteExpired`.

```
frozen fraction  =  scan time / cleanup interval

2.1s / 60s   =  3.5%  frozen
2.1s / 45s   =  4.7%  frozen
2.1s / 30s   =  7.0%  frozen
2.1s / 10s   = 21.0%  frozen  ← severe
```

| Cleanup interval | Scans/hour | Frozen/hour | Effective uptime |
|---|---|---|---|
| 10s | 360 | 756s (12.6 min) | 79.0% |
| 30s | 120 | 252s (4.2 min) | 93.0% |
| **45s** | **80** | **168s (2.8 min)** | **95.3%** |
| 60s | 60 | 126s (2.1 min) | 96.5% |
| 120s | 30 | 63s | 98.3% |
| 300s | 12 | 25s | 99.3% |

Every reduction in interval to reclaim memory faster **directly cuts into availability**.

---

## Stale Memory Accumulation

```
TTL = 60s, 21M entries uniform distribution
Expiry rate  =  21M / 60s  =  350K entries/sec expire

At 45s cleanup interval:
  entries expired before sweep  =  350K × 45  =  15.75M stale entries
  stale memory                  =  15.75M × 1,112 bytes  =  ~17.5 GB

At 60s cleanup interval:
  stale entries                 =  350K × 60  =  21M  (entire cache expired)
  stale memory                  =  ~23.4 GB   → nearly full 32 GB used by dead entries

At 120s cleanup interval:
  stale entries                 =  350K × 120  =  42M  → exceeds 21M capacity
  cache grows beyond 32 GB      → OOM crash
```

### Hard Constraint on Cleanup Interval

```
cleanup interval  MUST be  <  capacity / expiry_rate
                           =  21M / 350K per sec
                           =  60 seconds maximum

At exactly 60s  → 100% of cache is stale just before sweep
Safe target     → 30–45s keeps stale entries below 75%
```

---

## Latency Percentiles During Cleanup

With a single goroutine the next operation after cleanup starts waits for the **entire scan to finish**:

| Request timing | Wait time |
|---|---|
| Arrives at start of scan | ~2.1 s |
| Arrives halfway through | ~1.05 s |
| Arrives near end of scan | ~0.1 s |
| **p50** | **~1.05 s** |
| **p95** | **~1.99 s** |
| **p99** | **~2.08 s** |
| **p99.9** | **~2.10 s** |

---

## CPU Utilization

```
1 goroutine doing all work  →  1 core at ~100%
7 remaining cores           →  completely idle
Effective utilization       →  12.5% of 8 vCPUs
```

This is identical during both normal operation and cleanup — the machine is paid for but 7 of 8 cores sit unused.

---

## Full Profile Summary

| Metric | Value |
|---|---|
| Entries in 32 GB | ~21M |
| Bytes per entry | ~1,112 bytes |
| Get throughput (avg) | ~12.5M/s |
| Set new throughput (avg) | ~4.6M/s |
| Set update throughput (avg) | ~12.8M/s |
| 80/20 mix throughput | ~10–12.5M/s |
| Cleanup scan time | **~2.1 seconds** |
| Minimum safe interval | 30–45s |
| Stale memory at 45s interval | **~17.5 GB** (54% of RAM) |
| p50 latency during cleanup | **~1.05 s** |
| p99 latency during cleanup | **~2.08 s** |
| Max single stall | **~2.1 seconds** |
| Effective uptime at 45s interval | **~95.3%** |
| CPU utilization | 12.5% (1 of 8 cores) |
| Time frozen per hour | **~168 seconds** |

---

## Key Takeaway

With a single goroutine and 21M entries at 1 KB values, the unoptimized cache spends **~168 seconds of every hour completely frozen** — not degraded, not slow, but returning zero responses — while holding **~17.5 GB of RAM in expired entries** that cannot be reclaimed until the next sweep.

The root cause is that `deleteExpired` performs a full O(n) scan regardless of how many entries have actually expired. With 21M entries scattered across ~840 MB of heap memory, the CPU cache miss rate approaches 100% during iteration, making every cleanup cycle a guaranteed multi-second stall.