package agy

import (
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/detect"
)

// shiftTab cycles agy's execution mode at its prompt (auto, accept-edits, plan).
const shiftTab = "\x1b[Z"

// promptFooterRules matches agy's prompt footer ("? for shortcuts ...").
var promptFooterRules = detect.MustCompile([]detect.Rule{
	{
		ID: "prompt_footer", State: detect.Idle, Priority: 90, Region: detect.Bottom(1),
		Lines: []string{`^\s*\? for shortcuts\b`},
	},
})

// Modes lists agy's permission/execution modes using Mako's canonical mode names.
func (*Agent) Modes() []string { return []string{"auto", "acceptEdits", "plan"} }

// ModeKey is shift+tab, which cycles execution modes in agy's TUI.
func (*Agent) ModeKey() []byte { return []byte(shiftTab) }

// ScreenMode reads the active execution mode from agy's prompt footer
// ("? for shortcuts    auto · Gemini 4 Hydrogen · medium").
func (*Agent) ScreenMode(screen string) string {
	trimmed := strings.TrimRight(screen, " \t\r\n")
	if promptFooterRules.Detect(detect.Input{Screen: trimmed}).ID == "" {
		return ""
	}
	last := trimmed
	if i := strings.LastIndexByte(trimmed, '\n'); i >= 0 {
		last = trimmed[i+1:]
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(last), "? for shortcuts"))
	if rest == "" {
		return ""
	}
	mode, _, _ := strings.Cut(rest, "·")
	switch strings.TrimSpace(mode) {
	case "auto":
		return "auto"
	case "accept-edits":
		return "acceptEdits"
	case "plan":
		return "plan"
	}
	return ""
}

// Models returns nil because agy's model list is discovered dynamically via Catalog.
func (*Agent) Models() []string { return nil }

// Efforts lists the reasoning effort levels accepted by agy's /effort command.
func (*Agent) Efforts() []string { return slices.Clone(agyEfforts) }

// ModelCommand switches agy's active model at the prompt.
func (*Agent) ModelCommand(model string) string { return "/model " + model }

// EffortCommand switches agy's active reasoning effort at the prompt.
func (*Agent) EffortCommand(effort string) string { return "/effort " + effort }

// ConfirmKeys: agy switches models and efforts without a confirmation modal.
func (*Agent) ConfirmKeys(string) []byte { return nil }
