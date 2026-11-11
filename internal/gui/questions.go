package gui

import (
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/mako/internal/gui/skin"
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
	if call := pendingQuestionCall(messages); call != nil {
		return "question:" + chatMessageKey(call)
	}
	return ""
}

// pendingQuestionCall is the question call the agent waits on, the last of
// the trailing calls; nil when it asks none.
func pendingQuestionCall(messages []*protocol.ChatMessage) *protocol.ChatMessage {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.GetRole() != "tool" {
			break
		}
		if msg.GetKind() == protocol.ToolQuestion && msg.GetPending() && !msg.GetOutput() {
			return msg
		}
	}
	return nil
}

// chatInput lets the agent's own prompt receive answers inside Chat.
// Using the live terminal preserves each agent's navigation, multiple
// selection, free text and multi-question submission semantics.
func chatInput(m *model, id string, state *chatState) ggui.Widget {
	prompt := ggui.Derived(func() string {
		if !slices.Contains(m.Attached.Get(), id) {
			return ""
		}
		s, _ := sessionIn(m.Sessions.Get(), id)
		messages := state.result.Get().Chat.GetMessages()
		request := questionPrompt(s, messages)
		// Questions its terminal's dialog takes typed answers to are
		// answered in a form, once the log has the call.
		if request != "" && s.GetCaps()&protocol.CapAnswerKeys != 0 {
			if call := pendingQuestionCall(messages); call != nil {
				return keyedPrefix + chatMessageKey(call)
			}
		}
		return request
	})
	return ggui.Key(prompt, func(request string) ggui.Widget {
		if strings.HasPrefix(request, keyedPrefix) {
			if call := pendingQuestionCall(ggui.Peek(state.result).Chat.GetMessages()); call != nil {
				if qs := callQuestions(call); len(qs) > 0 {
					return keyedQuestions(m, id, qs)
				}
			}
			request = "waiting" // its questions do not read: the terminal answers them
		}
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
			return answerTerminal(m, id, header)
		})
	})
}

// keyedPrefix keys chatInput's questions answered in a form.
const keyedPrefix = "form:"

// answerTerminal is session id's terminal in Chat, to answer the agent in,
// under header.
func answerTerminal(m *model, id string, header ggui.Widget) ggui.Widget {
	term := newTerminalView(m.conns[id], func() prefs { return ggui.Peek(m.Prefs) }, false).
		OnRiskyPaste(m.confirmPaste).
		OnLink(func(target, cwd string) { m.followLink(id, target, cwd) })
	return ggui.Column(header, &lane{child: ggui.Box(ggui.Column(
		muted(ggui.Text(T("Click below to select options or type your answer.")).Size(12)),
		ggui.Box(term).Height(320),
	).Gap(6).Stretch()).Border(1, skin.DotWaiting).Radius(10).Pad(8)}).Stretch()
}

// keyedQuestions are the questions session id's agent asks in its
// terminal's dialog, answered in a form whose answers are typed there; or,
// as the user would rather, the terminal itself.
func keyedQuestions(m *model, id string, questions []*protocol.Question) ggui.Widget {
	inTerminal := ggui.State(false)
	sent := ggui.State(false)
	form := questionForm(questions, func(answers []*protocol.QuestionAnswer) {
		sent.Set(true)
		m.answerQuestion(id, questions, answers, func() { sent.Set(false) })
	})
	return ggui.View(inTerminal, func(terminal bool) ggui.Widget {
		if terminal && m.conns[id] != nil {
			return answerTerminal(m, id, &lane{child: ggui.Row(ggui.Spacer(),
				ui.Button(T("Answer here"), func() { inTerminal.Set(false) }).Ghost(),
			).Gap(8)})
		}
		body := ggui.View(sent, func(done bool) ggui.Widget {
			if done {
				return muted(ggui.Text(T("Answering…")).Size(12.5))
			}
			return form
		})
		return questionCard(body, inTerminalButton(func() { inTerminal.Set(true) }))
	})
}
