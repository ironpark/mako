package gui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/p2p"
)

// hostDraft is the settings page's copy of this node's daemon configuration
// (config.json). Lists are edited as text, one entry per line, so the struct
// stays comparable and a field edit is one signal write. The agents have a
// draft of their own, agentDraft rows.
type hostDraft struct {
	Name, Listen, PSK   string
	MDNS, Tailscale     bool
	TailnetHosts, Peers string // one per line
	P2P, Relay          bool
	Relays              string // one per line
}

func draftFrom(cfg *config.Config) hostDraft {
	return hostDraft{
		Name: cfg.Name, Listen: cfg.Listen, PSK: cfg.PSK,
		MDNS: cfg.Discovery.MDNS, Tailscale: cfg.Discovery.Tailscale,
		TailnetHosts: strings.Join(cfg.Discovery.TailnetHosts, "\n"),
		Peers:        strings.Join(cfg.Peers, "\n"),
		P2P:          !cfg.P2P.Disabled,
		Relay:        cfg.P2P.Relay,
		Relays:       strings.Join(cfg.P2P.Relays, "\n"),
	}
}

// apply returns cfg with the draft written over it, or the first line the
// draft cannot be read from.
func (d hostDraft) apply(cfg config.Config) (config.Config, error) {
	if strings.TrimSpace(d.Name) == "" {
		return cfg, errors.New(T("the node needs a name"))
	}
	if strings.TrimSpace(d.Listen) == "" {
		return cfg, fmt.Errorf(T("the node needs a listen address, such as :%d"), config.DefaultPort)
	}
	cfg.Name = strings.TrimSpace(d.Name)
	cfg.Listen = strings.TrimSpace(d.Listen)
	cfg.PSK = d.PSK
	cfg.Discovery.MDNS = d.MDNS
	cfg.Discovery.Tailscale = d.Tailscale
	cfg.Discovery.TailnetHosts = lines(d.TailnetHosts)
	cfg.Peers = lines(d.Peers)
	cfg.P2P.Disabled, cfg.P2P.Relay = !d.P2P, d.Relay
	cfg.P2P.Relays = lines(d.Relays)
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
// which; the GUI's own link picks up a new key now.
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
	keyChanged := next.PSK != m.cfg.PSK
	agentsOnly := ggui.Peek(m.Host) == draftFrom(m.cfg)
	*m.cfg = next
	m.Agents.Set(agentspec.Targets(next.Agents))
	m.AgentDraft.Set(draftAgents(next.Agents))
	if agentsOnly {
		m.HostNote.Set(T("Saved. Sessions started from now on use it."))
		return
	}
	if ggui.Peek(m.DaemonState).Running {
		// The app's daemon still has the old key until it restarts, which
		// is when the window changes to the new one.
		m.HostNote.Set(T("Saved. Restart the app's daemon to use it."))
		return
	}
	if keyChanged {
		m.reconnect(ggui.Peek(m.Prefs).Daemon, next.PSK)
	}
	m.HostNote.Set(T("Saved. Restart makod for the daemon to use it."))
}

// revertHost drops the draft's edits.
func (m *model) revertHost() {
	m.Host.Set(draftFrom(m.cfg))
	m.AgentDraft.Set(draftAgents(m.cfg.Agents))
	m.HostNote.Set("")
}
