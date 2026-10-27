package gui

import (
	"context"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
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

// answerApproval passes the user's decision to the agent.
func (m *model) answerApproval(id, approval, decision string) {
	info, ok := m.session(id)
	if !ok {
		return
	}
	req := &protocol.AnswerApproval{NodeId: m.remoteID(info.GetNodeId()), SessionId: id, ApprovalId: approval, Decision: decision}
	// Answered, the approval leaves at once rather than at the next poll.
	m.editSession(id, func(s *protocol.SessionInfo) {
		if s.GetApproval().GetId() == approval {
			s.Approval = nil
		}
	})
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
		return c.AnswerApproval(ctx, m.key(), req)
	}, func(_ *protocol.SessionInfo, err error) {
		if err != nil {
			m.Conn.Set(connState{OK: ggui.Peek(m.Conn).OK, Self: ggui.Peek(m.Conn).Self, Err: err.Error()})
		}
		m.pollNow()
	})
}

// pollNow refreshes the mesh now rather than at the next tick.
func (m *model) pollNow() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// approvalButtons are an approval's answers.
func approvalButtons(m *model, id string, a *protocol.Approval) []ggui.Widget {
	answer := func(decision string) func() { return func() { m.answerApproval(id, a.GetId(), decision) } }
	return []ggui.Widget{
		ui.Button(T("In terminal"), answer(protocol.DecisionTerminal)).Ghost().Name(T("Answer in the terminal")),
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
		open := ggui.State(false)
		head := ggui.Row(
			icon("shield-alert").Size(16).Color(tokDotWaiting),
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
			Fill(tokSurface1).Border(1, tokDotWaiting).Radius(12).Pad(12, 14)
		return ggui.Box(card).Pad(0, 16, 8, 16).MaxWidth(chatLaneWidth)
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
		row := append([]ggui.Widget{
			icon("shield-alert").Size(15).Color(tokDotWaiting),
			ggui.Text(T("Approve?")).Size(12.5).Weight(ggui.WeightMedium).NoWrap(),
			ggui.Expanded(ggui.Text(what).Size(12.5).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()),
		}, approvalButtons(m, id, a)...)
		return ggui.Column(
			ggui.Box(ggui.Row(row...).Gap(10).Align(ggui.AlignCenter)).Pad(6, 12).Fill(tokSurface2),
			hline(),
		).Stretch()
	})
}
