package daemon

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/protocol"
)

// agentFile follows the agents of the config file the daemon was started
// from, so an agent, a flag or a profile edited there applies to the next
// session without a restart. Everything else in the file is read at start.
type agentFile struct {
	mu     sync.Mutex
	path   string
	mod    time.Time
	size   int64
	agents map[string]agentspec.Agent
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

// agents are what this daemon can launch now.
func (d *Daemon) agents() map[string]agentspec.Agent {
	f := &d.agentFile
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agents == nil {
		f.agents = d.cfg.Agents
	}
	if f.path == "" {
		return f.agents
	}
	fi, err := os.Stat(f.path)
	if err != nil || (fi.ModTime().Equal(f.mod) && fi.Size() == f.size) {
		return f.agents
	}
	f.mod, f.size = fi.ModTime(), fi.Size()
	body, err := os.ReadFile(f.path)
	if err != nil {
		return f.agents
	}
	var file struct {
		Agents map[string]agentspec.Agent `json:"agents"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		d.log.Warn("config changed but cannot be read; keeping the agents", "path", f.path, "err", err)
		return f.agents
	}
	f.agents = file.Agents
	d.log.Info("agents reloaded", "path", f.path)
	return f.agents
}

// agentInfo lists the agents for this node's record: names only, since
// flags and environment, keys among them, stay here.
func agentInfo(agents map[string]agentspec.Agent) []protocol.AgentInfo {
	names := agentspec.Names(agents)
	out := make([]protocol.AgentInfo, len(names))
	for i, name := range names {
		a, _ := agentspec.Lookup(name, agents)
		out[i] = protocol.AgentInfo{Name: name, Profiles: a.ProfileNames()}
	}
	return out
}
