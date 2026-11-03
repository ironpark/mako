package gui

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// The Git tab follows the stamps of the sessions in the repository it
// shows, on its node: in its work tree or a folder of it, over it, or in
// a linked work tree of the same repository.
func TestTheGitTabFollowsItsRepositorysSessions(t *testing.T) {
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
