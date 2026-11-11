package claude

import (
	"regexp"
	"strings"

	"github.com/ironpark/mako/internal/detect"
)

// Claude Code shows its permission mode under its prompt box, and moves on
// to the next on shift+tab: manual ("default"), accept edits, plan, auto,
// and bypass for a session started allowed to. Its model and effort are set
// by /model and /effort, which also make them the user's defaults.

// shiftTab is shift+tab, as Claude Code reads it.
const shiftTab = "\x1b[Z"

// screenModes are the modes the line under the prompt box names.
var screenModes = []struct {
	line *regexp.Regexp
	mode string
}{
	{regexp.MustCompile(`(?m)^\s*⏵⏵\s+accept edits on\b`), "acceptEdits"},
	{regexp.MustCompile(`(?m)^\s*⏸\s+plan mode on\b`), "plan"},
	{regexp.MustCompile(`(?m)^\s*⏵⏵\s+auto mode on\b`), "auto"},
	{regexp.MustCompile(`(?m)^\s*⏸\s+manual mode on\b`), "default"},
	{regexp.MustCompile(`(?m)^\s*⏵⏵\s+bypass permissions on\b`), "bypassPermissions"},
}

// Modes are those shift+tab moves through for every session.
func (*Agent) Modes() []string { return []string{"default", "acceptEdits", "plan", "auto"} }

func (*Agent) ModeKey() []byte { return []byte(shiftTab) }

// ScreenMode is the mode named under the prompt box; with none named, as
// versions that name only the others draw it, the default.
func (*Agent) ScreenMode(screen string) string {
	in := detect.Input{Screen: strings.TrimRight(screen, " \t\r\n")}
	if promptRules.Detect(in).ID == "" {
		return "" // no prompt box: a dialog, a view over it
	}
	below := detect.AfterRule(in)
	for _, m := range screenModes {
		if m.line.MatchString(below) {
			return m.mode
		}
	}
	return "default"
}

// promptRules find the prompt box, under which the mode is named.
var promptRules = detect.MustCompile([]detect.Rule{promptBoxRule})

func (*Agent) Models() []string  { return []string{"opus", "sonnet", "haiku"} }
func (*Agent) Efforts() []string { return []string{"low", "medium", "high", "xhigh", "max"} }

func (*Agent) ModelCommand(model string) string   { return "/model " + model }
func (*Agent) EffortCommand(effort string) string { return "/effort " + effort }
