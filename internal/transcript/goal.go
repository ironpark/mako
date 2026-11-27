package transcript

import (
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// goalTracker folds goal status events recorded in a conversation's log
// (agent.LogGoals), deduplicating by revision and verifying session ID match.
type goalTracker struct {
	seen   map[string]bool
	status *protocol.GoalStatus
}

// take applies ev if it belongs to sessionID and has not already been seen.
func (g *goalTracker) take(ev agent.GoalEvent, sessionID string) {
	if sessionID != "" && ev.SessionID != sessionID {
		return
	}
	cond := strings.TrimSpace(ev.Condition)
	if ev.Revision == "" || ev.SessionID == "" || cond == "" {
		return
	}
	if g.seen == nil {
		g.seen = map[string]bool{}
	}
	if g.seen[ev.Revision] {
		return
	}
	g.seen[ev.Revision] = true

	observedAt := ev.Timestamp
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	source := ev.Source
	if source == "" {
		source = "claude"
	}
	if ev.Met {
		reason := protocol.GoalReasonCompleted
		if ev.Sentinel {
			reason = protocol.GoalReasonCleared
		}
		g.status = &protocol.GoalStatus{
			Source:          source,
			Status:          protocol.GoalInactive,
			Reason:          reason,
			ObservedAt:      protocol.Time(observedAt),
			SourceSessionId: ev.SessionID,
			SourceRevision:  ev.Revision,
		}
		return
	}
	g.status = &protocol.GoalStatus{
		Source:          source,
		Status:          protocol.GoalActive,
		Text:            cond,
		ObservedAt:      protocol.Time(observedAt),
		SourceSessionId: ev.SessionID,
		SourceRevision:  ev.Revision,
	}
}

// snapshot returns a defensive copy of the latest observed goal state, or nil
// when no goal event has been seen in the log.
func (g *goalTracker) snapshot() *protocol.GoalStatus {
	return proto.CloneOf(g.status)
}
