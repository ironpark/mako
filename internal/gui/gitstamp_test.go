package gui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/protocol"
)

// repoStamp tracks the stamps of sessions on root's node in its repository:
// in its work tree, a subfolder, or a linked work tree of the same repo.
func TestRepoStampMatchesRepositorySessions(t *testing.T) {
	sessions := []*protocol.SessionInfo{
		{Id: "a", NodeId: "n1", Project: "/w/repo", Repo: "/w/repo", GitStamp: "1"},
		{Id: "b", NodeId: "n1", Project: "/w/repo/sub", Repo: "/w/repo", GitStamp: "2"},
		{Id: "c", NodeId: "n2", Project: "/w/repo", Repo: "/w/repo", GitStamp: "3"},    // another node's clone
		{Id: "d", NodeId: "n1", Project: "/w/repo2", Repo: "/w/repo2", GitStamp: "4"},  // a neighbour sharing a prefix
		{Id: "e", NodeId: "n1", Project: "/w/repo-wt", Repo: "/w/repo", GitStamp: "5"}, // a linked work tree
		{Id: "f", NodeId: "n1", Project: "/w/repo", Repo: "/w/repo", GitStamp: ""},
	}
	if got := repoStamp(sessions, fileRoot{Node: "n1", Path: "/w/repo"}, ""); got != "a=1,b=2,e=5" {
		t.Fatalf("stamp %q", got)
	}
	// A folder of the work tree, shown with the top it is in.
	if got := repoStamp(sessions, fileRoot{Node: "n1", Path: "/w/repo/sub"}, "/w/repo"); got != "a=1,b=2,e=5" {
		t.Fatalf("stamp from a folder %q", got)
	}
	sessions[4].GitStamp = "9"
	if got := repoStamp(sessions, fileRoot{Node: "n1", Path: "/w/repo"}, ""); got != "a=1,b=2,e=9" {
		t.Fatalf("stamp after a commit in the linked work tree %q", got)
	}
	if got := repoStamp(sessions, fileRoot{Node: "n3", Path: "/w/repo"}, ""); got != "" {
		t.Fatalf("stamp with no session %q", got)
	}
}

// The plugin-backed Git tab refreshes automatically when a commit moves the
// repository's gitStamp / refs (including from an external or agent commit).
func TestTheGitTabFollowsItsRepositorysSessions(t *testing.T) {
	m, p := daemonApp(t)
	project, git := gitProject(t)

	m.DetailTab.Set(gitTab)
	m.launch("shell", "", project)
	scope := func() string {
		return ggui.Untrack(func() string { return m.resolveActiveScope(protocol.ScopeFolder) })
	}
	probeUntil(t, p, "initial history", func() bool {
		dag := findUiNodeSuffix(m.blockTreeFor(gitPluginBlock, scope()), "history-dag")
		return dag != nil && len(dag.GetRows()) == 4
	})

	// Wait for the daemon's async GitWatch goroutine to finish arming its
	// filesystem watchers (fswatch tree + git ref paths).
	time.Sleep(500 * time.Millisecond)

	// Commit outside the GUI (simulating an agent commit moving gitStamp / HEAD).
	if err := os.WriteFile(filepath.Join(project, "agent.txt"), []byte("from agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "agent.txt")
	git("commit", "-q", "-m", "Agent commit")

	probeUntil(t, p, "reloaded history after gitStamp change", func() bool {
		dag := findUiNodeSuffix(m.blockTreeFor(gitPluginBlock, scope()), "history-dag")
		return dag != nil && len(dag.GetRows()) == 5 && dag.GetRows()[0].GetText() == "Agent commit"
	})
}
