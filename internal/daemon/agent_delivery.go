package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// A message for an agent is typed into its terminal as a prompt once it
// waits at its prompt; until then it stays in the inbox, which read_messages
// reads too.
const (
	// deliverySettle is how long an agent must have been at its prompt: a
	// spawned one is at it a moment before the task it was started with.
	deliverySettle = time.Second
	// deliveryQuiet is how long its terminal must have gone untouched, as
	// someone may be typing there.
	deliveryQuiet = 2 * time.Second
	// deliveryRetry is how long a delivered prompt may go without the
	// agent's hooks saying anything before the next is typed all the same.
	deliveryRetry = 30 * time.Second
	// deliveryBatch bounds the messages typed as one prompt, past the first.
	deliveryBatch = 64 << 10
	// deliveryPoll is how often an agent with mail is looked at.
	deliveryPoll = 500 * time.Millisecond
)

// deliverAgentMessages types peer's mail into its terminal for as long as
// it serves MCP.
func (d *Daemon) deliverAgentMessages(peer *agentPeer) {
	var delivered time.Time
	for {
		inbox, changed := d.projectAgents.messages.pending(peer.id)
		var poll <-chan time.Time
		if len(inbox) > 0 {
			if s, ok := d.readyForMessages(peer, delivered); ok {
				batch := deliveryOf(inbox)
				if err := s.Prompt(d.agentPrompt(batch)); err != nil {
					d.log.Warn("agent messages: not typed", "session", peer.id, "err", err)
				} else {
					delivered = time.Now()
					if err := d.projectAgents.messages.delivered(peer.id, batch[len(batch)-1].ID); err != nil {
						d.log.Warn("agent messages: delivery not recorded", "session", peer.id, "err", err)
					}
					continue
				}
			}
			poll = time.After(deliveryPoll)
		}
		select {
		case <-changed:
		case <-poll:
		case <-peer.ctx.Done():
			return
		}
	}
}

// readyForMessages reports whether peer's agent has waited at its prompt a
// moment, with nobody at its terminal, since a prompt was last typed for it
// at delivered.
func (d *Daemon) readyForMessages(peer *agentPeer, delivered time.Time) (*session.Session, bool) {
	s, err := d.projectCaller(peer)
	if err != nil {
		return nil, false
	}
	at, since := s.AtPrompt()
	now := time.Now()
	return s, at && now.Sub(since) >= deliverySettle && now.Sub(s.LastInput()) >= deliveryQuiet &&
		(since.After(delivered) || now.Sub(delivered) >= deliveryRetry)
}

// deliveryOf is the start of inbox to type as one prompt.
func deliveryOf(inbox []agentMessage) []agentMessage {
	size := 0
	for i, msg := range inbox {
		if size += len(msg.Text); i > 0 && size > deliveryBatch {
			return inbox[:i]
		}
	}
	return inbox
}

// agentPrompt is messages as the agent is given them, each headed by who
// sent it and how to answer.
func (d *Daemon) agentPrompt(messages []agentMessage) string {
	var b strings.Builder
	var mesh []*protocol.SessionInfo // read once, for the first sender of another node's
	for i, msg := range messages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "[Mako message %d from session %s. Answer with send_message to %s if it asks for one.]\n%s", msg.ID, d.agentLabel(msg.From, &mesh), msg.From, typable(msg.Text))
	}
	return b.String()
}

// typable is text without the control characters that would act as keys
// in a terminal, its line breaks and tabs kept.
func typable(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return '\n'
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, text)
}
