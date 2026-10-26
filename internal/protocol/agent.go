package protocol

import "time"

// Revision numbers what a daemon of this build understands. A front-end
// that finds its daemon on an older one can say the daemon is out of date
// rather than leave the newer parts blank.
const Revision = 2

// AgentStatus is what an agent's own hooks last said it is doing.
type AgentStatus string

const (
	AgentIdle    AgentStatus = "idle"    // at its prompt, nothing asked of it
	AgentWorking AgentStatus = "working" // on a turn the user gave it
	AgentWaiting AgentStatus = "waiting" // asking the user, an approval or a question
	AgentDone    AgentStatus = "done"    // finished its turn
)

// Approval is a tool call an agent waits for the user to approve.
type Approval struct {
	ID     string    `json:"id"`
	Tool   string    `json:"tool"`
	Detail string    `json:"detail,omitempty"` // the command, path or description, on one line
	Input  string    `json:"input,omitempty"`  // the call's arguments, as indented JSON
	Since  time.Time `json:"since"`
}

// ListSessions asks for the mesh's sessions. Approvals says the asker shows
// approvals, so an agent asking for one may wait for its answer.
type ListSessions struct {
	Approvals bool `json:"approvals,omitempty"`
}

const (
	TypeAnswerApproval Type = "answer_approval"
)

// Decisions of an AnswerApproval.
const (
	DecisionAllow    = "allow"
	DecisionDeny     = "deny"
	DecisionTerminal = "terminal" // ask in the terminal, as without sanbo
)

// AnswerApproval answers an approval a session's agent waits for. The
// reply is the session's SessionInfo.
type AnswerApproval struct {
	NodeID     string `json:"node_id,omitempty"`
	SessionID  string `json:"session_id"`
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
	Message    string `json:"message,omitempty"` // why, for a denial
}

// Usage is the tokens a conversation has spent so far.
type Usage struct {
	Input       int64 `json:"input,omitempty"` // fresh input, cached reads excluded
	CachedInput int64 `json:"cached_input,omitempty"`
	Output      int64 `json:"output,omitempty"`
	// Context is the latest turn's whole prompt, as much of the model's
	// window as the conversation fills.
	Context int64 `json:"context,omitempty"`
	Window  int64 `json:"window,omitempty"` // the model's context window, when the agent says
	// FiveHour and Week are how much of the account's subscription limits
	// is spent, when the agent says: its rolling five-hour session and its
	// week.
	FiveHour RateLimit `json:"five_hour,omitzero"`
	Week     RateLimit `json:"week,omitzero"`
}

// RateLimit is how much of one of an account's usage limits is spent.
type RateLimit struct {
	Used   float64 `json:"used"`             // percent, 0 to 100
	Resets int64   `json:"resets,omitempty"` // when the window starts over, in Unix seconds
}

// Merge takes what r says over what u says: the window and each limit r
// names.
func (u *Usage) Merge(r Usage) {
	if r.Window != 0 {
		u.Window = r.Window
	}
	if !r.FiveHour.IsZero() {
		u.FiveHour = r.FiveHour
	}
	if !r.Week.IsZero() {
		u.Week = r.Week
	}
}

// IsZero reports whether the limit is unknown.
func (r RateLimit) IsZero() bool { return r == RateLimit{} }
