package session

import (
	"slices"
	"testing"
)

// A session's program sees the terminal it runs in, not the one the daemon
// was started from.
func TestTerminalEnvNamesTheSessionsTerminal(t *testing.T) {
	got := terminalEnv([]string{"HOME=/home/me", "TERM=xterm-kitty", "TERM_PROGRAM=vscode", "TERM_PROGRAM_VERSION=1.99", "LC_TERMINAL=iTerm2"})
	for _, want := range []string{"HOME=/home/me", "TERM=xterm-256color", "TERM_PROGRAM=ghostty", "TERM_PROGRAM_VERSION=" + ghosttyVersion, "COLORTERM=truecolor"} {
		if !slices.Contains(got, want) {
			t.Errorf("env lacks %s: %q", want, got)
		}
	}
	for _, gone := range []string{"TERM=xterm-kitty", "TERM_PROGRAM=vscode", "TERM_PROGRAM_VERSION=1.99", "LC_TERMINAL=iTerm2"} {
		if slices.Contains(got, gone) {
			t.Errorf("env keeps %s", gone)
		}
	}
}
