package uiblock

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ironpark/mako/internal/protocol"
)

// The bounds of an event from a front-end.
const (
	MaxEventValue = 8 << 10  // the value a change carries
	maxValues     = 64       // the block's values an event carries
	maxValuesSize = 32 << 10 // all of them
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
// form's submit carries. A value of a node not of those kinds is refused,
// as are too many or too much.
func (t *Tree) Values(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxValues {
		return nil, fmt.Errorf("an event carries at most %d values", maxValues)
	}
	out := make(map[string]string, len(in))
	size := 0
	for id, v := range in {
		size += len(id) + len(v)
		if len(v) > MaxEventValue || size > maxValuesSize {
			return nil, fmt.Errorf("an event's values are at most %d KiB", maxValuesSize>>10)
		}
		n := nodeOf(t.Root, id)
		if n == nil || (n.GetKind() != KindInput && n.GetKind() != KindToggle && n.GetKind() != KindSelect) {
			return nil, fmt.Errorf("node %q is no input, toggle or select of the block", id)
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
