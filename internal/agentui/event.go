package agentui

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ironpark/mako/internal/protocol"
)

// Event is the user acting on a block, as its agent is given it: one line
// of JSON, in a message the window sends (see the daemon's agentPrompt).
// Message is what the agent attached to the node, [name, ...args], as it
// gave it; Value a change's or submit's value; Values the block's inputs',
// toggles' and selects' values as the window had them, by node id, so
// that a form's submit carries every field. Whatever the user typed is in
// these strings, escaped, and so cannot read as a line of its own.
type Event struct {
	Block   string            `json:"block"`
	ID      string            `json:"id"`
	Node    string            `json:"node"`
	Kind    string            `json:"event"`
	Message json.RawMessage   `json:"message"`
	Value   *string           `json:"value,omitempty"`
	Values  map[string]string `json:"values,omitempty"`
}

// Event reads a front-end's event on a block: the session to tell, and
// what to tell it. An event of a node that sends nothing for it, of a
// block not shown, or beyond the block's rate, is refused.
func (s *Store) Event(ev *protocol.BlockEvent) (session, text string, err error) {
	if _, _, ok := split(ev.GetBlock()); !ok {
		return "", "", fmt.Errorf("%q is not an agent's block", ev.GetBlock())
	}
	s.mu.Lock()
	b, ok := s.blocks[ev.GetBlock()]
	if !ok {
		s.mu.Unlock()
		return "", "", fmt.Errorf("no block %s on this node", ev.GetBlock())
	}
	if !b.events.take(s.now()) {
		s.mu.Unlock()
		return "", "", errors.New("events come faster than the agent takes them; this one was dropped")
	}
	tree, name, id, session := b.tree, b.name, b.id, b.session
	s.mu.Unlock()
	msg, err := tree.Message(ev)
	if err != nil {
		return "", "", err
	}
	values, err := tree.Values(ev.GetValues())
	if err != nil {
		return "", "", err
	}
	out := Event{Block: name, ID: id, Node: ev.GetNode(), Kind: ev.GetKind(), Message: msg.Given, Values: values}
	if ev.GetKind() != protocol.UiTap {
		v := ev.GetValue()
		out.Value = &v
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", "", err
	}
	return session, string(data), nil
}
