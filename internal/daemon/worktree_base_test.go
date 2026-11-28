package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A session's new worktree starts at the start or base its request names,
// or else the repository's base_ref, read from its root even when the
// project is a folder in it; and records the branch it came off as its
// base.
func TestWorktreeSessionBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	d, _ := projectMCPDaemon(t)
	repo := filepath.Join(t.TempDir(), "repo")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(filepath.Join(repo, ".mako"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".mako", "worktree.toml"), []byte("base_ref = \"origin/main\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "main")
	git("commit", "-q", "--allow-empty", "-m", "origin main")
	git("remote", "add", "origin", t.TempDir())
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("reset", "-q", "--hard", "HEAD~")
	git("checkout", "-q", "-b", "dev")
	t.Cleanup(func() { os.RemoveAll(repo + ".worktrees") })

	for _, tc := range []struct {
		name            string
		w               *protocol.Worktree
		at, base, start string
	}{
		{"base_ref from a folder", &protocol.Worktree{}, "origin/main", "main", "origin/main"},
		{"the request's base", &protocol.Worktree{Base: "dev"}, "dev", "dev", ""},
		{"the request's start", &protocol.Worktree{Base: "main", Start: "dev"}, "dev", "main", "dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: filepath.Join(repo, "sub"), Worktree: tc.w})
			if err != nil {
				t.Fatal(err)
			}
			w := s.Info().GetWorktree()
			head := strings.TrimSpace(func() string {
				out, _ := exec.Command("git", "-C", w.GetPath(), "rev-parse", "HEAD").Output()
				return string(out)
			}())
			if head != git("rev-parse", tc.at) || w.GetBase() != tc.base || w.GetStart() != tc.start {
				t.Fatalf("worktree %+v at %s, want at %s, base %q, start %q", w, head, tc.at, tc.base, tc.start)
			}
		})
	}
}
