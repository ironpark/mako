package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/repotrust"
)

// remove_worktree runs the repository's teardown_cmd only with the user's
// trust, kept by the daemon, and otherwise says how to trust it.
func TestRemoveWorktreeTeardownNeedsTrust(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	config := []byte("teardown_cmd = \"touch \\\"$MAKO_WORKTREE_REPO/teardown.marker\\\"\"\n")
	if err := os.MkdirAll(filepath.Join(repo, ".mako"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".mako", "worktree.toml"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("teardown.marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "init")
	t.Cleanup(func() { os.RemoveAll(repo + ".worktrees") })

	d := New(nil, nil)
	remove := func(branch string) string {
		t.Helper()
		made, err := gitstat.AddWorktree(t.Context(), repo, gitstat.NewWorktree{Branch: branch})
		if err != nil {
			t.Fatal(err)
		}
		w := &protocol.Worktree{Repo: made.Repo, Path: made.Path, Branch: made.Branch, Base: "main"}
		res, err := d.gitRunLocal(t.Context(), &protocol.GitRun{Root: made.Path, Op: protocol.GitOpRemoveWT, Worktree: w})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetOutput()
	}
	if out := remove("untrusted"); !strings.Contains(out, "mako repo-trust allow") {
		t.Fatalf("output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo, "teardown.marker")); err == nil {
		t.Fatal("teardown_cmd ran without trust")
	}
	d.repoTrust = repotrust.Open(t.TempDir())
	if _, err := d.repoTrust.Trust(repo, gitstat.WorktreeConfigFile, repotrust.Hash(config)); err != nil {
		t.Fatal(err)
	}
	if out := remove("trusted"); strings.Contains(out, "mako repo-trust") {
		t.Fatalf("output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo, "teardown.marker")); err != nil {
		t.Fatal("trusted teardown_cmd did not run")
	}
}

// remove_worktree passes its force on: unforced, a worktree with changes
// stays; forced, it goes. And a worktree the request names that git does
// not have of the repository is refused.
func TestRemoveWorktreeRequestForce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "-c", "user.name=T", "-c", "user.email=t@example.com", "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	t.Cleanup(func() { os.RemoveAll(repo + ".worktrees") })
	made, err := gitstat.AddWorktree(t.Context(), repo, gitstat.NewWorktree{Branch: "wip"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(made.Path, "notes.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := New(nil, nil)
	remove := func(w *protocol.Worktree, force bool) error {
		_, err := d.gitRunLocal(t.Context(), &protocol.GitRun{Root: made.Path, Op: protocol.GitOpRemoveWT, Worktree: w, Force: force})
		return err
	}
	w := &protocol.Worktree{Repo: made.Repo, Path: made.Path, Branch: made.Branch, Base: "main"}
	if err := remove(&protocol.Worktree{Repo: made.Repo, Path: t.TempDir(), Branch: made.Branch}, true); err == nil || !strings.Contains(err.Error(), "is not a worktree of") {
		t.Fatalf("a folder git does not have: %v", err)
	}
	if err := remove(w, false); err == nil {
		t.Fatal("removed a worktree with changes unforced")
	}
	if err := remove(w, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(made.Path); !os.IsNotExist(err) {
		t.Fatal("worktree left")
	}
}
