package codex

import (
	"strings"

	"github.com/ironpark/gostty/input"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/detect"
	"github.com/ironpark/mako/internal/protocol"
)

// Codex shows, over its own prompt, views where text typed does not reach
// it: its agents' command center (←), whose keys stop and delete tasks;
// a sub-agent's thread (⌥→), whose input is disabled; its shortcuts; and,
// in older versions, its transcript (ctrl+t). Escape, which would leave a
// sub-agent's thread, interrupts it there, so ⌥← goes back instead, as it
// does only from an empty prompt.

// Keys, as Codex reads them.
const (
	keyEscape  = "\x1b"
	keyAltLeft = "\x1b[1;3D"
	keyQuit    = "q"
)

// recallRules are the views over the prompt, each left by recallKeys.
var recallRules = detect.MustCompile([]detect.Rule{
	transcriptViewer,
	{
		ID: "command_center", Priority: 900, Region: detect.Whole,
		Gate: detect.Gate{Regex: []string{`\A\s*Agent command center\b`, `esc back[^\n]*\z`}},
	},
	{
		ID: "shortcuts", Priority: 900, Region: detect.Bottom(1),
		Gate: detect.Gate{Regex: []string{`^\s*\?\s*/\s*esc close\s*\z`}},
	},
	{
		// The empty prompt of a sub-agent's thread.
		ID: "sub_agent", Priority: 900, Region: detect.Bottom(6),
		Gate: detect.Gate{Lines: []string{`^\s*›\s*Viewing sub-agent\b.*direct input is disabled`}},
	},
})

// recallKeys leave each of recallRules' views.
var recallKeys = map[string]string{
	"transcript_viewer": keyQuit,
	"command_center":    keyEscape,
	"shortcuts":         keyEscape,
	"sub_agent":         keyAltLeft,
}

// askingRules tell request_user_input's dialog.
var askingRules = detect.MustCompile([]detect.Rule{{ID: "asking", Region: detect.Bottom(3), Gate: detect.Gate{Any: questionDialog}}})

// Recall is what takes Codex a step back to its own prompt from the
// screen shown.
func (*Agent) Recall(screen string) []byte {
	if keys, ok := recallKeys[recallRules.Detect(detect.Input{Screen: screen}).ID]; ok {
		return []byte(keys)
	}
	return nil
}

// Asking reports whether the screen shows request_user_input's dialog.
func (*Agent) Asking(screen string) bool {
	return askingRules.Detect(detect.Input{Screen: screen}).ID != ""
}

// AnswerKeys answer request_user_input's dialog, a question at a time
// from its first, each with its first option marked: the option picked
// marked with ↓, Codex's own "None of the above" last of them for words
// of the user's own alone, which go in its notes (tab), and Enter, which
// goes on to the next question or, at the last, submits them all. A
// question takes one option; the first picked stands.
func (*Agent) AnswerKeys(questions []*protocol.Question, answers []*protocol.QuestionAnswer) []agent.Keystroke {
	var steps []agent.Keystroke
	for i, q := range questions {
		if i >= len(answers) {
			break
		}
		a := answers[i]
		pick := len(q.GetOptions()) // none of the above
		if p := a.GetPicked(); len(p) > 0 && int(p[0]) < len(q.GetOptions()) && p[0] >= 0 {
			pick = int(p[0])
		}
		var keys []input.Key
		for range pick {
			keys = append(keys, input.KeyArrowDown)
		}
		text := strings.TrimSpace(a.GetText())
		if text == "" {
			steps = append(steps, agent.Keystroke{Keys: append(keys, input.KeyEnter)})
			continue
		}
		steps = append(steps,
			agent.Keystroke{Keys: append(keys, input.KeyTab)},
			agent.Keystroke{Text: text, Keys: []input.Key{input.KeyEnter}},
		)
	}
	return steps
}
