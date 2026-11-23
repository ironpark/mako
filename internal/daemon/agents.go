package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/fsutil"
)

func cloneBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneStringSlicePtr(p *[]string) *[]string {
	if p == nil {
		return nil
	}
	cp := slices.Clone(*p)
	if cp == nil {
		cp = []string{}
	}
	return &cp
}

func cloneAgent(a agentspec.Agent) agentspec.Agent {
	a.Command = slices.Clone(a.Command)
	a.Args = slices.Clone(a.Args)
	a.Env = maps.Clone(a.Env)
	a.Enabled = cloneBoolPtr(a.Enabled)
	a.AllowAgent = cloneBoolPtr(a.AllowAgent)
	a.AllowTemplate = cloneBoolPtr(a.AllowTemplate)
	if a.Profiles != nil {
		profiles := make([]agentspec.Profile, len(a.Profiles))
		for i, p := range a.Profiles {
			p.Args = slices.Clone(p.Args)
			p.Env = maps.Clone(p.Env)
			p.Yolo = cloneBoolPtr(p.Yolo)
			p.ContextServers = cloneStringSlicePtr(p.ContextServers)
			p.AllowAgent = cloneBoolPtr(p.AllowAgent)
			p.AllowTemplate = cloneBoolPtr(p.AllowTemplate)
			profiles[i] = p
		}
		a.Profiles = profiles
	}
	return a
}

func cloneAgentsMap(m map[string]agentspec.Agent) map[string]agentspec.Agent {
	if m == nil {
		return nil
	}
	out := make(map[string]agentspec.Agent, len(m))
	for k, v := range m {
		out[k] = cloneAgent(v)
	}
	return out
}

func cloneShell(s agentspec.Shell) agentspec.Shell {
	s.Command = slices.Clone(s.Command)
	s.Args = slices.Clone(s.Args)
	s.Env = maps.Clone(s.Env)
	return s
}

func cloneContextServers(m map[string]mcp.Server) map[string]mcp.Server {
	if m == nil {
		return nil
	}
	out := make(map[string]mcp.Server, len(m))
	for k, s := range m {
		s.Enabled = cloneBoolPtr(s.Enabled)
		s.Settings = bytes.Clone(s.Settings)
		if s.Command != nil {
			cmd := *s.Command
			cmd.Args = slices.Clone(cmd.Args)
			cmd.Env = maps.Clone(cmd.Env)
			s.Command = &cmd
		}
		out[k] = s
	}
	return out
}

func cloneDecision(d config.Decision) config.Decision {
	d.Models = maps.Clone(d.Models)
	return d
}

func cloneWorkspace(w config.Workspace) config.Workspace {
	return config.Workspace{
		Sync:        cloneBoolPtr(w.Sync),
		SyncPlugins: cloneBoolPtr(w.SyncPlugins),
		SyncAgents:  cloneBoolPtr(w.SyncAgents),
	}
}

func cloneConfig(c *config.Config) *config.Config {
	if c == nil {
		c = config.Default()
	}
	out := *c
	out.Peers = slices.Clone(c.Peers)
	out.Discovery.TailnetHosts = slices.Clone(c.Discovery.TailnetHosts)
	out.P2P.Relays = slices.Clone(c.P2P.Relays)
	out.Agents = cloneAgentsMap(c.Agents)
	out.Shell = cloneShell(c.Shell)
	out.ContextServers = cloneContextServers(c.ContextServers)
	out.Devices.Share = slices.Clone(c.Devices.Share)
	out.Workspace = cloneWorkspace(c.Workspace)
	out.Decision = cloneDecision(c.Decision)
	return &out
}

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
	statMod     time.Time
	statSize    int64
	hasStat     bool
	reads       int
	read        bool
	agents      map[string]agentspec.Agent
	isolateHome bool
	shell       agentspec.Shell
	servers     map[string]mcp.Server
	// workspace is how the node shares its workspace.
	workspace config.Workspace
	decision  config.Decision
}

func (f *agentFile) setFromConfigLocked(c *config.Config) {
	f.agents = cloneAgentsMap(c.Agents)
	f.isolateHome = c.IsolateHome
	f.shell = cloneShell(c.Shell)
	f.servers = cloneContextServers(c.ContextServers)
	f.workspace = cloneWorkspace(c.Workspace)
	f.decision = cloneDecision(c.Decision)
	f.read = true
}

func (d *Daemon) syncConfigFromAgentFileLocked(f *agentFile) {
	d.updateConfig(func(c *config.Config) {
		c.Agents = cloneAgentsMap(f.agents)
		c.IsolateHome = f.isolateHome
		c.Shell = cloneShell(f.shell)
		c.ContextServers = cloneContextServers(f.servers)
		c.Workspace = cloneWorkspace(f.workspace)
		c.Decision = cloneDecision(f.decision)
	})
}

// FollowAgents makes the daemon read its agents from the config file at
// path whenever it changes, starting from the configuration it was given.
func (d *Daemon) FollowAgents(path string) {
	d.agentSync.applyMu.Lock()
	defer d.agentSync.applyMu.Unlock()
	f := &d.agentFile
	f.mu.Lock()
	f.path = path
	f.hasSum, f.hasBadSum, f.hasStat = false, false, false
	if fh, err := os.Open(path); err == nil {
		fi, statErr := fh.Stat()
		body, readErr := io.ReadAll(fh)
		_ = fh.Close()
		if statErr == nil && readErr == nil {
			f.reads++
			var file config.Config
			if json.Unmarshal(body, &file) == nil {
				file.TakeShell()
				f.setFromConfigLocked(&file)
				f.sum, f.hasSum, f.hasBadSum = sha256.Sum256(body), true, false
				f.statMod, f.statSize, f.hasStat = fi.ModTime(), fi.Size(), true
				d.syncConfigFromAgentFileLocked(f)
			}
		}
	}
	f.mu.Unlock()
	if d.syncsAgentsSnapshot() {
		d.captureLocalAgentConfigLocked()
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
	return cloneDecision(d.followed().decision)
}

// setDecisionConfig updates the daemon's decision defaults and persists them
// when FollowAgents is watching a configuration file.
func (d *Daemon) setDecisionConfig(next config.Decision) error {
	next = cloneDecision(next)
	f := &d.agentFile
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.path == "" {
		if !f.read {
			f.setFromConfigLocked(d.config())
		}
		f.decision = cloneDecision(next)
		d.updateConfig(func(c *config.Config) {
			c.Decision = cloneDecision(next)
		})
		return nil
	}
	cfg := cloneConfig(d.config())
	if body, err := os.ReadFile(f.path); err == nil {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			return err
		}
		if _, ok := raw["agents"]; ok {
			cfg.Agents = nil
		}
		if _, ok := raw["context_servers"]; ok {
			cfg.ContextServers = nil
		}
		if err := json.Unmarshal(body, cfg); err != nil {
			return err
		}
		cfg.TakeShell()
	} else if !os.IsNotExist(err) {
		return err
	}
	cfg.Decision = cloneDecision(next)
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
	var written config.Config
	if err := json.Unmarshal(payload, &written); err != nil {
		return err
	}
	written.TakeShell()
	f.setFromConfigLocked(&written)
	f.sum = sha256.Sum256(payload)
	f.hasSum = true
	f.hasBadSum = false
	if fi, err := os.Stat(f.path); err == nil {
		f.statMod, f.statSize, f.hasStat = fi.ModTime(), fi.Size(), true
	} else {
		f.hasStat = false
	}
	d.syncConfigFromAgentFileLocked(f)
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
		f.setFromConfigLocked(d.config())
	}
	now := followedConfig{agents: f.agents, isolateHome: f.isolateHome, shell: f.shell, servers: f.servers, workspace: f.workspace, decision: f.decision}
	reloaded := false
	if f.path != "" {
		fi, err := os.Stat(f.path)
		if err != nil {
			f.hasStat = false
		} else if !f.hasStat || !fi.ModTime().Equal(f.statMod) || fi.Size() != f.statSize {
			if fh, err := os.Open(f.path); err == nil {
				readInfo, statErr := fh.Stat()
				body, readErr := io.ReadAll(fh)
				_ = fh.Close()
				if statErr == nil && readErr == nil {
					f.reads++
					f.statMod, f.statSize, f.hasStat = readInfo.ModTime(), readInfo.Size(), true
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
							f.setFromConfigLocked(&file)
							f.sum, f.hasSum, f.hasBadSum = sum, true, false
							d.syncConfigFromAgentFileLocked(f)
							now = followedConfig{agents: f.agents, isolateHome: f.isolateHome, shell: f.shell, servers: f.servers, workspace: f.workspace, decision: f.decision}
							reloaded = true
							d.log.Info("configuration reloaded", "path", f.path)
						}
					}
				} else {
					f.hasStat = false
				}
			} else {
				f.hasStat = false
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
	if d.syncsAgents() {
		d.captureLocalAgentConfig()
	}
	if d.syncsPlugins() {
		d.captureLocalPluginConfig()
	}
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
		allowAgent := a.AgentAllowed()
		allowTemplate := a.TemplateAllowed()
		info := &protocol.AgentInfo{
			Name:          name,
			Profiles:      a.ProfileNames(),
			Yolo:          a.Yolo,
			AllowAgent:    &allowAgent,
			AllowTemplate: &allowTemplate,
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
