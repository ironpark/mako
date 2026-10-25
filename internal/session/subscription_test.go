package session

import (
	"context"
	"testing"
	"time"
)

func newTestSubscription() (*Session, *Subscription) {
	s := &Session{subs: map[int]*Subscription{}}
	return s, s.Subscribe()
}

func TestSubscriptionCoalescesReadyChunks(t *testing.T) {
	_, sub := newTestSubscription()
	defer sub.Close()
	if !sub.push([]byte("one")) || !sub.push([]byte("two")) {
		t.Fatal("push refused")
	}
	got, ok := sub.Next(context.Background())
	if !ok || string(got) != "onetwo" {
		t.Fatalf("Next = %q, %v", got, ok)
	}
}

func TestSubscriptionSingleChunkIsNotCopied(t *testing.T) {
	_, sub := newTestSubscription()
	defer sub.Close()
	chunk := []byte("one")
	if !sub.push(chunk) {
		t.Fatal("push refused")
	}
	got, ok := sub.Next(context.Background())
	if !ok || len(got) != len(chunk) || &got[0] != &chunk[0] {
		t.Fatalf("single chunk was copied: %q %v", got, ok)
	}
}

func TestSubscriptionDeliversQueuedOutputBeforeClosing(t *testing.T) {
	_, sub := newTestSubscription()
	if !sub.push([]byte("last")) {
		t.Fatal("push refused")
	}
	sub.closeLocked() // what the pump does when the session exits
	got, ok := sub.Next(context.Background())
	if !ok || string(got) != "last" {
		t.Fatalf("queued output lost: %q %v", got, ok)
	}
	if _, ok := sub.Next(context.Background()); ok {
		t.Fatal("Next reported output after the queue drained")
	}
}

func TestSubscriptionDropsWhenFarBehind(t *testing.T) {
	_, sub := newTestSubscription()
	sub.mu.Lock()
	sub.bytes = maxPending
	sub.mu.Unlock()
	if sub.push([]byte("x")) {
		t.Fatal("a subscription past the cap was kept")
	}
	if _, ok := sub.Next(context.Background()); ok {
		t.Fatal("a dropped subscription still reported output")
	}
}

func TestSubscriptionCloseWakesNext(t *testing.T) {
	_, sub := newTestSubscription()
	done := make(chan bool, 1)
	go func() {
		_, ok := sub.Next(context.Background())
		done <- ok
	}()
	time.Sleep(10 * time.Millisecond)
	sub.Close()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("Next reported output after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake Next")
	}
}

func TestSubscriptionNextHonorsContext(t *testing.T) {
	_, sub := newTestSubscription()
	defer sub.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := sub.Next(ctx); ok {
		t.Fatal("Next ignored a cancelled context")
	}
}
