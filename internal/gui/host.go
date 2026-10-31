package gui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/p2p"
)

// hostDraft is the settings page's copy of this node's daemon configuration
// (config.json). Lists are edited as text, one entry per line, so the struct
// stays comparable and a field edit is one signal write. The agents have a
// draft of their own, agentDraft rows.
type hostDraft struct {
	Name, Listen, SSHKey string
	MDNS, Tailscale      bool
	TailnetHosts, Peers  string // one per line
	P2P, Relay           bool
	Relays               string // one per line
	// The shell's program and flags as command lines, and its environment
	// as NAME=value lines.
	ShellCommand, ShellArgs, ShellEnv string
}

func draftFrom(cfg *config.Config) hostDraft {
	return hostDraft{
		Name: cfg.Name, Listen: cfg.P2P.Listen, SSHKey: cfg.SSHKey,
		MDNS: cfg.Discovery.MDNS, Tailscale: cfg.Discovery.Tailscale,
		TailnetHosts: strings.Join(cfg.Discovery.TailnetHosts, "\n"),
		Peers:        strings.Join(cfg.Peers, "\n"),
		P2P:          !cfg.P2P.Disabled,
		Relay:        cfg.P2P.Relay,
		Relays:       strings.Join(cfg.P2P.Relays, "\n"),
		ShellCommand: agentspec.JoinArgs(cfg.Shell.Command),
		ShellArgs:    agentspec.JoinArgs(cfg.Shell.Args),
		ShellEnv:     envText(cfg.Shell.Env),
	}
}

// live is the draft without what the daemon follows as config.json
// changes, the shell, as the rest is read when it starts.
func (d hostDraft) live() hostDraft {
	d.ShellCommand, d.ShellArgs, d.ShellEnv = "", "", ""
	return d
}

// apply returns cfg with the draft written over it, or the first line the
// draft cannot be read from.
func (d hostDraft) apply(cfg config.Config) (config.Config, error) {
	if strings.TrimSpace(d.Name) == "" {
		return cfg, errors.New(T("the node needs a name"))
	}
	if _, _, err := (config.P2P{Listen: strings.TrimSpace(d.Listen)}).HostPort(); err != nil {
		return cfg, fmt.Errorf(T("the listen address is a host and a port, such as :%d"), config.DefaultP2PPort)
	}
	cfg.Name = strings.TrimSpace(d.Name)
	cfg.P2P.Listen = strings.TrimSpace(d.Listen)
	cfg.SSHKey = strings.TrimSpace(d.SSHKey)
	cfg.Discovery.MDNS = d.MDNS
	cfg.Discovery.Tailscale = d.Tailscale
	cfg.Discovery.TailnetHosts = lines(d.TailnetHosts)
	cfg.Peers = lines(d.Peers)
	cfg.P2P.Disabled, cfg.P2P.Relay = !d.P2P, d.Relay
	cfg.P2P.Relays = lines(d.Relays)
	var err error
	if cfg.Shell.Command, err = agentspec.SplitArgs(d.ShellCommand); err != nil {
		return cfg, fmt.Errorf(T("the shell's program: %v"), err)
	}
	if cfg.Shell.Args, err = agentspec.SplitArgs(d.ShellArgs); err != nil {
		return cfg, fmt.Errorf(T("the shell's flags: %v"), err)
	}
	if cfg.Shell.Env, err = parseEnv(d.ShellEnv); err != nil {
		return cfg, fmt.Errorf(T("the shell's environment: %v"), err)
	}
	for _, r := range cfg.P2P.Relays {
		if _, err := p2p.ParseRelays([]string{r}); err != nil {
			return cfg, fmt.Errorf(T("a relay is a multiaddr ending in /p2p/ and its ID: %s"), r)
		}
	}
	return cfg, nil
}

// lines splits text into its non-blank, trimmed lines.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// saveHost writes the drafts to config.json. The daemon follows the agents
// in it as they change but reads the rest when it starts, so the note says
// which.
func (m *model) saveHost() {
	next, err := ggui.Peek(m.Host).apply(*m.cfg)
	if err != nil {
		m.HostNote.Set(err.Error())
		return
	}
	if next.Agents, err = applyAgents(ggui.Peek(m.AgentDraft)); err != nil {
		m.HostNote.Set(err.Error())
		return
	}
	if err := config.Save(&next); err != nil {
		m.HostNote.Set(Tf("Could not save: %v", err))
		return
	}
	agentsOnly := ggui.Peek(m.Host).live() == draftFrom(m.cfg).live()
	*m.cfg = next
	m.Agents.Set(agentspec.Targets(next.Agents))
	m.AgentDraft.Set(draftAgents(next.Agents))
	if agentsOnly {
		m.HostNote.Set(T("Saved. Sessions started from now on use it."))
		return
	}
	if ggui.Peek(m.DaemonState).Running {
		m.HostNote.Set(T("Saved. Restart the app's daemon to use it."))
		return
	}
	m.HostNote.Set(T("Saved. Restart makod for the daemon to use it."))
}

// revertHost drops the draft's edits.
func (m *model) revertHost() {
	m.Host.Set(draftFrom(m.cfg))
	m.AgentDraft.Set(draftAgents(m.cfg.Agents))
	m.HostNote.Set("")
}

// shellSettings is how this node starts its shell, which is not an agent:
// a session of it is a terminal of the user's own.
func shellSettings(m *model) ggui.Widget {
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont()}
	return page(T("Shell"), T("The shell a new terminal starts on this node. Changes apply to the next shell started."),
		section(T("Program"), card(
			row(T("Program"), Tf("The shell and any arguments it always needs; empty runs your login shell, %s.", agentspec.DefaultShell()),
				ggui.Box(ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.ShellCommand })).
					Style(mono).Placeholder(agentspec.DefaultShell()).Name(T("Shell program"))).Width(300)),
			row(T("Flags"), T("Added to every shell started, such as -l for a login shell. Quote as in a shell."),
				ggui.Box(ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.ShellArgs })).
					Style(mono).Placeholder("-l").Name(T("Shell flags"))).Width(300)),
			stackedRow(T("Environment"), T("One per line as NAME=value, added to the daemon's. $NAME, ${NAME} and a leading ~ expand when a shell starts; $$ is a $."),
				ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.ShellEnv })).
					Multiline().Lines(3).Style(mono).Name(T("Shell environment"))),
		)),
		saveBar(m),
	)
}
