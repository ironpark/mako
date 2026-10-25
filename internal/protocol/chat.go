package protocol

import "time"

const (
	TypeReadChat Type = "read_chat"
	TypeChat     Type = "chat"
)

// ReadChat reads an agent's native conversation on the session's node.
// Conversation is an opaque ID returned in Chat.Conversations, never a path.
type ReadChat struct {
	NodeID       string `json:"node_id,omitempty"`
	SessionID    string `json:"session_id"`
	Conversation string `json:"conversation,omitempty"`
}

type Conversation struct {
	ID      string    `json:"id"`
	Updated time.Time `json:"updated"`
}

type ChatMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type Chat struct {
	Supported     bool           `json:"supported"`
	Conversations []Conversation `json:"conversations,omitempty"`
	Conversation  string         `json:"conversation,omitempty"`
	Messages      []ChatMessage  `json:"messages,omitempty"`
	Truncated     bool           `json:"truncated,omitempty"`
}
