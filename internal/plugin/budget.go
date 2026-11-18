package plugin

import (
	"context"
	"errors"
	"sync"
	"time"
)

// A call into a plugin may run its JavaScript for so long, its CPU time,
// and no more; the time it waits on the host, for an HTTP response say,
// is not counted, though the whole call is bounded too. budget is the
// context such a call runs under: done once the JavaScript has run its
// time, or the call its wall time, which interrupts the runtime (qngg
// checks the context as the code runs).

var (
	errCPU  = errors.New("the plugin's JavaScript ran longer than a call may")
	errWall = errors.New("the plugin's call took longer than a call may")
)

type budget struct {
	parent context.Context
	done   chan struct{}

	mu      sync.Mutex
	err     error
	left    time.Duration // the CPU time left, when paused
	started time.Time     // when the clock last ran on
	paused  int           // the host calls under way
	timer   *time.Timer
	wall    *time.Timer
	stop    func() bool
}

// newBudget is a context done after cpu of the JavaScript's time, or wall
// of any, or when parent is.
func newBudget(parent context.Context, cpu, wall time.Duration) *budget {
	b := &budget{parent: parent, done: make(chan struct{}), left: cpu, started: time.Now()}
	b.timer = time.AfterFunc(cpu, func() { b.end(errCPU) })
	b.wall = time.AfterFunc(wall, func() { b.end(errWall) })
	b.stop = context.AfterFunc(parent, func() { b.end(parent.Err()) })
	return b
}

func (b *budget) end(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return
	}
	b.err = err
	close(b.done)
}

// pause stops the CPU clock while the host works for the call; resume
// starts it again. Calls nest.
func (b *budget) pause() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused++
	if b.paused == 1 && b.err == nil {
		b.timer.Stop()
		b.left -= time.Since(b.started)
	}
}

func (b *budget) resume() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused--
	if b.paused > 0 || b.err != nil {
		return
	}
	b.started = time.Now()
	if b.left <= 0 {
		b.err = errCPU
		close(b.done)
		return
	}
	b.timer.Reset(b.left)
}

// release lets the budget's timers go once the call is over.
func (b *budget) release() {
	b.timer.Stop()
	b.wall.Stop()
	b.stop()
}

// exceeded says the call ran out of time, and which.
func (b *budget) exceeded() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if errors.Is(b.err, errCPU) || errors.Is(b.err, errWall) {
		return b.err
	}
	return nil
}

func (b *budget) Deadline() (time.Time, bool) { return time.Time{}, false }
func (b *budget) Done() <-chan struct{}       { return b.done }
func (b *budget) Value(key any) any           { return b.parent.Value(key) }

func (b *budget) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		return nil
	}
	if errors.Is(b.err, errCPU) || errors.Is(b.err, errWall) {
		return context.DeadlineExceeded
	}
	return b.err
}
