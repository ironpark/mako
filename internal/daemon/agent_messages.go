package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

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
}

type messageFile struct {
	Next    uint64                    `json:"next_id"`
	Inboxes map[string][]agentMessage `json:"inboxes"`
	History []agentMessageRecord      `json:"history,omitempty"`
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
		if err := writeAtomic(m.path, data); err != nil {
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.data.Inboxes[to]) >= maxAgentInbox {
		return agentMessage{}, errors.New("recipient inbox is full; read_messages with ack_through to acknowledge received messages")
	}
	next := m.copyLocked()
	next.Next++
	msg := agentMessage{ID: next.Next, From: from, To: to, Text: text, Created: time.Now().UTC()}
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
		messages = append(messages, &protocol.AgentMessage{Id: msg.ID, FromSessionId: msg.From, ToSessionId: msg.To, Text: msg.Text, CreatedAt: protocol.Time(msg.Created), State: record.State})
	}
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return pending, messages
}
