package gui

import (
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestRaceTargetSetAndPartition(t *testing.T) {
	got := raceTargetSet("claude", []string{"codex", "claude", "shell", "claude:opus", "codex"})
	want := []string{"claude", "codex", "claude:opus"}
	if !slices.Equal(got, want) {
		t.Fatalf("raceTargetSet = %v, want %v", got, want)
	}

	entries := []raceEntry{
		{Target: "claude", SessionID: "s1"},
		{Target: "codex", SessionID: "s2"},
		{Target: "agy", SessionID: "s3"},
	}
	w, losers, ok := partitionRaceEntries(entries, "s2")
	if !ok || w.SessionID != "s2" || len(losers) != 2 {
		t.Fatalf("partition = %+v, %+v, %v", w, losers, ok)
	}
}

func TestRaceCommitMessage(t *testing.T) {
	if got := raceCommitMessage("codex", "  Fix the login bug\nand add a test"); got != "Race winner codex: Fix the login bug" {
		t.Fatalf("message %q", got)
	}
	if got := raceCommitMessage("claude:opus", ""); got != "Race winner claude:opus" {
		t.Fatalf("message without a prompt %q", got)
	}
	long := strings.Repeat("가", 100)
	if got := raceCommitMessage("agy", long); got != "Race winner agy: "+strings.Repeat("가", raceCommitSubjectLen)+"..." {
		t.Fatalf("long prompt cut to %q", got)
	}
}

func TestRaceWinnerBodySaysWhatIsCommitted(t *testing.T) {
	winner := raceEntry{Target: "codex", Branch: "race/codex-1"}
	wt := &protocol.Worktree{Branch: "race/codex-1", Base: "main"}
	clean := raceWinnerBody(winner, wt, "Fix it", 0)
	if strings.Contains(clean, "uncommitted") || !strings.Contains(clean, "race/codex-1") || !strings.Contains(clean, "main") {
		t.Fatalf("clean body %q", clean)
	}
	dirty := raceWinnerBody(winner, wt, "Fix it", 3)
	if !strings.Contains(dirty, "3 uncommitted") || !strings.Contains(dirty, `"Race winner codex: Fix it"`) {
		t.Fatalf("dirty body %q", dirty)
	}
}

func TestAggregateProjectUsage(t *testing.T) {
	sessions := []*protocol.SessionInfo{
		{Id: "s1", Project: "/repo/a", Agent: "claude"},
		{Id: "s2", Project: "/repo/a", Agent: "codex"},
		{Id: "s3", Project: "/repo/b", Agent: "claude"},
	}
	usages := map[string]*protocol.Usage{
		"s1": {Input: 1000, CachedInput: 500, Output: 200, CostUsd: 0.40},
		"s2": {Input: 2000, Output: 400, CostUsd: 0.60},
		"s3": {Input: 500, Output: 100, CostUsd: 0.10},
	}
	rows := aggregateProjectUsage(sessions, usages)
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2", len(rows))
	}
	if rows[0].Project != "/repo/a" || rows[0].Sessions != 2 || rows[0].Input != 3500 || rows[0].Output != 600 {
		t.Fatalf("top project row = %+v", rows[0])
	}
}
