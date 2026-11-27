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
		hold         bool
	}{
		{"question", "Which one?\n> A\n  B\n  ↑/↓ Navigate · enter Select · esc Skip\nesc to cancel", detect.Blocked, "question_prompt", false},
		{"question_partial_nav", "Question 2/5: Scope\n> 1. Keep\n  2. Change\n               · ← Back · → Next · enter Select · esc Skip · ctrl+r Review", detect.Blocked, "question_prompt", false},
		{"question_multiselect", "Question 3/5: Limits\n> 1. [ ] None\n  2. [ ] No libs\n  space Toggle · enter Submit · esc Skip · ctrl+r Review\nesc to cancel", detect.Blocked, "question_prompt", false},
		{"question_writein", "Question 1/4: Mode\n> 4. Write-in...\n  Write-in · esc Skip · ctrl+r Review\nesc to cancel", detect.Blocked, "question_prompt", false},
		{"question_submit_all", "Question 4/4: Alias\n> 1. Keep\n  2. Drop\n\n                          enter Submit All", detect.Blocked, "question_prompt", false},
		{"stacked_statusline_idle", "────\n>\n────\n? for shortcuts   auto · Gemini 4 Neon · xhigh\ncustom status line 1\ncustom status line 2", detect.Idle, "prompt_footer", false},
		{"stacked_statusline_working", "⣾  Editing files...\n────\n>\n────\nesc to cancel   auto · Gemini 4 Neon · xhigh\ncustom status line", detect.Working, "esc_cancel_footer_working", false},
		{"skills_overlay", "Skills\n10 skills\n> mako\nKeyboard: ↑/↓ Navigate  enter View  esc Cancel\nesc to cancel   auto · Gemini 4 Neon · xhigh", detect.Unknown, "overlay_view", true},
		{"resume_overlay", "Conversations\n> Fix bug\n[1-10 of 34 items]\nKeyboard: ↑/↓ Navigate  ←/→ Page  enter Resume  esc Go back", detect.Unknown, "overlay_view", true},
		// A spinner over the box, with its footer cut off.
		{"spinner", "> fix it\n⣾  Generating...\n────\n>\n────", detect.Working, "spinner_working", false},
	} {
		got := screenRules.Detect(detect.Input{Screen: c.screen})
		if got.State != c.state || got.ID != c.rule || got.Hold != c.hold {
			t.Errorf("%s: %s by %q (hold=%v), want %s by %q (hold=%v)", c.name, got.State, got.ID, got.Hold, c.state, c.rule, c.hold)
		}
	}
}

func TestAgysRecallOverlays(t *testing.T) {
	a := New()
	skills := "Skills\n10 skills\n> mako\nKeyboard: ↑/↓ Navigate  enter View  esc Cancel\nesc to cancel   auto · Gemini 4 Neon · xhigh\n"
	if got := string(a.Recall(skills)); got != "\x1b" {
		t.Fatalf("Recall(skills) = %q, want ESC", got)
	}
	resume := "Conversations\n> Fix bug\nKeyboard: ↑/↓ Navigate  ←/→ Page  enter Resume  esc Go back\n"
	if got := string(a.Recall(resume)); got != "\x1b" {
		t.Fatalf("Recall(resume) = %q, want ESC", got)
	}
	// Do not dismiss active prompts or dialogs awaiting user choice.
	for name, screen := range map[string]string{
		"idle":       "────\n>\n────\n? for shortcuts   auto · Gemini 4 Neon · xhigh\n",
		"permission": "Requesting permission\n> 1. Allow\n  2. Deny\n  ↑/↓ Navigate · tab Amend\nesc to cancel\n",
		"question":   "Question 1/2: Pick\n> 1. A\n  2. B\n  ↑/↓ Navigate · enter Select · esc Skip\nesc to cancel\n",
	} {
		if got := a.Recall(screen); got != nil {
			t.Errorf("Recall(%s) = %q, want nil", name, got)
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
