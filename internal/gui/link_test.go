//go:build !windows

package gui

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
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
