package main

import (
	"runtime/debug"
	"time"
)

// relieveEvery is how often relieve hands back memory nothing uses.
const relieveEvery = 30 * time.Second

// relieve hands the system back, now and then, the memory the program
// freed and kept: the Go heap's, which the runtime returns only slowly, and
// the C allocator's, which Metal and the window system leave in its zones.
// Both are counted as the app's until they are returned.
func relieve() {
	for range time.Tick(relieveEvery) {
		debug.FreeOSMemory()
		relieveMalloc()
	}
}
