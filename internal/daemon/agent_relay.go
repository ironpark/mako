package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// The agents of one project on several nodes see and message each other.
// Each daemon says in the state it sends its peers which project of
// several folders each of its sessions' agents is in, and whether it takes
// messages (shareScopes); the project's ID is the same on every node, as
// a front-end gives each the project's folders there. A message for an
// agent on another node goes to that node's daemon as a RelayAgentMessage,
// which keeps it in the recipient's inbox as one of its own agents' would
// be. So both nodes must let each other in, or reach each other through
// nodes that do: one to send it its sessions, the other to be asked.

// projectScopePrefix starts a sharedScope.
const projectScopePrefix = "project:"

// relayTimeout bounds how long a message for another node's agent waits
// for that node to take it.
const relayTimeout = 10 * time.Second

// errNotInProject is what an agent is told of a recipient it cannot reach.
var errNotInProject = errors.New("recipient is not an active messaging session in this project")

// shareScopes marks sessions, this daemon's, as they go to its peers: each
// with the project of several folders its agent is in, and whether the
// agent takes messages.
func (d *Daemon) shareScopes(sessions []*protocol.SessionInfo) {
	for _, info := range sessions {
		if !isAgent(info) {
			continue // a shell is in no project's agents, here or there
		}
		info.AgentScope = d.sharedScope(info)
		info.AgentMessaging = d.takesMessages(info)
	}
}

// isAgent reports whether a session runs an agent, which a project's
// agents list, rather than the node's shell.
func isAgent(info *protocol.SessionInfo) bool {
	return info.GetAgent() != agentspec.ShellName
}

// takesMessages reports whether the agent of a session of this daemon's
// takes messages over its MCP tools now.
func (d *Daemon) takesMessages(info *protocol.SessionInfo) bool {
	peer := d.projectAgents.peer(info.GetId())
	return peer != nil && peer.ctx.Err() == nil && info.GetState() == protocol.StateRunning
}

// remoteAgents are the sessions on other nodes whose agents are in scope,
// as their daemons last said: none outside a project of several folders.
func (d *Daemon) remoteAgents(scope string) []*protocol.SessionInfo {
	if !strings.HasPrefix(scope, projectScopePrefix) {
		return nil
	}
	var out []*protocol.SessionInfo
	for _, info := range d.meshSessions(d.routing()) {
		// A node of an older daemon shares its shells too.
		if info.GetAgentScope() == scope && info.GetNodeId() != d.cfg.NodeID && isAgent(info) {
			out = append(out, info)
		}
	}
	return out
}

// remoteAgent is the session id among remoteAgents(scope), nil for none.
func (d *Daemon) remoteAgent(scope, id string) *protocol.SessionInfo {
	for _, info := range d.remoteAgents(scope) {
		if info.GetId() == id {
			return info
		}
	}
	return nil
}

// remoteSummary is a session on another node as list_agents lists it.
func (d *Daemon) remoteSummary(info *protocol.SessionInfo) agentSummary {
	out := summarizeAgent(info, "", info.GetAgentMessaging())
	out.Node = d.nodeName(info.GetNodeId())
	return out
}

// sendAgentMessage sends text from peer's agent to the agent of session
// to: into its inbox when it is this daemon's, else to the daemon of the
// node it is on, when it is in peer's project there.
func (d *Daemon) sendAgentMessage(ctx context.Context, peer *agentPeer, to, text string) (agentMessage, error) {
	scope := d.projectScope(peer.session.Info())
	if d.projectAgents.peer(to) != nil {
		if _, err := d.projectTarget(scope, to); err != nil {
			return agentMessage{}, err
		}
		return d.projectAgents.messages.send(peer.id, to, text, scope)
	}
	remote := d.remoteAgent(scope, to)
	if remote == nil || !remote.GetAgentMessaging() {
		return agentMessage{}, errNotInProject
	}
	ctx, cancel := context.WithTimeout(ctx, relayTimeout)
	defer cancel()
	reply, err := forward[*protocol.AgentMessage](ctx, d, protocol.TypeRelayAgentMessage, &protocol.RelayAgentMessage{
		NodeId: remote.GetNodeId(), FromSessionId: peer.id, ToSessionId: to, Text: text, Scope: scope,
	})
	if err != nil {
		return agentMessage{}, fmt.Errorf("%s did not take the message: %w", d.nodeName(remote.GetNodeId()), err)
	}
	return agentMessageOf(reply), nil
}

func (d *Daemon) handleRelayAgentMessage(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeAgentMessage, d.takeRelayedMessage)
}

// takeRelayedMessage keeps a message an agent on another node sent one of
// this daemon's, in its inbox: when the two are in the project the message
// says, the sender as its node says and the recipient as this one does.
func (d *Daemon) takeRelayedMessage(req *protocol.RelayAgentMessage) (*protocol.AgentMessage, error) {
	scope := req.GetScope()
	if err := checkAgentText(req.GetText()); err != nil {
		return nil, err
	}
	if d.remoteAgent(scope, req.GetFromSessionId()) == nil {
		return nil, errors.New("the sender is not an agent of this project")
	}
	if _, err := d.projectTarget(scope, req.GetToSessionId()); err != nil {
		return nil, err
	}
	msg, err := d.projectAgents.messages.send(req.GetFromSessionId(), req.GetToSessionId(), req.GetText(), scope)
	if err != nil {
		return nil, err
	}
	return msg.proto("pending"), nil
}

// agentLabel is how a message's sender is named to its recipient: the
// session's ID, with its name, and its node when it is another's. mesh
// keeps the sessions of other nodes once read, for the next.
func (d *Daemon) agentLabel(id string, mesh *[]*protocol.SessionInfo) string {
	if s, ok := d.sessions.get(id); ok {
		if name := typable(s.Info().GetName()); name != "" {
			return fmt.Sprintf("%s (%s)", id, name)
		}
		return id
	}
	if *mesh == nil {
		*mesh = d.meshSessions(d.routing())
	}
	for _, info := range *mesh {
		if info.GetId() != id {
			continue
		}
		on := typable(d.nodeName(info.GetNodeId()))
		if name := typable(info.GetName()); name != "" {
			return fmt.Sprintf("%s (%s, on %s)", id, name, on)
		}
		return fmt.Sprintf("%s (on %s)", id, on)
	}
	return id
}

// withoutCommand is info without its runtime command, which monitoring
// does not need.
func withoutCommand(info *protocol.SessionInfo) *protocol.SessionInfo {
	info = proto.CloneOf(info)
	info.Command = nil
	return info
}
