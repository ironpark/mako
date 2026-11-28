package gui

import (
	"slices"
	"testing"
	"time"

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

func TestSuggestFallbackAgent(t *testing.T) {
	now := time.Now()
	data := &protocol.AccountUsage{
		Accounts: []*protocol.AgentAccountUsage{
			{
				Agent:  "claude",
				Status: "available",
				Limits: []*protocol.AccountLimit{{Used: 94}},
			},
			{
				Agent:  "codex",
				Status: "available",
				Limits: []*protocol.AccountLimit{{Used: 25}},
			},
			{
				Agent:  "agy",
				Status: "available",
				Limits: []*protocol.AccountLimit{{Used: 60}},
			},
		},
	}
	high, fallback := suggestFallbackAgent(data, "claude", []string{"claude", "codex", "agy"}, now)
	if !high || fallback != "codex" {
		t.Fatalf("suggestFallbackAgent(claude) = (%v, %q), want (true, codex)", high, fallback)
	}
	high2, _ := suggestFallbackAgent(data, "codex", []string{"claude", "codex"}, now)
	if high2 {
		t.Fatalf("codex at 25%% should not trigger high-usage warning")
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
