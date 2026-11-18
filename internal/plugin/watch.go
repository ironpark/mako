package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ironpark/mako/internal/uiblock"

	"github.com/ironpark/mako/internal/protocol"
)

// A front-end watching a block gets its tree whole, then the changes from
// one render to the next, no more often than Limits.Updates: a burst of
// renders in between goes as one change (uiblock.Follow). A block is
// shown, and its plugin running, while a stream watches it.

// view is a block of the plugin's as a stream sends it.
func (p *plugin) view(block string) uiblock.View {
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
	if in, ok := p.inst[block]; ok {
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

func (p *plugin) subscribe(block string) {
	p.mu.Lock()
	in, ok := p.inst[block]
	if !ok {
		in = &instance{id: block}
		p.inst[block] = in
	}
	in.subs++
	p.mu.Unlock()
}

func (p *plugin) unsubscribe(block string) {
	p.mu.Lock()
	if in, ok := p.inst[block]; ok {
		in.subs--
	}
	p.mu.Unlock()
	p.post(func() { p.lastUsed = time.Now() })
}

// noPlugin is the error of a block of a plugin the node does not have,
// coded so that a front-end says so rather than trying again.
func noPlugin(id string) error {
	return &protocol.CodedError{Code: protocol.CodeNoPlugin, Message: fmt.Sprintf("no plugin %s on this node", id)}
}

// Watch follows the block of the name, calling send with each update,
// until ctx ends, send fails, or the plugin is removed.
func (m *Manager) Watch(ctx context.Context, name string, send func(*protocol.BlockUpdate) error) error {
	id, block, ok := splitBlock(name)
	if !ok {
		return fmt.Errorf("%q is not a plugin's block", name)
	}
	p, ok := m.plugin(id)
	if !ok {
		return noPlugin(id)
	}
	p.subscribe(block)
	defer p.unsubscribe(block)
	if !p.post(func() { p.show(block) }) {
		return errors.New("the plugin is busy")
	}
	return uiblock.Follow(ctx, name, m.lim.Updates, func() uiblock.View { return p.view(block) }, send)
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
	p, ok := m.plugin(id)
	if !ok {
		return nil, noPlugin(id)
	}
	if !p.evRate.take() {
		return nil, errors.New("events come faster than the plugin takes them; this one was dropped")
	}
	p.mu.Lock()
	var tree *uiblock.Tree
	if in, ok := p.inst[block]; ok {
		tree = in.tree
	}
	p.mu.Unlock()
	msg, err := tree.Message(ev)
	if err != nil {
		return nil, err
	}
	// A plugin reads its fields from its own state, not from the
	// event's values, but they are bounded as an agent's block's are.
	if _, err := tree.Values(ev.GetValues()); err != nil {
		return nil, err
	}
	name := msg.Name
	argsJSON, _ := json.Marshal(msg.Args)
	args := string(argsJSON)
	var open string
	err = p.do(ctx, func() error {
		if err := p.ensure(); err != nil {
			return err
		}
		_, err := p.invoke("event", true, block, name, args)
		open = p.openURL
		p.openURL = ""
		p.setErr(block, err)
		p.afterCall()
		return err
	})
	if err != nil {
		return nil, err
	}
	return &protocol.PluginDone{OpenUrl: open}, nil
}
