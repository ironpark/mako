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
