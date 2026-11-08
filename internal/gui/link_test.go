//go:build !windows

package gui

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// Pointing the link elsewhere, as the UI goroutine does, does not wait out
// a worker's dial to a daemon that takes the connection and never answers.
func TestRetargetingDoesNotWaitOutADial(t *testing.T) {
	dir, err := os.MkdirTemp("", "mk") // short: a socket's path is
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // held, and never answered
		}
	}()

	l := newDaemonLink(sock, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := l.client(ctx)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // the dial is under way
	start := time.Now()
	l.setDaemon(filepath.Join(dir, "elsewhere"))
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Fatalf("retargeting waited %v for the dial", took)
	}
	if err := <-done; err == nil {
		t.Fatal("a client was kept from a daemon that never answered")
	}
}

// A visible tab whose stream broke off, as every stream does when a poll
// loses the daemon, is dialed again by the next attachVisible; one whose
// session exited is left as it is.
func TestAttachVisibleRedialsAFailedTerminal(t *testing.T) {
	m := newModel(config.Default(), "")
	m.post = func(func()) {}
	m.link.setDaemon(filepath.Join(t.TempDir(), "none"))
	for _, state := range []protocol.SessionState{protocol.StateRunning, protocol.StateExited} {
		link := newFakeLink()
		conn, err := newTermConn("s", link, 80, 24, func() {})
		if err != nil {
			t.Fatal(err)
		}
		close(link.in)
		for !conn.failed() {
			time.Sleep(time.Millisecond)
		}
		m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", State: state}})
		m.dialing["s"] = true // showTab's own attach
		m.showTab("s")
		delete(m.dialing, "s")
		m.conns["s"] = conn
		m.attachVisible()
		if redialed := m.dialing["s"] && m.conns["s"] == nil; redialed != (state == protocol.StateRunning) {
			t.Fatalf("%s: redialed=%v", state, redialed)
		}
		conn.close()
		delete(m.conns, "s")
		delete(m.dialing, "s")
	}
}

// The daemon's refusals are told apart from requests failing on the way,
// which the poll asks again.
func TestAskAgainTellsAnswersFromFailures(t *testing.T) {
	var stale atomic.Bool
	for _, e := range []*protocol.Error{{Message: "the mesh is off on this node"}, {Code: protocol.CodeUnsupported, Message: "unsupported"}} {
		_, err := transport.Reply[*protocol.AccessInfo](&protocol.Envelope{Body: &protocol.Envelope_Error{Error: e}})
		if err == nil || askAgain(&stale, true, err) || stale.Load() {
			t.Fatalf("%v is not taken for the daemon's answer", err)
		}
	}
	for _, err := range []error{context.DeadlineExceeded, io.EOF, net.ErrClosed} {
		stale.Store(false)
		if !askAgain(&stale, true, err) || !stale.Load() {
			t.Fatalf("%v is taken for the daemon's answer", err)
		}
	}
}
