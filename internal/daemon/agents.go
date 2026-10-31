package daemon

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
)

// agentFile follows the agents and MCP servers of the config file the
// daemon was started from, so an agent, a flag, a profile or a server
// edited there applies to the next session without a restart. Everything
// else in the file is read at start.
type agentFile struct {
	mu      sync.Mutex
	path    string
	mod     time.Time
	size    int64
	read    bool
	agents  map[string]agentspec.Agent
	shell   agentspec.Shell
	servers map[string]mcp.Server
}

// FollowAgents makes the daemon read its agents from the config file at
// path whenever it changes, starting from the configuration it was given.
func (d *Daemon) FollowAgents(path string) {
	d.agentFile.mu.Lock()
	defer d.agentFile.mu.Unlock()
	d.agentFile.path = path
	if fi, err := os.Stat(path); err == nil {
		d.agentFile.mod, d.agentFile.size = fi.ModTime(), fi.Size()
	}
}

// agents are what this daemon can launch now, the shell among them.
func (d *Daemon) agents() map[string]agentspec.Agent {
	f := d.followed()
	return agentspec.WithShell(f.agents, f.shell)
}

// contextServers are the MCP servers configured now.
func (d *Daemon) contextServers() map[string]mcp.Server {
	return d.followed().servers
}

// followedConfig is what the daemon follows of its config file.
type followedConfig struct {
	agents  map[string]agentspec.Agent
	shell   agentspec.Shell
	servers map[string]mcp.Server
}

func (d *Daemon) followed() followedConfig {
	f := &d.agentFile
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.read {
		f.agents, f.shell, f.servers, f.read = d.cfg.Agents, d.cfg.Shell, d.cfg.ContextServers, true
	}
	now := followedConfig{agents: f.agents, shell: f.shell, servers: f.servers}
	if f.path == "" {
		return now
	}
	fi, err := os.Stat(f.path)
	if err != nil || (fi.ModTime().Equal(f.mod) && fi.Size() == f.size) {
		return now
	}
	f.mod, f.size = fi.ModTime(), fi.Size()
	body, err := os.ReadFile(f.path)
	if err != nil {
		return now
	}
	var file config.Config
	if err := json.Unmarshal(body, &file); err != nil {
		d.log.Warn("config changed but cannot be read; keeping the agents", "path", f.path, "err", err)
		return now
	}
	file.TakeShell()
	f.agents, f.shell, f.servers = file.Agents, file.Shell, file.ContextServers
	d.log.Info("agents reloaded", "path", f.path)
	return followedConfig{agents: f.agents, shell: f.shell, servers: f.servers}
}

// agentInfo lists the agents for this node's record: names and what their
// drivers say of them, since flags and environment, keys among them, stay
// here.
func agentInfo(configured map[string]agentspec.Agent) []*protocol.AgentInfo {
	names := agentspec.Offered(configured)
	out := make([]*protocol.AgentInfo, len(names))
	for i, name := range names {
		a, _ := agentspec.Lookup(name, configured)
		out[i] = &protocol.AgentInfo{Name: name, Profiles: a.ProfileNames()}
		if e, ok := agents.Lookup(name); ok {
			c := e.Info.Color
			out[i].Icon, out[i].Caps, out[i].Color = e.Info.Icon, e.Caps, protocol.RGBA(c.R, c.G, c.B, c.A)
		}
	}
	return out
}
