//go:build unix

package repotrust

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Read refuses a FIFO rather than wait on it for a writer.
func TestReadRefusesAFIFO(t *testing.T) {
	repo := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(repo, "fifo"), 0o600); err != nil {
		t.Skip("no FIFOs:", err)
	}
	done := make(chan error, 1)
	go func() { _, err := Read(repo, "fifo"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read a FIFO")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO hangs")
	}
}
