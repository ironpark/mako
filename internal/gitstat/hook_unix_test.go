//go:build unix

package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A hook that runs out of time is killed with what it started, and leaves
// the time it was told to keep: a teardown_cmd that hangs leaves git time
// to remove its worktree.
func TestWorktreeHookTimesOutWithItsChildren(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := runWorktreeHook(ctx, 2*time.Second, dir, dir, "b", "main", `sleep 60 & echo $! > child.pid; wait`)
	if err == nil || !strings.Contains(err.Error(), "ran out of its") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Fatalf("the hook took %v, more than the 1s it had", took)
	}
	if ctx.Err() != nil {
		t.Fatal("the hook took the time it was to leave")
	}
	b, err := os.ReadFile(filepath.Join(dir, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the hook's child %d outlived it", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A teardown_cmd that runs out of time does not fail the removal: the
// worktree goes, and the output says the teardown_cmd ran out.
func TestRemoveWorktreeAfterATeardownThatHangs(t *testing.T) {
	_, w := removeRepo(t, `"sleep 60"`)
	ctx, cancel := context.WithTimeout(context.Background(), removeReserve+2*time.Second)
	defer cancel()
	out, err := RemoveWorktree(ctx, w, RemoveWorktreeOptions{Trusted: trustAll})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "teardown_cmd failed (ran out of its") {
		t.Fatalf("output = %q", out)
	}
	if _, err := os.Stat(w.GetPath()); !os.IsNotExist(err) {
		t.Fatal("worktree left")
	}
}

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
