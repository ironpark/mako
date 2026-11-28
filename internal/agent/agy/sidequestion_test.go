package agy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

func TestAgySideQuestionCanAsk(t *testing.T) {
	a := New()

	for _, file := range []string{
		"screen_idle.txt",
		"screen_working.txt",
		"screen_running_command.txt",
		"screen_done.txt",
		"screen_interrupted.txt",
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		if ok, reason := a.SideQuestionCanAsk(string(raw), ""); !ok {
			t.Errorf("%s: SideQuestionCanAsk = false (%q), want true", file, reason)
		}
	}

	for _, file := range []string{
		"screen_permission.txt",
		"screen_trust.txt",
		"screen_slash_menu.txt",
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		if ok, reason := a.SideQuestionCanAsk(string(raw), ""); ok || reason == "" {
			t.Errorf("%s: SideQuestionCanAsk = (%v, %q), want (false, non-empty reason)", file, ok, reason)
		}
	}

	extraBlocked := []struct {
		name   string
		screen string
	}{
		{
			name:   "empty screen",
			screen: "",
		},
		{
			name:   "question dialog",
			screen: "Which one?\n> A\n  B\n  ↑/↓ Navigate · enter Select · esc Skip\nesc to cancel",
		},
		{
			name:   "skills modal overlay",
			screen: "Skills\n10 skills\n> mako\nKeyboard: ↑/↓ Navigate  enter View  esc Cancel\nesc to cancel   auto · Gemini 4 Neon · xhigh",
		},
		{
			name:   "compacting conversation",
			screen: "⣾ Compacting conversation…\n────────────────────────\n>\n────────────────────────\nesc to cancel",
		},
		{
			name:   "btw overlay already open",
			screen: " ┃ /btw which file?\n ┃ It is in server.go.\n ┃ esc to dismiss\n────────────────────────\n>\n────────────────────────",
		},
		{
			name:   "missing prompt box",
			screen: "Antigravity CLI\nStarting up...",
		},
	}
	for _, tc := range extraBlocked {
		if ok, reason := a.SideQuestionCanAsk(tc.screen, ""); ok || reason == "" {
			t.Errorf("%s: SideQuestionCanAsk = (%v, %q), want (false, non-empty reason)", tc.name, ok, reason)
		}
	}
}

func TestAgySideQuestionCommandAndParse(t *testing.T) {
	a := New()

	if got := a.SideQuestionCommand("  which package\nowns retry? "); got != "/btw which package owns retry?" {
		t.Fatalf("SideQuestionCommand = %q", got)
	}

	idle, err := os.ReadFile(filepath.Join("testdata", "screen_working.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if _, st := a.SideQuestionParse(string(idle)); st != agent.SideQuestionNone {
		t.Fatalf("working screen parsed as %v, want None", st)
	}
	if dismiss := a.SideQuestionDismiss(string(idle)); dismiss != nil {
		t.Fatalf("working screen dismiss = %q, want nil", dismiss)
	}

	pending := ` ┃ /btw which file owns retry?
 ┃ ⣾ Generating...
 ┃ esc to dismiss`
	if _, st := a.SideQuestionParse(pending); st != agent.SideQuestionPending {
		t.Fatalf("pending parse = %v, want Pending", st)
	}
	if dismiss := a.SideQuestionDismiss(pending); string(dismiss) != "\x1b" {
		t.Fatalf("pending dismiss = %q, want ESC", dismiss)
	}

	done := ` ┃ /btw which file owns retry?
 ┃ Retry logic is in internal/daemon/codex_appserver.go.
 ┃ esc to dismiss
────────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────────
esc to cancel                                              auto`
	ans, st := a.SideQuestionParse(done)
	if st != agent.SideQuestionDone {
		t.Fatalf("done state = %v, want Done", st)
	}
	if ans != "Retry logic is in internal/daemon/codex_appserver.go." {
		t.Fatalf("done answer = %q", ans)
	}
	if dismiss := a.SideQuestionDismiss(done); string(dismiss) != "\x1b" {
		t.Fatalf("done dismiss = %q, want ESC", dismiss)
	}
}

func TestAgyOneShotNoToolsFlag(t *testing.T) {
	a := New()
	l := &agent.Launch{Command: []string{"agy"}}
	if _, err := a.PrepareOneShot(l, agent.OneShotRequest{Prompt: "hi", NoTools: true}, "", ""); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(l.Command, "--disable-slash-commands") {
		t.Fatalf("expected --disable-slash-commands in %v", l.Command)
	}
}
