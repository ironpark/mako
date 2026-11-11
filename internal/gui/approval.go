package gui

import (
	"context"
	"encoding/json"
	"strings"
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

// answerApproval passes the user's answer to an approval of session id's
// agent: its decision, with why for a denial, the option allowed with, or
// the answers to its questions.
func (m *model) answerApproval(id string, req *protocol.AnswerApproval) {
	info, ok := m.session(id)
	if !ok {
		return
	}
	req.NodeId, req.SessionId = m.remoteID(info.GetNodeId()), id
	// Answered, the approval leaves at once rather than at the next poll.
	m.editSession(id, func(s *protocol.SessionInfo) {
		if s.GetApproval().GetId() == req.GetApprovalId() {
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

// decide answers approval a of session id with decision alone.
func (m *model) decide(id string, a *protocol.Approval, decision string) func() {
	return func() { m.answerApproval(id, &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: decision}) }
}

// connErr shows err, as the connection's last error.
func (m *model) connErr(err error) {
	c := ggui.Peek(m.Conn)
	m.Conn.Set(connState{OK: c.OK, Self: c.Self, Err: err.Error()})
}

// approvalButtons are an approval's answers over a terminal.
func approvalButtons(m *model, id string, a *protocol.Approval) []ggui.Widget {
	return []ggui.Widget{
		inTerminalButton(m.decide(id, a, protocol.DecisionTerminal)),
		ui.Button(T("Deny"), m.decide(id, a, protocol.DecisionDeny)).Outline(),
		ui.Button(T("Allow"), m.decide(id, a, protocol.DecisionAllow)),
	}
}

// approvalCard is the approval at the foot of Chat: what the call would do,
// as its kind shows it, the answers, and room to tell the agent why not.
func approvalCard(m *model, id string) ggui.Widget {
	return ggui.View(m.approvalOfSession(id), func(a *protocol.Approval) ggui.Widget {
		switch {
		case a == nil:
			return ggui.Box()
		case len(a.GetQuestions()) > 0:
			return questionCard(questionForm(a.GetQuestions(), func(answers []*protocol.QuestionAnswer) {
				m.answerApproval(id, &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: protocol.DecisionAllow, Answers: answers})
			}),
				inTerminalButton(m.decide(id, a, protocol.DecisionTerminal)),
				ui.Button(T("Decline"), m.decide(id, a, protocol.DecisionDeny)).Outline().Name(T("Decline to answer")),
			)
		case a.GetKind() == protocol.ToolExitPlan:
			return planCard(m, id, a)
		}
		open, why := ggui.State(false), ggui.State("")
		body := approvalBody(m, a)
		head := ggui.Row(
			icon("shield-alert").Size(16).Color(skin.DotWaiting),
			ggui.Text(Tf("Allow %s?", a.GetTool())).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
			ggui.Spacer(),
			ggui.If(ggui.Const(body == nil && a.GetInput() != ""), func() ggui.Widget {
				return ui.Button(T("Arguments"), func() { open.Set(!ggui.Peek(open)) }).Ghost()
			}),
		).Gap(8).Align(ggui.AlignCenter)
		rows := []ggui.Widget{head}
		if body != nil {
			rows = append(rows, body)
		} else if a.GetDetail() != "" {
			rows = append(rows, toolBlock(a.GetDetail(), false))
		}
		allow := func(option string) func() {
			return func() {
				m.answerApproval(id, &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: protocol.DecisionAllow, Option: option})
			}
		}
		// They wrap in a narrow pane, the answer most give first.
		buttons := []ggui.Widget{ui.Tooltip(ui.Button(T("Allow"), allow("")), chordLabel("cmd+enter"))}
		for _, o := range a.GetOptions() {
			buttons = append(buttons, ui.Button(optionLabel(o), allow(o.GetId())).Outline())
		}
		buttons = append(buttons, inTerminalButton(m.decide(id, a, protocol.DecisionTerminal)))
		deny := func() { denyWith(m, id, a, ggui.Peek(why)) }
		rows = append(rows,
			reveal(open, func() ggui.Widget { return ggui.Box(toolBlock(a.GetInput(), false)).MaxHeight(260) }),
			ggui.Wrap(buttons...).Gap(8).RunGap(8).Align(ggui.AlignCenter),
			ggui.Row(
				ggui.Expanded(ui.TextField(why).Placeholder(T("Tell the agent what to do instead")).Name(T("Why not")).OnSubmit(func(string) { deny() })),
				ui.Tooltip(ui.Button(T("Deny"), deny).Outline(), chordLabel("cmd+.")),
			).Gap(8).Align(ggui.AlignCenter),
		)
		return approvalFrame(ggui.Column(rows...).Gap(10).Stretch())
	})
}

// approvalFrame frames an approval's card at the foot of Chat.
func approvalFrame(content ggui.Widget) ggui.Widget {
	card := ggui.Box(content).Fill(skin.Surface1).Border(1, skin.DotWaiting).Radius(12).Pad(12, 14)
	return &lane{child: arrive(ggui.Box(card).Pad(0, 0, 8, 0))}
}

// approvalBody is what an approval's call would do as its kind shows it,
// as a call of that kind in Chat does: an edit as a diff, a command as
// it would run; nil for a call of no kind, or arguments cut short.
func approvalBody(m *model, a *protocol.Approval) ggui.Widget {
	var in map[string]any
	if a.GetKind() == "" || json.Unmarshal([]byte(a.GetInput()), &in) != nil {
		return nil
	}
	rows := toolInput(m, a.GetKind(), a.GetInput(), in)
	if len(rows) == 0 {
		return nil
	}
	return ggui.Column(rows...).Gap(6).Stretch()
}

// denyWith denies approval a, telling the agent why, if the user said.
func denyWith(m *model, id string, a *protocol.Approval, why string) {
	m.answerApproval(id, &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: protocol.DecisionDeny, Message: strings.TrimSpace(why)})
}

// optionLabel says what allowing with option o does besides.
func optionLabel(o *protocol.ApprovalOption) string {
	if rules := o.GetRules(); len(rules) > 0 {
		return Tf("Always allow %s", strings.Join(rules, ", "))
	}
	switch o.GetMode() {
	case "acceptEdits":
		return T("Allow, and accept edits from now on")
	case "auto":
		return T("Allow, and switch to auto mode")
	}
	return Tf("Allow, and switch to %s", modeLabel(o.GetMode()))
}

// planCard is a plan the agent puts to the user: the plan, the modes to go
// on in once it is approved, and what to change, to keep planning.
func planCard(m *model, id string, a *protocol.Approval) ggui.Widget {
	why := ggui.State("")
	keep := func() { denyWith(m, id, a, ggui.Peek(why)) }
	head := ggui.Row(
		icon("list").Size(16).Color(skin.DotWaiting),
		ggui.Text(T("Ready to carry out the plan?")).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
		ggui.Spacer(),
		inTerminalButton(m.decide(id, a, protocol.DecisionTerminal)),
	).Gap(8).Align(ggui.AlignCenter)
	plan := approvalBody(m, a)
	if plan == nil {
		plan = toolBlock(a.GetDetail(), false)
	}
	var approve []ggui.Widget
	for i, o := range a.GetOptions() {
		b := ui.Button(planOptionLabel(o.GetMode()), func() {
			m.answerApproval(id, &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: protocol.DecisionAllow, Option: o.GetId()})
		})
		if i > 0 {
			b = b.Outline()
		}
		approve = append(approve, b)
	}
	return approvalFrame(ggui.Column(
		head,
		plan,
		ggui.Wrap(approve...).Gap(8).RunGap(8),
		ggui.Row(
			ggui.Expanded(ui.TextField(why).Placeholder(T("Tell the agent what to change")).Name(T("What to change")).
				OnSubmit(func(string) { keep() })),
			ui.Button(T("Keep planning"), keep).Outline(),
		).Gap(8).Align(ggui.AlignCenter),
	).Gap(10).Stretch())
}

// planOptionLabel says how work goes on once a plan is approved into mode.
func planOptionLabel(mode string) string {
	switch mode {
	case "auto":
		return T("Yes, in auto mode")
	case "acceptEdits":
		return T("Yes, accepting edits")
	case "default":
		return T("Yes, asking before edits")
	}
	return Tf("Yes, then %s", modeLabel(mode))
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
		if a.GetKind() == protocol.ToolExitPlan {
			glyph, ask = "list", T("Approve the plan?")
			buttons = []ggui.Widget{
				ui.Button(T("Review in Chat"), func() { m.setChatMode(id, true) }).Outline(),
				inTerminalButton(m.decide(id, a, protocol.DecisionTerminal)),
			}
		}
		if qs := a.GetQuestions(); len(qs) > 0 {
			// Its form is in Chat; the terminal shows no dialog until
			// it is handed back.
			glyph, ask, what = "message-square", T("The agent asks"), qs[0].GetQuestion()
			buttons = []ggui.Widget{
				ui.Button(T("Answer in Chat"), func() { m.setChatMode(id, true) }).Outline(),
				inTerminalButton(m.decide(id, a, protocol.DecisionTerminal)),
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
