package gui

import (
	"strings"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/protocol"
)

// A message sent from the composer shows in the feed at once, faded, as
// sent: the agent writes it to its log only as it takes it up, which may
// be a while when it is at work. It goes once the log has it, and the
// agent's own record of it stands in its place; or, as a command the
// agent does not log, once a while has passed.

// pendingMessage is a message sent and not yet in the log. It is in the
// log once the log says it after the user message that was the latest
// when it was sent, After's Occurrence-th of that key (none for ""), or
// has more user messages there than Ahead, those sent before it and still
// waiting then, which come first.
type pendingMessage struct {
	Text       string
	After      string
	Occurrence int
	Ahead      int
	At         time.Time
}

// pendingFor is how long a message sent waits for the log, a command less
// long: one that only changes the agent's settings is not logged.
const (
	pendingFor        = 2 * time.Minute
	pendingCommandFor = 10 * time.Second
)

// notePending keeps text as sent to session id, to show until the log has
// it.
func (m *model) notePending(id, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	state := m.chatState(id)
	settlePending(state)
	after, occurrence := latestUser(loadedMessages(state))
	pending := ggui.Peek(state.pending)
	state.pending.Set(append(pending, pendingMessage{Text: text, After: after, Occurrence: occurrence, Ahead: len(pending), At: time.Now()}))
}

// loadedMessages are the messages of the session's chat as read.
func loadedMessages(state *chatState) []*protocol.ChatMessage {
	return chatMessages(ggui.Peek(state.result), ggui.Peek(state.older))
}

// latestUser is the key of the latest user message of messages, and which
// of the user messages of that key it is, from 1.
func latestUser(messages []*protocol.ChatMessage) (string, int) {
	key := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].GetRole() == "user" {
			key = chatMessageKey(messages[i])
			break
		}
	}
	if key == "" {
		return "", 0
	}
	n := 0
	for _, msg := range messages {
		if msg.GetRole() == "user" && chatMessageKey(msg) == key {
			n++
		}
	}
	return key, n
}

// usersAfter counts the user messages of messages after p's anchor, and
// reports whether one of them says what p does.
func usersAfter(messages []*protocol.ChatMessage, p pendingMessage) (int, bool) {
	start, seen := 0, 0
	if p.After != "" {
		start = -1
		for i, msg := range messages {
			if msg.GetRole() == "user" && chatMessageKey(msg) == p.After {
				if seen++; seen == p.Occurrence {
					start = i + 1
					break
				}
			}
		}
		if start < 0 {
			return 0, false // the anchor is not read yet, or gone
		}
	}
	n, said := 0, false
	want := strings.TrimSpace(p.Text)
	for _, msg := range messages[start:] {
		if msg.GetRole() == "user" {
			n++
			said = said || strings.TrimSpace(msg.GetText()) == want
		}
	}
	return n, said
}

// settlePending drops the messages sent that the log has now, and those
// that waited too long.
func settlePending(state *chatState) {
	pending := ggui.Peek(state.pending)
	if len(pending) == 0 {
		return
	}
	messages := loadedMessages(state)
	var kept []pendingMessage
	for _, p := range pending {
		n, said := usersAfter(messages, p)
		wait := pendingFor
		if strings.HasPrefix(strings.TrimSpace(p.Text), "/") {
			wait = pendingCommandFor
		}
		if said || n > p.Ahead || time.Since(p.At) > wait {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) != len(pending) {
		state.pending.Set(kept)
	}
}

// pendingTurns are the turns of the messages sent and not yet logged, at
// the foot of the feed.
func pendingTurns(pending []pendingMessage) []chatTurnItem {
	out := make([]chatTurnItem, len(pending))
	for i, p := range pending {
		out[i] = chatTurnItem{
			Key:      "sent:" + p.At.Format(time.RFC3339Nano),
			Messages: []*protocol.ChatMessage{{Role: "user", Text: p.Text}},
			Sending:  true,
		}
	}
	return out
}
