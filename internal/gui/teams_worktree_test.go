package gui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// A session that starts in a new worktree is waited for as long as the
// node can take to make it; one that does not, as long as before.
func TestOpenWaitsForTheWorktree(t *testing.T) {
	plain := &protocol.OpenSession{}
	inOne := &protocol.OpenSession{Worktree: &protocol.Worktree{Path: "/w"}}
	newOne := &protocol.OpenSession{Worktree: &protocol.Worktree{}}
	if openTimeout(plain, 10*time.Second) != 10*time.Second || openTimeout(inOne, 10*time.Second) != 10*time.Second {
		t.Fatal("a session in no new worktree waits longer")
	}
	if got := openTimeout(newOne, 10*time.Second); got < gitstat.WorktreeTimeout {
		t.Fatalf("a new worktree is waited for %v, less than the node's %v", got, gitstat.WorktreeTimeout)
	}
}

// A team isolated in worktrees starts its agents one after another: the
// next is asked for once the one before it started.
func TestIsolatedTeamStartsInTurn(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	m, _, posted := linkedTestModel(t)
	repo := filepath.Join(t.TempDir(), "repo")
	for _, args := range [][]string{{"init", "-q", "-b", "main", repo}, {"-C", repo, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "first"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	worktrees := repo + ".worktrees"
	t.Cleanup(func() { os.RemoveAll(worktrees) })
	made := func() int {
		entries, _ := os.ReadDir(worktrees)
		return len(entries)
	}

	c, err := m.link.client(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	lead, err := c.Open(t.Context(), &protocol.OpenSession{Agent: "shell", Project: repo, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	m.Sessions.Set([]*protocol.SessionInfo{lead})
	team := agentTeam{Name: "t", IsolateWorktrees: true, Members: []teamMember{{Name: "A", Agent: "shell"}, {Name: "B", Agent: "shell"}}}
	m.startTeam(lead.GetId(), team, "")
	first := waitPosted(t, posted)
	time.Sleep(300 * time.Millisecond)
	if n := made(); n != 1 {
		t.Fatalf("%d worktrees before the first agent started, want 1", n)
	}
	first()
	waitPosted(t, posted)()
	if n := made(); n != 2 {
		t.Fatalf("%d worktrees, want 2", n)
	}
	if n := len(ggui.Peek(m.Sessions)); n != 3 {
		t.Fatalf("%d sessions, want the leader and two agents", n)
	}
}

// waitPosted is the next reply the window was posted, not yet run.
func waitPosted(t *testing.T, posted chan func()) func() {
	t.Helper()
	select {
	case f := <-posted:
		return f
	case <-time.After(30 * time.Second):
		t.Fatal("no reply posted")
		return nil
	}
}
