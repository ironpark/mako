package gitstat

import (
	"path/filepath"
	"testing"
	"time"
)

// The stamp moves with the repository's history, not with the work
// tree's files, nor with what is staged.
func TestStampMovesWithTheRepository(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "a.txt", "a\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	last := Stamp(root)
	if last == "" {
		t.Fatal("no stamp in a repository")
	}
	moved := func(what string) {
		t.Helper()
		if now := Stamp(root); now == last {
			t.Fatalf("the stamp did not move for %s", what)
		} else {
			last = now
		}
	}
	// The file times git compares are coarse on some file systems.
	tick := func() { time.Sleep(20 * time.Millisecond) }

	tick()
	write(t, root, "a.txt", "b\n")
	if Stamp(root) != last {
		t.Fatal("the stamp moved for an edit git has not seen")
	}
	run("add", "-A")
	if Stamp(root) != last {
		t.Fatal("the stamp moved for what was staged")
	}
	tick()
	run("commit", "-q", "-m", "second")
	moved("a commit")
	tick()
	run("branch", "other")
	moved("a branch made")
	tick()
	run("switch", "-q", "other")
	moved("a checkout")
	tick()
	write(t, root, "a.txt", "c\n")
	run("stash", "-q")
	moved("a stash")
	tick()
	remote := t.TempDir()
	run("init", "-q", "--bare", remote)
	run("remote", "add", "origin", remote)
	run("push", "-q", "-u", "origin", "other")
	moved("a push")
	tick()
	run("commit", "-q", "--allow-empty", "-m", "third")
	moved("a commit to push")
	tick()
	run("push", "-q")
	moved("a push of the branch again")
	if Stamp(filepath.Join(root, "missing")) != last {
		t.Fatal("a folder in the repository has another stamp")
	}
	if Stamp(t.TempDir()) != "" {
		t.Fatal("a stamp outside a repository")
	}
}
