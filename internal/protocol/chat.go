package protocol

const (
	TypeReadChat Type = "read_chat"
	TypeChat     Type = "chat"
)

// ReadChat reads an agent's native conversation on the session's node. The
// daemon always reads the conversation bound to SessionID.
type ReadChat struct {
	NodeID    string `json:"node_id,omitempty"`
	SessionID string `json:"session_id"`
}

type ChatMessage struct {
	Role string `json:"role"` // user, assistant or tool
	Text string `json:"text"` // prose, or a tool's arguments
	// ID is the native log's identity for a tool call, so the chat can keep
	// a tool folded or open across refreshes.
	ID     string `json:"id,omitempty"`
	Tool   string `json:"tool,omitempty"`   // the tool a role tool message runs
	Detail string `json:"detail,omitempty"` // its short argument summary, such as a path or command
	Result string `json:"result,omitempty"` // what the tool printed
	Output bool   `json:"output,omitempty"` // a result whose call is not in the window
	Error  bool   `json:"error,omitempty"`  // the tool failed
}

type Chat struct {
	Supported    bool          `json:"supported"`
	Conversation string        `json:"conversation,omitempty"`
	Messages     []ChatMessage `json:"messages,omitempty"`
	Truncated    bool          `json:"truncated,omitempty"`
}
