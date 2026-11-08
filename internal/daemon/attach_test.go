package daemon

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

// An attach replays the screen, streams live output, and ends with the exit
// code.
func TestAttachCarriesSnapshotOutputAndExit(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	s, err := session.Start(session.Spec{
		ID:      "s",
		NodeID:  "test-node",
		Agent:   "shell",
		Project: t.TempDir(),
		Command: []string{"sh", "-c", "printf hello; sleep 0.5"},
		Cols:    40,
		Rows:    6,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		served := transport.NewStream(b)
		env, err := served.Recv()
		if err != nil {
			serveErr <- err
			return
		}
		serveErr <- d.handleAttach(ctx, served, env)
	}()

	stream := transport.NewStream(a)
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := stream.Send(protocol.TypeAttach, "", &protocol.Attach{SessionId: "s", Replay: true}); err != nil {
		t.Fatal(err)
	}

	snap := &protocol.Snapshot{}
	var output strings.Builder
	exitCode, exited := 0, false
	for !exited {
		env, err := stream.Recv()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		ev, ok := protocol.StreamEventOf(env)
		if !ok {
			t.Fatalf("frame %q", env.Type())
		}
		switch ev.Kind {
		case protocol.EventSnapshot:
			snap = ev.Snapshot
		case protocol.EventOutput:
			output.Write(ev.Data)
		case protocol.EventExited:
			exitCode, exited = ev.ExitCode, true
		case protocol.EventError:
			t.Fatalf("attach failed: %s", ev.Err)
		}
	}
	if snap.GetCols() != 40 || snap.GetRows() != 6 {
		t.Fatalf("snapshot %dx%d, want 40x6", snap.GetCols(), snap.GetRows())
	}
	if !strings.Contains(output.String(), "hello") {
		t.Fatalf("output %q missing hello", output.String())
	}
	if exitCode != 0 {
		t.Fatalf("exit code %d", exitCode)
	}
	// The attach reads the stream until the front-end hangs up, so that
	// what reads it next does not read it alongside.
	select {
	case err := <-serveErr:
		t.Fatalf("the attach ended with its input still read: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	a.Close()
	if err := <-serveErr; err != nil && !isClosed(err) {
		t.Fatalf("handler: %v", err)
	}
}

// A daemon stopping ends an attach whose front-end holds on.
func TestAnAttachEndsAsTheDaemonStops(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	s, err := session.Start(session.Spec{ID: "s", NodeID: "test-node", Agent: "shell", Project: t.TempDir(), Command: []string{"sleep", "10"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := transport.NewStream(b)
	serveErr := make(chan error, 1)
	go func() {
		env, err := served.Recv()
		if err != nil {
			serveErr <- err
			return
		}
		serveErr <- d.handleAttach(ctx, served, env)
	}()
	go func() { _, _ = io.Copy(io.Discard, a) }()
	if err := transport.NewStream(a).Send(protocol.TypeAttach, "", &protocol.Attach{SessionId: "s"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-serveErr:
	case <-time.After(2 * time.Second):
		t.Fatal("the attach outlived its daemon")
	}
}
