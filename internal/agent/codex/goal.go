package codex

import (
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// IsActiveGoalStatus reports whether status is one of the Codex thread goal
// statuses that represent an ongoing/visible goal.
func IsActiveGoalStatus(status string) bool {
	switch status {
	case "active", "paused", "blocked", "usageLimited", "budgetLimited":
		return true
	}
	return false
}

// MapGoalUpdated normalizes a `thread/goal/updated` notification for
// currentThreadID into a protocol.GoalStatus. It returns nil if the
// notification belongs to another thread (such as a subagent) or has no thread
// ID to match against.
func MapGoalUpdated(n ThreadGoalUpdatedNotification, currentThreadID string, canEdit, canClear bool, observedAt time.Time) *protocol.GoalStatus {
	threadID := n.ResolvedThreadID()
	if threadID == "" {
		threadID = strings.TrimSpace(currentThreadID)
	}
	if threadID == "" {
		return nil
	}
	if currentThreadID != "" && threadID != currentThreadID {
		return nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	if n.Goal == nil {
		return &protocol.GoalStatus{
			Source:          "codex",
			Status:          protocol.GoalUnavailable,
			Reason:          protocol.GoalReasonMalformed,
			ObservedAt:      protocol.Time(observedAt),
			SourceSessionId: threadID,
		}
	}
	return MapThreadGoal(n.Goal, threadID, canEdit, canClear, observedAt)
}

// MapThreadGoal normalizes a Codex ThreadGoal value for threadID into a
// protocol.GoalStatus.
func MapThreadGoal(goal *ThreadGoal, threadID string, canEdit, canClear bool, observedAt time.Time) *protocol.GoalStatus {
	if threadID == "" {
		threadID = goal.ResolvedThreadID()
	}
	if threadID == "" {
		return nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	if goal == nil {
		return &protocol.GoalStatus{
			Source:          "codex",
			Status:          protocol.GoalUnavailable,
			Reason:          protocol.GoalReasonMalformed,
			ObservedAt:      protocol.Time(observedAt),
			SourceSessionId: threadID,
		}
	}
	rev := ""
	if goal.UpdatedAt > 0 {
		rev = strconv.FormatInt(goal.UpdatedAt, 10)
	}
	status := strings.TrimSpace(goal.Status)
	if status == "complete" {
		return &protocol.GoalStatus{
			Source:          "codex",
			Status:          protocol.GoalInactive,
			Reason:          protocol.GoalReasonCompleted,
			ObservedAt:      protocol.Time(observedAt),
			SourceSessionId: threadID,
			SourceRevision:  rev,
		}
	}
	objective := strings.TrimSpace(goal.Objective)
	if !IsActiveGoalStatus(status) || objective == "" {
		return &protocol.GoalStatus{
			Source:          "codex",
			Status:          protocol.GoalUnavailable,
			Reason:          protocol.GoalReasonMalformed,
			ObservedAt:      protocol.Time(observedAt),
			SourceSessionId: threadID,
			SourceRevision:  rev,
		}
	}
	return &protocol.GoalStatus{
		Source:          "codex",
		Status:          protocol.GoalActive,
		Text:            objective,
		ProviderStatus:  status,
		ObservedAt:      protocol.Time(observedAt),
		SourceSessionId: threadID,
		SourceRevision:  rev,
		CanEdit:         canEdit,
		CanClear:        canClear,
	}
}

// MapGoalCleared normalizes a `thread/goal/cleared` notification for
// currentThreadID into an inactive/cleared protocol.GoalStatus.
func MapGoalCleared(n ThreadGoalClearedNotification, currentThreadID string, observedAt time.Time) *protocol.GoalStatus {
	threadID := n.ResolvedThreadID()
	if threadID == "" {
		threadID = strings.TrimSpace(currentThreadID)
	}
	if threadID == "" {
		return nil
	}
	if currentThreadID != "" && threadID != currentThreadID {
		return nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	return &protocol.GoalStatus{
		Source:          "codex",
		Status:          protocol.GoalInactive,
		Reason:          protocol.GoalReasonCleared,
		ObservedAt:      protocol.Time(observedAt),
		SourceSessionId: threadID,
	}
}

// ParseGoalCommand parses an explicit `/goal <objective>` or `/goal clear`
// command string into a normalized GoalAction action and objective.
func ParseGoalCommand(text string) (action, objective string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 5 || !strings.EqualFold(trimmed[:5], "/goal") {
		return "", "", false
	}
	rest := trimmed[5:]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t' && rest[0] != '\n' && rest[0] != '\r') {
		return "", "", false
	}
	arg := strings.TrimSpace(rest)
	if arg == "" {
		return "", "", false
	}
	if strings.EqualFold(arg, "clear") {
		return protocol.GoalActionClear, "", true
	}
	return protocol.GoalActionSet, arg, true
}
