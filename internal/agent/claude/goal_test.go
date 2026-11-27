package claude

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

const (
	fixtureActive     = `{"parentUuid":null,"isSidechain":false,"type":"attachment","uuid":"001008f1-0438-414a-86c0-eef745e1227f","timestamp":"2026-06-19T14:38:48.095Z","attachment":{"type":"goal_status","met":false,"sentinel":true,"condition":"keep this goal active until I explicitly clear it"},"userType":"external","entrypoint":"cli","cwd":"/private/tmp/happy-claude-goal-fixtures","sessionId":"ff668493-8325-49cc-976f-689b779b85a2","version":"2.1.153","gitBranch":"HEAD"}`
	fixtureEditActive = `{"parentUuid":"6e0a93e2-86bc-4fca-a99e-5521e2e8da21","isSidechain":false,"type":"attachment","uuid":"acde1bd2-f79f-43fb-8dd1-e2c8e2a0b294","timestamp":"2026-06-19T14:39:26.067Z","attachment":{"type":"goal_status","met":false,"sentinel":true,"condition":"replace this goal with edited fixture objective"},"userType":"external","entrypoint":"cli","cwd":"/private/tmp/happy-claude-goal-fixtures","sessionId":"ff668493-8325-49cc-976f-689b779b85a2","version":"2.1.153","gitBranch":"HEAD"}`
	fixtureCleared    = `{"parentUuid":"ad8e5f5c-e23f-48f2-9822-eb413244d9f7","isSidechain":false,"type":"attachment","uuid":"0a45cef5-97b4-4b5c-80c2-5575307bf695","timestamp":"2026-06-19T14:39:45.988Z","attachment":{"type":"goal_status","met":true,"sentinel":true,"condition":"replace this goal with edited fixture objective"},"userType":"external","entrypoint":"cli","cwd":"/private/tmp/happy-claude-goal-fixtures","sessionId":"ff668493-8325-49cc-976f-689b779b85a2","version":"2.1.153","gitBranch":"HEAD"}`
	fixtureCompleted  = `{"parentUuid":"ae21e250-9191-4e75-8b10-645318acd1dc","isSidechain":false,"attachment":{"type":"goal_status","met":true,"condition":"analyze the project","reason":"The assistant completed a comprehensive project analysis.","iterations":2,"durationMs":41613,"tokens":2393},"type":"attachment","uuid":"9b657b8a-bbd6-4147-ab35-780a3ae80e06","timestamp":"2026-06-17T14:49:46.439Z","userType":"external","entrypoint":"remote_mobile","cwd":"/tmp/project","sessionId":"204f1ce3-84e7-4985-849f-4489c1ab3894","version":"2.1.167"}`
)

func TestParseGoalStatusEventFixturesAndRejectsInvalid(t *testing.T) {
	ev, ok := New().GoalEvent([]byte(fixtureActive))
	if !ok {
		t.Fatal("expected active fixture to parse")
	}
	if ev.Source != "claude" || ev.Revision != "001008f1-0438-414a-86c0-eef745e1227f" ||
		ev.SessionID != "ff668493-8325-49cc-976f-689b779b85a2" ||
		ev.Met || !ev.Sentinel || ev.Condition != "keep this goal active until I explicitly clear it" {
		t.Fatalf("unexpected active event: %+v", ev)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("expected timestamp to parse")
	}

	comp, ok := ParseGoalStatusEvent([]byte(fixtureCompleted))
	if !ok {
		t.Fatal("expected completed fixture to parse")
	}
	if !comp.Met || comp.Sentinel || comp.Condition != "analyze the project" ||
		comp.Reason != "The assistant completed a comprehensive project analysis." ||
		comp.Iterations != 2 || comp.DurationMs != 41613 || comp.Tokens != 2393 {
		t.Fatalf("unexpected completed event: %+v", comp)
	}

	rejected := []string{
		`{"type":"attachment","uuid":"att-1","sessionId":"c1","attachment":{"type":"skill_listing","content":"skills"}}`,
		`{"type":"assistant","uuid":"msg-1","sessionId":"c1","message":{"role":"assistant","content":"hello"}}`,
		`{"type":"attachment","isSidechain":true,"uuid":"att-2","sessionId":"c1","attachment":{"type":"goal_status","met":false,"condition":"subagent"}}`,
		`{"type":"attachment","uuid":"att-3","sessionId":"c1","attachment":{"type":"goal_status","met":"false","condition":"bad bool"}}`,
		`{"type":"attachment","uuid":"att-4","sessionId":"c1","attachment":{"type":"goal_status","condition":"missing met"}}`,
		`{"type":"attachment","uuid":"","sessionId":"c1","attachment":{"type":"goal_status","met":false,"condition":"missing uuid"}}`,
		`{"type":"attachment","uuid":"att-5","sessionId":"","attachment":{"type":"goal_status","met":false,"condition":"missing session"}}`,
		`{"type":"attachment","uuid":"att-6","sessionId":"c1","attachment":{"type":"goal_status","met":false,"condition":"   "}}`,
	}
	for _, line := range rejected {
		if got, ok := ParseGoalStatusEvent([]byte(line)); ok {
			t.Errorf("expected rejection for %s, got %+v", line, got)
		}
	}
}

func TestMapAndReduceGoalEvents(t *testing.T) {
	active, _ := ParseGoalStatusEvent([]byte(fixtureActive))
	edited, _ := ParseGoalStatusEvent([]byte(fixtureEditActive))
	cleared, _ := ParseGoalStatusEvent([]byte(fixtureCleared))
	completed, _ := ParseGoalStatusEvent([]byte(fixtureCompleted))

	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	got := MapGoalEventToStatus(active, active.SessionID, true, true, now)
	if got == nil || got.GetStatus() != protocol.GoalActive || got.GetText() != active.Condition ||
		got.GetSource() != "claude" || got.GetSourceRevision() != active.Revision ||
		!got.GetCanEdit() || !got.GetCanClear() || !protocol.TimeOf(got.GetObservedAt()).Equal(now) {
		t.Fatalf("unexpected active mapped status: %v", got)
	}

	// Mismatched session ID is ignored.
	if MapGoalEventToStatus(active, "different-session", true, true, now) != nil {
		t.Fatal("expected nil for mismatched session ID")
	}

	// Completed attachment maps to inactive/completed without leaking evaluator prose/metrics.
	compStatus := MapGoalEventToStatus(completed, completed.SessionID, true, true, now)
	if compStatus == nil || compStatus.GetStatus() != protocol.GoalInactive || compStatus.GetReason() != protocol.GoalReasonCompleted {
		t.Fatalf("unexpected completed status: %v", compStatus)
	}
	raw, _ := json.Marshal(compStatus)
	if strings.Contains(string(raw), "comprehensive project analysis") {
		t.Fatalf("evaluator reason leaked into GoalStatus: %s", raw)
	}

	// Sentinel met:true maps to inactive/cleared.
	clearStatus := MapGoalEventToStatus(cleared, cleared.SessionID, true, true, now)
	if clearStatus == nil || clearStatus.GetStatus() != protocol.GoalInactive || clearStatus.GetReason() != protocol.GoalReasonCleared {
		t.Fatalf("unexpected cleared status: %v", clearStatus)
	}

	// Reduction follows transcript order, deduplicates by revision, and ignores other sessions.
	dupActiveChanged := active
	dupActiveChanged.Condition = "stale duplicate with same uuid"
	reduced := ReduceGoalEvents([]agent.GoalEvent{
		cleared,
		completed, // different session ID -> ignored
		active,
		dupActiveChanged, // duplicate UUID -> ignored
		edited,
	}, active.SessionID, true, true)
	if reduced == nil || reduced.GetStatus() != protocol.GoalActive || reduced.GetText() != edited.Condition || reduced.GetSourceRevision() != edited.Revision {
		t.Fatalf("unexpected reduced status: %v", reduced)
	}

	// Verify duplicate UUID did not overwrite active when active was last.
	reducedDup := ReduceGoalEvents([]agent.GoalEvent{active, dupActiveChanged}, active.SessionID, true, true)
	if reducedDup == nil || reducedDup.GetText() != active.Condition {
		t.Fatalf("duplicate revision overwrote prior event: %v", reducedDup)
	}
}

func TestGoalCommandsSanitizeObjective(t *testing.T) {
	a := New()
	if a.GoalCommandName() != "goal" {
		t.Fatalf("GoalCommandName = %q", a.GoalCommandName())
	}
	if a.GoalClearCommand() != "/goal clear" {
		t.Fatalf("GoalClearCommand = %q", a.GoalClearCommand())
	}
	if got := a.GoalSetCommand("  ship\nrelease\tnow\x1b[201~  "); got != "/goal ship release now" {
		t.Fatalf("GoalSetCommand = %q", got)
	}
	if got := a.GoalSetCommand("   \n\t  "); got != "" {
		t.Fatalf("expected empty command for whitespace objective, got %q", got)
	}
}
