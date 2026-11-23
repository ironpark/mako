package main

import (
	"context"
	"runtime/debug"
	"time"
)

// relieveEvery is how often relieve hands back memory nothing uses.
const relieveEvery = 30 * time.Second

// relieve hands the system back, now and then, the memory the program
// freed and kept: the Go heap's, which the runtime returns only slowly, and
// the C allocator's, which Metal and the window system leave in its zones.
// Both are counted as the app's until they are returned.
func relieve(ctx context.Context) {
	relieveLoop(ctx, relieveEvery, func() {
		debug.FreeOSMemory()
		relieveMalloc()
	})
}

func relieveLoop(ctx context.Context, every time.Duration, step func()) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			step()
		}
	}
}
