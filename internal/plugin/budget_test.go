package plugin

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The host's time is not counted: a budget paused while the host works
// runs out only on the time it ran.
func TestBudgetCountsOnlyItsOwnTime(t *testing.T) {
	b := newBudget(context.Background(), 100*time.Millisecond, time.Minute)
	defer b.release()
	b.pause()
	time.Sleep(300 * time.Millisecond)
	if b.Err() != nil {
		t.Fatal("the budget ran out while paused")
	}
	b.resume()
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the budget never ran out")
	}
	if !errors.Is(b.exceeded(), errCPU) {
		t.Fatalf("ran out for %v", b.exceeded())
	}
	// The wall time is counted, paused or not.
	w := newBudget(context.Background(), time.Minute, 100*time.Millisecond)
	defer w.release()
	w.pause()
	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the wall time never ran out")
	}
	if !errors.Is(w.exceeded(), errWall) {
		t.Fatalf("ran out for %v", w.exceeded())
	}
}

// A runtime's making is the host's time: on a slow machine, it may take
// longer than a call may, and the plugin is not stopped for it.
func TestStartingARuntimeIsTheHostsTime(t *testing.T) {
	hostDelay = func() { time.Sleep(300 * time.Millisecond) }
	defer func() { hostDelay = nil }()
	v := &version{man: &Manifest{Entry: "main.js"}, src: map[string]string{"main.js": `export default { activate() {} }`}, order: []string{"main.js"}}
	b := newBudget(context.Background(), 100*time.Millisecond, time.Minute)
	defer b.release()
	rt, err := startRuntime(b, v, DefaultLimits, nil)
	if err != nil {
		t.Fatalf("the host's time was counted: %v", err)
	}
	rt.close()
}
