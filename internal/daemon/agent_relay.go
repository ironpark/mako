package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

// The agents of one project on several nodes see and message each other.
// Each daemon says in the state it sends its peers which project each of
// its sessions' agents is in, and whether it takes messages (shareScopes):
// a project of several folders, whose ID is the same on every node, as a
// front-end gives each the project's folders there, or, with none, the
// clones of one repository, by its remote. A message for an
// agent on another node goes to that node's daemon as a RelayAgentMessage,
// which keeps it in the recipient's inbox as one of its own agents' would
// be. So both nodes must let each other in, or reach each other through
// nodes that do: one to send it its sessions, the other to be asked.

// projectScopePrefix and repoScopePrefix start a sharedScope: of a
// project of several folders, and of the clones of one repository.
const (
	projectScopePrefix = "project:"
	repoScopePrefix    = "repo:"
)

// shared reports whether scope spans nodes.
func shared(scope string) bool {
	return strings.HasPrefix(scope, projectScopePrefix) || strings.HasPrefix(scope, repoScopePrefix)
}

// relayTimeout bounds how long a message for another node's agent waits
// for that node to take it.
const (
	relayTimeout      = 10 * time.Second
	relaySpawnTimeout = 60 * time.Second
	relayStopTimeout  = 10 * time.Second
)

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
	return peer != nil && peer.ctx.Err() == nil && d.sessionAgentMCPEnabled(info)
}

// remoteAgents are the sessions on other nodes whose agents are in scope,
// as their daemons last said: none outside a shared project or repository.
// Recently spawned remote children are included until their node's state
// catches up so immediate follow-up messages and watches never race.
func (d *Daemon) remoteAgents(scope string) []*protocol.SessionInfo {
	if !shared(scope) {
		return nil
	}
	var out []*protocol.SessionInfo
	inMesh := map[string]bool{}
	nodeID := d.config().NodeID
	routes := d.routing()
	for _, info := range d.meshSessions(routes) {
		inMesh[info.GetId()] = true
		// A node of an older daemon shares its shells too.
		if info.GetAgentScope() == scope && info.GetNodeId() != nodeID && isAgent(info) {
			out = append(out, info)
		}
	}
	for _, info := range d.projectAgents.remoteChildrenForScope(scope) {
		if !inMesh[info.GetId()] && info.GetNodeId() != nodeID && routes[info.GetNodeId()].hops > 0 {
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
	out := summarizeAgent(info, info.GetParentSessionId(), info.GetAgentMessaging())
	out.Node = d.nodeName(info.GetNodeId())
	out.NodeID = info.GetNodeId()
	return out
}

// sendAgentMessage sends text from peer's agent to the agent of session
// to: into its inbox when it is this daemon's, else to the daemon of the
// node it is on, when it is in peer's project there.
func (d *Daemon) sendAgentMessage(ctx context.Context, peer *agentPeer, to, text string) (agentMessage, error) {
	if err := d.sandboxReach(peer.id, to, true); err != nil {
		return agentMessage{}, err
	}
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
	if rc, ok := d.projectAgents.remoteChild(id); ok && rc.info != nil {
		on := typable(d.nodeName(rc.info.GetNodeId()))
		if name := typable(rc.info.GetName()); name != "" {
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

// projectFoldersForScope returns the existing local folders that belong to
// scope on this node.
func (d *Daemon) projectFoldersForScope(scope string) []string {
	var out []string
	add := func(p string) {
		p = filepath.Clean(p)
		if p != "" && dirExists(p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if projectID, ok := strings.CutPrefix(scope, projectScopePrefix); ok {
		for _, ps := range d.projects.get() {
			if ps.GetId() == projectID {
				for _, p := range ps.GetPaths() {
					add(p)
				}
			}
		}
	}
	for _, info := range d.listSessions() {
		if d.projectScope(info) == scope {
			add(cmp.Or(info.GetWorktree().GetRepo(), info.GetProject()))
		}
	}
	return out
}

// projectNodes returns the local node and any remote nodes that share scope,
// along with each node's project folders, reachability, and spawnable agents.
func (d *Daemon) projectNodes(scope, localFolder string) []projectNode {
	selfID := d.config().NodeID
	localFolders := d.projectFoldersForScope(scope)
	if len(localFolders) == 0 && localFolder != "" {
		localFolders = []string{localFolder}
	}
	out := []projectNode{{
		ID:        selfID,
		Name:      d.nodeName(selfID),
		Local:     true,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Reachable: true,
		Spawnable: len(d.spawnablePresets()) > 0,
		Folders:   localFolders,
		Available: d.availableAgents(),
	}}
	if !shared(scope) {
		return out
	}

	routes := d.routing()
	allNodes := d.nodes()
	nodesByID := make(map[string]*protocol.Node, len(allNodes))
	for _, n := range allNodes {
		nodesByID[n.GetId()] = n
	}

	foldersByNode := map[string][]string{}
	addRemoteFolder := func(nid, folder string) {
		if nid == "" || nid == selfID {
			return
		}
		if _, exists := foldersByNode[nid]; !exists {
			foldersByNode[nid] = nil
		}
		if folder != "" && !slices.Contains(foldersByNode[nid], folder) {
			foldersByNode[nid] = append(foldersByNode[nid], folder)
		}
	}

	if projectID, ok := strings.CutPrefix(scope, projectScopePrefix); ok {
		for nid, paths := range d.workspace.projectLocations(projectID) {
			for _, p := range paths {
				addRemoteFolder(nid, p)
			}
		}
		for _, n := range allNodes {
			if n.GetId() == selfID {
				continue
			}
			for _, ps := range n.GetProjects() {
				if ps.GetId() == projectID {
					for _, p := range ps.GetPaths() {
						addRemoteFolder(n.GetId(), p)
					}
				}
			}
		}
	}
	for _, info := range d.remoteAgents(scope) {
		addRemoteFolder(info.GetNodeId(), cmp.Or(info.GetWorktree().GetRepo(), info.GetProject()))
	}

	remotes := make([]projectNode, 0, len(foldersByNode))
	for nid, folders := range foldersByNode {
		n := nodesByID[nid]
		reachable := routes[nid].hops > 0
		revOK := n != nil && n.GetRevision() >= protocol.RevisionRemoteAgentSpawn
		avail := remoteAvailableAgents(n)
		presets := remoteSpawnablePresets(n)
		remotes = append(remotes, projectNode{
			ID:        nid,
			Name:      d.nodeName(nid),
			OS:        n.GetOs(),
			Arch:      n.GetArch(),
			Reachable: reachable,
			Spawnable: reachable && revOK && len(presets) > 0,
			Folders:   folders,
			Available: avail,
		})
	}
	slices.SortFunc(remotes, func(a, b projectNode) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return append(out, remotes...)
}

// resolveProjectNode resolves a node ID or name within scope.
func (d *Daemon) resolveProjectNode(scope, localFolder, query string) (projectNode, *protocol.Node, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		nodes := d.projectNodes(scope, localFolder)
		return nodes[0], d.Self(), nil
	}
	nodes := d.projectNodes(scope, localFolder)
	allNodes := d.nodes()
	byID := make(map[string]*protocol.Node, len(allNodes))
	for _, n := range allNodes {
		byID[n.GetId()] = n
	}

	// Exact ID match first.
	for _, pn := range nodes {
		if pn.ID == query {
			return pn, byID[pn.ID], nil
		}
	}
	// Exact name match next.
	var exact []projectNode
	for _, pn := range nodes {
		if pn.Name == query {
			exact = append(exact, pn)
		}
	}
	if len(exact) == 1 {
		return exact[0], byID[exact[0].ID], nil
	}
	if len(exact) > 1 {
		return projectNode{}, nil, fmt.Errorf("node name %q is ambiguous; pass the node_id instead", query)
	}
	// Case-insensitive name match.
	var fold []projectNode
	for _, pn := range nodes {
		if strings.EqualFold(pn.Name, query) {
			fold = append(fold, pn)
		}
	}
	if len(fold) == 1 {
		return fold[0], byID[fold[0].ID], nil
	}
	if len(fold) > 1 {
		return projectNode{}, nil, fmt.Errorf("node name %q is ambiguous; pass the node_id instead", query)
	}

	// Give a clearer error when the node exists on the mesh but does not share this project.
	for _, n := range allNodes {
		if n.GetId() == query || strings.EqualFold(n.GetName(), query) {
			if !shared(scope) {
				return projectNode{}, nil, fmt.Errorf("this project is local to this node; share it as a project or git repository to spawn on %s", d.nodeName(n.GetId()))
			}
			return projectNode{}, nil, fmt.Errorf("node %q does not have this project", d.nodeName(n.GetId()))
		}
	}
	return projectNode{}, nil, fmt.Errorf("unknown node %q; use list_agents or list_agent_profiles to see project nodes", query)
}

func (d *Daemon) handleRelaySpawnAgent(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeSessionInfo, func(req *protocol.RelaySpawnAgent) (*protocol.SessionInfo, error) {
		return d.takeRelayedSpawn(ctx, req)
	})
}

func (d *Daemon) takeRelayedSpawn(ctx context.Context, req *protocol.RelaySpawnAgent) (*protocol.SessionInfo, error) {
	scope := req.GetScope()
	if !shared(scope) {
		return nil, errors.New("remote spawning requires a shared project or repository scope")
	}
	parentID := strings.TrimSpace(req.GetParentSessionId())
	parentNodeID := strings.TrimSpace(req.GetParentNodeId())
	if parentID == "" || parentNodeID == "" || parentNodeID == d.config().NodeID {
		return nil, errors.New("invalid calling session or node identity")
	}
	parent := d.remoteAgent(scope, parentID)
	if parent == nil || parent.GetNodeId() != parentNodeID || !parent.GetAgentMessaging() {
		return nil, errors.New("the calling session is not an active messaging agent of this project on this node (ensure both nodes trust each other)")
	}
	promptText := req.GetPrompt()
	if strings.TrimSpace(promptText) == "" || len(promptText) > 64<<10 {
		return nil, errors.New("prompt must be non-empty and at most 65536 bytes")
	}
	name := strings.Join(strings.Fields(req.GetName()), " ")
	if len(name) > maxSessionName {
		return nil, fmt.Errorf("name must be at most %d bytes", maxSessionName)
	}

	folder := strings.TrimSpace(req.GetFolder())
	if folder == "" {
		folders := d.projectFoldersForScope(scope)
		if len(folders) == 0 {
			return nil, errors.New("this node has no folder for this project")
		}
		folder = folders[0]
	} else {
		if !filepath.IsAbs(folder) || !dirExists(folder) {
			return nil, fmt.Errorf("folder %q does not exist on this node", folder)
		}
		folder = filepath.Clean(folder)
		if d.sharedScope(&protocol.SessionInfo{Project: folder}) != scope {
			return nil, errors.New("folder is not in this project on this node")
		}
	}

	agentName := strings.TrimSpace(req.GetAgent())
	if agentName == "" {
		return nil, errors.New("agent is required; use list_agent_profiles(node=...) for available choices")
	}
	configured, ok := agentspec.Lookup(agentName, d.agents())
	if !ok || len(configured.Command) == 0 {
		return nil, fmt.Errorf("unknown agent %q on %s; use list_agent_profiles(node=...) for available agents", agentName, d.nodeName(d.config().NodeID))
	}
	driver, ok := agents.ByProgram(configured.Command[0])
	if !ok {
		return nil, errors.New("this agent cannot receive Mako MCP tools")
	}
	if _, ok := driver.Driver.(agent.Servers); !ok {
		return nil, errors.New("this agent cannot receive Mako MCP tools")
	}
	var picks *[]string
	profileName := req.GetProfile()
	if profileName != "" {
		profile, ok := configured.Profile(profileName)
		if !ok {
			return nil, fmt.Errorf("unknown agent profile %q on %s", profileName, d.nodeName(d.config().NodeID))
		}
		if !profile.AgentAllowed() {
			return nil, errors.New("this profile is not enabled for agent spawning")
		}
		picks = profile.ContextServers
	} else if !configured.AgentAllowed() {
		return nil, errors.New("this agent default configuration is not enabled for agent spawning")
	}
	if !d.agentMCPEnabled(picks) {
		return nil, errors.New("the mako MCP server is disabled for this profile")
	}
	prompter, ok := driver.Driver.(agent.Prompter)
	if !ok {
		return nil, errors.New("this agent has no interactive startup prompt")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	taskPrompt := projectTaskPromptFromNode(parentID, d.nodeName(parentNodeID), promptText)
	child, err := d.startSessionWithParent(ctx, newSessionID(), &protocol.OpenSession{
		Agent:   agentName,
		Profile: profileName,
		Project: folder,
		Args:    prompter.PromptArgs(taskPrompt),
	}, parentID, parentNodeID)
	if err != nil {
		return nil, err
	}
	if d.projectAgents.peer(child.ID()) == nil {
		_ = d.closeSession(child.ID())
		return nil, errors.New("the new agent could not be configured with MCP")
	}
	if name != "" {
		if _, err := d.renameSession(child.ID(), name); err != nil {
			return nil, fmt.Errorf("agent %s started but could not be named: %w", child.ID(), err)
		}
	}
	d.pokeStatus()
	info := withoutCommand(d.withHead(child.Info()))
	info.ParentSessionId = parentID
	info.ParentNodeId = parentNodeID
	info.AgentScope = scope
	info.AgentMessaging = d.takesMessages(info)
	return info, nil
}

func (d *Daemon) handleRelayStopAgent(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeExited, func(req *protocol.RelayStopAgent) (*protocol.Exited, error) {
		return d.takeRelayedStop(req)
	})
}

func (d *Daemon) takeRelayedStop(req *protocol.RelayStopAgent) (*protocol.Exited, error) {
	scope := req.GetScope()
	if !shared(scope) {
		return nil, errors.New("only agents you spawned can be stopped")
	}
	parentID := strings.TrimSpace(req.GetParentSessionId())
	parentNodeID := strings.TrimSpace(req.GetParentNodeId())
	if parentID == "" || parentNodeID == "" || parentNodeID == d.config().NodeID {
		return nil, errors.New("only agents you spawned can be stopped")
	}
	d.sessionMu.Lock()
	saved, exists := d.saved[req.GetSessionId()]
	d.sessionMu.Unlock()
	owned := exists && saved.ParentID == parentID && saved.ParentNodeID == parentNodeID && d.projectScope(saved.Info) == scope
	if !owned {
		return nil, errors.New("only agents you spawned can be stopped")
	}
	if err := d.closeSession(req.GetSessionId()); err != nil {
		return nil, err
	}
	d.pokeStatus()
	return &protocol.Exited{SessionId: req.GetSessionId()}, nil
}

func (d *Daemon) stopProjectAgent(ctx context.Context, peer *agentPeer, sessionID string) (stopAgentResult, error) {
	caller, err := d.projectCaller(peer)
	if err != nil {
		return stopAgentResult{}, err
	}
	scope := d.projectScope(caller.Info())
	d.sessionMu.Lock()
	saved, exists := d.saved[sessionID]
	d.sessionMu.Unlock()
	// Scoping reads the folders, off the lock.
	if exists {
		owned := saved.ParentID == peer.id && saved.ParentNodeID == "" && d.projectScope(saved.Info) == scope
		if !owned {
			return stopAgentResult{}, errors.New("only agents you spawned can be stopped")
		}
		err := d.closeSession(sessionID)
		return stopAgentResult{Stopped: err == nil}, err
	}
	// Check if sessionID is a remote child spawned by this session.
	remote := d.remoteAgent(scope, sessionID)
	selfNodeID := d.config().NodeID
	if remote == nil || remote.GetParentSessionId() != peer.id || (remote.GetParentNodeId() != "" && remote.GetParentNodeId() != selfNodeID) {
		return stopAgentResult{}, errors.New("only agents you spawned can be stopped")
	}
	stopCtx, cancel := context.WithTimeout(ctx, relayStopTimeout)
	defer cancel()
	_, err = forward[*protocol.Exited](stopCtx, d, protocol.TypeRelayStopAgent, &protocol.RelayStopAgent{
		NodeId:          remote.GetNodeId(),
		SessionId:       sessionID,
		ParentSessionId: peer.id,
		ParentNodeId:    selfNodeID,
		Scope:           scope,
	})
	if err != nil {
		return stopAgentResult{}, fmt.Errorf("%s did not stop the agent: %w", d.nodeName(remote.GetNodeId()), err)
	}
	d.projectAgents.markRemoteStopped(sessionID)
	return stopAgentResult{Stopped: true}, nil
}

// followRemoteProjectAgent mirrors followProjectAgent for a child running on
// another node: keeps the parent's aggregate activity in step with the remote
// child's turns and delivers notes if it waits on the user or exits without
// reporting.
func (d *Daemon) followRemoteProjectAgent(parent *session.Session, childID, scope string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer parent.ReportSubagent("", childID, false, time.Now().UnixNano())
	var watch childWatch
	for {
		info := d.remoteAgent(scope, childID)
		if info == nil {
			return
		}
		running := info.GetState() == protocol.StateRunning && info.GetStatus() != protocol.AgentDone && info.GetStatus() != protocol.AgentIdle
		parent.ReportSubagent("", childID, running, time.Now().UnixNano())
		if info.GetState() == protocol.StateExited {
			d.noteRemoteExit(info, parent.ID(), watch.workSince)
			return
		}
		d.noteChild(info, parent.ID(), &watch, time.Now())
		select {
		case <-tick.C:
		case <-parent.Done():
			return
		case <-d.ctx.Done():
			return
		}
	}
}
