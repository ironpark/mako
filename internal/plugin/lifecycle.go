package plugin

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// A plugin's runtime comes and goes: read from its folder, checked,
// started when one of its blocks shows or a command runs, stopped once
// idle, or for going over a limit, or when its folder changes.

// reload reads the folder again. A version that does not read, or does
// not start, leaves the last that did running, the error shown beside it;
// with none, the plugin has failed.
func (p *plugin) reload() {
	if p.m.installs.Settle(p.id) {
		_ = p.disown()
	}
	next, err := readVersion(p.m.cfg.Dir, p.id)
	if err == nil {
		err = p.check(next)
	}
	p.mu.Lock()
	prev := p.v
	p.mu.Unlock()
	if err != nil {
		p.m.cfg.Log.Warn("plugin did not load", "plugin", p.id, "err", err)
		p.status(func() {
			p.loadErr, p.read = err.Error(), true
			p.stale = prev != nil
		})
		return
	}
	if prev != nil && next.sum == prev.sum {
		// The files were touched, or put back as they are running: there
		// is nothing to start again.
		p.status(func() { p.loadErr, p.stale = "", false })
		return
	}
	wasRunning := p.rt != nil
	p.sleep("its folder changed")
	p.status(func() {
		p.v, p.loadErr, p.stale, p.failErr, p.read = next, "", false, "", true
		p.syncInstancesLocked()
	})
	p.failures, p.renderFailures = 0, 0
	p.settings.migrateAll(next.man)
	p.m.cfg.Log.Info("plugin loaded", "plugin", p.id, "version", next.man.Version)
	if !wasRunning && !p.watched() {
		return
	}
	if state, _ := p.state(); state != protocol.PluginReady {
		// The newer version waits for the user to allow what it asks for
		// anew, or is refused, disabled or blocked: it does not start, and
		// the last does not run on in its place, which would run a version
		// the user did not allow as if it were this one.
		p.showAll()
		return
	}
	if err := p.ensure(); err != nil && prev != nil {
		// The newer version does not start: the last that did runs on.
		p.m.cfg.Log.Warn("plugin's new version did not start; the last runs on", "plugin", p.id, "err", err)
		p.status(func() {
			p.v, p.loadErr, p.stale, p.failErr = prev, err.Error(), true, ""
			p.syncInstancesLocked()
		})
		_ = p.ensure()
	}
	p.showAll()
}

// check compiles a version's modules in a runtime of their own, so that
// one that does not parse is refused before it replaces the one running.
func (p *plugin) check(v *version) error {
	b := newBudget(p.m.ctx, p.m.lim.CPU, p.m.lim.Wall)
	defer b.release()
	rt, err := startRuntime(b, v, p.m.lim, func(context.Context, string, []byte) ([]byte, error) {
		return nil, errors.New("the plugin is being checked")
	})
	if err != nil {
		return classify(b, err)
	}
	rt.close()
	return nil
}

// syncInstancesLocked keeps an instance for each block the version
// declares.
func (p *plugin) syncInstancesLocked() {
	for _, b := range p.v.man.Blocks {
		if _, ok := p.inst[b.ID]; !ok {
			p.inst[b.ID] = &instance{id: b.ID}
		}
	}
	for id, in := range p.inst {
		if !slices.ContainsFunc(p.v.man.Blocks, func(b BlockDecl) bool { return b.ID == id }) && in.subs == 0 {
			delete(p.inst, id)
		}
	}
}

// watched says a stream watches one of its blocks.
func (p *plugin) watched() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, in := range p.inst {
		if in.subs > 0 {
			return true
		}
	}
	return false
}

// ensure starts the runtime, if the plugin may run and it is not up. It
// is the gate every entry into the plugin's code passes, a runtime up or
// not: how the plugin stands is asked anew each time, so that a plugin
// blocked, refused or disabled since it started runs nothing more, and
// its runtime is closed (without the save that would run its code).
func (p *plugin) ensure() error {
	state, why := p.state()
	if p.rt != nil {
		if state == protocol.PluginReady || state == protocol.PluginRunning {
			return nil
		}
		p.closeRuntime()
		p.m.cfg.Log.Warn("plugin stopped", "plugin", p.id, "state", state, "why", why)
		p.status(func() { p.running = false })
	}
	switch state {
	case protocol.PluginReady:
	case protocol.PluginWaiting:
		return errors.New("the plugin waits for you to allow what it asks for")
	case protocol.PluginDenied:
		return errors.New("you refused what the plugin asks for")
	case protocol.PluginDisabled:
		return errors.New("the plugin is disabled")
	case protocol.PluginBlocked:
		return errors.New(why)
	default:
		return errors.New(cmpOr(why, "the plugin cannot run"))
	}
	v := p.current()
	b := newBudget(p.m.ctx, p.m.lim.CPU, p.m.lim.Wall)
	defer b.release()
	rt, err := startRuntime(b, v, p.m.lim, p.bridge)
	if err == nil {
		p.rt = rt
		_, err = rt.invoke(b, "activate")
	}
	if err = classify(b, err); err != nil {
		p.kill("activate: " + err.Error())
		return err
	}
	p.lastUsed = time.Now()
	p.mu.Lock()
	for _, in := range p.inst {
		in.loaded = false
	}
	p.mu.Unlock()
	for id, state := range p.saved {
		if _, err := p.invoke("restore", false, id, state); err != nil {
			break
		}
	}
	p.saved = map[string]string{}
	p.status(func() { p.running = true })
	p.m.cfg.Log.Info("plugin started", "plugin", p.id)
	return nil
}

// kill stops the runtime for a reason, which stands until the folder
// changes or the user enables the plugin again.
func (p *plugin) kill(why string) {
	p.closeRuntime()
	p.m.cfg.Log.Warn("plugin stopped", "plugin", p.id, "why", why)
	p.status(func() { p.failErr, p.running = why, false })
}

func (p *plugin) closeRuntime() {
	if p.rt != nil {
		p.rt.close()
		p.rt = nil
	}
}

// sleep stops a runtime that is up, for why, keeping its blocks' states
// for the next. A plugin blocked meanwhile is stopped without saving,
// which would run its code.
func (p *plugin) sleep(why string) {
	if p.rt == nil {
		return
	}
	if p.installStatus().Blocked != "" {
		p.closeRuntime()
		p.status(func() { p.running = false })
		p.m.cfg.Log.Warn("plugin stopped: blocked", "plugin", p.id, "why", why)
		return
	}
	p.mu.Lock()
	ids := make([]string, 0, len(p.inst))
	for id := range p.inst {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		if s, err := p.invoke("save", false, id); err == nil && len(s) <= maxSavedState && s != "null" {
			p.saved[id] = s
		}
		if p.rt == nil {
			return // saving went over a limit
		}
	}
	p.closeRuntime()
	p.status(func() { p.running = false })
	p.m.cfg.Log.Info("plugin stopped", "plugin", p.id, "why", why)
}
