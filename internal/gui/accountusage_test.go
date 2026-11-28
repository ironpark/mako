package gui

import (
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

func TestAccountUsageHelpers(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	future := now.Add(2 * time.Hour).Unix()
	past := now.Add(-10 * time.Minute).Unix()

	credits := 14.5
	data := &protocol.AccountUsage{
		Accounts: []*protocol.AgentAccountUsage{
			{
				Agent:  "claude",
				Status: "available",
				Plan:   "Max 5x",
				Limits: []*protocol.AccountLimit{
					{Id: "five_hour", Label: "5h", Used: 40, Resets: future},
					{Id: "weekly", Label: "Weekly", Used: 85, Resets: future},
				},
			},
			{
				Agent:   "codex",
				Status:  "available",
				Plan:    "Pro",
				Credits: &credits,
				Limits: []*protocol.AccountLimit{
					{Id: "expired", Label: "5h", Used: 99, Resets: past},
				},
			},
			{
				Agent:  "agy",
				Status: "available",
				Limits: []*protocol.AccountLimit{
					{Id: "off", Label: "Exp", Used: 100, Disabled: true},
					{Id: "g5h", Label: "5h", Used: 20, Resets: future},
				},
			},
		},
	}

	if peak := peakAccountUsageShare(data, now); peak != 0.85 {
		t.Fatalf("peakAccountUsageShare = %v, want 0.85 (expired & disabled ignored)", peak)
	}
	if sum := compactAccountUsageSummary(data, now); sum != "claude 85% · codex 0% · agy 20%" {
		t.Fatalf("compactAccountUsageSummary = %q", sum)
	}
	used, resets := accountLimitNow(&protocol.AccountLimit{Used: 99, Resets: past}, now)
	if used != 0 || resets != 0 {
		t.Fatalf("expired accountLimitNow = (%v, %v), want (0, 0)", used, resets)
	}
}

func TestCanRewindTurnAndUserTurnIndices(t *testing.T) {
	claudeInfo := &protocol.SessionInfo{Agent: "claude", ConversationId: "c-1"}
	codexInfo := &protocol.SessionInfo{Agent: "codex", ConversationId: "th-1"}
	shellInfo := &protocol.SessionInfo{Agent: "shell", ConversationId: "sh-1"}

	userWithID := &protocol.ChatMessage{Role: "user", Id: "uuid-1", Text: "hi"}
	userNoID := &protocol.ChatMessage{Role: "user", Text: "hi"}
	asst := &protocol.ChatMessage{Role: "assistant", Text: "hello"}

	if !canRewindTurn(claudeInfo, userWithID, 0, false) {
		t.Fatal("claude user turn with UUID should be rewindable even without full history")
	}
	if canRewindTurn(claudeInfo, userNoID, 0, false) {
		t.Fatal("claude user turn without UUID requires full history")
	}
	if !canRewindTurn(claudeInfo, userNoID, 0, true) {
		t.Fatal("claude user turn without UUID should be rewindable with full history")
	}
	if canRewindTurn(codexInfo, userWithID, 0, false) {
		t.Fatal("codex user turn requires full history for accurate turn index")
	}
	if !canRewindTurn(codexInfo, userNoID, 1, true) {
		t.Fatal("codex user turn should be rewindable with full history")
	}
	if canRewindTurn(codexInfo, asst, 0, true) {
		t.Fatal("assistant turns must not be rewindable")
	}
	if canRewindTurn(shellInfo, userWithID, 0, true) {
		t.Fatal("shell sessions must not be rewindable")
	}

	msgs := []*protocol.ChatMessage{
		{Role: "user", Id: "u0", Text: "first"},
		{Role: "assistant", Text: "reply 1"},
		{Role: "tool", Id: "t1", Tool: "Bash"},
		{Role: "user", Id: "u1", Text: "second"},
	}
	turns := chatTurns(msgs, nil)
	if len(turns) != 4 {
		t.Fatalf("turns len = %d, want 4", len(turns))
	}
	if turns[0].UserTurnIndex != 0 || turns[1].UserTurnIndex != -1 || turns[2].UserTurnIndex != -1 || turns[3].UserTurnIndex != 1 {
		t.Fatalf("unexpected UserTurnIndex values: [%d, %d, %d, %d]",
			turns[0].UserTurnIndex, turns[1].UserTurnIndex, turns[2].UserTurnIndex, turns[3].UserTurnIndex)
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

func TestSuggestFallbackAgentSkipsSpentAlternatives(t *testing.T) {
	now := time.Now()
	usage := func(used map[string]float64) *protocol.AccountUsage {
		var accs []*protocol.AgentAccountUsage
		for agent, u := range used {
			accs = append(accs, &protocol.AgentAccountUsage{Agent: agent, Status: "available", Limits: []*protocol.AccountLimit{{Used: u}}})
		}
		return &protocol.AccountUsage{Accounts: accs}
	}
	targets := []string{"claude", "codex", "agy"}
	for _, tc := range []struct {
		name string
		used map[string]float64
		want string
	}{
		{"90% against an exhausted 100%", map[string]float64{"claude": 90, "codex": 100}, ""},
		{"90% against another one past the threshold", map[string]float64{"claude": 90, "codex": 88}, ""},
		{"100% against one at the threshold", map[string]float64{"claude": 100, "codex": quotaWarnThreshold}, ""},
		{"the one below the threshold of two", map[string]float64{"claude": 90, "codex": 100, "agy": 50}, "agy"},
	} {
		high, fallback := suggestFallbackAgent(usage(tc.used), "claude", targets, now)
		if !high || fallback != tc.want {
			t.Errorf("%s: suggestFallbackAgent = (%v, %q), want (true, %q)", tc.name, high, fallback, tc.want)
		}
	}
}
