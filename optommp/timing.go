package optommp

import "sync"

// timingEdgesUs are a timing histogram's bucket edges, 1-2-5 per decade
// from 1 µs to 100 ms: the runtime's coarse lateness buckets, so a
// driver's numbers and a task's read the same way.
var timingEdgesUs = [...]int64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000, 50000, 100000}

// timing is a cumulative histogram of microsecond samples since start:
// bucket i counts samples below timingEdgesUs[i] and at or above the edge
// before it; the last bucket everything from 100 ms up.
type timing struct {
	mu    sync.Mutex
	n     [len(timingEdgesUs) + 1]uint64
	maxUs int64
}

func (t *timing) add(us int64) {
	i := 0
	for i < len(timingEdgesUs) && us >= timingEdgesUs[i] {
		i++
	}
	t.mu.Lock()
	t.n[i]++
	t.maxUs = max(t.maxUs, us)
	t.mu.Unlock()
}

// stats are the p99 (the upper edge of the bucket it falls in, so a bound)
// and the worst sample; zeros before any.
func (t *timing) stats() (p99Us, maxUs int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var total uint64
	for _, c := range t.n {
		total += c
	}
	if total == 0 {
		return 0, 0
	}
	k, acc := total-total/100, uint64(0) // the 99th-percentile sample's rank
	for i, c := range t.n {
		acc += c
		if acc >= k {
			if i < len(timingEdgesUs) {
				return timingEdgesUs[i], t.maxUs
			}
			return t.maxUs, t.maxUs
		}
	}
	return t.maxUs, t.maxUs
}
