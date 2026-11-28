package codex

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

func TestCodexSideQuestionCanAsk(t *testing.T) {
	a := New()

	allowed := []struct {
		name   string
		title  string
		screen string
	}{
		{
			name:   "idle at prompt",
			title:  "mako",
			screen: "• Done.\n\n› Ask Codex to do anything\n\n  100% context left",
		},
		{
			name:   "working on a turn",
			title:  "⠙ mako",
			screen: "› fix it\n\n• Working (3s • esc to interrupt)\n\n› \n",
		},
		{
			name:   "working untitled",
			title:  "",
			screen: "› fix it\n\n• Working (1m 3s • esc to interrupt)\n\n› Ask Codex",
		},
	}
	for _, tc := range allowed {
		if ok, reason := a.SideQuestionCanAsk(tc.screen, tc.title); !ok {
			t.Errorf("%s: SideQuestionCanAsk = false (%q), want true", tc.name, reason)
		}
	}

	blocked := []struct {
		name   string
		title  string
		screen string
	}{
		{
			name:   "empty screen",
			title:  "",
			screen: "",
		},
		{
			name:   "title blocked approval",
			title:  "Action Required",
			screen: "Allow command?\n  go test ./...\n› ",
		},
		{
			name:   "allow command dialog with stale working title",
			title:  "⠙ mako",
			screen: "› run tests\n\nAllow command?\n  go test ./...\n  Press enter to confirm or esc to cancel",
		},
		{
			name:   "trust directory prompt",
			title:  "mako",
			screen: "> You are in /tmp/x\n\n  Do you trust the contents of this directory?\n\n› 1. Yes, continue\n  2. No, quit",
		},
		{
			name:   "startup update picker",
			title:  "mako",
			screen: "Update available!\nUpdate now\nSkip until next version\nPress enter to continue",
		},
		{
			name:   "request user input dialog",
			title:  "mako",
			screen: "› \nWhich file?\nEnter to submit answer",
		},
		{
			name:   "transcript viewer overlay",
			title:  "mako",
			screen: "› \n↑/↓ to scroll · pgup/pgdn to page · home/end to jump · esc to edit prev · q to quit",
		},
		{
			name:   "compacting conversation",
			title:  "⠙ mako",
			screen: "• Compacting conversation…\n\n› ",
		},
		{
			name:  "side overlay already open",
			title: "⠙ mako",
			screen: `Side conversation
• The retry logic is in retry.go.
Esc to close`,
		},
		{
			name:   "no current prompt line",
			title:  "mako",
			screen: "› hi\n• still printing without prompt",
		},
	}
	for _, tc := range blocked {
		if ok, reason := a.SideQuestionCanAsk(tc.screen, tc.title); ok || reason == "" {
			t.Errorf("%s: SideQuestionCanAsk = (%v, %q), want (false, non-empty reason)", tc.name, ok, reason)
		}
	}
}

func TestCodexSideQuestionCommandAndParse(t *testing.T) {
	a := New()

	if got := a.SideQuestionCommand("  what does\tfoo do? "); got != "/side what does foo do?" {
		t.Fatalf("SideQuestionCommand = %q", got)
	}
	if got := a.SideQuestionCommand(""); got != "" {
		t.Fatalf("empty SideQuestionCommand = %q", got)
	}

	idle := "• Done.\n\n› Ask Codex to do anything"
	if _, st := a.SideQuestionParse(idle); st != agent.SideQuestionNone {
		t.Fatalf("idle parse = %v, want None", st)
	}
	if dismiss := a.SideQuestionDismiss(idle); dismiss != nil {
		t.Fatalf("idle dismiss = %q, want nil", dismiss)
	}

	// Still answering (timer visible).
	pending := `Side conversation
› what does foo do?
• Working (2s • esc to interrupt)
Esc to return`
	if _, st := a.SideQuestionParse(pending); st != agent.SideQuestionPending {
		t.Fatalf("pending parse = %v, want Pending", st)
	}
	if dismiss := a.SideQuestionDismiss(pending); string(dismiss) != "\x1b" {
		t.Fatalf("pending dismiss = %q, want ESC", dismiss)
	}

	// Completed answer.
	done := `╭────────────────────────────────────────────╮
│ Side conversation                          │
│ › what does foo do?                        │
│ • Foo normalizes retry backoff durations.  │
│ Esc to close                               │
╰────────────────────────────────────────────╯`
	ans, st := a.SideQuestionParse(done)
	if st != agent.SideQuestionDone {
		t.Fatalf("done state = %v, want Done", st)
	}
	if ans != "Foo normalizes retry backoff durations." {
		t.Fatalf("done answer = %q", ans)
	}
	if dismiss := a.SideQuestionDismiss(done); string(dismiss) != "\x1b" {
		t.Fatalf("done dismiss = %q, want ESC", dismiss)
	}
}

func TestCodexOneShotNoToolsSandbox(t *testing.T) {
	a := New()
	l := &agent.Launch{Command: []string{"codex"}}
	prep, err := a.PrepareOneShot(l, agent.OneShotRequest{Prompt: "hi", NoTools: true}, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if prep.Cleanup != nil {
		defer prep.Cleanup()
	}
	idx := slices.Index(l.Command, "--sandbox")
	if idx < 0 || idx+1 >= len(l.Command) || l.Command[idx+1] != "read-only" {
		t.Fatalf("expected --sandbox read-only in command, got %v", l.Command)
	}
}
