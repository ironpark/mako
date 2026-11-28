package claude

import (
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

func TestClaudeSideQuestionCanAsk(t *testing.T) {
	a := New()

	allowed := []struct {
		name   string
		title  string
		screen string
	}{
		{
			name:   "idle at prompt",
			title:  "✳ Fix the build",
			screen: "⏺ Done.\n" + promptBox,
		},
		{
			name:   "working on a turn",
			title:  "⠂ Fix the build",
			screen: "✻ Pondering… (12s · esc to interrupt)\n" + promptBox,
		},
		{
			name:   "working in plan mode",
			title:  "⠂ Plan refactor",
			screen: "✻ Exploring codebase… (8s · esc to interrupt)\n" + promptBox + "\n  ⏸ plan mode on (shift+tab to cycle)",
		},
		{
			name:   "waiting for background agents",
			title:  "✳ Fix the build",
			screen: "· Waiting for 2 background agents to finish\n" + promptBox,
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
			name:  "bash permission dialog with idle title",
			title: "✳ Fix the build",
			screen: `⏺ Bash(go test ./...)
────────────────────────────────────────────────
 Bash command

   go test ./...

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don't ask again for go test commands
   3. No, and tell Claude what to do differently (esc)

 Esc to cancel · Tab to amend · ctrl+e to explain`,
		},
		{
			name:  "bash permission dialog with stale working spinner title",
			title: "⠂ Fix the build",
			screen: `⏺ Bash(go test ./...)
────────────────────────────────────────────────
 Bash command

   go test ./...

 Do you want to proceed?
 ❯ 1. Yes
   2. No

 Esc to cancel · Tab to amend`,
		},
		{
			name:  "mcp elicitation dialog",
			title: "⠂ Fix the build",
			screen: `────────────────────────────────────────────────
 MCP server "github" requests your input

 ❯ Accept
   Decline

 Esc to cancel`,
		},
		{
			name:  "picker form",
			title: "✳ Fix the build",
			screen: `────────────────────────────────────────────────
 Which approach?
 ❯ 1. Rewrite
   2. Patch

 Enter to select · ↑/↓ to navigate · Esc to cancel`,
		},
		{
			name:   "transcript viewer overlay",
			title:  "⠂ Fix",
			screen: "⏺ Read(file)\n  Showing detailed transcript · ctrl+o to toggle",
		},
		{
			name:  "model picker overlay",
			title: "✳ Fix",
			screen: `Select model
❯ 1. Opus
  2. Sonnet
Enter to set as default · Esc to cancel`,
		},
		{
			name:  "subagent view active",
			title: "⠂ Fix",
			screen: `────────────────────────────────────────────────
❯
────────────────────────────────────────────────
  ◯ main
  ⏺ Explore`,
		},
		{
			name:   "compacting conversation",
			title:  "⠂ Fix",
			screen: "✻ Compacting conversation…\n" + promptBox,
		},
		{
			name:  "btw overlay already open",
			title: "⠂ Fix",
			screen: `/btw which file owns retries?
The retry logic lives in retry.go.
Esc to close`,
		},
		{
			name:   "no prompt box visible",
			title:  "⠂ Starting",
			screen: "Welcome to Claude Code\nLoading workspace...",
		},
	}
	for _, tc := range blocked {
		if ok, reason := a.SideQuestionCanAsk(tc.screen, tc.title); ok || reason == "" {
			t.Errorf("%s: SideQuestionCanAsk = (%v, %q), want (false, non-empty reason)", tc.name, ok, reason)
		}
	}
}

func TestClaudeSideQuestionCommandAndParse(t *testing.T) {
	a := New()

	if got := a.SideQuestionCommand("  which file\n  holds retries? "); got != "/btw which file holds retries?" {
		t.Fatalf("SideQuestionCommand = %q", got)
	}
	if got := a.SideQuestionCommand("   \n\t "); got != "" {
		t.Fatalf("empty SideQuestionCommand = %q, want empty", got)
	}

	// No overlay: never dismiss (avoids interrupting a running turn).
	idle := "✻ Pondering… (12s · esc to interrupt)\n" + promptBox
	if ans, st := a.SideQuestionParse(idle); st != agent.SideQuestionNone || ans != "" {
		t.Fatalf("idle parse = (%q, %v), want (\"\", None)", ans, st)
	}
	if dismiss := a.SideQuestionDismiss(idle); dismiss != nil {
		t.Fatalf("idle dismiss = %q, want nil", dismiss)
	}

	// Typing /btw inside the prompt line is not an overlay yet.
	typing := "────────────────────────────────────────────────\n❯ /btw which file?\n────────────────────────────────────────────────"
	if _, st := a.SideQuestionParse(typing); st != agent.SideQuestionNone {
		t.Fatalf("prompt typing parsed as %v, want None", st)
	}

	// Loading overlay: Pending, dismissable.
	loading := `────────────────────────────────────────────────
/btw which file holds retry logic?
✻ Thinking…
Esc to close`
	if ans, st := a.SideQuestionParse(loading); st != agent.SideQuestionPending || ans != "" {
		t.Fatalf("loading parse = (%q, %v), want (\"\", Pending)", ans, st)
	}
	if dismiss := a.SideQuestionDismiss(loading); string(dismiss) != "\x1b" {
		t.Fatalf("loading dismiss = %q, want ESC", dismiss)
	}

	// Completed overlay (plain).
	done := `✻ Pondering… (14s · esc to interrupt)
────────────────────────────────────────────────
/btw which file holds retry logic?

Retry logic is in internal/daemon/codex_appserver.go.
It retries up to 3 times on SQLite startup failures.

Press Esc or Enter to dismiss
────────────────────────────────────────────────`
	ans, st := a.SideQuestionParse(done)
	if st != agent.SideQuestionDone {
		t.Fatalf("done state = %v, want Done", st)
	}
	want := "Retry logic is in internal/daemon/codex_appserver.go.\nIt retries up to 3 times on SQLite startup failures."
	if ans != want {
		t.Fatalf("done answer = %q, want %q", ans, want)
	}
	if dismiss := a.SideQuestionDismiss(done); string(dismiss) != "\x1b" {
		t.Fatalf("done dismiss = %q, want ESC", dismiss)
	}

	// Boxed overlay.
	boxed := `╭──────────────────────────────────────────╮
│ /btw what is the default timeout?        │
│ The default timeout is 45 seconds.       │
│ Esc to close                             │
╰──────────────────────────────────────────╯`
	ans, st = a.SideQuestionParse(boxed)
	if st != agent.SideQuestionDone || ans != "The default timeout is 45 seconds." {
		t.Fatalf("boxed parse = (%q, %v)", ans, st)
	}
}

func TestClaudeOneShotNoToolsFlag(t *testing.T) {
	a := New()
	l := &agent.Launch{Command: []string{"claude"}}
	_, err := a.PrepareOneShot(l, agent.OneShotRequest{Prompt: "hi", NoTools: true}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.Index(l.Command, "--tools")
	if idx < 0 || idx+1 >= len(l.Command) || l.Command[idx+1] != "" {
		t.Fatalf("expected --tools \"\" in command, got %v", l.Command)
	}
	if !strings.Contains(strings.Join(l.Command, " "), "--no-session-persistence") {
		t.Fatalf("expected --no-session-persistence, got %v", l.Command)
	}
}
