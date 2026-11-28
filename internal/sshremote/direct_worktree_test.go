package sshremote

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A worktree asked of a direct SSH host without a branch gets one named
// after the repository's folder and a random ID, as a local one does; one
// with a branch keeps it.
func TestDirectWorktreeWithoutBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake ssh test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	useFakeSSH(t)
	repo := filepath.Join(t.TempDir(), "My Repo")
	for _, args := range [][]string{{"init", "-q", "-b", "main", repo}, {"-C", repo, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "first"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	host := config.SSHHost{ID: "test1", Name: "TestHost", Host: "fake.local"}
	named := regexp.MustCompile(`^My-Repo-[0-9a-z]{6}$`)
	seen := map[string]bool{}
	for range 2 {
		req := &protocol.OpenSession{Project: repo, Worktree: &protocol.Worktree{Base: "main"}}
		if err := EnsureWorktree(t.Context(), host, "", req); err != nil {
			t.Fatal(err)
		}
		w := req.GetWorktree()
		t.Cleanup(func() { os.RemoveAll(w.GetPath()) })
		if !named.MatchString(w.GetBranch()) || seen[w.GetBranch()] || req.GetProject() != w.GetPath() ||
			filepath.Base(w.GetPath()) != "My Repo-"+w.GetBranch() {
			t.Fatalf("worktree %+v, project %s", w, req.GetProject())
		}
		seen[w.GetBranch()] = true
		out, err := exec.Command("git", "-C", w.GetPath(), "branch", "--show-current").Output()
		if err != nil || strings.TrimSpace(string(out)) != w.GetBranch() {
			t.Fatalf("checked out %q, %v", out, err)
		}
	}

	req := &protocol.OpenSession{Project: repo, Worktree: &protocol.Worktree{Branch: "feat/x"}}
	if err := EnsureWorktree(t.Context(), host, "", req); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(req.GetWorktree().GetPath()) })
	if w := req.GetWorktree(); w.GetBranch() != "feat/x" || filepath.Base(w.GetPath()) != "My Repo-feat-x" {
		t.Fatalf("worktree %+v", w)
	}
}
