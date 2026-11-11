package claude

import (
	"encoding/json"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// Screens as Claude Code 2.1.322 draws them.
const (
	ruleLine  = "────────────────────────────────────────────────────────────"
	agentRule = "──────────────────────────── Ping localhost and report last line ─"
)

func TestRecall(t *testing.T) {
	for _, c := range []struct {
		name, screen, keys string
	}{
		{"at its prompt", "⏺ ok\n\n" + ruleLine + "\n❯ \n" + ruleLine + "\n\n  ⏵⏵ auto mode on (shift+tab to cycle)", ""},
		{"main shown", "⏺ ok\n\n" + ruleLine + "\n❯ \n" + ruleLine + `

  ⏵⏵ auto mode on (shift+tab to cycle)

  ⏺ main
  ◯ general-purpose  Ping localhost and report last line                 20s · ↓ 29.4k tokens`, ""},
		{"main shown, the list with the keys", "⏺ ok\n\n" + ruleLine + "\n❯ \n" + ruleLine + `

  ↑/↓ to select

❯ ⏺ main
  ◯ general-purpose  Ping localhost and report last line                 20s · ↓ 29.4k tokens`, ""},
		{"a subagent shown", "  Running 1 shell command · 26s…\n\n" + agentRule + "\n❯ Message @general-purpose…\n" + ruleLine + `

  ⏵⏵ auto mode on (shift+tab to cycle)

  ◯ main
  ⏺ general-purpose  Ping localhost and report last line                 28s · ↓ 29.4k tokens`, keyDown},
		{"a subagent picked", agentRule + "\n❯ Message @general-purpose…\n" + ruleLine + `

  ↑/↓ to select

  ◯ main
❯ ⏺ general-purpose  Ping localhost and report last line                 26s · ↓ 29.4k tokens`, keyUp},
		{"main picked", agentRule + "\n❯ draft\n" + ruleLine + `

  ↑/↓ to select · Enter to view

❯ ◯ main
  ⏺ general-purpose  Ping localhost and report last line                 20s · ↓ 29.3k tokens`, keyEnter},
		{"its transcript", "✻ Crunched for 36s · done 9:39 PM\n\n" + ruleLine + "\n  Showing detailed transcript · ctrl+o to toggle · ↑↓ scroll · v to open in nvim · ? for shortcuts     verbose", ctrlO},
		{"a message that says main", "⏺ main\n" + ruleLine + "\n❯ \n" + ruleLine + "\n  ? for shortcuts", ""},
	} {
		if got := string((*Agent)(nil).Recall(c.screen)); got != c.keys {
			t.Errorf("%s: %q, want %q", c.name, got, c.keys)
		}
	}
}

func TestAnswerReply(t *testing.T) {
	input := []byte(`{"questions":[{"question":"Which color?","options":[{"label":"Red"},{"label":"Blue"}]}],"metadata":{"source":"x"}}`)
	qs := []*protocol.Question{{Question: "Which color?", Options: []*protocol.QuestionOption{{Label: "Red"}, {Label: "Blue"}}}}
	var out struct {
		HookSpecificOutput struct {
			HookEventName string
			Decision      struct {
				Behavior     string
				UpdatedInput map[string]any
			}
		}
	}
	reply := (*Agent)(nil).AnswerReply(input, qs, []*protocol.QuestionAnswer{{Picked: []int32{1}, Text: "dark"}})
	if err := json.Unmarshal(reply, &out); err != nil {
		t.Fatal(err)
	}
	d := out.HookSpecificOutput.Decision
	answers, _ := d.UpdatedInput["answers"].(map[string]any)
	if out.HookSpecificOutput.HookEventName != "PermissionRequest" || d.Behavior != "allow" || answers["Which color?"] != "Blue, dark" ||
		d.UpdatedInput["questions"] == nil || d.UpdatedInput["metadata"] == nil {
		t.Fatalf("reply %s", reply)
	}
}
