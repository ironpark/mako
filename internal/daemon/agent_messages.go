package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/atomicfile"
	"github.com/ironpark/mako/internal/protocol"
)

const maxAgentMessage = 32 << 10
const maxAgentInbox = 256
const maxAgentHistory = 200 // per project, independently of inbox acknowledgement

type agentMessageRecord struct {
	Message agentMessage `json:"message"`
	Project string       `json:"project,omitempty"`
	State   string       `json:"state"`
}

type agentMessage struct {
	ID      uint64    `json:"id"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Text    string    `json:"text"`
	Created time.Time `json:"created_at"`
	// Note is Mako's own word about the agent From, not one it sent.
	Note bool `json:"note,omitempty"`
}

type messageFile struct {
	Next    uint64                    `json:"next_id"`
	Inboxes map[string][]agentMessage `json:"inboxes"`
	History []agentMessageRecord      `json:"history,omitempty"`
}

// proto is msg as the wire carries it, in state.
func (msg agentMessage) proto(state string) *protocol.AgentMessage {
	return &protocol.AgentMessage{Id: msg.ID, FromSessionId: msg.From, ToSessionId: msg.To, Text: msg.Text, CreatedAt: protocol.Time(msg.Created), State: state, Note: msg.Note}
}

// agentMessageOf is the message the wire carries as m.
func agentMessageOf(m *protocol.AgentMessage) agentMessage {
	return agentMessage{ID: m.GetId(), From: m.GetFromSessionId(), To: m.GetToSessionId(), Text: m.GetText(), Created: protocol.TimeOf(m.GetCreatedAt())}
}

// checkAgentText says what is wrong with text as a message, nil if nothing.
func checkAgentText(text string) error {
	if strings.TrimSpace(text) == "" || len(text) > maxAgentMessage {
		return fmt.Errorf("text must be non-empty and at most %d bytes", maxAgentMessage)
	}
	return nil
}

// Messages are kept until explicitly acknowledged. Reconnecting a tool
// client or restarting the daemon must not silently consume a message.
type agentMessages struct {
	mu      sync.Mutex
	path    string
	data    messageFile
	changed chan struct{}
}

func (m *agentMessages) load(dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.path = filepath.Join(dir, "agent-messages.json")
	data, err := os.ReadFile(m.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(data, &m.data); err != nil {
			return err
		}
	}
	if len(m.data.History) == 0 {
		for _, inbox := range m.data.Inboxes {
			for _, msg := range inbox {
				m.data.History = append(m.data.History, agentMessageRecord{Message: msg, State: "pending"})
			}
		}
		slices.SortFunc(m.data.History, func(a, b agentMessageRecord) int {
			if a.Message.ID < b.Message.ID {
				return -1
			}
			if a.Message.ID > b.Message.ID {
				return 1
			}
			return 0
		})
		m.data.History = trimAgentHistory(m.data.History)
	}
	return nil
}

func (m *agentMessages) copyLocked() messageFile {
	inboxes := maps.Clone(m.data.Inboxes)
	if inboxes == nil {
		inboxes = map[string][]agentMessage{}
	}
	return messageFile{Next: m.data.Next, Inboxes: inboxes, History: slices.Clone(m.data.History)}
}

func trimAgentHistory(history []agentMessageRecord) []agentMessageRecord {
	counts := map[string]int{}
	kept := make([]agentMessageRecord, 0, len(history))
	for i := len(history) - 1; i >= 0; i-- {
		record := history[i]
		if counts[record.Project] < maxAgentHistory {
			kept = append(kept, record)
			counts[record.Project]++
		}
	}
	slices.Reverse(kept)
	return kept
}

func (m *agentMessages) saveLocked(next messageFile) error {
	if m.path != "" {
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := atomicfile.Write(m.path, data); err != nil {
			return err
		}
	}
	m.data = next
	if m.changed != nil {
		close(m.changed)
	}
	m.changed = make(chan struct{})
	return nil
}

func (m *agentMessages) send(from, to, text, project string) (agentMessage, error) {
	return m.add(agentMessage{From: from, To: to, Text: text}, project)
}

// note leaves to a note about the agent from, as a message from it.
func (m *agentMessages) note(from, to, text, project string) (agentMessage, error) {
	return m.add(agentMessage{From: from, To: to, Text: text, Note: true}, project)
}

// sentSince reports whether from sent to a message, not a note, after t.
func (m *agentMessages) sentSince(from, to string, t time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.data.History {
		if msg := r.Message; msg.From == from && msg.To == to && !msg.Note && msg.Created.After(t) {
			return true
		}
	}
	return false
}

func (m *agentMessages) add(msg agentMessage, project string) (agentMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	to := msg.To
	if len(m.data.Inboxes[to]) >= maxAgentInbox {
		return agentMessage{}, errors.New("recipient inbox is full; read_messages with ack_through to acknowledge received messages")
	}
	next := m.copyLocked()
	next.Next++
	msg.ID, msg.Created = next.Next, time.Now().UTC()
	next.Inboxes[to] = append(append([]agentMessage(nil), next.Inboxes[to]...), msg)
	next.History = trimAgentHistory(append(next.History, agentMessageRecord{Message: msg, Project: project, State: "pending"}))
	if err := m.saveLocked(next); err != nil {
		return agentMessage{}, err
	}
	return msg, nil
}

func (m *agentMessages) remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.data.Inboxes[id]; !exists {
		return nil
	}
	next := m.copyLocked()
	delete(next.Inboxes, id)
	for i := range next.History {
		if next.History[i].Message.To == id && next.History[i].State == "pending" {
			next.History[i].State = "removed"
		}
	}
	return m.saveLocked(next)
}

func (m *agentMessages) read(ctx context.Context, id string, after, ack uint64, limit int, wait time.Duration) ([]agentMessage, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		m.mu.Lock()
		if ack > m.data.Next {
			m.mu.Unlock()
			return nil, errors.New("ack_through is beyond the latest message")
		}
		if ack > 0 {
			next := m.copyLocked()
			var kept []agentMessage
			for _, msg := range next.Inboxes[id] {
				if msg.ID > ack {
					kept = append(kept, msg)
				}
			}
			next.Inboxes[id] = kept
			for i := range next.History {
				if next.History[i].Message.To == id && next.History[i].Message.ID <= ack && next.History[i].State == "pending" {
					next.History[i].State = "acknowledged"
				}
			}
			if err := m.saveLocked(next); err != nil {
				m.mu.Unlock()
				return nil, err
			}
			ack = 0
		}
		messages := make([]agentMessage, 0)
		for _, msg := range m.data.Inboxes[id] {
			if msg.ID > after {
				messages = append(messages, msg)
				if len(messages) == limit {
					break
				}
			}
		}
		if m.changed == nil {
			m.changed = make(chan struct{})
		}
		changed := m.changed
		m.mu.Unlock()
		if len(messages) > 0 || wait == 0 {
			return messages, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return messages, nil
		}
	}
}

// monitor reads a snapshot without acknowledging messages or waking inboxes.
func (m *agentMessages) monitor(project string, members map[string]bool, limit int) (map[string]uint32, []*protocol.AgentMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := map[string]uint32{}
	for id := range members {
		pending[id] = uint32(len(m.data.Inboxes[id]))
	}
	var messages []*protocol.AgentMessage
	for _, record := range m.data.History {
		msg := record.Message
		if record.Project != project && !(record.Project == "" && (members[msg.From] || members[msg.To])) {
			continue
		}
		messages = append(messages, msg.proto(record.State))
	}
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return pending, messages
}

// pending is id's inbox, and a channel closed when any inbox next changes.
func (m *agentMessages) pending(id string) ([]agentMessage, <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.changed == nil {
		m.changed = make(chan struct{})
	}
	return slices.Clone(m.data.Inboxes[id]), m.changed
}

// delivered takes the messages through, typed into id's terminal, out of
// its inbox: the agent has them as a prompt and need not read them.
func (m *agentMessages) delivered(id string, through uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.copyLocked()
	var kept []agentMessage
	for _, msg := range next.Inboxes[id] {
		if msg.ID > through {
			kept = append(kept, msg)
		}
	}
	if len(kept) == len(next.Inboxes[id]) {
		return nil
	}
	next.Inboxes[id] = kept
	for i := range next.History {
		if next.History[i].Message.To == id && next.History[i].Message.ID <= through && next.History[i].State == "pending" {
			next.History[i].State = "delivered"
		}
	}
	return m.saveLocked(next)
}
