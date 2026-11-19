package agy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/mako/internal/detect"
)

// The screens in testdata are agy 1.5.12's, read from its terminal.
func TestAgysScreen(t *testing.T) {
	for _, c := range []struct {
		file  string
		state detect.State
		rule  string
	}{
		{"screen_trust.txt", detect.Blocked, "trust_prompt"},
		{"screen_idle.txt", detect.Idle, "prompt_footer"},
		{"screen_working.txt", detect.Working, "esc_cancel_footer_working"},
		{"screen_running_command.txt", detect.Working, "esc_cancel_footer_working"},
		{"screen_permission.txt", detect.Blocked, "permission_prompt"},
		{"screen_done.txt", detect.Idle, "prompt_footer"},
		{"screen_interrupted.txt", detect.Idle, "prompt_footer"},
		{"screen_slash_menu.txt", detect.Idle, "slash_menu"},
	} {
		screen, err := os.ReadFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}
		got := screenRules.Detect(detect.Input{Screen: string(screen) + "\n\n\n"})
		if got.State != c.state || got.ID != c.rule {
			t.Errorf("%s: %s by %q, want %s by %q", c.file, got.State, got.ID, c.state, c.rule)
		}
	}
}

func TestAgysQuestionAndSpinner(t *testing.T) {
	for _, c := range []struct {
		name, screen string
		state        detect.State
		rule         string
	}{
		{"question", "Which one?\n> A\n  B\n  ↑/↓ Navigate · enter Select · esc Skip\nesc to cancel", detect.Blocked, "question_prompt"},
		// A spinner over the box, with its footer cut off.
		{"spinner", "> fix it\n⣾  Generating...\n────\n>\n────", detect.Working, "spinner_working"},
	} {
		got := screenRules.Detect(detect.Input{Screen: c.screen})
		if got.State != c.state || got.ID != c.rule {
			t.Errorf("%s: %s by %q, want %s by %q", c.name, got.State, got.ID, c.state, c.rule)
		}
	}
}

// Its dialogs mark the option picked with ">", which the window picks
// from as it does Claude's; another agent's ">" is no cursor.
func TestAgysDialogsArePicked(t *testing.T) {
	for _, c := range []struct {
		file    string
		options int
	}{
		{"screen_permission.txt", 4},
		{"screen_trust.txt", 2},
	} {
		screen, err := os.ReadFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}
		choice, ok := screenRules.ParseChoice(string(screen), 120)
		if !ok || len(choice.Options) != c.options || choice.Selected != 0 {
			t.Errorf("%s: %+v %v, want %d options, the first picked", c.file, choice, ok, c.options)
		}
		if _, ok := detect.ParseChoice(string(screen), 120); ok {
			t.Errorf("%s: read with another agent's cursors", c.file)
		}
	}
}
