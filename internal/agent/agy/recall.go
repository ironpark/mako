package agy

import (
	"strings"

	"github.com/ironpark/mako/internal/detect"
)

// agy shows full-screen and modal picker views over its prompt (such as
// /skills, /resume Conversations, and tabbed settings views) whose footer
// begins with "Keyboard:" and offers Escape to return to the prompt.
const keyEscape = "\x1b"

var recallRules = detect.MustCompile([]detect.Rule{overlayView})

// Recall returns the key sequence that steps agy back from an overlay view
// to its prompt, or nil when already at the prompt or in a dialog awaiting
// a user decision.
func (*Agent) Recall(screen string) []byte {
	trimmed := strings.TrimRight(screen, " \t\r\n")
	if trimmed == "" {
		return nil
	}
	if recallRules.Detect(detect.Input{Screen: trimmed}).ID != "" {
		return []byte(keyEscape)
	}
	return nil
}
