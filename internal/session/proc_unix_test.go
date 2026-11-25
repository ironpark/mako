//go:build unix

package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStartProcPreflightRejectsMissingDirAndBrokenShebang(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "missing")
	if _, err := startProc([]string{"/bin/sh", "-c", "true"}, missingDir, os.Environ(), 80, 24); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir: err = %v, want os.ErrNotExist", err)
	}

	dir := t.TempDir()
	broken := filepath.Join(dir, "broken-agent")
	if err := os.WriteFile(broken, []byte("#!/nonexistent/node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := startProc([]string{broken}, dir, os.Environ(), 80, 24); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("broken shebang: err = %v, want os.ErrNotExist", err)
	}
}
