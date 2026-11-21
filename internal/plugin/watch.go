package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/uiblock"

	"github.com/ironpark/mako/internal/protocol"
)

// A front-end watching a block gets its tree whole, then the changes from
// one render to the next, no more often than Limits.Updates: a burst of
// renders in between goes as one change (uiblock.Follow). A block is
// shown, and its plugin running, while a stream watches it.

// view is a (block, scope) instance of the plugin's as a stream sends it.
func (p *plugin) view(key, block string) uiblock.View {
	g, st := p.m.grant.get(p.id), p.installStatus()
	p.mu.Lock()
	defer p.mu.Unlock()
	state, why := p.stateLocked(g, st)
	v := uiblock.View{Plugin: p.id, State: state, Err: why, Stale: p.stale, Changed: p.changed}
	if p.stale && v.Err == "" {
		v.Err = p.loadErr
	}
	select {
	case <-p.done:
		v.Gone = true
	default:
	}
	declared := false
	if p.v != nil {
		for _, b := range p.v.man.Blocks {
			if b.ID == block {
				v.Title, declared = b.Title, true
			}
		}
	}
	if in, ok := p.inst[key]; ok {
		if in.tree != nil {
			v.Tree = in.tree.Root
		}
		if v.Err == "" {
			v.Err = in.err
		}
	}
	if !declared && v.Err == "" {
		v.Err = fmt.Sprintf("the plugin declares no block %s", block)
	}
	// An error may quote what the plugin's code threw.
	v.Err = protocol.Printable(v.Err)
	return v
}

func (p *plugin) subscribe(block, scopeKey string) (string, []string) {
	p.mu.Lock()
	in, evicted := p.getOrCreateInstanceLocked(block, scopeKey)
	in.subs++
	key := in.key
	p.mu.Unlock()
	return key, evicted
}

func (p *plugin) unsubscribe(key string) {
	p.mu.Lock()
	var evicted []string
	if in, ok := p.inst[key]; ok {
		in.subs--
		in.lastUsed = time.Now()
		evicted = p.trimBlockInstancesLocked(in.id, "")
	}
	p.mu.Unlock()
	p.post(func() {
		p.lastUsed = time.Now()
		p.evictInstances(evicted)
	})
}

// noPlugin is the error of a block of a plugin the node does not have,
// coded so that a front-end says so rather than trying again.
func noPlugin(id string) error {
	return &protocol.CodedError{Code: protocol.CodeNoPlugin, Message: fmt.Sprintf("no plugin %s on this node", id)}
}

// Watch follows the block of the name in the global scope, calling send with
// each update, until ctx ends, send fails, or the plugin is removed.
func (m *Manager) Watch(ctx context.Context, name string, send func(*protocol.BlockUpdate) error) error {
	return m.WatchScoped(ctx, name, "", send)
}

// WatchScoped follows the block of the name in the canonical scope key ("" for
// global, "space:<id>", "project:<id>", or "session:<id>"), calling send with
// each update until ctx ends, send fails, or the plugin is removed.
func (m *Manager) WatchScoped(ctx context.Context, name, scope string, send func(*protocol.BlockUpdate) error) error {
	id, block, ok := splitBlock(name)
	if !ok {
		return fmt.Errorf("%q is not a plugin's block", name)
	}
	if _, _, err := parseScopeKey(scope); err != nil {
		return err
	}
	p, ok := m.plugin(id)
	if !ok {
		return noPlugin(id)
	}
	key, evicted := p.subscribe(block, scope)
	defer p.unsubscribe(key)
	if !p.post(func() {
		p.evictInstances(evicted)
		p.show(key)
	}) {
		return errors.New("the plugin is busy")
	}
	wrappedSend := send
	if scope != "" {
		wrappedSend = func(u *protocol.BlockUpdate) error {
			u.Scope = scope
			return send(u)
		}
	}
	return uiblock.Follow(ctx, name, m.lim.Updates, func() uiblock.View { return p.view(key, block) }, wrappedSend)
}

// Event is the user acting on a node of a block: the message the node's
// render gave for it goes to the block's on, with the value of a change
// last. Events from the front-ends beyond Limits.Events a second are
// refused, as are those of a node that sends no such message.
func (m *Manager) Event(ctx context.Context, ev *protocol.BlockEvent) (*protocol.PluginDone, error) {
	id, block, ok := splitBlock(ev.GetBlock())
	if !ok {
		return nil, fmt.Errorf("%q is not a plugin's block", ev.GetBlock())
	}
	scopeKey := ev.GetScope()
	if _, _, err := parseScopeKey(scopeKey); err != nil {
		return nil, err
	}
	p, ok := m.plugin(id)
	if !ok {
		return nil, noPlugin(id)
	}
	if !p.evRate.take() {
		return nil, errors.New("events come faster than the plugin takes them; this one was dropped")
	}
	key := instanceKey(block, scopeKey)
	p.mu.Lock()
	var tree *uiblock.Tree
	if in, ok := p.inst[key]; ok {
		in.lastUsed = time.Now()
		tree = in.tree
	}
	p.mu.Unlock()
	msg, err := tree.Message(ev)
	if err != nil {
		return nil, err
	}
	vals, err := tree.Values(ev.GetValues())
	if err != nil {
		return nil, err
	}
	// Expose validated form values by full node ID and by unique trailing key
	// so event handlers can read current input/textarea values on button clicks.
	valsMap := map[string]string{}
	keyCounts := map[string]int{}
	for id, v := range vals {
		valsMap[id] = v
		if idx := strings.LastIndex(id, "/"); idx >= 0 && idx+1 < len(id) {
			k := id[idx+1:]
			if !strings.HasPrefix(k, "#") {
				keyCounts[k]++
			}
		}
	}
	for id, v := range vals {
		if idx := strings.LastIndex(id, "/"); idx >= 0 && idx+1 < len(id) {
			k := id[idx+1:]
			if keyCounts[k] == 1 {
				valsMap[k] = v
			}
		}
	}
	valsJSON, _ := json.Marshal(valsMap)
	name := msg.Name
	argsJSON, _ := json.Marshal(msg.Args)
	args := string(argsJSON)
	ctxJSON := marshalScopeContext(m.resolveScope(scopeKey))
	var open, toast string
	err = p.do(ctx, func() error {
		if err := p.ensure(); err != nil {
			return err
		}
		_, err := p.invoke("event", true, block, scopeKey, ctxJSON, name, args, string(valsJSON))
		open, toast = p.openURL, p.toast
		p.openURL, p.toast = "", ""
		p.setErr(key, err)
		p.afterCall(scopeKey, key)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &protocol.PluginDone{OpenUrl: open, Toast: toast}, nil
}
