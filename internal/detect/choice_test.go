package detect

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Dialogs as Claude Code and Codex draw them are read as the options they
// offer, the one the cursor is on, and what they ask.
func TestParseChoice(t *testing.T) {
	for _, c := range []struct {
		file     string
		options  []string
		selected int
		text     string // its last line
	}{
		{"claude_trust", []string{"No, exit", "Yes, I trust this folder"}, 0, "Security guide"},
		{"claude_edit_perm", []string{"Yes", "Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)", "No"}, 0, "Do you want to make this edit to a.txt?"},
		{"claude_bash_perm", []string{"Yes", "Yes, and don’t ask again for: rtk wc *", "Yes, and switch to auto mode · auto mode handles these prompts for you", "No"}, 0, "Do you want to proceed?"},
		{"claude_plan", []string{"Yes, and use auto mode", "Yes, manually approve edits", "Tell Claude what to change"}, 0, "Claude has written up a plan and is ready to execute. Would you like to proceed?"},
		{"codex_trust", []string{"Trust and continue", "Back to Agent Command Center"}, 0, ""},
		{"codex_exec_perm", []string{"Yes, proceed (y)", "Yes, and don't ask again for commands that start with `touch b.txt` (p)", "No, and tell Codex what to do differently (esc)"}, 0, "$ touch b.txt"},
	} {
		screen, err := os.ReadFile("testdata/choice_" + c.file + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		got, ok := ParseChoice(string(screen), 140)
		if !ok {
			t.Errorf("%s: no choice read", c.file)
			continue
		}
		if !reflect.DeepEqual(got.Options, c.options) || got.Selected != c.selected {
			t.Errorf("%s: options %q, selected %d", c.file, got.Options, got.Selected)
		}
		lines := strings.Split(got.Text, "\n")
		if c.text != "" && lines[len(lines)-1] != c.text {
			t.Errorf("%s: text %q", c.file, got.Text)
		}
	}
}

// The cursor moved down, a list of checkboxes, an agent at its prompt.
func TestParseChoiceOthers(t *testing.T) {
	moved := " Do you want to proceed?\n   1. Yes\n ❯ 2. No\n Esc to cancel"
	if c, ok := ParseChoice(moved, 80); !ok || c.Selected != 1 || c.Text != "Do you want to proceed?" {
		t.Errorf("moved: %+v %v", c, ok)
	}
	boxes := " Which fruits?\n ❯ 1. [ ] Apple\n   2. [ ] Pear\n Enter to select"
	if c, ok := ParseChoice(boxes, 80); ok {
		t.Errorf("checkboxes read as %+v", c)
	}
	prompt := "⏺ Done.\n─────\n❯ \n─────\n  ? for shortcuts"
	if c, ok := ParseChoice(prompt, 80); ok {
		t.Errorf("prompt read as %+v", c)
	}
	typed := "─────\n❯ fix the bug\n─────"
	if c, ok := ParseChoice(typed, 80); ok {
		t.Errorf("a prompt typed read as %+v", c)
	}
}
