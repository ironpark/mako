package session

import (
	"context"
	"sync"
)

// maxPending is how far a front-end may fall behind before it is dropped and
// resynced from a snapshot rather than stalling the PTY.
const maxPending = 8 << 20

// Subscription is one front-end's view of a session's live output. Chunks
// queue up while the reader is busy, and Next coalesces whatever is ready
// into one slice, so a burst becomes one frame instead of many. A reader
// slower than the PTY is dropped only once it is far enough behind that a
// snapshot is cheaper than the backlog.
type Subscription struct {
	s  *Session
	id int

	mu     sync.Mutex
	chunks [][]byte
	bytes  int
	read   []byte // coalescing buffer reused by Next
	closed bool
	signal chan struct{}
}

// Next waits for output and returns everything queued, coalesced into one
// slice. The slice belongs to the subscription and may be reused by the next
// call; callers write it out before receiving again. One goroutine reads at a
// time. It reports false once the session exits, the subscription is closed
// or dropped, or ctx ends.
func (sub *Subscription) Next(ctx context.Context) ([]byte, bool) {
	for {
		sub.mu.Lock()
		if len(sub.chunks) > 0 {
			chunks := sub.chunks
			sub.chunks, sub.bytes = nil, 0
			if len(chunks) == 1 {
				// The chunk is the reader's alone: the pump hands every
				// subscription its own copy of each read.
				out := chunks[0]
				sub.mu.Unlock()
				return out, true
			}
			out := sub.read[:0]
			for _, p := range chunks {
				out = append(out, p...)
			}
			sub.read = out
			sub.mu.Unlock()
			return out, true
		}
		closed := sub.closed
		sub.mu.Unlock()
		if closed {
			return nil, false
		}
		select {
		case <-sub.signal:
		case <-ctx.Done():
			return nil, false
		}
	}
}

// Close detaches the subscription. The session keeps running.
func (sub *Subscription) Close() {
	sub.s.mu.Lock()
	if current, ok := sub.s.subs[sub.id]; ok && current == sub {
		delete(sub.s.subs, sub.id)
	}
	sub.s.mu.Unlock()

	sub.mu.Lock()
	closed := sub.closed
	sub.closed = true
	sub.chunks, sub.bytes = nil, 0
	sub.mu.Unlock()
	if !closed {
		sub.notify()
	}
}

// push queues a chunk, reporting false when the subscription was dropped for
// falling too far behind.
func (sub *Subscription) push(p []byte) bool {
	sub.mu.Lock()
	if sub.closed {
		sub.mu.Unlock()
		return false
	}
	if sub.bytes+len(p) > maxPending {
		sub.closed = true
		sub.chunks, sub.bytes = nil, 0
		sub.mu.Unlock()
		sub.notify()
		return false
	}
	sub.chunks = append(sub.chunks, p)
	sub.bytes += len(p)
	sub.mu.Unlock()
	sub.notify()
	return true
}

// closeLocked marks the subscription closed and wakes its reader. The caller
// holds the session lock, as the pump and feed do.
func (sub *Subscription) closeLocked() {
	sub.mu.Lock()
	sub.closed = true
	sub.mu.Unlock()
	sub.notify()
}

func (sub *Subscription) notify() {
	select {
	case sub.signal <- struct{}{}:
	default:
	}
}
