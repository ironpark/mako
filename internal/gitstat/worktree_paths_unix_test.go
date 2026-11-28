//go:build unix

package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO among copy_paths is left out, not opened: reading one would wait
// for a writer that never comes.
func TestWorktreeBootstrapLeavesOutFIFOs(t *testing.T) {
	root, _ := bootstrapRepo(t, "copy_paths = [\"conf\", \"pipe\"]\n", "conf/\npipe\n")
	write(t, root, "conf/dev.json", "{}\n")
	for _, p := range []string{filepath.Join(root, "conf", "fifo"), filepath.Join(root, "pipe")} {
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wt, err := AddWorktree(ctx, root, NewWorktree{Branch: "feat/fifo", Start: "main"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(wt.Path)) })
	if _, err := os.Stat(filepath.Join(wt.Path, "conf", "dev.json")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(wt.Path, "conf", "fifo"), filepath.Join(wt.Path, "pipe")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("%s copied: %v", p, err)
		}
	}
}
