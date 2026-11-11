package claude

import (
	"encoding/json"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// AnswerReply answers AskUserQuestion from its PermissionRequest hook: the
// call allowed, its input given the answers, by each question's text, as
// Claude Code takes them from its own dialog. Options picked and words of
// the user's own go joined by ", ", as its dialog joins them.
func (*Agent) AnswerReply(input json.RawMessage, questions []*protocol.Question, answers []*protocol.QuestionAnswer) []byte {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil || in == nil {
		in = map[string]any{}
	}
	given := map[string]any{}
	for i, q := range questions {
		if i < len(answers) {
			given[q.GetQuestion()] = agent.AnswerText(q, answers[i])
		}
	}
	in["answers"] = given
	return agent.PermissionDecision(map[string]any{"behavior": protocol.DecisionAllow, "updatedInput": in})
}
