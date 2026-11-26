package claude

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

func TestPrepareOneShotDisablesHooksByDefault(t *testing.T) {
	a := New()

	l := agent.Launch{Command: []string{"claude", "--model", "claude-sonnet-4-6"}}
	if _, err := a.PrepareOneShot(&l, agent.OneShotRequest{Prompt: "summarize", Format: "json"}, "", ""); err != nil {
		t.Fatalf("PrepareOneShot: %v", err)
	}

	idx := slices.Index(l.Command, "--settings")
	if idx < 0 || idx+1 >= len(l.Command) {
		t.Fatalf("expected --settings flag in %q", l.Command)
	}
	if l.Command[idx+1] != `{"disableAllHooks":true}` {
		t.Fatalf("--settings value = %q, want %q", l.Command[idx+1], `{"disableAllHooks":true}`)
	}

	// Caller-supplied --settings must not be duplicated or overwritten.
	l2 := agent.Launch{Command: []string{"claude", "--settings", `{"custom":true}`}}
	if _, err := a.PrepareOneShot(&l2, agent.OneShotRequest{Prompt: "summarize", Format: "text"}, "", ""); err != nil {
		t.Fatalf("PrepareOneShot(custom): %v", err)
	}
	count := 0
	for _, arg := range l2.Command {
		if arg == "--settings" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 --settings flag when caller supplied one, got %d in %q", count, l2.Command)
	}

	// --settings=... form must also be respected.
	l3 := agent.Launch{Command: []string{"claude", `--settings={"custom":true}`}}
	if _, err := a.PrepareOneShot(&l3, agent.OneShotRequest{Prompt: "summarize", Format: "text"}, "", ""); err != nil {
		t.Fatalf("PrepareOneShot(equals): %v", err)
	}
	if slices.Contains(l3.Command, "--settings") {
		t.Fatalf("unexpected extra --settings when --settings=... was present: %q", l3.Command)
	}
}
