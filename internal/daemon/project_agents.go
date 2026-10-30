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
	scope := projectScope(&protocol.SessionInfo{Project: req.GetPath()})
	for _, info := range all {
		if projectScope(&protocol.SessionInfo{Project: info.GetProject()}) == scope {
			scope = projectScope(info)
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
	for _, info := range all {
		if projectScope(info) != scope {
			continue
		}
		info.Command = nil // runtime commands are not needed for monitoring
		summary := d.summarizeAgent(info, parents[info.GetId()])
		out.Agents = append(out.Agents, &protocol.ProjectAgent{Session: info, ParentSessionId: summary.ParentID, Messaging: summary.Messaging})
		members[info.GetId()] = true
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
