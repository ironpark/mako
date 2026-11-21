package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ironpark/mako/internal/protocol"
)

// Act acts on a plugin for the user: allows or refuses what it asks for,
// enables or disables it, reads its folder again, runs a command of its,
// or sets a setting.
func (m *Manager) Act(ctx context.Context, a *protocol.PluginAction) (*protocol.PluginDone, error) {
	p, ok := m.plugin(a.GetPluginId())
	if !ok {
		return nil, fmt.Errorf("no plugin %s on this node", a.GetPluginId())
	}
	v := p.current()
	switch a.GetOp() {
	case protocol.PluginGrant:
		if v == nil {
			return nil, errors.New("the plugin's manifest has not been read")
		}
		perms := v.man.Permissions
		err := m.grant.update(p.id, func(e *grantEntry) {
			for _, perm := range perms {
				if !contains(e.Granted, perm) {
					e.Granted = append(e.Granted, perm)
				}
			}
			e.Denied = nil
		})
		if err != nil {
			return nil, err
		}
		m.cfg.Log.Info("plugin's permissions granted", "plugin", p.id, "permissions", perms)
		p.status(func() {})
		p.post(p.showAll)
	case protocol.PluginDeny:
		var perms []string
		if v != nil {
			perms = v.man.Permissions
		}
		if err := m.grant.update(p.id, func(e *grantEntry) { e.Granted, e.Denied, e.AgentsAllowed = nil, perms, false }); err != nil {
			return nil, err
		}
		p.dropAsk("the user refused the plugin's permissions")
		if m.cfg.Layout != nil {
			m.cfg.Layout.Forget(p.id)
		}
		m.cfg.Log.Info("plugin's permissions refused", "plugin", p.id)
		p.post(func() { p.closeRuntime(); p.status(func() { p.running = false }) })
		p.status(func() {})
	case protocol.PluginDisable, protocol.PluginEnable:
		off := a.GetOp() == protocol.PluginDisable
		if st := p.installStatus(); !off && st.Blocked != "" {
			version := ""
			if v != nil {
				version = v.man.Version
			}
			return nil, &protocol.CodedError{Code: protocol.CodePluginBlocked, Args: []string{p.id, version, st.Blocked},
				Message: fmt.Sprintf("the plugin cannot be enabled while it is blocked: %s", st.Blocked)}
		}
		if err := m.grant.update(p.id, func(e *grantEntry) { e.Disabled = off }); err != nil {
			return nil, err
		}
		if off {
			p.dropAsk("the plugin was disabled")
		}
		p.post(func() {
			p.failures, p.renderFailures = 0, 0
			if off {
				p.sleep("disabled")
			}
			p.status(func() {
				if !off {
					p.failErr = ""
				}
			})
			if !off {
				p.showAll()
			}
		})
	case protocol.PluginReload:
		p.post(p.reload)
	case protocol.PluginRun:
		if v == nil || !contains(commandIDs(v.man), a.GetCommand()) {
			return nil, fmt.Errorf("the plugin has no command %q", a.GetCommand())
		}
		var open, toast string
		err := p.do(ctx, func() error {
			if err := p.ensure(); err != nil {
				return err
			}
			_, err := p.invoke("command", true, a.GetCommand())
			open, toast = p.openURL, p.toast
			p.openURL, p.toast = "", ""
			p.afterCall()
			return err
		})
		if err != nil {
			return nil, err
		}
		return &protocol.PluginDone{OpenUrl: open, Toast: toast}, nil
	case protocol.PluginSet:
		if v == nil {
			return nil, errors.New("the plugin's manifest has not been read")
		}
		var d *SettingDecl
		for i := range v.man.Settings {
			if v.man.Settings[i].Key == a.GetKey() {
				d = &v.man.Settings[i]
			}
		}
		if d == nil {
			return nil, fmt.Errorf("the plugin declares no setting %q", a.GetKey())
		}
		if err := p.settings.set(*d, a.GetValue()); err != nil {
			return nil, err
		}
		p.status(func() {})
		// A block drawn from the settings is loaded again with them.
		p.post(func() {
			if p.rt == nil || p.ensure() != nil {
				return
			}
			p.mu.Lock()
			var ins []*instance
			for _, in := range p.inst {
				if in.loaded {
					ins = append(ins, in)
				}
			}
			p.mu.Unlock()
			for _, in := range ins {
				p.load(in)
			}
			p.renderAll()
		})
	case protocol.PluginAllowAgent, protocol.PluginDenyAgent:
		if err := p.answerAgent(a.GetKey(), a.GetOp(), a.GetValue()); err != nil {
			return nil, err
		}
	case protocol.PluginForgetAgents:
		if err := m.grant.update(p.id, func(e *grantEntry) { e.AgentsAllowed = false }); err != nil {
			return nil, err
		}
		p.status(func() {})
	default:
		return nil, fmt.Errorf("unknown op %q", a.GetOp())
	}
	return &protocol.PluginDone{}, nil
}

func commandIDs(m *Manifest) []string {
	out := make([]string, len(m.Commands))
	for i, c := range m.Commands {
		out[i] = c.ID
	}
	return out
}

func contains(list []string, s string) bool {
	return slices.Contains(list, s)
}
