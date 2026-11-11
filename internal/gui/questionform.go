package gui

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// An agent's question tool is answered here, in a form, as its own dialog
// in the terminal would take it: an option picked, several where the
// question allows, or words of the user's own. Claude's waits on its hook,
// as an approval does, which the answers then answer; Codex's waits in its
// terminal, where the answers are typed into its dialog.

// questionForm asks questions a step at a time and gives submit the
// answers, one for each, in order.
func questionForm(questions []*protocol.Question, submit func([]*protocol.QuestionAnswer)) ggui.Widget {
	items := make([]ui.Question, len(questions))
	for i, q := range questions {
		item := ui.Question{
			Name: strconv.Itoa(i), Title: q.GetQuestion(), Description: q.GetHeader(),
			Required: true, Multiple: q.GetMultiSelect(),
			InputLabel: T("Other"), Placeholder: T("Answer in your own words"),
		}
		for j, o := range q.GetOptions() {
			item.Choices = append(item.Choices, ui.QuestionOption{Value: strconv.Itoa(j), Label: o.GetLabel(), Description: o.GetDescription()})
		}
		items[i] = item
	}
	answers := ggui.State(ui.QuestionAnswers{})
	return ui.Questionnaire(answers, items...).
		Name(T("The agent's questions")).
		Shortcuts(ui.QuestionNumbers).
		SubmitLabel(T("Answer")).
		OnSubmit(func(got ui.QuestionAnswers) { submit(formAnswers(len(questions), got)) })
}

// formAnswers are the form's answers to n questions as the protocol's.
func formAnswers(n int, got ui.QuestionAnswers) []*protocol.QuestionAnswer {
	out := make([]*protocol.QuestionAnswer, n)
	for i := range out {
		a := got[strconv.Itoa(i)]
		answer := &protocol.QuestionAnswer{Text: strings.TrimSpace(a.Text)}
		for _, v := range a.Values {
			if j, err := strconv.Atoi(v); err == nil {
				answer.Picked = append(answer.Picked, int32(j))
			}
		}
		out[i] = answer
	}
	return out
}

// questionCard frames a form of questions at the foot of Chat, with what
// else answers them: the terminal, or declining.
func questionCard(form ggui.Widget, actions ...ggui.Widget) ggui.Widget {
	head := ggui.Row(append([]ggui.Widget{
		icon("message-square").Size(16).Color(skin.DotWaiting),
		ggui.Text(T("The agent asks")).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
		ggui.Spacer(),
	}, actions...)...).Gap(8).Align(ggui.AlignCenter)
	return approvalFrame(ggui.Column(head, form).Gap(12).Stretch())
}

// answerQuestion types answers to the questions session id's agent shows
// in its terminal, calling failed if they could not be.
func (m *model) answerQuestion(id string, questions []*protocol.Question, answers []*protocol.QuestionAnswer, failed func()) {
	info, ok := m.session(id)
	if !ok {
		return
	}
	req := &protocol.AnswerQuestion{NodeId: m.remoteID(info.GetNodeId()), SessionId: id, Questions: questions, Answers: answers}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
		return c.AnswerQuestion(ctx, req)
	}, func(_ *protocol.SessionInfo, err error) {
		if err != nil {
			m.connErr(err)
			failed()
		}
		m.link.pollNow()
	})
}

// inTerminalButton hands a question to the agent's own terminal.
func inTerminalButton(onTap func()) ggui.Widget {
	return ui.Button(T("In terminal"), onTap).Ghost().Name(T("Answer in the terminal"))
}
