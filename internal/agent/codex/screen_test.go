package codex

import (
	"testing"

	"github.com/ironpark/mako/internal/detect"
)

func TestCodexsScreen(t *testing.T) {
	for _, c := range []struct {
		name  string
		in    detect.Input
		state detect.State
		rule  string
	}{
		{"at its prompt", detect.Input{Title: "mako", Screen: "• Done.\n\n› Ask Codex to do anything\n\n  100% context left"}, detect.Idle, "title_idle"},
		{"at work", detect.Input{Title: "⠙ mako", Screen: "› fix it\n\n• Working (3s • esc to interrupt)\n\n› \n"}, detect.Working, "title_working"},
		{"at work, untitled", detect.Input{Screen: "› fix it\n\n• Working (1m 3s • esc to interrupt)\n\n› Ask Codex"}, detect.Working, "turn_timer"},
		{"done, untitled", detect.Input{Screen: "› fix it\n\n• Fixed it.\n\n› Ask Codex"}, detect.Unknown, ""},
		{"asking", detect.Input{Title: "Action Required", Screen: "Allow command?\n  go test ./..."}, detect.Blocked, "title_blocked"},
		{"trusting a folder", detect.Input{Screen: "> You are in /tmp/x\n\n  Do you trust the contents of this directory?\n\n› 1. Yes, continue\n  2. No, quit"}, detect.Blocked, "trust_directory"},
	} {
		got := screenRules.Detect(c.in)
		if got.State != c.state || got.ID != c.rule {
			t.Errorf("%s: %s by %q, want %s by %q", c.name, got.State, got.ID, c.state, c.rule)
		}
	}
}

func TestCodexsPromptRegions(t *testing.T) {
	for _, c := range []struct {
		name, got, want string
	}{
		{"after prompt", afterPrompt(detect.Input{Screen: "› hi\n• done\n› \nfoot"}), "foot"},
		{"before prompt", beforePrompt(detect.Input{Screen: "• done\n› \nfoot"}), "• done"},
		{"replied prompt", withoutPrompt(detect.Input{Screen: "› hi\n• reply"}), "› hi\n• reply"},
		{"current prompt", withoutPrompt(detect.Input{Screen: "• reply\n› "}), ""},
	} {
		if c.got != c.want {
			t.Errorf("%s: %q, want %q", c.name, c.got, c.want)
		}
	}
}
