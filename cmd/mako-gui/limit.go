package main

import (
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// A soft memory limit collects sooner as the heap nears it; with the live
// heap itself near the limit, the collector runs again as soon as it ends,
// on half the machine's cores: 900% CPU for a live heap of 150 MB against
// 192. So the limit keeps above the live heap, with room for it to grow
// and for the runtime's own, and comes back down as the live heap does.

// limitEvery is how often the limit is fitted to the live heap.
const limitEvery = 5 * time.Second

// fitLimit sets the soft limit to memoryLimit, or where the live heap
// needs more, above it: half again the live heap and 64 MB.
func fitLimit() {
	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	for {
		metrics.Read(sample)
		live := int64(sample[0].Value.Uint64())
		debug.SetMemoryLimit(limitFor(live))
		time.Sleep(limitEvery)
	}
}

// limitFor is the soft limit for a live heap of live bytes.
func limitFor(live int64) int64 {
	return max(memoryLimit, live+live/2+64<<20)
}
