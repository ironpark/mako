package client

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// silentDaemon listens at a socket of its own, greets each connection and
// then answers nothing, until the test ends.
func silentDaemon(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mako")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "d.sock") // short, as a Unix socket's path must be
	ln, err := transport.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				s := transport.NewStream(conn)
				if _, err := protocol.ReadFrame(s); err != nil {
					return
				}
				_ = s.Send(protocol.TypeWelcome, "", &protocol.Welcome{Node: &protocol.Node{Id: "d"}})
				_, _ = io.Copy(io.Discard, conn) // until the client hangs up
			}(conn)
		}
	}()
	return path
}

// A request keeps to its context after the daemon greets, by its deadline
// or by its being cancelled, and the client takes the next one as before.
func TestARequestKeepsToItsContext(t *testing.T) {
	path := silentDaemon(t)
	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := c.Nodes(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a request past its deadline: %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := c.Nodes(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("a request cancelled: %v", err)
	}
	if c.Node().GetId() != "d" {
		t.Fatalf("the client lost its daemon: %v", c.Node())
	}
}
