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
//
// The limit counts all the runtime holds, not only the live heap: the
// stacks, the runtime's own, and the room in the spans in use that
// objects freed left and no collection gives back. That room is large
// after the live heap shrinks: a heap of 1150 MB that fell to 515 left
// 333 MB of it, and a limit fitted to the live heap alone, 840 MB, sat
// below the 1145 MB the runtime could not return, so the collector ran
// back to back, 20 times a second on 9 cores. The limit keeps above that
// room as well, and comes down as it does.

// limitEvery is how often the limit is fitted to the live heap.
const limitEvery = 5 * time.Second

// limitSamples are the runtime's numbers the limit is fitted to.
var limitSamples = []string{
	"/gc/heap/live:bytes",
	"/memory/classes/total:bytes",
	"/memory/classes/heap/released:bytes",
	"/memory/classes/heap/free:bytes",
	"/memory/classes/heap/objects:bytes",
}

// fitLimit sets the soft limit to memoryLimit, or where the live heap
// and what the runtime holds besides need more, above it.
func fitLimit() {
	s := make([]metrics.Sample, len(limitSamples))
	for i, name := range limitSamples {
		s[i].Name = name
	}
	for {
		metrics.Read(s)
		live := int64(s[0].Value.Uint64())
		debug.SetMemoryLimit(limitFor(live, besidesHeap(s[1].Value.Uint64(),
			s[2].Value.Uint64(), s[3].Value.Uint64(), s[4].Value.Uint64())))
		time.Sleep(limitEvery)
	}
}

// besidesHeap is what the runtime holds, and the limit counts, that no
// collection or scavenge can give back: all it maps less what it has
// returned to the system, the free pages it may return, and the heap's
// objects, which the collection either keeps as live or frees. What is
// left is the stacks, the runtime's own and the spans' unused room.
func besidesHeap(total, released, free, objects uint64) int64 {
	held := total - min(total, released+free+objects)
	return int64(held)
}

// limitFor is the soft limit for a live heap of live bytes, with other
// bytes the runtime holds besides: half again the live heap and 64 MB,
// above what is held besides, and never under memoryLimit.
func limitFor(live, other int64) int64 {
	return max(memoryLimit, other+live+live/2+64<<20)
}
