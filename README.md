Copyright (c) 2026 Oleg Klimenko

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

The copyright allows anyone to freely use, modify, and distribute the software, but they must keep the original copyright notice (my name) in the source and any substantial portions. 

# go-cache-ttl
This repo is for my articles about cache optimization using Golang


Naive (Simple TTL) implementation calculation

Let's calculate using an AWS compute server m6i.2xlarge with 8 vCPU and 32 GiB RAM.

Memory usage.

Memory per entry:
```
type cacheEntry struct {
value     any       → 16 bytes (interface header) + 1024 bytes (payload)
expiresAt time.Time → 24 bytes
}
```

```
map[string]*cacheEntry:
├── map bucket overhead  →   8 bytes
├── string key header    →  16 bytes
├── string key data      →  16 bytes avg
└── *cacheEntry pointer  →   8 bytes
```

cacheEntry struct        →  40 bytes (without payload)
value payload            → 1024 bytes

Total per entry          → 1,136 bytes  ≈ 1.1 KB
No heapEntry overhead here — the unoptimized version has no heap.

Per entry:
map bucket overhead    →    8 bytes
string key header      →   16 bytes
string key data        →   16 bytes
*cacheEntry pointer    →    8 bytes
cacheEntry struct      →   40 bytes
value payload (1KB)    → 1024 bytes
──────────────────────────────────
total                  → 1,112 bytes  ≈ 1.1 KB

32 GB × 0.75 usable = 24 GB
24 GB / 1,112 bytes  = ~21.5M entries
We'll use ~21M entries as the working number.





AWS instance baseline: 8 vCPU
A typical 8 vCPU AWS instance (c6i.2xlarge / m6i.2xlarge) gives you:
8 vCPUs        → 8 hardware threads (2 physical cores × 4 HT, or 4 cores × 2 HT)
L1 cache       → 48 KB per core (data)
L2 cache       → 1.25 MB per core
L3 cache       → 30–40 MB shared across all cores
Memory BW      → ~50 GB/s (DDR4-3200, dual channel)
Core clock     → ~3.5 GHz base, ~3.9 GHz boost


Operation latencies — broken down cycle by cycle:
Get:
sync.Mutex.Lock()   (uncontested CAS)  →   5–8 ns
map bucket hash + lookup               →  30–50 ns
*cacheEntry pointer deref              →  10–40 ns  (L1 hit → L3 miss depending on key age)
time.Now()                             →   8–10 ns
expiresAt compare                      →   2 ns
sync.Mutex.Unlock()                    →   3–5 ns
────────────────────────────────────────────────────
total best  (L1 hot)                   →  ~58 ns
total avg   (L2/L3)                    →  ~80 ns
total worst (L3 miss, cold entry)      → ~115 ns




Set (new key):  
sync.Mutex.Lock()                      →   5–8 ns
time.Now() + add TTL                   →  10 ns
map hash + bucket find                 →  30–50 ns
go runtime malloc ~1KB                 →  50–100 ns
map insert + possible rehash           →  30–80 ns
sync.Mutex.Unlock()                    →   3–5 ns
────────────────────────────────────────────────────
total best                             → ~128 ns
total avg                              → ~215 ns
total worst (rehash triggered)         → ~300 ns


Set (existing key update):
sync.Mutex.Lock()                      →   5–8 ns
map lookup                             →  30–50 ns
overwrite value + expiresAt            →  10–15 ns
sync.Mutex.Unlock()                    →   3–5 ns
────────────────────────────────────────────────────
total best                             →  ~48 ns
total avg                              →  ~78 ns
Note: unoptimized update is faster than heap update because there's no heap.Fix — the cost shows up later at cleanup time instead.


Cleanup cycle — the full cost:
21M entries × 100 ns avg iteration cost:

map range overhead per entry   →  15–20 ns  (bucket pointer walk)
*cacheEntry deref              →  10–40 ns  (cache miss pattern)
time.Now().After() compare     →   2–5 ns
delete(map, key) when expired  →  20–40 ns  (amortized)
──────────────────────────────────────────
avg per entry                  →  ~80–105 ns

21M × 80 ns   =  1.68 s  (optimistic)
21M × 100 ns  =  2.10 s  (realistic)
21M × 150 ns  =  3.15 s  (pessimistic, cold L3 throughout)
Realistic number: ~2.1 seconds lock held per cleanup cycle.

Stall impact on the single goroutine:
With 1 goroutine there's no queue of waiting callers — the goroutine itself is blocked inside cleanup. All ops simply stop for the duration:
Cleanup runs every 60s (minimum safe interval for 2.1s scan):

2.1s frozen / 60s cycle  =  3.5% of wall time fully stopped

In a 1-hour window:
60 cleanup cycles × 2.1s  =  126 seconds frozen
3,600s total − 126s        =  3,474s serving
effective uptime           =  96.5%
If you tighten the interval trying to keep memory clean:

