package claude

import "testing"

// The mode is read off the line under the prompt box; none named is the
// default, and no prompt box, as under a dialog, tells none.
func TestScreenMode(t *testing.T) {
	box := "⏺ Done.\n────────────────\n❯ \n────────────────\n"
	for _, c := range []struct{ foot, want string }{
		{"  ⏵⏵ accept edits on (shift+tab to cycle)", "acceptEdits"},
		{"  ⏸ plan mode on (shift+tab to cycle) · ← for agents", "plan"},
		{"  ⏵⏵ auto mode on (shift+tab to cycle)                    ◐ medium · /effort", "auto"},
		{"  ⏸ manual mode on", "default"},
		{"  ⏵⏵ bypass permissions on (shift+tab to cycle)", "bypassPermissions"},
		{"  ? for shortcuts", "default"},
	} {
		if got := (&Agent{}).ScreenMode(box + c.foot); got != c.want {
			t.Errorf("%q: %q, want %q", c.foot, got, c.want)
		}
	}
	dialog := " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel"
	if got := (&Agent{}).ScreenMode(dialog); got != "" {
		t.Errorf("under a dialog: %q", got)
	}
}

// Claude asks before it leaves the model a conversation is cached for;
// yes is typed to that, and to nothing else.
func TestConfirmKeys(t *testing.T) {
	ask := "⏺ ok\n\n   Switch model?\n   Your next response will be slower and use more tokens\n\n" +
		"   This conversation is cached for the current model. Switching to Fable 5.2 means the full history gets re-read.\n\n"
	if got := string((&Agent{}).ConfirmKeys(ask + "   ❯ 1. Yes, switch to Fable 5.2\n     2. No, go back\n")); got != keyEnter {
		t.Errorf("at yes: %q", got)
	}
	if got := string((&Agent{}).ConfirmKeys(ask + "     1. Yes, switch to Fable 5.2\n   ❯ 2. No, go back\n")); got != keyUp+keyEnter {
		t.Errorf("at no: %q", got)
	}
	other := " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel"
	if got := (&Agent{}).ConfirmKeys(other); got != nil {
		t.Errorf("another dialog: %q", got)
	}
}
