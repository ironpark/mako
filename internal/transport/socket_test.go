package transport

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mako-sock-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestListenCloseRemovesOwnSocket(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "daemon.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("socket missing while listening: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket still exists after Close: %v", err)
	}
}

func TestListenCloseKeepsReplacedSocket(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "daemon.sock")
	first, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the socket at path with a newer listener before first closes.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	second, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	// Closing the older listener must not unlink the newer listener's socket.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("newer socket unreachable after older Close: %v", err)
	}
	c.Close()
}
