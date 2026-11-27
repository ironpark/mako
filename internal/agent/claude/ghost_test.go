package claude

import (
	"strings"
	"testing"
)

// formattedScreen is a prompt box as the daemon's terminal formats Claude
// Code 2.1's screen (gostty VT format, unwrapped): each row reset first, the
// rules colored, the marker followed by a no-break space, and unstyled gaps
// written as cursor-forward sequences.
func formattedScreen(promptLine string) string {
	rule := "\x1b[0m\x1b[38;5;244m" + strings.Repeat("─", 120) + "\x1b[0m"
	return strings.Join([]string{
		"\x1b[0m\x1b[38;5;174m ▐\x1b[0m\x1b[1mClaude Code\x1b[1C\x1b[0m\x1b[38;5;246mv2.1.332\x1b[0m",
		"",
		rule,
		"❯ \x1b[0m" + promptLine,
		rule,
		"\x1b[2C\x1b[0m\x1b[38;5;220m⏵⏵ auto mode on\x1b[0m\x1b[38;5;246m (shift+tab to cycle)\x1b[0m\x1b[0m",
	}, "\r\n")
}

func TestExtractGhost(t *testing.T) {
	var a Agent
	rule := "──────────────────────────────────────────────────"

	cases := []struct {
		name   string
		screen string
		want   string
	}{
		{
			name:   "formatted screen with no-break space after the marker",
			screen: formattedScreen("\x1b[2mRun the unit tests and commit\x1b[0m"),
			want:   "Run the unit tests and commit",
		},
		{
			name:   "formatted screen hint after a cursor-forward gap",
			screen: formattedScreen("\x1b[2mRun the tests\x1b[0m\x1b[40C\x1b[2m↵ send\x1b[0m"),
			want:   "Run the tests",
		},
		{
			name:   "cursor-forward inside the suggestion is a space",
			screen: formattedScreen("\x1b[2mRun\x1b[1Cthe tests\x1b[0m"),
			want:   "Run the tests",
		},
		{
			name:   "formatted screen example placeholder",
			screen: formattedScreen("\x1b[2mTry \"create a util logging.py that…\"\x1b[0m"),
			want:   "",
		},
		{
			name:   "formatted screen typed text",
			screen: formattedScreen("fix the failing test"),
			want:   "",
		},
		{
			name:   "formatted screen empty prompt",
			screen: formattedScreen(""),
			want:   "",
		},
		{
			name:   "pure dim suggestion",
			screen: "All tests pass.\n" + rule + "\n❯ \x1b[2mAdd edge-case tests for empty input\x1b[0m\n" + rule + "\n  ? for shortcuts",
			want:   "Add edge-case tests for empty input",
		},
		{
			name:   "ink inverse cursor cell on first char plus dim rest",
			screen: "Done.\n" + rule + "\n❯ \x1b[7mR\x1b[27m\x1b[2mun go vet and commit\x1b[0m\n" + rule + "\n  ? for shortcuts",
			want:   "Run go vet and commit",
		},
		{
			name:   "wrapped suggestion continues on the next row",
			screen: "Done.\n" + rule + "\n❯ \x1b[2mAdd tests for the empty input case\x1b[0m\n  \x1b[2mand the unicode case\x1b[0m\n" + rule + "\n  ? for shortcuts",
			want:   "Add tests for the empty input case and the unicode case",
		},
		{
			name:   "strips right-aligned shortcut hint",
			screen: "Done.\n" + rule + "\n❯ \x1b[2mRun the tests          ↵ send\x1b[0m\n" + rule + "\n  ? for shortcuts",
			want:   "Run the tests",
		},
		{
			name:   "ignores static Try placeholder",
			screen: "Welcome\n" + rule + "\n❯ \x1b[2mTry \"refactor main.go\"\x1b[0m\n" + rule + "\n  ? for shortcuts",
			want:   "",
		},
		{
			name:   "ignores composer state placeholder",
			screen: "Welcome\n" + rule + "\n❯ \x1b[2mDescribe something to build, change, or fix\x1b[0m\n" + rule + "\n  ? for shortcuts",
			want:   "",
		},
		{
			name:   "ignores normal user-typed text",
			screen: "Done.\n" + rule + "\n❯ fix the failing test\n" + rule + "\n  ? for shortcuts",
			want:   "",
		},
		{
			name:   "ignores typed text followed by dim text",
			screen: "Done.\n" + rule + "\n❯ fix \x1b[2mthe failing test\x1b[0m\n" + rule + "\n  ? for shortcuts",
			want:   "",
		},
		{
			name:   "ignores truecolor RGB 38;2;r;g;b user text without SGR dim",
			screen: "Done.\n" + rule + "\n❯ \x1b[38;2;200;200;200muser typed text\x1b[0m\n" + rule + "\n  ? for shortcuts",
			want:   "",
		},
		{
			name:   "ignores permission dialog",
			screen: "Do you want to proceed?\n" + rule + "\n❯ 1. Yes\n  2. No\n" + rule + "\n  Esc to cancel",
			want:   "",
		},
		{
			name:   "empty prompt returns empty",
			screen: "Done.\n" + rule + "\n❯ \n" + rule + "\n  ? for shortcuts",
			want:   "",
		},
		{
			name:   "no prompt box",
			screen: "Compiling...\nstill working",
			want:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.ExtractGhost(tc.screen); got != tc.want {
				t.Fatalf("ExtractGhost() = %q, want %q", got, tc.want)
			}
		})
	}
}
