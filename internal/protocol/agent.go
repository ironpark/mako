package protocol

import (
	"cmp"
	"slices"

	"google.golang.org/protobuf/proto"
)

// Revision numbers what a daemon of this build understands. A front-end
// that finds its daemon on an older one can say the daemon is out of date
// rather than leave the newer parts blank.
const Revision = 3

// AgentStatus is what an agent's own hooks last said it is doing,
// SessionInfo.Status.
type AgentStatus = string

const (
	AgentIdle    AgentStatus = "idle"    // at its prompt, nothing asked of it
	AgentWorking AgentStatus = "working" // on a turn the user gave it
	AgentWaiting AgentStatus = "waiting" // asking the user, an approval or a question
	AgentDone    AgentStatus = "done"    // finished its turn
)

// Decisions of an AnswerApproval.
const (
	DecisionAllow    = "allow"
	DecisionDeny     = "deny"
	DecisionTerminal = "terminal" // ask in the terminal, as without sanbo
)

// The windows of the limits agents name, in minutes.
const (
	FiveHours = 5 * 60
	Week      = 7 * 24 * 60
)

// IsZero reports whether nothing is counted; nil counts nothing.
func (u *Usage) IsZero() bool { return proto.Size(u) == 0 }

// Merge takes what r says over what u says: the window and each limit r
// names.
func (u *Usage) Merge(r *Usage) {
	if r.GetWindow() != 0 {
		u.Window = r.GetWindow()
	}
	for _, l := range r.GetLimits() {
		u.SetLimit(l)
	}
}

// SetLimit records l in place of the limit of the same window, keeping
// the shortest window first. It changes u's list, not the limits in it.
func (u *Usage) SetLimit(l *RateLimit) {
	limits := slices.DeleteFunc(slices.Clone(u.Limits), func(o *RateLimit) bool { return o.GetMinutes() == l.GetMinutes() })
	i, _ := slices.BinarySearchFunc(limits, l.GetMinutes(), func(o *RateLimit, m int64) int { return cmp.Compare(o.GetMinutes(), m) })
	u.Limits = slices.Insert(limits, i, l)
}
