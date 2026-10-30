package protocol

import "testing"

func TestTitlesLoseTheirSpinner(t *testing.T) {
	for in, want := range map[string]string{
		"✳ Fix the login": "Fix the login",
		"⠋ Thinking":      "Thinking",
		"◐ ◑ 제외된 항목":      "제외된 항목",
		"* Claude Code":   "Claude Code",
		"plain title":     "plain title",
		"v1.2 · release":  "v1.2 · release",
	} {
		if got := BareTitle(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
