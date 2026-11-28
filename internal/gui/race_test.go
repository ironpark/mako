package gui

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

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

func TestRaceTargetSetIsNoRaceForAShell(t *testing.T) {
	// Agents picked to compare with, left over after the primary became a
	// shell, start no race of AI agents.
	for _, primary := range []string{"shell", "shell:zsh", "", "  "} {
		if got := raceTargetSet(primary, []string{"claude", "codex"}); got != nil {
			t.Errorf("raceTargetSet(%q) = %v, want no race", primary, got)
		}
	}
	// The primary picked again among the extras counts once.
	if n := len(raceTargetSet("claude", []string{"claude", "codex"})); n != 2 {
		t.Fatalf("contestants = %d, want 2", n)
	}
	if n := len(raceTargetSet("claude", []string{"claude"})); n != 1 {
		t.Fatalf("contestants = %d, want 1: no race", n)
	}
}

func TestRaceBranchNameIsAValidRef(t *testing.T) {
	valid := regexp.MustCompile(`^race/[a-z0-9_]+(-[a-z0-9_]+)*-20261128-101500-k3x9-[0-9]+$`)
	for target, hint := range map[string]string{
		"claude":                  "claude",
		"claude:opus 4.5":         "claude-opus-4-5",
		"Codex:Fast~^?*[x]":       "codex-fast-x",
		"agy:..lock@{1}\\a//b":    "agy-lock-1-a-b",
		"-.lock":                  "lock",
		"한글":                      "agent",
		":::":                     "agent",
		strings.Repeat("ab", 40):  strings.Repeat("ab", 16),
		strings.Repeat("a-", 40):  strings.TrimRight(strings.Repeat("a-", 16), "-"),
		"claude:profile_with_und": "claude-profile_with_und",
	} {
		got := raceBranchName(target, "20261128-101500", "k3x9", 1)
		if !valid.MatchString(got) || strings.Contains(got, "..") || strings.Contains(got, "--") {
			t.Errorf("raceBranchName(%q) = %q, not a clean ref", target, got)
		}
		if want := "race/" + hint + "-20261128-101500-k3x9-2"; got != want {
			t.Errorf("raceBranchName(%q) = %q, want %q", target, got, want)
		}
	}
}

func TestRaceSuffixIsRandomAndRefSafe(t *testing.T) {
	ok := regexp.MustCompile(`^[0-9a-z]{4}$`)
	seen := map[string]bool{}
	for range 50 {
		s := raceSuffix()
		if !ok.MatchString(s) {
			t.Fatalf("raceSuffix() = %q", s)
		}
		seen[s] = true
	}
	if len(seen) < 45 {
		t.Fatalf("only %d distinct suffixes of 50", len(seen))
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

func TestRemoveRaceGroup(t *testing.T) {
	list := []raceGroup{{ID: "race-2"}, {ID: "race-1"}}
	left := removeRaceGroup(list, "race-2")
	if len(left) != 1 || left[0].ID != "race-1" || len(list) != 2 {
		t.Fatalf("removeRaceGroup = %+v, list %+v", left, list)
	}
}

func TestActiveRace(t *testing.T) {
	list := []raceGroup{{ID: "race-2"}, {ID: "race-1"}}
	if g, ok := activeRace(list, "race-1"); !ok || g.ID != "race-1" {
		t.Fatalf("activeRace(race-1) = %+v %v", g, ok)
	}
	if g, ok := activeRace(list, "gone"); !ok || g.ID != "race-2" {
		t.Fatalf("activeRace(gone) = %+v %v, want the newest", g, ok)
	}
	if _, ok := activeRace(nil, "race-1"); ok {
		t.Fatal("an active race without races")
	}
}

func TestSetAgentDropsTheComparison(t *testing.T) {
	l := &launchState{Form: ggui.State(newSession{Agent: "claude", RaceTargets: []string{"codex", "agy"}})}
	l.setAgent("claude")
	if got := ggui.Peek(l.Form).RaceTargets; len(got) != 2 {
		t.Fatalf("the same agent dropped the comparison: %v", got)
	}
	l.setAgent("shell")
	if f := ggui.Peek(l.Form); f.Agent != "shell" || f.RaceTargets != nil {
		t.Fatalf("form %+v after picking a shell", f)
	}
}
