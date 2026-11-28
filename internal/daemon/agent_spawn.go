package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

func (d *Daemon) availableAgents() []availableAgent {
	configured := d.agents()
	var out []availableAgent
	// What an agent may spawn: those this node offers, installed or on.
	for _, name := range agentspec.Offered(configured) {
		a, _ := agentspec.Lookup(name, configured)
		if len(a.Command) == 0 {
			continue
		}
		driver, ok := agents.ByProgram(a.Command[0])
		if !ok {
			continue
		}
		if _, ok := driver.Driver.(agent.Servers); !ok {
			continue
		}
		if _, ok := driver.Driver.(agent.Prompter); !ok {
			continue
		}
		available := availableAgent{Name: name, Spawnable: a.AgentAllowed() && d.agentMCPEnabled(nil), Profiles: []availableProfile{}}
		for _, p := range a.Profiles {
			available.Profiles = append(available.Profiles, availableProfile{
				Name: p.Name, Description: p.Description, Spawnable: p.AgentAllowed() && d.agentMCPEnabled(p.ContextServers),
			})
		}
		out = append(out, available)
	}
	return out
}

func (d *Daemon) spawnablePresets() []spawnablePreset {
	configured := d.agents()
	out := []spawnablePreset{}
	for _, name := range agentspec.Offered(configured) {
		a, _ := agentspec.Lookup(name, configured)
		if len(a.Command) == 0 {
			continue
		}
		driver, ok := agents.ByProgram(a.Command[0])
		if !ok {
			continue
		}
		if _, ok := driver.Driver.(agent.Servers); !ok {
			continue
		}
		if _, ok := driver.Driver.(agent.Prompter); !ok {
			continue
		}
		if a.AgentAllowed() && d.agentMCPEnabled(nil) {
			out = append(out, spawnablePreset{
				Agent:  name,
				Target: agentspec.Target(name, ""),
				Model:  a.Model,
				Effort: a.Effort,
				Yolo:   a.EffectiveYolo(name),
			})
		}
		for _, p := range a.Profiles {
			if !p.AgentAllowed() || !d.agentMCPEnabled(p.ContextServers) {
				continue
			}
			out = append(out, spawnablePreset{
				Agent:       name,
				Profile:     p.Name,
				Target:      agentspec.Target(name, p.Name),
				Description: p.Description,
				Model:       cmp.Or(p.Model, a.Model),
				Effort:      cmp.Or(p.Effort, a.Effort),
				Yolo:        p.EffectiveYolo(name, a),
			})
		}
	}
	return out
}

func remoteAvailableAgents(node *protocol.Node) []availableAgent {
	if node == nil {
		return nil
	}
	var out []availableAgent
	for _, ai := range node.GetAgents() {
		if ai.GetName() == "" || ai.GetName() == agentspec.ShellName {
			continue
		}
		profiles := make([]availableProfile, 0, len(ai.GetProfileDetails()))
		hasSpawnableProfile := false
		for _, p := range ai.GetProfileDetails() {
			if p.GetSpawnable() {
				hasSpawnableProfile = true
			}
			profiles = append(profiles, availableProfile{
				Name:        p.GetName(),
				Description: p.GetDescription(),
				Spawnable:   p.GetSpawnable(),
			})
		}
		if !ai.GetSpawnable() && !hasSpawnableProfile && len(profiles) == 0 {
			continue
		}
		out = append(out, availableAgent{
			Name:      ai.GetName(),
			Spawnable: ai.GetSpawnable(),
			Profiles:  profiles,
		})
	}
	return out
}

func remoteSpawnablePresets(node *protocol.Node) []spawnablePreset {
	if node == nil {
		return nil
	}
	out := []spawnablePreset{}
	for _, ai := range node.GetAgents() {
		name := ai.GetName()
		if name == "" || name == agentspec.ShellName {
			continue
		}
		if ai.GetSpawnable() {
			out = append(out, spawnablePreset{
				Agent:  name,
				Target: agentspec.Target(name, ""),
				Yolo:   ai.GetYolo(),
			})
		}
		for _, p := range ai.GetProfileDetails() {
			if !p.GetSpawnable() {
				continue
			}
			out = append(out, spawnablePreset{
				Agent:       name,
				Profile:     p.GetName(),
				Target:      agentspec.Target(name, p.GetName()),
				Description: p.GetDescription(),
				Model:       p.GetModel(),
				Effort:      p.GetEffort(),
				Yolo:        p.GetYolo(),
			})
		}
	}
	return out
}

func groupedSpawnableAgents(list []availableAgent) []availableAgent {
	grouped := []availableAgent{}
	for _, a := range list {
		spawnableProfiles := make([]availableProfile, 0, len(a.Profiles))
		for _, p := range a.Profiles {
			if p.Spawnable {
				spawnableProfiles = append(spawnableProfiles, p)
			}
		}
		if !a.Spawnable && len(spawnableProfiles) == 0 {
			continue
		}
		grouped = append(grouped, availableAgent{
			Name:      a.Name,
			Spawnable: a.Spawnable,
			Profiles:  spawnableProfiles,
		})
	}
	return grouped
}

func (d *Daemon) listAgentProfiles(peer *agentPeer, in listAgentProfilesArgs) (listAgentProfilesResult, error) {
	caller, err := d.projectCaller(peer)
	if err != nil {
		return listAgentProfilesResult{}, err
	}
	info := caller.Info()
	scope := d.projectScope(info)
	if strings.TrimSpace(in.Node) != "" {
		pn, rawNode, err := d.resolveProjectNode(scope, info.GetProject(), in.Node)
		if err != nil {
			return listAgentProfilesResult{}, err
		}
		if pn.Local {
			return listAgentProfilesResult{
				Node:    pn.Name,
				NodeID:  pn.ID,
				Folders: pn.Folders,
				Presets: d.spawnablePresets(),
				Agents:  groupedSpawnableAgents(d.availableAgents()),
			}, nil
		}
		if !pn.Reachable {
			return listAgentProfilesResult{}, fmt.Errorf("node %s is not reachable", pn.Name)
		}
		return listAgentProfilesResult{
			Node:    pn.Name,
			NodeID:  pn.ID,
			Folders: pn.Folders,
			Presets: remoteSpawnablePresets(rawNode),
			Agents:  groupedSpawnableAgents(pn.Available),
		}, nil
	}

	presets := d.spawnablePresets()
	grouped := groupedSpawnableAgents(d.availableAgents())
	var remoteNodes []remoteNodeInfo
	pNodes := d.projectNodes(scope, info.GetProject())
	if len(pNodes) > 1 {
		allNodes := d.nodes()
		byID := make(map[string]*protocol.Node, len(allNodes))
		for _, n := range allNodes {
			byID[n.GetId()] = n
		}
		for _, pn := range pNodes {
			if pn.Local {
				continue
			}
			remoteNodes = append(remoteNodes, remoteNodeInfo{
				ID:        pn.ID,
				Name:      pn.Name,
				OS:        pn.OS,
				Arch:      pn.Arch,
				Reachable: pn.Reachable,
				Spawnable: pn.Spawnable,
				Folders:   pn.Folders,
				Presets:   remoteSpawnablePresets(byID[pn.ID]),
			})
		}
	}
	return listAgentProfilesResult{
		Presets: presets,
		Agents:  grouped,
		Nodes:   remoteNodes,
	}, nil
}

func (d *Daemon) listProjectAgents(peer *agentPeer) (listAgentsResult, error) {
	s, err := d.projectCaller(peer)
	if err != nil {
		return listAgentsResult{}, err
	}
	info := s.Info()
	self := d.projectScope(info)
	out := listAgentsResult{
		SelfID:    peer.id,
		SelfNode:  d.nodeName(d.config().NodeID),
		Project:   info.GetProject(),
		Agents:    []agentSummary{},
		Nodes:     d.projectNodes(self, info.GetProject()),
		Available: []availableAgent{},
	}
	if s, _ := d.projectForSession(info); s != nil {
		out.ProjectName = cmp.Or(s.GetName(), s.GetId())
	}
	d.sessionMu.Lock()
	parents := map[string]string{}
	for id, saved := range d.saved {
		parents[id] = saved.ParentID
	}
	d.sessionMu.Unlock()
	for _, other := range d.listSessions() {
		if d.inProject(self, other) {
			out.Agents = append(out.Agents, summarizeAgent(other, parents[other.GetId()], d.takesMessages(other)))
		}
	}
	for _, other := range d.remoteAgents(self) {
		out.Agents = append(out.Agents, d.remoteSummary(other))
	}
	out.Available = append(out.Available, d.availableAgents()...)
	return out, nil
}

func (d *Daemon) spawnProjectAgent(ctx context.Context, peer *agentPeer, in spawnAgentArgs) (agentSummary, error) {
	caller, err := d.projectCaller(peer)
	if err != nil {
		return agentSummary{}, err
	}
	if strings.TrimSpace(in.Prompt) == "" || len(in.Prompt) > 64<<10 {
		return agentSummary{}, errors.New("prompt must be non-empty and at most 65536 bytes")
	}
	in.Name = strings.Join(strings.Fields(in.Name), " ")
	if len(in.Name) > maxSessionName {
		return agentSummary{}, fmt.Errorf("name must be at most %d bytes", maxSessionName)
	}
	info := caller.Info()
	scope := d.projectScope(info)
	if strings.TrimSpace(in.Node) != "" {
		pn, rawNode, err := d.resolveProjectNode(scope, info.GetProject(), in.Node)
		if err != nil {
			return agentSummary{}, err
		}
		if !pn.Local {
			return d.spawnRemoteProjectAgent(ctx, peer, caller, scope, pn, rawNode, in)
		}
	}

	projectDir := info.GetProject()
	worktree := info.GetWorktree()
	if folder := strings.TrimSpace(in.Folder); folder != "" {
		if !filepath.IsAbs(folder) || !dirExists(folder) {
			return agentSummary{}, fmt.Errorf("folder %q does not exist", folder)
		}
		folder = filepath.Clean(folder)
		if d.projectScope(&protocol.SessionInfo{Project: folder}) != scope {
			return agentSummary{}, errors.New("folder is not in this project")
		}
		if cleanPath(folder) != cleanPath(projectDir) {
			projectDir = folder
			worktree = nil
		}
	}

	if in.Agent == "" {
		in.Agent = info.GetAgent()
	}
	configured, ok := agentspec.Lookup(in.Agent, d.agents())
	if !ok || len(configured.Command) == 0 {
		return agentSummary{}, errors.New("unknown agent; use list_agents for available agents")
	}
	driver, ok := agents.ByProgram(configured.Command[0])
	if !ok {
		return agentSummary{}, errors.New("this agent cannot receive Mako MCP tools")
	}
	if _, ok := driver.Driver.(agent.Servers); !ok {
		return agentSummary{}, errors.New("this agent cannot receive Mako MCP tools")
	}
	var picks *[]string
	if in.Profile != "" {
		profile, ok := configured.Profile(in.Profile)
		if !ok {
			return agentSummary{}, errors.New("unknown agent profile")
		}
		if !profile.AgentAllowed() {
			return agentSummary{}, errors.New("this profile is not enabled for agent spawning")
		}
		picks = profile.ContextServers
	} else if !configured.AgentAllowed() {
		return agentSummary{}, errors.New("this agent default configuration is not enabled for agent spawning")
	}
	if !d.agentMCPEnabled(picks) {
		return agentSummary{}, errors.New("the mako MCP server is disabled for this profile")
	}
	if err := ctx.Err(); err != nil {
		return agentSummary{}, err
	}
	prompt := projectTaskPrompt(peer.id, in.Prompt)
	prompter, ok := driver.Driver.(agent.Prompter)
	if !ok {
		return agentSummary{}, errors.New("this agent has no interactive startup prompt")
	}
	child, err := d.startSessionAs(ctx, newSessionID(), &protocol.OpenSession{Agent: in.Agent, Profile: in.Profile, Project: projectDir, Worktree: worktree, Args: prompter.PromptArgs(prompt)}, peer.id)
	if err != nil {
		return agentSummary{}, err
	}
	if d.projectAgents.peer(child.ID()) == nil {
		_ = d.closeSession(child.ID())
		return agentSummary{}, errors.New("the new agent could not be configured with MCP")
	}
	if in.Name != "" {
		if _, err := d.renameSession(child.ID(), in.Name); err != nil {
			return agentSummary{}, fmt.Errorf("agent %s started but could not be named: %w", child.ID(), err)
		}
	}
	info = child.Info()
	summary := summarizeAgent(info, peer.id, d.takesMessages(info))
	summary.Hint = "Spawned asynchronously. Continue other work or end your turn—Mako will push its reply automatically (or use non-blocking watch_agent to get a note when its turn ends)."
	return summary, nil
}

func (d *Daemon) spawnRemoteProjectAgent(ctx context.Context, peer *agentPeer, caller *session.Session, scope string, pn projectNode, rawNode *protocol.Node, in spawnAgentArgs) (agentSummary, error) {
	if !shared(scope) {
		return agentSummary{}, errors.New("this project is local to this node; remote spawning requires a shared project or repository")
	}
	if !pn.Reachable {
		return agentSummary{}, fmt.Errorf("node %s is not reachable; ensure both nodes trust each other", pn.Name)
	}
	if rawNode != nil && rawNode.GetRevision() > 0 && rawNode.GetRevision() < protocol.RevisionRemoteAgentSpawn {
		return agentSummary{}, fmt.Errorf("node %s needs a newer Mako daemon (revision %d+) to spawn remote agents", pn.Name, protocol.RevisionRemoteAgentSpawn)
	}
	agentName := cmp.Or(strings.TrimSpace(in.Agent), caller.Info().GetAgent())
	spawnCtx, cancel := context.WithTimeout(ctx, relaySpawnTimeout)
	defer cancel()
	childInfo, err := forward[*protocol.SessionInfo](spawnCtx, d, protocol.TypeRelaySpawnAgent, &protocol.RelaySpawnAgent{
		NodeId:          pn.ID,
		Scope:           scope,
		ParentSessionId: peer.id,
		ParentNodeId:    d.config().NodeID,
		Agent:           agentName,
		Profile:         in.Profile,
		Name:            in.Name,
		Prompt:          in.Prompt,
		Folder:          strings.TrimSpace(in.Folder),
	})
	if err != nil {
		return agentSummary{}, fmt.Errorf("%s did not spawn the agent: %w", pn.Name, err)
	}
	if childInfo.GetAgentScope() == "" {
		childInfo.AgentScope = scope
	}
	if childInfo.GetParentSessionId() == "" {
		childInfo.ParentSessionId = peer.id
	}
	if childInfo.GetParentNodeId() == "" {
		childInfo.ParentNodeId = d.config().NodeID
	}
	d.projectAgents.recordRemoteChild(childInfo, peer.id, scope)
	running := childInfo.GetState() == protocol.StateRunning && childInfo.GetStatus() != protocol.AgentDone && childInfo.GetStatus() != protocol.AgentIdle
	caller.ReportSubagent("", childInfo.GetId(), running, time.Now().UnixNano())
	go d.followRemoteProjectAgent(caller, childInfo.GetId(), scope)

	summary := d.remoteSummary(childInfo)
	summary.ParentID = peer.id
	summary.Hint = fmt.Sprintf("Spawned asynchronously on %s. Continue other work or end your turn—Mako will push its reply automatically via cross-node messaging (or use non-blocking watch_agent to get a note when its turn ends).", summary.Node)
	return summary, nil
}

// Project agents are sessions of their own. Keep the parent's aggregate
// activity in step with their turns, rather than their process lifetime.
func (d *Daemon) followProjectAgent(child *session.Session, parentID string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var watch childWatch
	for {
		changed := child.Changed()
		info := d.reportProjectAgent(child, parentID)
		if info.GetState() == protocol.StateExited {
			d.noteExit(info, parentID, watch.workSince)
			return
		}
		d.noteChild(info, parentID, &watch, time.Now())
		// The tick is for what changes as time passes: work gone stale, a
		// wait on the user that has lasted.
		select {
		case <-changed:
		case <-tick.C:
		case <-d.ctx.Done():
			return
		}
	}
}

// reportProjectAgent reports to the parent whether child is running a
// turn, and returns child's info.
func (d *Daemon) reportProjectAgent(child *session.Session, parentID string) *protocol.SessionInfo {
	info := child.Info()
	running := info.GetState() == protocol.StateRunning && info.GetStatus() != protocol.AgentDone && info.GetStatus() != protocol.AgentIdle
	if parent, ok := d.sessions.get(parentID); ok {
		parent.ReportSubagent("", child.ID(), running, time.Now().UnixNano())
	}
	return info
}
