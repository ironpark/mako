package gui

import (
	"time"

	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/protocol"
)

// noteSkippedHooks tells of a session's worktree made without its
// repository's setup_cmd, its .mako/worktree.toml not trusted as it
// reads: a toast that stays until it is answered, whose Review opens the
// file to trust it. A file is told of once a minute, however many
// teammates' worktrees skipped it.
func (m *model) noteSkippedHooks(s *protocol.SessionInfo) {
	f := s.GetWorktree().GetSkippedHooks()
	if f == "" {
		return
	}
	node := m.remoteID(s.GetNodeId())
	key := node + "\x00" + f
	if at, ok := m.hooksNoted[key]; ok && time.Since(at) < time.Minute {
		return
	}
	if m.hooksNoted == nil {
		m.hooksNoted = map[string]time.Time{}
	}
	m.hooksNoted[key] = time.Now()
	note := T("Mako made the worktree but did not run the setup_cmd this file gives, since you have not trusted the file as it reads. Once you trust it, it runs for the worktrees made after.")
	m.toast(ui.Toast(T("Worktree setup skipped"), Tf("%s is not trusted, so its setup_cmd did not run.", protocol.Printable(f))).
		Duration(0).Action(T("Review…"), func() { m.askRepoTrust(node, f, note) }))
}
