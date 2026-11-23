package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
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

type halfCloseMock struct {
	r          *io.PipeReader
	w          *io.PipeWriter
	closeReads atomic.Int32
	closes     atomic.Int32
}

func (m *halfCloseMock) Read(p []byte) (int, error)  { return m.r.Read(p) }
func (m *halfCloseMock) Write(p []byte) (int, error) { return m.w.Write(p) }
func (m *halfCloseMock) CloseRead() error {
	m.closeReads.Add(1)
	return m.r.Close()
}
func (m *halfCloseMock) Close() error {
	m.closes.Add(1)
	// Deliberately only closes the write half (like libp2p network.Stream)
	// so Read only unblocks if Stream.Close() calls CloseRead().
	return m.w.Close()
}

type resetOnlyMock struct {
	r      *io.PipeReader
	w      *io.PipeWriter
	resets atomic.Int32
}

func (m *resetOnlyMock) Read(p []byte) (int, error)  { return m.r.Read(p) }
func (m *resetOnlyMock) Write(p []byte) (int, error) { return m.w.Write(p) }
func (m *resetOnlyMock) Reset() error {
	m.resets.Add(1)
	_ = m.r.Close()
	return m.w.Close()
}
func (m *resetOnlyMock) Close() error {
	_ = m.r.Close()
	return m.w.Close()
}

func TestStreamCloseUnblocksRecvAndClosesPacker(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	pr2, pw2 := io.Pipe()
	defer pr2.Close()

	mock := &halfCloseMock{r: pr, w: pw2}
	s := NewPeerStream(mock)
	s.PackStream()

	recvErr := make(chan error, 1)
	go func() {
		_, err := s.Recv()
		recvErr <- err
	}()

	// Drain write pipe so Send succeeds while Recv is blocked.
	go func() { _, _ = io.Copy(io.Discard, pr2) }()
	if err := s.Send(protocol.TypeOutput, "", &protocol.Output{SessionId: "s", Data: []byte("hi")}); err != nil {
		t.Fatalf("Send before Close: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("first Stream.Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Stream.Close: %v", err)
	}

	select {
	case err := <-recvErr:
		if err == nil {
			t.Fatal("expected Recv to fail after Stream.Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv stayed blocked after Stream.Close")
	}

	if mock.closeReads.Load() != 1 || mock.closes.Load() != 1 {
		t.Fatalf("CloseRead=%d Close=%d, want 1, 1", mock.closeReads.Load(), mock.closes.Load())
	}
	if err := s.Send(protocol.TypeOutput, "", &protocol.Output{SessionId: "s", Data: []byte("after")}); err == nil {
		t.Fatal("expected Send after Stream.Close to fail")
	}
}

func TestStreamHangupResetClosesPacker(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	pr2, pw2 := io.Pipe()
	defer pr2.Close()

	mock := &resetOnlyMock{r: pr, w: pw2}
	s := NewPeerStream(mock)
	s.PackStream()

	s.mu.Lock()
	packer := s.packer
	s.mu.Unlock()
	if packer == nil {
		t.Fatal("expected packer to be initialized")
	}

	if err := s.Hangup(); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	if mock.resets.Load() != 1 {
		t.Fatalf("Reset called %d times, want 1", mock.resets.Load())
	}
	// Sending after Hangup must fail because packer and stream were closed.
	if err := s.Send(protocol.TypeOutput, "", &protocol.Output{SessionId: "s", Data: []byte("after")}); err == nil {
		t.Fatal("expected Send after Hangup to fail")
	}
}

func TestStreamConcurrentSendCloseRace(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	go func() { _, _ = io.Copy(io.Discard, b) }()

	s := NewPeerStream(a)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		s.PackStream()
	}()
	go func() {
		defer wg.Done()
		for range 20 {
			_ = s.Send(protocol.TypeOutput, "", &protocol.Output{SessionId: "s", Data: []byte("payload")})
		}
	}()
	go func() {
		defer wg.Done()
		time.Sleep(time.Millisecond)
		_ = s.Close()
	}()
	wg.Wait()
}
