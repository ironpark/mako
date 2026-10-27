package gui

import (
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/protocol"
)

// A session can start in a git worktree of its own, on a new branch, so
// several agents work on one repository without writing over each other.
// Its changes then compare with the branch it started from, and once done
// it merges back, goes up as a pull request, or is removed.

// worktreeAt is the worktree a session was made, whose folder root is.
func (m *model) worktreeAt(root fileRoot) *protocol.Worktree {
	for _, s := range m.Sessions.Get() {
		if w := s.GetWorktree(); w != nil && s.GetNodeId() == root.Node && w.GetPath() == root.Path {
			return w
		}
	}
	return nil
}

// worktreeItems are a worktree's actions, for a menu.
func worktreeItems(m *model, w *protocol.Worktree) []ggui.Widget {
	run := func(op, label string) func() {
		return func() { m.gitRun(&protocol.GitRun{Op: op, Worktree: w}, label, nil) }
	}
	merge := T("Merge into the base branch")
	if w.GetBase() != "" {
		merge = Tf("Merge into %s", w.GetBase())
	}
	return []ggui.Widget{
		ui.MenuItem(merge, run(protocol.GitOpMerge, T("Merging…"))),
		ui.MenuItem(T("Open a pull request"), run(protocol.GitOpPR, T("Opening a pull request…"))),
		ui.MenuItem(T("Remove worktree"), run(protocol.GitOpRemoveWT, T("Removing the worktree…"))),
	}
}
