package daemon

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/session"
	"github.com/ironpark/sanbo/internal/transport"
)

// An attach replays the screen, streams live output, and ends with the exit
// code.
func TestAttachCarriesSnapshotOutputAndExit(t *testing.T) {
	tlsConf, err := transport.ServerTLS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := transport.Listen("127.0.0.1:0", tlsConf)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

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
		conn, err := ln.Accept(ctx)
		if err != nil {
			serveErr <- err
			return
		}
		stream, err := transport.AcceptStream(ctx, conn)
		if err != nil {
			serveErr <- err
			conn.CloseWithError(0, "done")
			return
		}
		env, err := stream.Recv()
		if err != nil {
			serveErr <- err
			conn.CloseWithError(0, "done")
			return
		}
		serveErr <- d.handleAttach(ctx, stream, env)
		// Keep the connection up until the test has read what was written.
		<-ctx.Done()
		conn.CloseWithError(0, "done")
	}()

	conn, err := transport.Dial(ctx, ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "done")
	stream, err := transport.OpenStream(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := stream.Send(protocol.TypeAttach, "", protocol.Attach{SessionID: "s", Replay: true}); err != nil {
		t.Fatal(err)
	}

	var snap protocol.Snapshot
	var output strings.Builder
	exitCode, exited := 0, false
	var ev protocol.StreamEvent
	for !exited {
		env, err := stream.Recv()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if ok, err := protocol.StreamEventOf(env, &ev); err != nil || !ok {
			t.Fatalf("frame %q: %v", env.Type, err)
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
	if snap.Cols != 40 || snap.Rows != 6 {
		t.Fatalf("snapshot %dx%d, want 40x6", snap.Cols, snap.Rows)
	}
	if !strings.Contains(output.String(), "hello") {
		t.Fatalf("output %q missing hello", output.String())
	}
	if exitCode != 0 {
		t.Fatalf("exit code %d", exitCode)
	}
	if err := <-serveErr; err != nil && !isClosed(err) {
		t.Fatalf("handler: %v", err)
	}
}
