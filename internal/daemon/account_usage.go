package daemon

import (
	"context"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/internal/usage"
)

var accountUsageAgents = []string{"claude", "codex", "agy"}

func (d *Daemon) usageCollector() *usage.Collector {
	d.usageMu.Lock()
	defer d.usageMu.Unlock()
	if d.usage == nil {
		d.usage = usage.NewCollector()
	}
	return d.usage
}

func (d *Daemon) handleReadAccountUsage(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeAccountUsage, func(r *protocol.ReadAccountUsage) (*protocol.AccountUsage, error) {
		configured := d.agents()
		isolate := d.isolateHome()
		specs := make([]usage.AgentSpec, 0, len(accountUsageAgents))
		for _, name := range accountUsageAgents {
			callEnv := agentspec.IsolatedHomeEnvForAgent(name, isolate, configured)
			program := ""
			if a, ok := agentspec.Lookup(name, configured); ok && len(a.Command) > 0 {
				program = a.Command[0]
			}
			if program == "" {
				if e, ok := agents.Lookup(name); ok && len(e.Info.Programs) > 0 {
					if found, err := agent.Find(e.Info.Programs[0], e.Info.Dirs...); err == nil {
						program = found
					}
				}
			}
			specs = append(specs, usage.AgentSpec{
				Name:    name,
				Program: program,
				Env:     callEnv,
			})
		}
		return d.usageCollector().Collect(ctx, specs, r.GetRefresh()), nil
	})
}
