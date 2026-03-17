Required background to understand this article.

To understand this article, one needs to understand a few Min-Heap data structure nuances.
If you already know how a Min-Heap works, feel free to skip this section.

------

1. Heap Property
   For each node `i`:
```
value(parent(i)) ≤ value(i)
```
As a conclusion, the smallest element is always at the root.

2. Complete Binary Tree
   A heap is stored as a **complete binary tree**, meaning:
- All levels are fully filled except possibly the last.
- The last level is filled **from left to right**.


The heaps are usually stored in a **compact array representation** rather than pointer-based trees.

For node index `i`:
```
parent = (i - 1) / 2
left   = 2*i + 1
right  = 2*i + 2
```

Conclusion: Peek - reading the smallest element is O(1).

Overall, the complexity of core operations in a Min-Heap:

|Operation|Description|Complexity|
|---|---|---|
|`Peek()`|Read smallest element|**O(1)**|
|`Push()`|Insert element and restore heap property|**O(log n)**|
|`Pop()`|Remove smallest element|**O(log n)**|
|`Fix()`|Update element priority and rebalance|**O(log n)**|
![[Pasted image 20260315215525.png]]

We get a **Max-Heap** if we reverse the condition in Heap Property #1 above.

End of required background.


------


Now, let me start with my Big Data problem and how this Min-Heap data structure is useful here.
I was building an in-memory cache in Golang for a big data solution. The dedicated server had to handle millions of cache items, and had 32 GB RAM, Linux, and 8 virtual CPUs.

At first, I created a classical TTL cache. Its full implementation is in my GitHub (https://github.com/outofboxer/go-cache-ttl/blob/main/naive/cache.go); here I just show the bottleneck, which is periodic item cleanup based on expiration time, like this:
```
// cleanup runs a ticker that purges expired entries 

type cacheEntry struct { 
	value any 
	expiresAt time.Time 
} 
type TTLCache struct { 
	mu sync.RWMutex 
	items map[string]cacheEntry 
	ttl time.Duration 
	stopChan chan struct{} 
}

// this is run periodically in a goroutine
func (c *TTLCache) deleteExpired() 
{ 
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

The `deleteExpired` function iterates over map items, so it's O(N) in the map's length.

I have detailed calculations in my GitHub repo; here are the main points.

**Full summary: 8 vCPU AWS / 32 GB / 1 KB values / unoptimized:**

| Metric                    | Value            |
| ------------------------- | ---------------- |
| Entries in 32 GB          | ~21M             |
| Get latency               | ~80 ns           |
| Set latency               | ~300 ns          |
| CPU utilization           | ~12% of 8 vCPUs  |
| Cleanup lock hold         | **~3.3 seconds** |
| Throughput during cleanup | **zero**         |
| Effective core usage      | **~1 of 8**      |
For calculations, see my GitHub analysis for this implementation: https://github.com/outofboxer/go-cache-ttl/blob/main/docs/naive-analysis.md

So, for 20 million cache items with 1 KB values each, we get a useless cache stuck for 3 seconds during each cleanup.

Sure thing, sharding per CPU could help. But let's consider using something more clever, namely involving a min-heap data structure.

Why should this help? Because of this chart. Min-heap has O(log n) complexity, and for millions of items the comparison with O(n) looks significant:

![[Pasted image 20260315221320.png]]


But there is no free meal: we need to combine a min-heap with a hashmap. So, I did it like below.


```

type heapEntry struct {  
    key       string  
    expiresAt time.Time  
    index     int // maintained by heap.Interface for O(log n) updates
}

type expiryHeap []*heapEntry // this is Min-Heap

type cacheEntry struct {  
    value     any  
    expiresAt time.Time  
    heapNode  *heapEntry // pointer back to heap node for O(log n) re-heap on update  
}

// deleteExpired pops at most k expired entries from the heap. 
// Holding the lock only long enough to drain the k oldest entries 
// avoids a full map scan and caps the critical section. 
func (c *TTLCache) deleteExpired(k int) { 
	c.mu.Lock() 
	defer c.mu.Unlock() 
	now := time.Now() 
	removed := 0 
	for c.h.Len() > 0 && removed < k { 
		top := c.h[0] // peek — O(1) 
		if now.Before(top.expiresAt) { 
			break // everything else expires later — heap guarantee 
		} 
		heap.Pop(&c.h) 
		delete(c.items, top.key) 
		removed++ 
	} 
}
```

Besides, Get and Set become a bit more complex:
```
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
    c.mu.Lock()  
    defer c.mu.Unlock()  
  
    entry, ok := c.items[key]  
    if !ok || time.Now().After(entry.expiresAt) {  
       return nil, false  
    }  
    return entry.value, true  
}
```

The cross-pointer `cacheEntry.heapNode` links the map entry back to its heap node. This is what makes O(log n) updates on `Set` possible — you don't search the heap for the key, you jump straight to its node.

The heap array is a flat `[]*heapEntry` slice — sequential memory, much friendlier to the prefetcher than random map bucket traversal.

**Throughput comparison — 1 goroutine:**

|Operation|Unoptimized|Min-Heap|Delta|
|---|---|---|---|
|Get|~12–16M/s|~12–16M/s|same|
|Set new|~3–5M/s|~3–5M/s|same|
|Set update|~3–5M/s|~6–10M/s|**~2×**|
|80/20 read-write|~10–13M/s|~10–14M/s|+5–10%|

---

**Latency percentiles during cleanup — 1 goroutine:**

| Metric                | Unoptimized | Min-Heap k=100 | Min-Heap k=50K |
| --------------------- | ----------- | -------------- | -------------- |
| Cleanup pause         | **3.3 s**   | **15 µs**      | **6 ms**       |
| Ops stalled per cycle | ~46M        | ~210           | ~84K           |
| p50 during cleanup    | ~1.6 s      | < 1 µs         | ~3 ms          |
| p99 during cleanup    | ~3.3 s      | ~15 µs         | ~6 ms          |
| p99.9 during cleanup  | ~3.3 s      | ~15 µs         | ~6 ms          |

**CPU utilization — 1 goroutine on 8 vCPU machine:**

```
1 goroutine doing all work  →  1 core at ~100%
7 cores                     →  completely idle
Effective utilization       →  12.5% of machine  (1 of 8 vCPUs)
```

Identical for both implementations — this is now purely a **single-threaded workload** on a machine built for parallelism.

---

**Full summary — 1 goroutine / 8 vCPU AWS / 32 GB / 1 KB values:**
More details at my GitHub: https://github.com/outofboxer/go-cache-ttl/blob/main/docs/minheap-analysis.md

|Metric|Unoptimized|Min-Heap|
|---|---|---|
|Entries in 32 GB|~22M|~21M|
|Get throughput|~12–16M/s|~12–16M/s|
|Set new throughput|~3–5M/s|~3–5M/s|
|Set update throughput|~3–5M/s|**~6–10M/s**|
|80/20 mix throughput|~10–13M/s|~10–14M/s|
|Cleanup pause|**~3.3 seconds**|**~15 µs**|
|CPU utilization|12.5%|12.5%|
|Cores actually used|1 of 8|1 of 8|

---

**The honest picture at 1 goroutine:**

The **only place the heap wins decisively is the cleanup pause** — cutting 3.3 seconds down to microseconds. For a single-goroutine workload that's still the critical result, because a 3.3 second stall every 60 seconds means roughly **5.5% of wall time the cache is completely frozen**.

```
Unoptimized:  3.3s pause / 60s TTL  =  5.5% of time frozen
Min-heap k=100:  15µs / 100ms tick  =  0.015% of time frozen
```

Both implementations are leaving ~88% of the 8 vCPUs idle. That's the next problem to solve — and it points directly to the **sharded cache**.



This is an important correction to a common intuition: the heap version is not “faster overall.” It is faster specifically at expiration management.

Thanks for reading!