package daemon

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/fsutil"
)

// agentFile follows the agents and MCP servers of the config file the
// daemon was started from, so an agent, a flag, a profile or a server
// edited there applies to the next session without a restart. Everything
// else in the file is read at start.
type agentFile struct {
	mu          sync.Mutex
	path        string
	sum         [32]byte
	hasSum      bool
	badSum      [32]byte
	hasBadSum   bool
	read        bool
	agents      map[string]agentspec.Agent
	isolateHome bool
	shell       agentspec.Shell
	servers     map[string]mcp.Server
	// workspace is how the node shares its workspace.
	workspace config.Workspace
	decision  config.Decision
}

// FollowAgents makes the daemon read its agents from the config file at
// path whenever it changes, starting from the configuration it was given.
func (d *Daemon) FollowAgents(path string) {
	f := &d.agentFile
	f.mu.Lock()
	defer f.mu.Unlock()
	f.path = path
	f.hasSum, f.hasBadSum = false, false
	if body, err := os.ReadFile(path); err == nil {
		var file config.Config
		if json.Unmarshal(body, &file) == nil {
			file.TakeShell()
			f.agents, f.isolateHome, f.shell, f.servers, f.workspace, f.decision = file.Agents, file.IsolateHome, file.Shell, file.ContextServers, file.Workspace, file.Decision
			f.sum, f.hasSum, f.read = sha256.Sum256(body), true, true
		}
	}
}

// agents are what this daemon can launch now, the shell among them.
func (d *Daemon) agents() map[string]agentspec.Agent {
	f := d.followed()
	return agentspec.WithShell(f.agents, f.shell)
}

// isolateHome reports whether Mako's global isolated home is enabled.
func (d *Daemon) isolateHome() bool {
	return d.followed().isolateHome
}

// contextServers are the MCP servers configured now.
func (d *Daemon) contextServers() map[string]mcp.Server {
	return d.followed().servers
}

// decisionConfig returns the active decision provider/model defaults.
func (d *Daemon) decisionConfig() config.Decision {
	return d.followed().decision
}

// setDecisionConfig updates the daemon's decision defaults and persists them
// when FollowAgents is watching a configuration file.
func (d *Daemon) setDecisionConfig(next config.Decision) error {
	f := &d.agentFile
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.read {
		f.agents, f.isolateHome, f.shell, f.servers, f.workspace, f.decision, f.read = d.cfg.Agents, d.cfg.IsolateHome, d.cfg.Shell, d.cfg.ContextServers, d.cfg.Workspace, d.cfg.Decision, true
	}
	f.decision = next
	d.cfg.Decision = next
	if f.path == "" {
		return nil
	}
	cfg := config.Default()
	if d.cfg != nil {
		*cfg = *d.cfg
	}
	if body, err := os.ReadFile(f.path); err == nil {
		if err := json.Unmarshal(body, cfg); err != nil {
			return err
		}
		cfg.TakeShell()
	} else if !os.IsNotExist(err) {
		return err
	}
	cfg.Decision = next
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	payload := append(body, '\n')
	if err := fsutil.Write(f.path, payload); err != nil {
		return err
	}
	f.sum = sha256.Sum256(payload)
	f.hasSum = true
	f.hasBadSum = false
	return nil
}

// followedConfig is what the daemon follows of its config file.
type followedConfig struct {
	agents      map[string]agentspec.Agent
	isolateHome bool
	shell       agentspec.Shell
	servers     map[string]mcp.Server
	workspace   config.Workspace
	decision    config.Decision
}

func (d *Daemon) followed() followedConfig {
	f := &d.agentFile
	f.mu.Lock()
	if !f.read {
		f.agents, f.isolateHome, f.shell, f.servers, f.workspace, f.decision, f.read = d.cfg.Agents, d.cfg.IsolateHome, d.cfg.Shell, d.cfg.ContextServers, d.cfg.Workspace, d.cfg.Decision, true
	}
	now := followedConfig{agents: f.agents, isolateHome: f.isolateHome, shell: f.shell, servers: f.servers, workspace: f.workspace, decision: f.decision}
	reloaded := false
	if f.path != "" {
		if body, err := os.ReadFile(f.path); err == nil {
			sum := sha256.Sum256(body)
			if !f.hasSum || sum != f.sum {
				var file config.Config
				if err := json.Unmarshal(body, &file); err != nil {
					if !f.hasBadSum || sum != f.badSum {
						f.badSum, f.hasBadSum = sum, true
						d.log.Warn("config changed but cannot be read; keeping the agents", "path", f.path, "err", err)
					}
				} else {
					file.TakeShell()
					f.agents, f.isolateHome, f.shell, f.servers, f.workspace, f.decision = file.Agents, file.IsolateHome, file.Shell, file.ContextServers, file.Workspace, file.Decision
					f.sum, f.hasSum, f.hasBadSum = sum, true, false
					now = followedConfig{agents: f.agents, isolateHome: f.isolateHome, shell: f.shell, servers: f.servers, workspace: f.workspace, decision: f.decision}
					reloaded = true
					d.log.Info("configuration reloaded", "path", f.path)
				}
			}
		}
	}
	f.mu.Unlock()
	if reloaded {
		d.onConfigReloaded()
	}
	return now
}

func (d *Daemon) onConfigReloaded() {
	d.syncProjectAgents()
	d.changes.fire(true, true)
	d.pokeStatus()
}

// agentInfo lists the agents for this node's record: names and what their
// drivers say of them, since flags and environment, keys among them, stay
// here.
func (d *Daemon) agentInfo() []*protocol.AgentInfo {
	configured := d.agents()
	names := agentspec.Offered(configured)
	out := make([]*protocol.AgentInfo, len(names))
	for i, name := range names {
		a, _ := agentspec.Lookup(name, configured)
		info := &protocol.AgentInfo{
			Name:     name,
			Profiles: a.ProfileNames(),
			Yolo:     a.Yolo,
		}
		var hasServers, hasPrompter bool
		if e, ok := agents.Lookup(name); ok {
			c := e.Info.Color
			info.Icon, info.Caps, info.Color = e.Info.Icon, e.Caps, protocol.RGBA(c.R, c.G, c.B, c.A)
			_, hasServers = e.Driver.(agent.Servers)
			_, hasPrompter = e.Driver.(agent.Prompter)
		}
		if !hasServers && len(a.Command) > 0 {
			if drv, ok := agents.ByProgram(a.Command[0]); ok {
				_, hasServers = drv.Driver.(agent.Servers)
				_, hasPrompter = drv.Driver.(agent.Prompter)
			}
		}
		if len(a.Profiles) > 0 {
			details := make([]*protocol.AgentProfileInfo, len(a.Profiles))
			for j, p := range a.Profiles {
				yolo := a.Yolo
				if p.Yolo != nil {
					yolo = *p.Yolo
				}
				allowAgent := p.AgentAllowed()
				allowTemplate := p.TemplateAllowed()
				details[j] = &protocol.AgentProfileInfo{
					Name:          p.Name,
					Description:   p.Description,
					Model:         p.Model,
					Effort:        p.Effort,
					Yolo:          yolo,
					Spawnable:     hasServers && hasPrompter && d.agentMCPEnabled(p.ContextServers) && allowAgent,
					AllowAgent:    &allowAgent,
					AllowTemplate: &allowTemplate,
				}
			}
			info.ProfileDetails = details
		}
		out[i] = info
	}
	return out
}

func profileContextServers(a agentspec.Agent, profileName string) *[]string {
	if profileName == "" {
		return nil
	}
	if p, ok := a.Profile(profileName); ok {
		return p.ContextServers
	}
	return nil
}

func (d *Daemon) supportsAgentMCP(agentName, profileName string) bool {
	a, ok := agentspec.Lookup(agentName, d.agents())
	if !ok || len(a.Command) == 0 {
		return false
	}
	drv, ok := agents.ByProgram(a.Command[0])
	if !ok {
		return false
	}
	if _, ok := drv.Driver.(agent.Servers); !ok {
		return false
	}
	return d.agentMCPEnabled(profileContextServers(a, profileName))
}

func (d *Daemon) sessionAgentMCPEnabled(info *protocol.SessionInfo) bool {
	if info == nil || info.GetState() != protocol.StateRunning {
		return false
	}
	a, _ := agentspec.Lookup(info.GetAgent(), d.agents())
	return d.agentMCPEnabled(profileContextServers(a, info.GetProfile()))
}
