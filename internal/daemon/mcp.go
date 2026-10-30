package daemon

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
)

// resolveTimeout bounds how long a session's start waits on the MCP
// servers, the first time an extension installs one; a server not ready
// by then is left out of that session.
const resolveTimeout = 45 * time.Second

func newResolver(log *slog.Logger) *mcp.Resolver {
	ext, err := config.ExtensionsDir()
	if err != nil {
		return nil
	}
	work, err := config.ExtensionsWorkDir()
	if err != nil {
		return nil
	}
	return &mcp.Resolver{Extensions: ext, Work: work, Log: log}
}

// warmContextServers resolves the servers once at start, so the first
// session need not wait for an extension to install its server.
func (d *Daemon) warmContextServers(ctx context.Context) {
	if d.mcp == nil {
		return
	}
	d.mcp.Resolve(ctx, d.externalContextServers())
}

func (d *Daemon) externalContextServers() map[string]mcp.Server {
	servers := maps.Clone(d.contextServers())
	delete(servers, agentMCPName)
	return servers
}

// withContextServers gives an agent that takes MCP servers those this node
// has, or those its profile picks, in its launch, and returns what to clean
// up after the session. Any other launch stays as it is.
func (d *Daemon) withContextServers(ctx context.Context, launch *agentspec.Launch, session, parent string) func() {
	var takes agent.Servers
	if len(launch.Command) > 0 {
		driver, _ := agents.ByProgram(launch.Command[0])
		takes, _ = driver.Driver.(agent.Servers)
	}
	if takes == nil {
		return func() {}
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	var servers []mcp.Launch
	if d.mcp != nil {
		if launch.ContextServers != nil { // the profile picks them
			names := slices.DeleteFunc(slices.Clone(*launch.ContextServers), func(name string) bool { return name == agentMCPName })
			servers = d.mcp.ResolveOnly(ctx, d.externalContextServers(), names)
		} else {
			servers = d.mcp.Resolve(ctx, d.externalContextServers())
		}
	}
	release := func() {}
	if d.projectAgents.socket != "" && d.agentMCPEnabled(launch.ContextServers) {
		var builtin agent.Server
		builtin, release = d.projectAgents.server(d.ctx, d.hookBinary, session, parent)
		servers = append(servers, builtin)
	}
	if len(servers) == 0 {
		return func() {}
	}
	run, err := config.RunDir(d.dir)
	if err != nil {
		release()
		d.log.Warn("mcp: no directory for the agent's configuration", "err", err)
		return func() {}
	}
	cleanup, err := takes.WithServers(&launch.Launch, servers, run, session)
	if err != nil {
		release()
		d.log.Warn("mcp: servers not given to the session", "session", session, "err", err)
		return func() {}
	}
	ids := make([]string, len(servers))
	for i, s := range servers {
		ids[i] = s.ID
	}
	d.log.Info("mcp: servers given to the session", "session", session, "servers", ids)
	return func() { release(); cleanup() }
}

func (d *Daemon) agentMCPEnabled(picks *[]string) bool {
	if picks != nil {
		return slices.Contains(*picks, agentMCPName)
	}
	return d.contextServers()[agentMCPName].On()
}
