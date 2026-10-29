package gui

import (
	"slices"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/mako/internal/protocol"
)

// questionPrompt identifies the current input request. Only the final run
// of tools can still be asking: older unanswered calls may have been
// abandoned. Approvals have their own card.
func questionPrompt(s *protocol.SessionInfo, messages []*protocol.ChatMessage) string {
	if s.GetState() != protocol.StateRunning || s.GetApproval() != nil {
		return ""
	}
	if s.GetStatus() == protocol.AgentWaiting {
		return "waiting"
	}
	if s.GetStatus() == protocol.AgentDone || s.GetStatus() == protocol.AgentIdle {
		return ""
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.GetRole() != "tool" {
			break
		}
		if msg.GetKind() == protocol.ToolQuestion && msg.GetPending() && !msg.GetOutput() {
			return "question:" + chatMessageKey(msg)
		}
	}
	return ""
}

// chatInput lets the agent's own prompt receive answers inside Chat.
// Using the live terminal preserves each agent's navigation, multiple
// selection, free text and multi-question submission semantics.
func chatInput(m *model, id string, group int, state *chatState) ggui.Widget {
	prompt := ggui.Derived(func() string {
		if !slices.Contains(m.Attached.Get(), id) {
			return ""
		}
		s, _ := sessionIn(m.Sessions.Get(), id)
		return questionPrompt(s, state.result.Get().Chat.GetMessages())
	})
	return ggui.Key(prompt, func(request string) ggui.Widget {
		open := ggui.State(request != "")
		disabled := ggui.Derived(func() bool { return !slices.Contains(m.Attached.Get(), id) })
		return ggui.View(open, func(show bool) ggui.Widget {
			label := T("Answer the agent")
			if show {
				label = T("Back to message")
			}
			header := &lane{child: ggui.Row(ggui.Spacer(),
				ui.Button(label, func() { open.Set(!ggui.Peek(open)) }).Ghost().BindDisabled(disabled),
			).Gap(8)}
			if !show || m.conns[id] == nil {
				return ggui.Column(header, composer(m, id, state)).Stretch()
			}
			term := newTerminalView(m.conns[id], func() prefs { return ggui.Peek(m.Prefs) }, false).
				OnPress(func() { m.focusGroup(group) }).OnRiskyPaste(m.confirmPaste).
				OnLink(func(target, cwd string) { m.followLink(id, target, cwd) })
			return ggui.Column(header, &lane{child: ggui.Box(ggui.Column(
				muted(ggui.Text(T("Click below to select options or type your answer.")).Size(12)),
				ggui.Box(term).Height(320),
			).Gap(6).Stretch()).Border(1, tokDotWaiting).Radius(10).Pad(8)}).Stretch()
		})
	})
}
