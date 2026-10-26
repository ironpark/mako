package gitstat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBranchReadsHEAD(t *testing.T) {
	root, run := cliRepo(t)
	if b := Branch(root); b != "main" {
		t.Fatalf("unborn branch %q", b)
	}
	write(t, root, "a.txt", "a\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	run("switch", "-q", "-c", "feature/x")
	if b := Branch(sub); b != "feature/x" {
		t.Fatalf("from a subfolder %q", b)
	}
	// A linked work tree's .git is a file pointing at its git directory.
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "-b", "other", linked)
	if b := Branch(linked); b != "other" {
		t.Fatalf("linked work tree %q", b)
	}
	run("switch", "-q", "--detach")
	if b := Branch(root); len(b) != 7 {
		t.Fatalf("detached %q", b)
	}
	if b := Branch(t.TempDir()); b != "" {
		t.Fatalf("outside a repository %q", b)
	}
}

// Repo names one folder for a repository's subfolders and its linked work
// trees: the main work tree.
func TestRepoIsTheMainWorkTree(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "a.txt", "a\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	sub := filepath.Join(root, "sub", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "-b", "other", linked)
	inside := filepath.Join(root, ".claude", "worktrees", "x")
	run("worktree", "add", "-q", "-b", "inside", inside)
	want, _ := filepath.EvalSymlinks(root) // as git records it for the linked ones
	for _, dir := range []string{root, sub, linked, filepath.Join(linked, "."), inside} {
		if got := Repo(dir); got != want {
			t.Errorf("Repo(%s) = %q, want %q", dir, got, want)
		}
	}
	if got := Repo(t.TempDir()); got != "" {
		t.Errorf("outside a repository %q", got)
	}
}
