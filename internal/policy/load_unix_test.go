//go:build unix

package policy

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO where a repository's policy file would be is refused at once, not
// waited on.
func TestProjectPolicyFIFOIsNotWaitedOn(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".mako"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(proj, ".mako", "policy.toml"), 0o644); err != nil {
		t.Skip("no FIFO here:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := LoadProjectDenyRules(proj)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO read as a policy file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO as a policy file waited on it")
	}
}
