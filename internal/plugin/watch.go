package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// A front-end watching a block gets its tree whole, then the changes from
// one render to the next, no more often than Limits.Updates: a burst of
// renders in between goes as one change. A block is shown, and its plugin
// running, while a stream watches it.

// maxEventValue bounds the value an event carries.
const maxEventValue = 8 << 10

// blockView is what a stream sends of a block.
type blockView struct {
	tree                *protocol.UiNode
	plugin, title       string
	state, err          string
	stale               bool
	changed             <-chan struct{}
	gone                bool
	declared, hasPlugin bool
}

func (p *plugin) view(block string) blockView {
	g := p.m.grant.get(p.id)
	p.mu.Lock()
	defer p.mu.Unlock()
	state, why := p.stateLocked(g)
	v := blockView{plugin: p.id, state: state, err: why, stale: p.stale, changed: p.changed, hasPlugin: true}
	if p.stale && v.err == "" {
		v.err = p.loadErr
	}
	select {
	case <-p.done:
		v.gone = true
	default:
	}
	if p.v != nil {
		for _, b := range p.v.man.Blocks {
			if b.ID == block {
				v.title, v.declared = b.Title, true
			}
		}
	}
	if in, ok := p.inst[block]; ok {
		if in.tree != nil {
			v.tree = in.tree.root
		}
		if v.err == "" {
			v.err = in.err
		}
	}
	if !v.declared && v.err == "" {
		v.err = fmt.Sprintf("the plugin declares no block %s", block)
	}
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

// Watch follows the block of the name, calling send with each update,
// until ctx ends, send fails, or the plugin is removed.
func (m *Manager) Watch(ctx context.Context, name string, send func(*protocol.BlockUpdate) error) error {
	id, block, ok := splitBlock(name)
	if !ok {
		return fmt.Errorf("%q is not a plugin's block", name)
	}
	p, ok := m.plugin(id)
	if !ok {
		return fmt.Errorf("no plugin %s on this node", id)
	}
	p.subscribe(block)
	defer p.unsubscribe(block)
	if !p.post(func() { p.show(block) }) {
		return errors.New("the plugin is busy")
	}
	var last blockView
	var seq uint64
	var sent time.Time
	for {
		v := p.view(block)
		upd := &protocol.BlockUpdate{Block: name, Plugin: v.plugin, Title: v.title, State: v.state, Error: v.err, Stale: v.stale}
		switch {
		case seq == 0 || last.tree == nil && v.tree != nil:
			upd.Tree = v.tree
		case v.tree != nil && v.tree != last.tree:
			upd.Ops = protocol.DiffUi(last.tree, v.tree)
		}
		same := seq > 0 && upd.Tree == nil && len(upd.Ops) == 0 &&
			last.title == v.title && last.state == v.state && last.err == v.err && last.stale == v.stale
		if !same {
			seq++
			upd.Seq = seq
			if err := send(upd); err != nil {
				return err
			}
			last, sent = v, time.Now()
		}
		if v.gone {
			return errGone
		}
		select {
		case <-v.changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		if wait := m.lim.Updates - time.Since(sent); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
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
		return nil, fmt.Errorf("no plugin %s on this node", id)
	}
	if len(ev.GetValue()) > maxEventValue {
		return nil, fmt.Errorf("the event's value is over %d KiB", maxEventValue>>10)
	}
	if !p.evRate.take() {
		return nil, errors.New("events come faster than the plugin takes them; this one was dropped")
	}
	name, args, err := p.message(block, ev)
	if err != nil {
		return nil, err
	}
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

// message is the message a node sends for an event, as the block's last
// render gave it: its name, and its arguments as JSON.
func (p *plugin) message(block string, ev *protocol.BlockEvent) (string, string, error) {
	p.mu.Lock()
	in, ok := p.inst[block]
	var a actions
	var kind string
	found := false
	if ok && in.tree != nil {
		a, found = in.tree.actions[ev.GetNode()]
		if n := nodeUi(in.tree.root, ev.GetNode()); n != nil {
			kind = n.GetKind()
		}
	}
	p.mu.Unlock()
	var raw json.RawMessage
	switch ev.GetKind() {
	case protocol.UiTap:
		raw = a.tap
	case protocol.UiChange:
		raw = a.change
	case protocol.UiSubmit:
		raw = cmpRaw(a.submit, a.change)
	default:
		return "", "", fmt.Errorf("%q is not a kind of event", ev.GetKind())
	}
	if !found || raw == nil {
		return "", "", fmt.Errorf("node %q of %s sends no message on %s", ev.GetNode(), block, ev.GetKind())
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return "", "", errors.New("the node's message does not read")
	}
	var name string
	_ = json.Unmarshal(parts[0], &name)
	args := parts[1:]
	if ev.GetKind() != protocol.UiTap {
		var value any = ev.GetValue()
		if kind == KindToggle {
			value = ev.GetValue() == "true"
		}
		b, _ := json.Marshal(value)
		args = append(args, b)
	}
	b, _ := json.Marshal(args)
	return name, string(b), nil
}

func cmpRaw(a, b json.RawMessage) json.RawMessage {
	if a != nil {
		return a
	}
	return b
}

// nodeUi is the node of id in a tree, nil for none.
func nodeUi(n *protocol.UiNode, id string) *protocol.UiNode {
	if n.GetId() == id {
		return n
	}
	for _, c := range n.GetChildren() {
		if m := nodeUi(c, id); m != nil {
			return m
		}
	}
	return nil
}
