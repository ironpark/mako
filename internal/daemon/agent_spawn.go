package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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
				Yolo:   a.Yolo,
			})
		}
		for _, p := range a.Profiles {
			if !p.AgentAllowed() || !d.agentMCPEnabled(p.ContextServers) {
				continue
			}
			yolo := a.Yolo
			if p.Yolo != nil {
				yolo = *p.Yolo
			}
			out = append(out, spawnablePreset{
				Agent:       name,
				Profile:     p.Name,
				Target:      agentspec.Target(name, p.Name),
				Description: p.Description,
				Model:       p.Model,
				Effort:      p.Effort,
				Yolo:        yolo,
			})
		}
	}
	return out
}

func (d *Daemon) listAgentProfiles(peer *agentPeer) (listAgentProfilesResult, error) {
	if _, err := d.projectCaller(peer); err != nil {
		return listAgentProfilesResult{}, err
	}
	presets := d.spawnablePresets()
	grouped := []availableAgent{}
	for _, a := range d.availableAgents() {
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
	return listAgentProfilesResult{
		Presets: presets,
		Agents:  grouped,
	}, nil
}

func (d *Daemon) listProjectAgents(peer *agentPeer) (listAgentsResult, error) {
	s, err := d.projectCaller(peer)
	if err != nil {
		return listAgentsResult{}, err
	}
	info := s.Info()
	out := listAgentsResult{SelfID: peer.id, Project: info.GetProject(), Agents: []agentSummary{}, Available: []availableAgent{}}
	self := d.projectScope(info)
	if s, _ := d.projectFor(info.GetProject(), cmp.Or(info.GetWorktree().GetRepo(), info.GetRepo())); s != nil {
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
	child, err := d.startSessionAs(ctx, newSessionID(), &protocol.OpenSession{Agent: in.Agent, Profile: in.Profile, Project: info.GetProject(), Worktree: info.GetWorktree(), Args: prompter.PromptArgs(prompt)}, peer.id)
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
