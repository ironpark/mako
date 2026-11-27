package agy

import (
	"slices"
	"strings"

	"github.com/ironpark/gostty/input"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/detect"
	"github.com/ironpark/mako/internal/protocol"
)

// askingRules match agy's ask_question choice dialog (both single-question
// and multi-question, single-select and multi-select), tolerating a stacked
// custom statusLine beneath agy's default footer.
var askingRules = detect.MustCompile([]detect.Rule{{
	ID:       "asking",
	Region:   detect.Bottom(8),
	Contains: []string{"navigate", "esc skip"},
}})

// Asking reports whether the screen shows agy's ask_question choice dialog.
func (*Agent) Asking(screen string) bool {
	return askingRules.Detect(detect.Input{Screen: screen}).ID != ""
}

// singleLineWriteIn flattens line breaks in custom write-in text so pasting it
// into agy's single-line "Your answer:" prompt never submits early.
var singleLineWriteIn = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")

// AnswerKeys answers agy's ask_question dialog a question at a time from its
// first, each starting with its first option highlighted and "Write-in..."
// immediately after the last option:
//   - Single-select: move to the chosen option with ↓ and press Enter (which
//     advances to the next question or submits all on the last). Custom text
//     moves to "Write-in...", presses Enter to open the text box, then pastes
//     the text and presses Enter.
//   - Multi-select: traverse picked options in ascending order with ↓, toggle
//     each with Space, and press Enter (or move to "Write-in...", press Enter,
//     then paste custom text and press Enter to combine both).
func (*Agent) AnswerKeys(questions []*protocol.Question, answers []*protocol.QuestionAnswer) []agent.Keystroke {
	var steps []agent.Keystroke
	for i, q := range questions {
		if i >= len(answers) {
			break
		}
		a := answers[i]
		n := len(q.GetOptions())
		picked := sortedValidPicks(a.GetPicked(), n)
		text := strings.TrimSpace(singleLineWriteIn.Replace(a.GetText()))

		if q.GetMultiSelect() {
			var keys []input.Key
			cur := 0
			for _, idx := range picked {
				keys = appendDown(keys, idx-cur)
				keys = append(keys, input.KeySpace)
				cur = idx
			}
			if text == "" {
				steps = append(steps, agent.Keystroke{Keys: append(keys, input.KeyEnter)})
				continue
			}
			keys = appendDown(keys, n-cur)
			steps = append(steps,
				agent.Keystroke{Keys: append(keys, input.KeyEnter)},
				agent.Keystroke{Text: text, Keys: []input.Key{input.KeyEnter}},
			)
			continue
		}

		if text != "" {
			writeIn := strings.TrimSpace(singleLineWriteIn.Replace(agent.AnswerText(q, a)))
			steps = append(steps,
				agent.Keystroke{Keys: append(appendDown(nil, n), input.KeyEnter)},
				agent.Keystroke{Text: writeIn, Keys: []input.Key{input.KeyEnter}},
			)
			continue
		}
		if len(picked) == 0 {
			continue
		}
		steps = append(steps, agent.Keystroke{Keys: append(appendDown(nil, picked[0]), input.KeyEnter)})
	}
	return steps
}

func sortedValidPicks(raw []int32, n int) []int {
	var out []int
	for _, p := range raw {
		if p >= 0 && int(p) < n {
			out = append(out, int(p))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func appendDown(keys []input.Key, n int) []input.Key {
	for range n {
		keys = append(keys, input.KeyArrowDown)
	}
	return keys
}
