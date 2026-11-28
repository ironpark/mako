package codex

import (
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/detect"
)

// ScreenRules read what Codex shows: its title while it works or asks,
// its dialogs, the timer of a turn under way. They follow herdr's rules
// for Codex (github.com/herdrdev/herdr, Apache-2.0).
func (*Agent) ScreenRules() *detect.Rules { return screenRules }

// spinner is the title's spinner while Codex works.
const spinner = `(?:^| )[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏](?: |$)`

// askYes are a question with a yes or a choice marked.
var askYes = []detect.Gate{
	{Contains: []string{"do you want to"}, Any: []detect.Gate{{Contains: []string{"yes"}}, {Contains: []string{"❯"}}}},
	{Contains: []string{"would you like to"}, Any: []detect.Gate{{Contains: []string{"yes"}}, {Contains: []string{"❯"}}}},
}

// transcriptViewer is the transcript (ctrl+t) of older versions, over the
// prompt.
var transcriptViewer = detect.Rule{
	ID: "transcript_viewer", Priority: 1000, Region: afterPrompt, Hold: true,
	Contains: []string{"↑/↓ to scroll", "pgup/pgdn to", "home/end to jump", "q to quit"},
	Any:      []detect.Gate{{Contains: []string{"esc to edit prev"}}, {Contains: []string{"esc/← to edit prev"}}},
}

// sideOverlay is Codex's ephemeral /side (or /btw) overlay over the prompt.
var sideOverlay = detect.Rule{
	ID: "side_overlay", Priority: 920, Region: detect.Whole, Hold: true,
	Lines: []string{
		`(?i)^\s*(?:[│┃]\s*)?(?:/side(?:\s|$)|/btw(?:\s|$)|side\s+conversation\b)`,
		`(?i)esc\s+to\s+(?:close|dismiss|return)\b`,
	},
}

// questionDialog are the footers of request_user_input's dialog.
var questionDialog = []detect.Gate{
	{Contains: []string{"enter to submit answer"}},
	{Contains: []string{"enter to submit all"}},
}

var screenRules = detect.MustCompile([]detect.Rule{
	{
		ID: "title_blocked", State: detect.Blocked, Priority: 1100, Region: detect.Title,
		Contains: []string{"Action Required"},
	},
	{
		ID: "title_working", State: detect.Working, Priority: 1050, Region: detect.Title,
		Regex: []string{spinner},
	},
	transcriptViewer,
	sideOverlay,
	{
		ID: "trust_directory", State: detect.Blocked, Priority: 950, Region: detect.Top(20),
		All: []detect.Gate{
			{Any: []detect.Gate{
				{Regex: []string{`\A> You are in [^\r\n]+(?:\r?\n|$)`}},
				{Contains: []string{"Folder access"}},
			}},
			{Any: []detect.Gate{
				{Regex: []string{`(?s)Do\s+you\s+trust\s+the\s+contents\s+of\s+this\s+directory\?`}},
				{
					Contains: []string{"Trust this folder?", "Codex can read, edit, and run files here"},
					Any:      []detect.Gate{{Contains: []string{"Trust and continue"}}, {Contains: []string{"enter continue"}}},
				},
			}},
		},
	},
	{
		// The update picker, old and new; both its heading and its footer
		// stay on screen though its choices may scroll.
		ID: "startup_update", State: detect.Blocked, Priority: 950, Region: detect.Bottom(20),
		Any: []detect.Gate{
			{
				Contains: []string{"Update available!", "Update now"},
				Regex:    []string{`Skip\s+until\s+next\s+version`, `Press enter to continue\s*\z`},
			},
			{Regex: []string{`(?m)^\s*Update\s+available\s+·`, `enter\s+continue\s+·\s+esc\s+skip\s*\z`}},
		},
	},
	{
		ID: "dialog", State: detect.Blocked, Priority: 900, Region: afterPrompt,
		Any: slices.Concat(
			[]detect.Gate{{Contains: []string{"press enter to confirm or esc to cancel"}}},
			questionDialog,
			[]detect.Gate{
				{Contains: []string{"allow command?"}},
				{Contains: []string{"All Results", "Filesystem Only", "Plugins"}},
			},
		),
	},
	{
		// A question in place of the composer; a prompt still being typed,
		// its marker sparkling, is not one.
		ID: "question", State: detect.Blocked, Priority: 600, Region: withoutPrompt, Suggests: true,
		Not: []detect.Gate{{Regex: []string{`(?m)^›[⠁⠂⠄⠈⠐⠠⡀⢀][^\n]*(?:\n(?:[^•■✗✓\n][^\n]*)?)*\z`}}},
		Any: append([]detect.Gate{{Contains: []string{"[y/n]"}}, {Contains: []string{"yes (y)"}}}, askYes...),
	},
	{
		// A turn's live timer, "(12s • esc to interrupt)", with nothing it
		// replied below it but queued input; a reconnect that failed keeps
		// its last timer.
		ID: "turn_timer", State: detect.Working, Priority: 500, Region: beforePrompt,
		Any:   []detect.Gate{{Contains: []string{" to interrupt)"}}, {Contains: []string{"s)"}}},
		Regex: []string{`(?m)^(?:[•◦][ \t]+)?[^\s›•◦■✗✓─][^\r\n]* \((?:[0-9]+[hm] )*[0-9]+s(?: • [^\r\n]+? to interrupt)?\)(?: · [^\r\n]*)?(?:\r?\n(?:[^•◦›■✗✓─\r\n][^\r\n]*|•[ \t]+(?:Queued\s+follow-up\s+inputs|Messages\s+to\s+be\s+submitted\s+after\s+next\s+tool\s+call(?:\s+\(press\s+[^\r\n]+?\s+to\s+interrupt\s+and\s+send\s+immediately\))?|Messages\s+to\s+be\s+submitted\s+at\s+end\s+of\s+turn)|›[⠁⠂⠄⠈⠐⠠⡀⢀][^\r\n]*)?)*\s*\z`},
		Not:   []detect.Gate{{Lines: []string{`^(?:[•◦][ \t]+)?Reconnect failed — check the endpoint, then relaunch \([0-9hms ]+\)$`}}},
	},
	{
		ID: "title_idle", State: detect.Idle, Priority: 100, Region: detect.Title,
		Regex: []string{`\S`},
		Not:   []detect.Gate{{Regex: []string{spinner}}, {Contains: []string{"Action Required"}}},
	},
})

// Codex draws its composer after "› " and begins each block of its reply
// with a marker, "•" among them.

func promptLine(line string) bool { return line == "›" || strings.HasPrefix(line, "› ") }

func blockLine(line string) bool { return strings.IndexAny(line, "•■✗✓") == 0 }

// currentPrompt is the line of the last prompt, unless a reply began below
// it.
func currentPrompt(lines []string) (int, bool) {
	for i, line := range slices.Backward(lines) {
		if promptLine(line) {
			if slices.ContainsFunc(lines[i+1:], blockLine) {
				return 0, false
			}
			return i, true
		}
	}
	return 0, false
}

// afterPrompt is the screen below the last prompt line; the whole screen
// without one.
func afterPrompt(in detect.Input) string {
	lines := strings.Split(in.Screen, "\n")
	for i, line := range slices.Backward(lines) {
		if promptLine(line) {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return in.Screen
}

// beforePrompt is the screen above the current prompt; the whole screen
// without one.
func beforePrompt(in detect.Input) string {
	lines := strings.Split(in.Screen, "\n")
	if i, ok := currentPrompt(lines); ok {
		return strings.Join(lines[:i], "\n")
	}
	return in.Screen
}

// withoutPrompt is the whole screen when it has no current prompt, and ""
// when it has one: what a dialog in place of the composer shows.
func withoutPrompt(in detect.Input) string {
	if _, ok := currentPrompt(strings.Split(in.Screen, "\n")); ok {
		return ""
	}
	return in.Screen
}
