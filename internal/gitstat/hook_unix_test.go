//go:build unix

package gitstat

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A repository's config is read only as a regular file of a bounded
// size inside it: a FIFO is not waited on, which would hold up every
// worktree of the repository, and a file too large or one through a link
// out of the repository is refused.
func TestWorktreeConfigReadIsBounded(t *testing.T) {
	for name, mk := range map[string]func(t *testing.T, root string){
		"fifo": func(t *testing.T, root string) {
			if err := syscall.Mkfifo(filepath.Join(root, ".mako", "worktree.toml"), 0o600); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
		},
		"too large": func(t *testing.T, root string) {
			write(t, root, ".mako/worktree.toml", "# "+strings.Repeat("x", maxWorktreeConfig)+"\n")
		},
		"linked out": func(t *testing.T, root string) {
			out := filepath.Join(t.TempDir(), "worktree.toml")
			write(t, filepath.Dir(out), "worktree.toml", "setup_cmd = \"true\"\n")
			if err := os.Symlink(out, filepath.Join(root, ".mako", "worktree.toml")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, _ := cliRepo(t)
			if err := os.MkdirAll(filepath.Join(root, ".mako"), 0o700); err != nil {
				t.Fatal(err)
			}
			mk(t, root)
			done := make(chan error, 1)
			go func() {
				_, err := LoadWorktreeConfig(root)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("config read")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("reading the config waited")
			}
		})
	}
}
