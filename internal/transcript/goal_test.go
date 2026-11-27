package transcript

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestActivityTracksClaudeGoalStatusIncrementally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	info := &protocol.SessionInfo{Agent: "claude", ConversationId: "c"}

	head := `{"type":"user","sessionId":"c","cwd":"/p","message":{"role":"user","content":"go"}}` + "\n"
	active := `{"type":"attachment","isSidechain":false,"uuid":"u-1","sessionId":"c","timestamp":"2026-06-19T14:38:48.095Z","attachment":{"type":"goal_status","met":false,"sentinel":true,"condition":"finish migration"}}` + "\n"
	otherSess := `{"type":"attachment","isSidechain":false,"uuid":"u-other","sessionId":"other","timestamp":"2026-06-19T14:38:49.095Z","attachment":{"type":"goal_status","met":false,"sentinel":true,"condition":"hijack"}}` + "\n"
	dupRev := `{"type":"attachment","isSidechain":false,"uuid":"u-1","sessionId":"c","timestamp":"2026-06-19T14:38:50.095Z","attachment":{"type":"goal_status","met":true,"sentinel":true,"condition":"finish migration"}}` + "\n"
	cleared := `{"type":"attachment","isSidechain":false,"uuid":"u-2","sessionId":"c","timestamp":"2026-06-19T14:39:45.988Z","attachment":{"type":"goal_status","met":true,"sentinel":true,"condition":"finish migration"}}` + "\n"

	writeLog(t, path, head+active+otherSess+dupRev)
	var activity Activity
	got, err := activity.Read(info, path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal == nil || got.Goal.GetStatus() != protocol.GoalActive || got.Goal.GetText() != "finish migration" || got.Goal.GetSourceRevision() != "u-1" {
		t.Fatalf("unexpected initial goal: %v", got.Goal)
	}

	// Appending a cleared sentinel updates goal state incrementally.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(cleared)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}

	got, err = activity.Read(info, path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal == nil || got.Goal.GetStatus() != protocol.GoalInactive || got.Goal.GetReason() != protocol.GoalReasonCleared || got.Goal.GetSourceRevision() != "u-2" {
		t.Fatalf("unexpected cleared goal: %v", got.Goal)
	}
}
