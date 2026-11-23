package daemon

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

type blockingFileWatcher struct {
	pathExited atomic.Bool
	treeExited atomic.Bool
	release    chan struct{}
}

func (w *blockingFileWatcher) NewPathWatcher(func(string, ...any)) PathWatcher {
	return &blockingPathWatcher{parent: w}
}

func (w *blockingFileWatcher) NewTreeWatcher(time.Duration) TreeWatcher {
	return &blockingTreeWatcher{parent: w}
}

type blockingPathWatcher struct{ parent *blockingFileWatcher }

func (*blockingPathWatcher) Set([]string) {}
func (p *blockingPathWatcher) Run(ctx context.Context, _ chan<- []string) {
	<-ctx.Done()
	if p.parent.release != nil {
		<-p.parent.release
	}
	p.parent.pathExited.Store(true)
}

type blockingTreeWatcher struct{ parent *blockingFileWatcher }

func (*blockingTreeWatcher) Set([]string) {}
func (t *blockingTreeWatcher) Run(ctx context.Context, _ chan<- []string) error {
	<-ctx.Done()
	if t.parent.release != nil {
		<-t.parent.release
	}
	t.parent.treeExited.Store(true)
	return nil
}

func TestStreamWatchHandlersCleanExitOnCancelAndError(t *testing.T) {
	helloStream := func(t *testing.T, ctx context.Context, d *Daemon) (*transport.Stream, <-chan error) {
		t.Helper()
		clientConn, serverConn := net.Pipe()
		t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close() })
		clientStream := transport.NewStream(clientConn)
		serverStream := transport.NewStream(serverConn)
		done := make(chan error, 1)
		go func() {
			done <- d.serveStream(ctx, serverStream, false)
		}()
		if err := clientStream.Send(protocol.TypeHello, "h", &protocol.Hello{Name: "test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := clientStream.Recv(); err != nil {
			t.Fatal(err)
		}
		return clientStream, done
	}

	t.Run("handleWatch cancels cleanly without Recv race", func(t *testing.T) {
		d := New(&config.Config{NodeID: "n1"}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cs, done := helloStream(t, ctx, d)
		if err := cs.Send(protocol.TypeWatch, "w1", &protocol.Watch{}); err != nil {
			t.Fatal(err)
		}
		env, err := cs.Recv()
		if err != nil || env.Type() != protocol.TypeChanged {
			t.Fatalf("initial changed: env=%v err=%v", env, err)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil && !isClosed(err) {
				t.Fatalf("serveStream returned unexpected error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("handleWatch / serveStream did not exit after cancel")
		}
	})

	t.Run("handleWatchChat cancels cleanly without Recv race", func(t *testing.T) {
		d := New(&config.Config{NodeID: "n1"}, nil)
		s, err := session.Start(session.Spec{
			ID: "chat-s1", NodeID: "n1", Agent: "shell", Project: t.TempDir(),
			Command: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		d.sessions.add(s)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cs, done := helloStream(t, ctx, d)
		if err := cs.Send(protocol.TypeWatchChat, "wc1", &protocol.WatchChat{SessionId: s.ID()}); err != nil {
			t.Fatal(err)
		}
		env, err := cs.Recv()
		if err != nil || env.Type() != protocol.TypeChatLive {
			t.Fatalf("initial chat live: env=%v err=%v", env, err)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil && !isClosed(err) {
				t.Fatalf("serveStream returned unexpected error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("handleWatchChat / serveStream did not exit after cancel")
		}
	})

	t.Run("handleWatchBlock sends domain error and exits cleanly without adapter leak", func(t *testing.T) {
		d := New(&config.Config{NodeID: "n1"}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cs, done := helloStream(t, ctx, d)
		if err := cs.Send(protocol.TypeWatchBlock, "wb1", &protocol.WatchBlock{Block: "agent:missing-session/panel"}); err != nil {
			t.Fatal(err)
		}
		env, err := cs.Recv()
		if err != nil || env.Type() != protocol.TypeError {
			t.Fatalf("expected explicit TypeError for missing block, got env=%v err=%v", env, err)
		}
		_ = cs.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("serveStream did not exit")
		}

		ctx2, cancel2 := context.WithCancel(t.Context())
		defer cancel2()
		if _, err := d.agentUI.Show("s1", "claude", "notes", "Notes", []byte(`{"kind":"markdown","text":"hi"}`)); err != nil {
			t.Fatal(err)
		}
		cs2, done2 := helloStream(t, ctx2, d)
		if err := cs2.Send(protocol.TypeWatchBlock, "wb2", &protocol.WatchBlock{Block: "agent:s1/notes"}); err != nil {
			t.Fatal(err)
		}
		env2, err := cs2.Recv()
		if err != nil || env2.Type() != protocol.TypeBlockUpdate {
			t.Fatalf("block update: env=%v err=%v", env2, err)
		}
		cancel2()
		select {
		case err := <-done2:
			if err != nil && !isClosed(err) {
				t.Fatalf("serveStream returned unexpected error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("handleWatchBlock / serveStream did not exit after cancel")
		}
	})

	t.Run("handleWatchFiles waits for PathWatcher and TreeWatcher before returning", func(t *testing.T) {
		bw := &blockingFileWatcher{release: make(chan struct{})}
		d := New(&config.Config{NodeID: "n1"}, nil)
		d.fileWatcher = bw

		tree := gitTree(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cs, done := helloStream(t, ctx, d)
		if err := cs.Send(protocol.TypeWatchFiles, "wf1", &protocol.WatchFiles{Trees: []string{tree}}); err != nil {
			t.Fatal(err)
		}
		env, err := cs.Recv()
		if err != nil || env.Type() != protocol.TypeFilesChanged {
			t.Fatalf("initial files changed: env=%v err=%v", env, err)
		}

		// Closing the client triggers exit of handleWatchFiles, which must still
		// wait for both PathWatcher.Run and TreeWatcher.Run to complete.
		_ = cs.Close()
		select {
		case <-done:
			t.Fatal("handleWatchFiles returned before PathWatcher and TreeWatcher exited")
		case <-time.After(80 * time.Millisecond):
		}
		close(bw.release)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("handleWatchFiles did not exit after watchers finished")
		}
		if !bw.pathExited.Load() || !bw.treeExited.Load() {
			t.Fatalf("expected both watchers to exit: path=%v tree=%v", bw.pathExited.Load(), bw.treeExited.Load())
		}
	})
}
