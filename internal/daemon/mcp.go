package daemon

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agent/agy"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
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

func (d *Daemon) resolveExternalServers(ctx context.Context, picks *[]string) []mcp.Launch {
	if d.mcp == nil {
		return nil
	}
	if picks != nil { // the profile picks them
		names := slices.DeleteFunc(slices.Clone(*picks), func(name string) bool { return name == agentMCPName })
		return d.mcp.ResolveOnly(ctx, d.externalContextServers(), names)
	}
	return d.mcp.Resolve(ctx, d.externalContextServers())
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
	servers := d.resolveExternalServers(ctx, launch.ContextServers)
	release := func() {}
	var builtin agent.Server
	hasAgentMCP := d.projectAgents.url != "" || d.projectAgents.socket != ""
	if hasAgentMCP {
		builtin, release = d.projectAgents.server(d.ctx, d.hookBinary, session, parent)
		if d.agentMCPEnabled(launch.ContextServers) {
			servers = append(servers, builtin)
			// Expose session messaging token and CLI socket so agents (such as
			// Claude Code via background Bash) can use `mako-msg wait` directly.
			if builtin.BearerToken != "" {
				launch.SetEnv(agentMCPTokenEnv, builtin.BearerToken)
			}
			if d.projectAgents.cliSocket != "" {
				launch.SetEnv("AGENT_MAKO_SOCKET", d.projectAgents.cliSocket)
			}
			if _, isAgy := takes.(*agy.Agent); isAgy && d.hookBinary != "" && d.projectAgents.cliSocket != "" {
				_ = agy.EnsureSidecar(launch.Env, d.hookBinary, []string{agySidecarArg, d.projectAgents.cliSocket})
			}
		}
	}
	if len(servers) == 0 {
		return release
	}
	run, err := config.RunDir(d.dir)
	if err != nil {
		release()
		d.log.Warn("mcp: no directory for the agent's configuration", "err", err)
		return func() {}
	}
	cmd0 := launch.Command[0]
	launchEnv := maps.Clone(launch.Env)
	cleanup, err := takes.WithServers(&launch.Launch, servers, run, session)
	if err != nil {
		release()
		d.log.Warn("mcp: servers not given to the session", "session", session, "err", err)
		return func() {}
	}
	if agent.HasLoopbackURL(servers) {
		agent.EnsureLocalProxyBypass(&launch.Launch)
	}
	if hasAgentMCP {
		d.projectAgents.setRefresh(session, func(rctx context.Context, info *protocol.SessionInfo) {
			a, _ := agentspec.Lookup(info.GetAgent(), d.agents())
			picks := profileContextServers(a, info.GetProfile())
			next := d.resolveExternalServers(rctx, picks)
			if d.sessionAgentMCPEnabled(info) {
				next = append(next, builtin)
			}
			_, _ = takes.WithServers(&agent.Launch{Command: []string{cmd0}, Env: maps.Clone(launchEnv)}, next, run, session)
		})
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
