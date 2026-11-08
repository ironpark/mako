package transport

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// answer reads one request on s and answers it with nodes, handing back
// the request as read.
func answer(t *testing.T, s *Stream) <-chan *protocol.Envelope {
	got := make(chan *protocol.Envelope, 1)
	go func() {
		env, err := protocol.ReadFrame(s)
		if err != nil {
			close(got)
			return
		}
		got <- env
		_ = s.Send(protocol.TypeNodes, env.GetId(), &protocol.Nodes{})
	}()
	return got
}

// Ask tells the node answering how long it is waited for; CallContext,
// whose stream goes on past the reply, does not.
func TestAskTellsItsBound(t *testing.T) {
	for _, whole := range []bool{true, false} {
		a, b := net.Pipe()
		asker, answerer := NewStream(a), NewStream(b)
		got := answer(t, answerer)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		if whole {
			_, err = Ask[*protocol.Nodes](ctx, asker, protocol.TypeListNodes, nil)
		} else {
			_, err = CallContext[*protocol.Nodes](ctx, asker, protocol.TypeListNodes, nil)
		}
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		within := Within(<-got)
		if whole && (within <= 4*time.Second || within > 5*time.Second) || !whole && within != 0 {
			t.Fatalf("whole=%v: told %v", whole, within)
		}
		a.Close()
		b.Close()
	}
}

// A call keeps to its context, failing with its error and closing the
// stream; one answered in time leaves the stream open.
func TestACallKeepsToItsContext(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	go func() { _, _ = protocol.ReadFrame(NewStream(b)) }() // and never answers
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := CallContext[*protocol.Nodes](ctx, NewStream(a), protocol.TypeListNodes, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a call past its deadline: %v", err)
	}
	if _, err := a.Write([]byte{0}); err == nil {
		t.Fatal("the stream was left open")
	}

	a, b = net.Pipe()
	defer a.Close()
	defer b.Close()
	answer(t, NewStream(b))
	ctx, cancel = context.WithCancel(context.Background())
	s := NewStream(a)
	if _, err := CallContext[*protocol.Nodes](ctx, s, protocol.TypeListNodes, nil); err != nil {
		t.Fatal(err)
	}
	cancel() // after the reply: the stream is the caller's now
	go func() { _, _ = b.Read(make([]byte, 1)) }()
	if _, err := a.Write([]byte{0}); err != nil {
		t.Fatalf("the stream was closed after its reply: %v", err)
	}
}
