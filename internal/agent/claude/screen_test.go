package claude

import (
	"testing"

	"github.com/ironpark/mako/internal/detect"
)

const promptBox = `
────────────────────────────────────────────────
❯
────────────────────────────────────────────────
  ? for shortcuts`

func TestClaudesScreen(t *testing.T) {
	for _, c := range []struct {
		name  string
		in    detect.Input
		state detect.State
		rule  string
	}{
		{"at its prompt", detect.Input{Title: "✳ Fix the build", Screen: "⏺ Done.\n" + promptBox}, detect.Idle, "prompt_box"},
		{"at work", detect.Input{Title: "⠂ Fix the build", Screen: "✻ Pondering… (12s · ↓ 1.2k tokens)\n" + promptBox}, detect.Working, "title_working"},
		{"at work, untitled", detect.Input{Screen: "✻ Pondering… (12s · esc to interrupt)\n" + promptBox}, detect.Working, "live_turn"},
		{"asking to run a command", detect.Input{Title: "✳ Fix the build", Screen: `⏺ Bash(go test ./...)
────────────────────────────────────────────────
 Bash command

   go test ./...

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don't ask again for go test commands
   3. No, and tell Claude what to do differently (esc)

 Esc to cancel · Tab to amend · ctrl+e to explain`}, detect.Blocked, "bash_permission"},
		{"asking for an MCP server", detect.Input{Screen: `────────────────────────────────────────────────
 MCP server "github" requests your input

 ❯ Accept
   Decline

 Esc to cancel`}, detect.Blocked, "mcp_elicitation"},
		{"a picker", detect.Input{Screen: `────────────────────────────────────────────────
 Which approach?
 ❯ 1. Rewrite
   2. Patch

 Enter to select · ↑/↓ to navigate · Esc to cancel`}, detect.Blocked, "form"},
		{"its transcript", detect.Input{Title: "⠂ Fix", Screen: "⏺ Read(file)\n  Showing detailed transcript · ctrl+o to toggle"}, detect.Working, "title_working"},
		{"its transcript, still", detect.Input{Title: "✳ Fix", Screen: "⏺ Read(file)\n  Showing detailed transcript · ctrl+o to toggle"}, detect.Unknown, "transcript_viewer"},
	} {
		got := screenRules.Detect(c.in)
		if got.State != c.state || got.ID != c.rule {
			t.Errorf("%s: %s by %q, want %s by %q", c.name, got.State, got.ID, c.state, c.rule)
		}
	}
}
