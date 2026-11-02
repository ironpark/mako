package daemon

import (
	"context"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

func (d *Daemon) handleReadProjectAgents(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeProjectAgents, d.readProjectAgents)
}

func (d *Daemon) readProjectAgents(req *protocol.ReadProjectAgents) (*protocol.ProjectAgents, error) {
	if err := absolute(req.GetPath()); err != nil {
		return nil, err
	}
	all := d.listSessions()
	// Each session's project, worked out once: it reads the folders.
	scopes := make([]string, len(all))
	for i, info := range all {
		scopes[i] = d.projectScope(info)
	}
	// The folder's project, or the project of a session working in it, as
	// a worktree's is its repository's.
	scope := d.projectScope(&protocol.SessionInfo{Project: req.GetPath()})
	path := cleanPath(req.GetPath())
	for i, info := range all {
		if cleanPath(info.GetProject()) == path {
			scope = scopes[i]
			break
		}
	}
	d.sessionMu.Lock()
	parents := map[string]string{}
	for id, saved := range d.saved {
		parents[id] = saved.ParentID
	}
	d.sessionMu.Unlock()
	out := &protocol.ProjectAgents{Project: scope}
	members := map[string]bool{}
	for i, info := range all {
		if scopes[i] != scope {
			continue
		}
		out.Agents = append(out.Agents, &protocol.ProjectAgent{Session: withoutCommand(info), ParentSessionId: parents[info.GetId()], Messaging: d.takesMessages(info)})
		members[info.GetId()] = true
	}
	// The project's agents on other nodes, whose inboxes are theirs.
	for _, info := range d.remoteAgents(scope) {
		out.Agents = append(out.Agents, &protocol.ProjectAgent{Session: withoutCommand(info), Messaging: info.GetAgentMessaging()})
	}
	limit := int(min(req.GetMessageLimit(), 100))
	if limit == 0 {
		limit = 50
	}
	pending, messages := d.projectAgents.messages.monitor(scope, members, limit)
	out.Messages = messages
	for _, agent := range out.Agents {
		agent.PendingMessages = pending[agent.GetSession().GetId()]
	}
	return out, nil
}
