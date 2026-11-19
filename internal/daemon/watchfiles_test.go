package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// A front-end follows a folder's entries and a work tree, on this node or
// through it on another, and what it follows can change on the stream.
func TestWatchingFiles(t *testing.T) {
	a := meshDaemon(t, "watch-a")
	b := meshDaemon(t, "watch-b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	waitFor(t, "the nodes linked", func() bool { _, err := a.node(b.cfg.NodeID); return err == nil })
	for _, node := range []string{"", b.cfg.NodeID} {
		dir, tree := t.TempDir(), gitTree(t)
		if err := os.MkdirAll(filepath.Join(tree, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		changes := make(chan *protocol.FilesChanged, 16)
		more := make(chan *protocol.WatchFiles, 1)
		go dialDaemon(t, a).WatchFiles(ctx, &protocol.WatchFiles{NodeId: node, Paths: []string{dir}}, more, func(c *protocol.FilesChanged) { changes <- c })
		next := func(what string, ok func(*protocol.FilesChanged) bool) {
			t.Helper()
			for {
				select {
				case c := <-changes:
					if ok(c) {
						return
					}
				case <-ctx.Done():
					t.Fatalf("node %q: no %s", node, what)
				}
			}
		}
		next("start", func(c *protocol.FilesChanged) bool { return len(c.GetPaths())+len(c.GetTrees()) == 0 })

		file := filepath.Join(dir, "new.txt")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		next("new file", func(c *protocol.FilesChanged) bool { return slices.Contains(c.GetPaths(), file) })

		more <- &protocol.WatchFiles{NodeId: node, Trees: []string{tree}}
		time.Sleep(200 * time.Millisecond) // for the node to take it
		if err := os.WriteFile(filepath.Join(tree, "src", "main.go"), []byte("package main"), 0o644); err != nil {
			t.Fatal(err)
		}
		next("change in the tree", func(c *protocol.FilesChanged) bool { return slices.Contains(c.GetTrees(), tree) })
		cancel()
	}
}

// A file staged changes nothing in the work tree, but its index: the tree
// is told changed all the same.
func TestStagingChangesTheTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	d := meshDaemon(t, "watch-git")
	tree := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", tree}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(tree, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-q", "-m", "a")
	if err := os.WriteFile(filepath.Join(tree, "a.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	changes := make(chan *protocol.FilesChanged, 16)
	go dialDaemon(t, d).WatchFiles(ctx, &protocol.WatchFiles{Trees: []string{tree}}, nil, func(c *protocol.FilesChanged) { changes <- c })
	<-changes                          // it watches
	time.Sleep(300 * time.Millisecond) // the edit before, settled
	for drained := false; !drained; {
		select {
		case <-changes:
		default:
			drained = true
		}
	}
	git("add", "a.txt")
	for {
		select {
		case c := <-changes:
			if slices.Contains(c.GetTrees(), tree) {
				return
			}
		case <-ctx.Done():
			t.Fatal("staging told of no change")
		}
	}
}

// gitTree is a new git work tree, or the test is skipped without git.
func gitTree(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	tree := t.TempDir()
	if out, err := exec.Command("git", "-C", tree, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return tree
}

// A folder outside a work tree has no status to read again: it is not
// followed, whatever is written in it, and not said to be unwatched.
func TestAFolderOutsideAWorkTreeIsNotFollowed(t *testing.T) {
	d := meshDaemon(t, "watch-plain")
	plain := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	changes := make(chan *protocol.FilesChanged, 16)
	go dialDaemon(t, d).WatchFiles(ctx, &protocol.WatchFiles{Trees: []string{plain}}, nil, func(c *protocol.FilesChanged) { changes <- c })
	<-changes // it watches
	if err := os.WriteFile(filepath.Join(plain, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-changes:
		t.Fatalf("told of %v", c)
	case <-time.After(2 * time.Second):
	}
}
