package claude

import (
	"regexp"
	"strings"

	"github.com/ironpark/mako/internal/detect"
)

// Claude Code shows, over its own prompt, views where text typed does not
// reach it: its detailed transcript (ctrl+o), which takes no text, and a
// background agent's, whose prompt messages that agent. Escape, which
// would leave the latter, stops the agent shown, so its list of agents
// under the prompt is walked instead, to main.

// Keys, as Claude Code reads them.
const (
	keyUp    = "\x1b[A"
	keyDown  = "\x1b[B"
	keyEnter = "\r"
	ctrlO    = "\x0f"
)

// agentRow is a row of the agents' list: "⏺" at the agent shown, "◯" at
// the others, "❯" before the one picked while the list has the keys.
var agentRow = regexp.MustCompile(`^\s*(❯)?\s*([⏺◯])\s+(\S.*?)\s*$`)

// recallRules are the views over the prompt the screen rules know.
var recallRules = detect.MustCompile([]detect.Rule{transcriptViewer})

// Recall is what takes Claude Code a step back to its own prompt from the
// screen shown: out of its transcript, or toward main in the agents' list,
// which is under the prompt box's lower rule.
func (*Agent) Recall(screen string) []byte {
	in := detect.Input{Screen: strings.TrimRight(screen, " \t\r\n")}
	if recallRules.Detect(in).ID != "" {
		return []byte(ctrlO)
	}
	below := detect.AfterRule(in)
	if below == in.Screen {
		return nil // no rule, no prompt box
	}
	keys, mainAt, mainShown, pickedAt := false, -1, false, -1
	for i, l := range strings.Split(below, "\n") {
		if strings.Contains(l, "to select") {
			keys = true // its footer, while the list has the keys
		}
		m := agentRow.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if m[1] != "" {
			pickedAt = i
		}
		if m[3] == "main" {
			mainAt, mainShown = i, m[2] == "⏺"
		}
	}
	switch {
	case mainAt < 0 || mainShown:
		return nil // no agents' list, or main shown: text typed reaches it
	case !keys:
		return []byte(keyDown) // from the prompt into the list
	case pickedAt == mainAt:
		return []byte(keyEnter) // view main
	default:
		return []byte(keyUp)
	}
}
