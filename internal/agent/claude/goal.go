package claude

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

var goalStatusMark = []byte(`"goal_status"`)

// GoalEvent parses a Claude Code JSONL transcript line for a goal_status
// attachment record, returning false for ordinary messages, sidechains,
// other attachments, or malformed records.
func (*Agent) GoalEvent(line []byte) (agent.GoalEvent, bool) {
	return ParseGoalStatusEvent(line)
}

// ParseGoalStatusEvent parses a single Claude JSONL transcript line for a
// type:"attachment" record with attachment.type:"goal_status".
func ParseGoalStatusEvent(line []byte) (agent.GoalEvent, bool) {
	if !bytes.Contains(line, goalStatusMark) {
		return agent.GoalEvent{}, false
	}
	var r struct {
		Type       string          `json:"type"`
		Sidechain  bool            `json:"isSidechain"`
		UUID       string          `json:"uuid"`
		SessionID  string          `json:"sessionId"`
		Timestamp  string          `json:"timestamp"`
		Attachment json.RawMessage `json:"attachment"`
	}
	if json.Unmarshal(line, &r) != nil || r.Type != "attachment" || r.Sidechain || len(r.Attachment) == 0 {
		return agent.GoalEvent{}, false
	}
	uuid := strings.TrimSpace(r.UUID)
	sessionID := strings.TrimSpace(r.SessionID)
	if uuid == "" || sessionID == "" {
		return agent.GoalEvent{}, false
	}
	var att struct {
		Type       string          `json:"type"`
		Met        json.RawMessage `json:"met"`
		Condition  string          `json:"condition"`
		Sentinel   json.RawMessage `json:"sentinel"`
		Reason     json.RawMessage `json:"reason"`
		Iterations json.RawMessage `json:"iterations"`
		DurationMs json.RawMessage `json:"durationMs"`
		Tokens     json.RawMessage `json:"tokens"`
	}
	if json.Unmarshal(r.Attachment, &att) != nil || att.Type != "goal_status" {
		return agent.GoalEvent{}, false
	}
	met, ok := parseStrictBool(att.Met)
	if !ok {
		return agent.GoalEvent{}, false
	}
	condition := strings.TrimSpace(att.Condition)
	if condition == "" {
		return agent.GoalEvent{}, false
	}
	ev := agent.GoalEvent{
		Source:    "claude",
		Revision:  uuid,
		SessionID: sessionID,
		Met:       met,
		Condition: condition,
	}
	if ts := strings.TrimSpace(r.Timestamp); ts != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			ev.Timestamp = parsed
		}
	}
	if sentinel, ok := parseStrictBool(att.Sentinel); ok {
		ev.Sentinel = sentinel
	}
	if reason, ok := parseOptionalString(att.Reason); ok {
		ev.Reason = reason
	}
	if n, ok := parseOptionalNumber(att.Iterations); ok {
		ev.Iterations = int(n)
	}
	if n, ok := parseOptionalNumber(att.DurationMs); ok {
		ev.DurationMs = int64(n)
	}
	if n, ok := parseOptionalNumber(att.Tokens); ok {
		ev.Tokens = int64(n)
	}
	return ev, true
}

// MapGoalEventToStatus maps a parsed Claude GoalEvent into a normalized
// protocol.GoalStatus, returning nil if currentSessionID is set and does not
// match the event's session ID. Evaluator details (reason, durationMs, tokens)
// are deliberately omitted from the synced GoalStatus.
func MapGoalEventToStatus(ev agent.GoalEvent, currentSessionID string, canEdit, canClear bool, observedAt time.Time) *protocol.GoalStatus {
	if currentSessionID != "" && ev.SessionID != currentSessionID {
		return nil
	}
	cond := strings.TrimSpace(ev.Condition)
	if ev.Revision == "" || ev.SessionID == "" || cond == "" {
		return nil
	}
	if observedAt.IsZero() {
		if !ev.Timestamp.IsZero() {
			observedAt = ev.Timestamp
		} else {
			observedAt = time.Now()
		}
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
		return &protocol.GoalStatus{
			Source:          source,
			Status:          protocol.GoalInactive,
			Reason:          reason,
			ObservedAt:      protocol.Time(observedAt),
			SourceSessionId: ev.SessionID,
			SourceRevision:  ev.Revision,
		}
	}
	return &protocol.GoalStatus{
		Source:          source,
		Status:          protocol.GoalActive,
		Text:            cond,
		ObservedAt:      protocol.Time(observedAt),
		SourceSessionId: ev.SessionID,
		SourceRevision:  ev.Revision,
		CanEdit:         canEdit,
		CanClear:        canClear,
	}
}

// ReduceGoalEvents folds Claude goal events in transcript order for
// currentSessionID, deduplicating by revision (attachment UUID) and keeping
// the latest applicable state.
func ReduceGoalEvents(events []agent.GoalEvent, currentSessionID string, canEdit, canClear bool) *protocol.GoalStatus {
	seen := make(map[string]bool, len(events))
	var latest *protocol.GoalStatus
	for _, ev := range events {
		if ev.Revision != "" && seen[ev.Revision] {
			continue
		}
		mapped := MapGoalEventToStatus(ev, currentSessionID, canEdit, canClear, time.Time{})
		if mapped == nil {
			continue
		}
		if ev.Revision != "" {
			seen[ev.Revision] = true
		}
		latest = mapped
	}
	return latest
}

// GoalCommandName returns Claude Code's goal slash command name.
func (*Agent) GoalCommandName() string { return "goal" }

// GoalSetCommand builds the single-line `/goal <objective>` command for Claude
// Code, returning "" when objective has no non-whitespace content.
func (*Agent) GoalSetCommand(objective string) string {
	clean := sanitizeGoalObjective(objective)
	if clean == "" {
		return ""
	}
	return "/goal " + clean
}

// GoalClearCommand returns the `/goal clear` command for Claude Code.
func (*Agent) GoalClearCommand() string { return "/goal clear" }

// sanitizeGoalObjective folds line breaks and strips terminal control
// characters (including bracketed-paste end markers) so typing `/goal ...`
// into Claude's PTY cannot inject extra prompts or escape sequences.
func sanitizeGoalObjective(objective string) string {
	objective = strings.ReplaceAll(objective, "\x1b[201~", " ")
	var b strings.Builder
	b.Grow(len(objective))
	for _, r := range objective {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func parseStrictBool(raw json.RawMessage) (bool, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false, false
	}
	var v bool
	if json.Unmarshal(trimmed, &v) != nil {
		return false, false
	}
	return v, true
}

func parseOptionalString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false
	}
	var v string
	if json.Unmarshal(trimmed, &v) != nil {
		return "", false
	}
	return v, true
}

func parseOptionalNumber(raw json.RawMessage) (float64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, false
	}
	var v float64
	if json.Unmarshal(trimmed, &v) != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
