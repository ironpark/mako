package uiblock

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// The bounds of an event from a front-end. Its values are as many as a
// tree has nodes at most, a front-end sending every field of the tree it
// has, so they are bounded by their bytes.
const (
	MaxEventValue = 8 << 10  // the value a change carries
	maxValuesSize = 32 << 10 // the block's values an event carries, ids and values
)

// Message is what a node sends for an event: the message its tree gave
// it, [name, ...arguments], and for a change the value last.
type Message struct {
	Name string
	// Given is the node's message as the tree gave it.
	Given json.RawMessage
	// Args are its arguments, then a change's value: a toggle's as a
	// boolean, any other's as text.
	Args []json.RawMessage
	// Kind is the node's kind.
	Kind string
}

// Message is the message the node of an event sends, refusing an event of
// a node the tree does not hold, or one that sends nothing for it.
func (t *Tree) Message(ev *protocol.BlockEvent) (Message, error) {
	if len(ev.GetValue()) > MaxEventValue {
		return Message{}, fmt.Errorf("the event's value is over %d KiB", MaxEventValue>>10)
	}
	var a actions
	var kind string
	found := false
	if t != nil {
		a, found = t.actions[ev.GetNode()]
		if n := nodeOf(t.Root, ev.GetNode()); n != nil {
			kind = n.GetKind()
		}
	}
	var raw json.RawMessage
	switch ev.GetKind() {
	case protocol.UiTap:
		raw = a.tap
	case protocol.UiChange:
		raw = a.change
	case protocol.UiSubmit:
		raw = a.submit
		if raw == nil {
			raw = a.change
		}
	case protocol.UiDrop:
		if ev.GetValue() == "" || len(ev.GetValue()) > maxDrag || strings.ContainsFunc(ev.GetValue(), Spoofing) {
			return Message{}, fmt.Errorf("invalid drop payload for %q of %s", ev.GetNode(), ev.GetBlock())
		}
		raw = a.drop
	default:
		return Message{}, fmt.Errorf("%q is not a kind of event", ev.GetKind())
	}
	if !found || raw == nil {
		return Message{}, fmt.Errorf("node %q of %s sends no message on %s", ev.GetNode(), ev.GetBlock(), ev.GetKind())
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return Message{}, errors.New("the node's message does not read")
	}
	m := Message{Given: raw, Args: parts[1:], Kind: kind}
	_ = json.Unmarshal(parts[0], &m.Name)
	if ev.GetKind() != protocol.UiTap {
		var value any = ev.GetValue()
		if kind == KindToggle {
			value = ev.GetValue() == "true"
		}
		b, _ := json.Marshal(value)
		m.Args = append(m.Args, b)
	}
	return m, nil
}

// Values are the values of an event's block, as the front-end has them,
// kept to the tree's inputs, toggles and selects, by node id: what a
// form's submit carries. Too many, more than a tree holds nodes, or too
// much is refused, as is a value of a node of another kind. A value of a
// node the tree no longer holds is left out: the front-end's tree may be
// a render behind the daemon's, and the rest of the form is still the
// user's.
func (t *Tree) Values(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxNodes {
		return nil, fmt.Errorf("an event carries at most %d values", maxNodes)
	}
	size := 0
	for id, v := range in {
		size += len(id) + len(v)
		if len(v) > MaxEventValue || size > maxValuesSize {
			return nil, fmt.Errorf("an event's values are at most %d KiB", maxValuesSize>>10)
		}
	}
	kinds := map[string]string{}
	var walk func(n *protocol.UiNode)
	walk = func(n *protocol.UiNode) {
		if n == nil {
			return
		}
		kinds[n.GetId()] = n.GetKind()
		for _, c := range n.GetChildren() {
			walk(c)
		}
	}
	if t != nil {
		walk(t.Root)
	}
	out := make(map[string]string, len(in))
	for id, v := range in {
		switch kind, ok := kinds[id]; {
		case !ok:
			continue
		case kind != KindInput && kind != KindTextarea && kind != KindToggle && kind != KindSelect && kind != KindTabs:
			return nil, fmt.Errorf("node %q is no input, textarea, toggle, select or tabs of the block", id)
		}
		out[id] = v
	}
	return out, nil
}

// nodeOf is the node of id in a tree, nil for none.
func nodeOf(n *protocol.UiNode, id string) *protocol.UiNode {
	if n.GetId() == id {
		return n
	}
	for _, c := range n.GetChildren() {
		if m := nodeOf(c, id); m != nil {
			return m
		}
	}
	return nil
}
