package agy

import (
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/detect"
)

// shiftTab cycles agy's execution mode at its prompt (auto, accept-edits, plan).
const shiftTab = "\x1b[Z"

// Modes lists agy's permission/execution modes using Mako's canonical mode names.
func (*Agent) Modes() []string { return []string{"auto", "acceptEdits", "plan"} }

// ModeKey is shift+tab, which cycles execution modes in agy's TUI.
func (*Agent) ModeKey() []byte { return []byte(shiftTab) }

// ScreenMode reads the active execution mode from agy's prompt footer
// ("? for shortcuts    auto · Gemini 4 Hydrogen · medium"), tolerating a
// stacked custom statusLine beneath it and recognizing default mode when agy
// omits the mode prefix ("? for shortcuts    Gemini 4 Neon · high").
func (*Agent) ScreenMode(screen string) string {
	trimmed := strings.TrimRight(screen, " \t\r\n")
	if trimmed == "" {
		return ""
	}
	detected := screenRules.Detect(detect.Input{Screen: trimmed})
	if detected.ID != "prompt_footer" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	floor := max(0, len(lines)-6)
	var footer string
	for i := len(lines) - 1; i >= floor; i-- {
		if promptFooterRe.MatchString(lines[i]) {
			footer = lines[i]
			break
		}
	}
	if footer == "" {
		return ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(footer), "? for shortcuts"))
	if rest == "" {
		return ""
	}
	mode, _, _ := strings.Cut(rest, "·")
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto":
		return "auto"
	case "accept-edits":
		return "acceptEdits"
	case "plan":
		return "plan"
	case "bypass", "bypass-permissions", "yolo":
		return "bypassPermissions"
	case "default", "ask", "manual":
		return "default"
	default:
		// In default/manual mode agy prints the model right after "? for shortcuts"
		// with no mode prefix (e.g. "? for shortcuts   Gemini 4 Neon · high").
		return "default"
	}
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
