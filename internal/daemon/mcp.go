package daemon

import (
	"context"
	"log/slog"
	"time"

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
	d.mcp.Resolve(ctx, d.contextServers())
}

// withContextServers gives an agent that takes MCP servers those this node
// has, or those its profile picks, returning the command to start and what to clean up after the
// session. Any other command comes back as it is.
func (d *Daemon) withContextServers(ctx context.Context, launch agentspec.Launch, session string) ([]string, func()) {
	command := launch.Command
	if d.mcp == nil || !mcp.Takes(command) {
		return command, func() {}
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	var servers []mcp.Launch
	if launch.ContextServers != nil { // the profile picks them
		servers = d.mcp.ResolveOnly(ctx, d.contextServers(), *launch.ContextServers)
	} else {
		servers = d.mcp.Resolve(ctx, d.contextServers())
	}
	if len(servers) == 0 {
		return command, func() {}
	}
	run, err := config.RunDir(d.dir)
	if err != nil {
		d.log.Warn("mcp: no directory for the agent's configuration", "err", err)
		return command, func() {}
	}
	out, cleanup, err := mcp.Apply(command, servers, run, session)
	if err != nil {
		d.log.Warn("mcp: servers not given to the session", "session", session, "err", err)
		return command, func() {}
	}
	ids := make([]string, len(servers))
	for i, s := range servers {
		ids[i] = s.ID
	}
	d.log.Info("mcp: servers given to the session", "session", session, "servers", ids)
	return out, cleanup
}
