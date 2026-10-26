package protocol

import (
	"cmp"
	"slices"
	"time"
)

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
	// Limits are how much of the account's subscription limits is spent,
	// when the agent says, the shortest window first.
	Limits []RateLimit `json:"limits,omitempty"`
}

// The windows of the limits agents name, in minutes.
const (
	FiveHours = 5 * 60
	Week      = 7 * 24 * 60
)

// RateLimit is how much of one of an account's usage limits is spent.
type RateLimit struct {
	Minutes int64   `json:"minutes"`          // the window the limit spans
	Used    float64 `json:"used"`             // percent, 0 to 100
	Resets  int64   `json:"resets,omitempty"` // when the window starts over, in Unix seconds
}

// Equal compares usage by value, limits included.
func (u Usage) Equal(o Usage) bool {
	return u.Input == o.Input && u.CachedInput == o.CachedInput && u.Output == o.Output &&
		u.Context == o.Context && u.Window == o.Window && slices.Equal(u.Limits, o.Limits)
}

// IsZero reports whether nothing is counted.
func (u Usage) IsZero() bool { return u.Equal(Usage{}) }

// Merge takes what r says over what u says: the window and each limit r
// names.
func (u *Usage) Merge(r Usage) {
	if r.Window != 0 {
		u.Window = r.Window
	}
	for _, l := range r.Limits {
		u.SetLimit(l)
	}
}

// SetLimit records l in place of the limit of the same window. It copies
// the limits first, as u may share them with a cached read.
func (u *Usage) SetLimit(l RateLimit) {
	limits := slices.DeleteFunc(slices.Clone(u.Limits), func(o RateLimit) bool { return o.Minutes == l.Minutes })
	i, _ := slices.BinarySearchFunc(limits, l.Minutes, func(o RateLimit, m int64) int { return cmp.Compare(o.Minutes, m) })
	u.Limits = slices.Insert(limits, i, l)
}
