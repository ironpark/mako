package gui

import (
	"context"
	"time"

	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/lifecycle"
	"github.com/ironpark/mako/internal/protocol"
)

// noteRepoHooks tells, of an agent's session the window started, when
// the repository it is in has a .mako/hooks.toml the user does not trust
// as it reads, so that the node runs none of its hooks: a toast that stays
// until it is answered, whose Review opens the file to trust it. The node
// reads the file from the repository's main work tree, which the
// session's record names. A file is asked about once a minute, counted
// from an answer: a request that fails, the node not reached, say, leaves
// it to the next session to ask. A shell's session runs no hooks.
//
// The window calls it for each session it starts: from the new-session
// form, a team's, a prompt template's and a rewind's.
func (m *model) noteRepoHooks(s *protocol.SessionInfo) {
	repo := s.GetRepo()
	if repo == "" || isShellTarget(s.GetAgent()) {
		return
	}
	node := m.remoteID(s.GetNodeId())
	file := lifecycle.ProjectFileIn(repo)
	key := node + "\x00" + file
	if at, ok := m.repoHooksNoted[key]; ok && time.Since(at) < time.Minute {
		return
	}
	if m.repoHooksAsking[key] {
		return // another session's asks already
	}
	if m.repoHooksAsking == nil {
		m.repoHooksAsking = map[string]bool{}
	}
	m.repoHooksAsking[key] = true
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.RepoTrustInfo, error) {
		return c.RepoTrust(ctx, &protocol.RepoTrust{NodeId: node, Path: file})
	}, func(info *protocol.RepoTrustInfo, err error) {
		delete(m.repoHooksAsking, key)
		// No file, one the node cannot read, a node older than trust or
		// one not reached: nothing to tell, and the minute not begun.
		if err != nil {
			return
		}
		if m.repoHooksNoted == nil {
			m.repoHooksNoted = map[string]time.Time{}
		}
		m.repoHooksNoted[key] = time.Now()
		if len(info.GetFiles()) != 1 || info.GetFiles()[0].GetTrusted() {
			return
		}
		note := T("Mako did not run the hooks this file gives, since you have not trusted the file as it reads. Once you trust it, they run from the next event on.")
		m.toast(ui.Toast(T("Repository hooks not run"), Tf("%s is not trusted, so its hooks do not run.", protocol.Printable(file))).
			Duration(0).Action(T("Review hooks…"), func() { m.askRepoTrust(node, file, note) }))
	})
}
