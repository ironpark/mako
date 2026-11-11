package gui

import (
	"context"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// An approval an agent asks for while the window is open waits for an
// answer here: a card at the foot of Chat, and a bar over the terminal.
// "In terminal" hands the question back to the agent's own prompt.

// approvalOfSession follows the approval a session's agent waits for.
func (m *model) approvalOfSession(id string) ggui.Readable[*protocol.Approval] {
	return ggui.Derived(func() *protocol.Approval {
		s, _ := sessionIn(m.Sessions.Get(), id)
		return s.GetApproval()
	}).WithEqual(func(a, b *protocol.Approval) bool { return a.GetId() == b.GetId() })
}

// answerApproval passes the user's decision to the agent, with the
// answers to its questions, if it asks any.
func (m *model) answerApproval(id, approval, decision string, answers ...*protocol.QuestionAnswer) {
	info, ok := m.session(id)
	if !ok {
		return
	}
	req := &protocol.AnswerApproval{NodeId: m.remoteID(info.GetNodeId()), SessionId: id, ApprovalId: approval, Decision: decision, Answers: answers}
	// Answered, the approval leaves at once rather than at the next poll.
	m.editSession(id, func(s *protocol.SessionInfo) {
		if s.GetApproval().GetId() == approval {
			s.Approval = nil
		}
	})
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
		return c.AnswerApproval(ctx, req)
	}, func(_ *protocol.SessionInfo, err error) {
		if err != nil {
			m.connErr(err)
		}
		m.link.pollNow()
	})
}

// connErr shows err, as the connection's last error.
func (m *model) connErr(err error) {
	c := ggui.Peek(m.Conn)
	m.Conn.Set(connState{OK: c.OK, Self: c.Self, Err: err.Error()})
}

// approvalButtons are an approval's answers.
func approvalButtons(m *model, id string, a *protocol.Approval) []ggui.Widget {
	answer := func(decision string) func() { return func() { m.answerApproval(id, a.GetId(), decision) } }
	return []ggui.Widget{
		inTerminalButton(answer(protocol.DecisionTerminal)),
		ui.Button(T("Deny"), answer(protocol.DecisionDeny)).Outline(),
		ui.Button(T("Allow"), answer(protocol.DecisionAllow)),
	}
}

// approvalCard is the approval at the foot of Chat: the tool and what it
// would run, its arguments folded away, and the answers.
func approvalCard(m *model, id string) ggui.Widget {
	return ggui.View(m.approvalOfSession(id), func(a *protocol.Approval) ggui.Widget {
		if a == nil {
			return ggui.Box()
		}
		if qs := a.GetQuestions(); len(qs) > 0 {
			answer := func(decision string) func() { return func() { m.answerApproval(id, a.GetId(), decision) } }
			return questionCard(questionForm(qs, func(answers []*protocol.QuestionAnswer) {
				m.answerApproval(id, a.GetId(), protocol.DecisionAllow, answers...)
			}),
				inTerminalButton(answer(protocol.DecisionTerminal)),
				ui.Button(T("Decline"), answer(protocol.DecisionDeny)).Outline().Name(T("Decline to answer")),
			)
		}
		open := ggui.State(false)
		head := ggui.Row(
			icon("shield-alert").Size(16).Color(skin.DotWaiting),
			ggui.Text(Tf("Allow %s?", a.GetTool())).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
			ggui.Spacer(),
			ggui.If(ggui.Const(a.GetInput() != ""), func() ggui.Widget {
				return ui.Button(T("Arguments"), func() { open.Set(!ggui.Peek(open)) }).Ghost()
			}),
		).Gap(8).Align(ggui.AlignCenter)
		rows := []ggui.Widget{head}
		if a.GetDetail() != "" {
			rows = append(rows, toolBlock(a.GetDetail(), false))
		}
		rows = append(rows,
			ggui.If(open, func() ggui.Widget { return ggui.Box(toolBlock(a.GetInput(), false)).MaxHeight(260) }),
			ggui.Row(append([]ggui.Widget{ggui.Spacer()}, approvalButtons(m, id, a)...)...).Gap(8),
		)
		card := ggui.Box(ggui.Column(rows...).Gap(10).Stretch()).
			Fill(skin.Surface1).Border(1, skin.DotWaiting).Radius(12).Pad(12, 14)
		return &lane{child: ggui.Box(card).Pad(0, 0, 8, 0)}
	})
}

// approvalBar is the approval over a terminal, in one line.
func approvalBar(m *model, id string) ggui.Widget {
	return ggui.View(m.approvalOfSession(id), func(a *protocol.Approval) ggui.Widget {
		if a == nil {
			return ggui.Box()
		}
		what := a.GetTool()
		if a.GetDetail() != "" {
			what += "  " + a.GetDetail()
		}
		glyph, ask, buttons := "shield-alert", T("Approve?"), approvalButtons(m, id, a)
		if qs := a.GetQuestions(); len(qs) > 0 {
			// Its form is in Chat; the terminal shows no dialog until
			// it is handed back.
			glyph, ask, what = "message-square", T("The agent asks"), qs[0].GetQuestion()
			buttons = []ggui.Widget{
				ui.Button(T("Answer in Chat"), func() { m.setChatMode(id, true) }).Outline(),
				inTerminalButton(func() { m.answerApproval(id, a.GetId(), protocol.DecisionTerminal) }),
			}
		}
		row := append([]ggui.Widget{
			icon(glyph).Size(15).Color(skin.DotWaiting),
			ggui.Text(ask).Size(12.5).Weight(ggui.WeightMedium).NoWrap(),
			ggui.Expanded(ggui.Text(what).Size(12.5).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()),
		}, buttons...)
		return ggui.Column(
			ggui.Box(ggui.Row(row...).Gap(10).Align(ggui.AlignCenter)).Pad(6, 12).Fill(skin.Surface2),
			hline(),
		).Stretch()
	})
}
