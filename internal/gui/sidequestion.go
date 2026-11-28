package gui

import (
	"context"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/mdview"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// Side questions (/btw): ask a quick question about a session while its agent
// keeps working, without interrupting the turn, queueing a follow-up, touching
// the main composer's draft, or adding the exchange to the session's history.

type sideQuestionState struct {
	open     *ggui.StateValue[bool]
	asking   *ggui.StateValue[bool] // true while the inline question box is shown
	draft    *ggui.StateValue[string]
	loading  *ggui.StateValue[bool]
	question *ggui.StateValue[string]
	answer   *ggui.StateValue[string]
	mode     *ggui.StateValue[string]
	err      *ggui.StateValue[string]

	focus  ggui.FocusRef
	cancel context.CancelFunc
	gen    uint64
}

func newSideQuestionState() *sideQuestionState {
	return &sideQuestionState{
		open:     ggui.State(false),
		asking:   ggui.State(false),
		draft:    ggui.State(""),
		loading:  ggui.State(false),
		question: ggui.State(""),
		answer:   ggui.State(""),
		mode:     ggui.State(""),
		err:      ggui.State(""),
	}
}

// sideQuestionCapable reports whether session s can answer a side question
// either natively (CapSideQuestion) or through a read-only transcript fallback
// (CapChat), returning a human-readable reason when hidden/unavailable.
func (m *model) sideQuestionCapable(id string) (ok bool, reason string) {
	s, found := m.session(id)
	if !found {
		return false, ""
	}
	if !hasCap(s, protocol.CapSideQuestion) && !hasCap(s, protocol.CapChat) {
		return false, T("This agent does not support side questions.")
	}
	if s.GetNodeId() != "" && len(ggui.Peek(m.Nodes)) > 0 && !m.nodeHas(id, protocol.RevisionSideQuestion) {
		return false, T("Update this node's daemon to ask side questions.")
	}
	return true, ""
}

// parseSideQuestionCommand checks whether draft starts with `/btw` (or `/side`
// for Codex). matched is true for both bare `/btw` (empty question, which opens
// the inline side-question box) and `/btw <question>`.
func parseSideQuestionCommand(agentName, draft string) (question string, matched bool) {
	trimmed := strings.TrimSpace(draft)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return "", false
	}
	cmd, rest, hasRest := strings.Cut(trimmed[1:], " ")
	if nl := strings.IndexByte(cmd, '\n'); nl >= 0 {
		rest = cmd[nl+1:] + " " + rest
		cmd = cmd[:nl]
		hasRest = true
	}
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	switch cmd {
	case "btw":
	case "side":
		if agentName != "codex" {
			return "", false
		}
	default:
		return "", false
	}
	if !hasRest {
		return "", true
	}
	return strings.TrimSpace(rest), true
}

// openSideQuestion opens and focuses the inline side-question prompt for
// session id without modifying the session's main composer draft.
func (m *model) openSideQuestion(id string) {
	if id == "" || !isSessionTab(id) {
		return
	}
	ok, reason := m.sideQuestionCapable(id)
	if !ok {
		if reason != "" && m.toaster != nil {
			m.toaster.Push(ui.Toast(T("Side question unavailable"), reason))
		}
		return
	}
	state := m.chatState(id)
	sq := state.side
	sq.open.Set(true)
	sq.asking.Set(true)
	sq.err.Set("")
	sq.focus.Focus()
}

// dismissSideQuestion closes the side-question card for session id and cancels
// any in-flight request.
func (m *model) dismissSideQuestion(id string) {
	state, ok := m.chats[id]
	if !ok || state == nil || state.side == nil {
		return
	}
	sq := state.side
	sq.gen++
	if sq.cancel != nil {
		sq.cancel()
		sq.cancel = nil
	}
	sq.loading.Set(false)
	sq.open.Set(false)
	sq.asking.Set(false)
	sq.draft.Set("")
	sq.question.Set("")
	sq.answer.Set("")
	sq.mode.Set("")
	sq.err.Set("")
}

// submitSideQuestion submits the inline side-question box's draft for session id.
func (m *model) submitSideQuestion(id string) {
	state := m.chatState(id)
	sq := state.side
	q := strings.TrimSpace(ggui.Peek(sq.draft))
	if q == "" {
		return
	}
	sq.draft.Set("")
	m.askSideQuestion(id, q)
}

// askSideQuestion sends question to the daemon's AskSideQuestion RPC for
// session id without touching the session's composer draft, pending queue, or
// conversation history.
func (m *model) askSideQuestion(id, question string) {
	question = strings.TrimSpace(question)
	if question == "" {
		return
	}
	info, ok := m.session(id)
	if !ok {
		return
	}
	state := m.chatState(id)
	sq := state.side
	sq.gen++
	gen := sq.gen
	if sq.cancel != nil {
		sq.cancel()
		sq.cancel = nil
	}
	sq.open.Set(true)
	sq.asking.Set(false)
	sq.question.Set(question)
	sq.answer.Set("")
	sq.mode.Set("")
	sq.err.Set("")
	sq.loading.Set(true)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	sq.cancel = cancel

	req := &protocol.AskSideQuestion{
		NodeId:    m.remoteID(info.GetNodeId()),
		SessionId: id,
		Question:  question,
	}
	ggui.Async(func() (*protocol.SideQuestionAnswer, error) {
		defer cancel()
		c, err := m.link.client(ctx)
		if err != nil {
			return nil, err
		}
		return c.AskSideQuestion(ctx, req)
	}, func(res *protocol.SideQuestionAnswer, err error) {
		if m.chats[id] != state || sq.gen != gen {
			return
		}
		sq.cancel = nil
		sq.loading.Set(false)
		if err != nil {
			sq.err.Set(nodeErr(err))
			return
		}
		sq.answer.Set(strings.TrimSpace(res.GetAnswer()))
		sq.mode.Set(res.GetMode())
	})
}

// sendSideAnswerToComposer inserts a side-question answer into session id's
// main composer draft on purpose and focuses the composer.
func (m *model) sendSideAnswerToComposer(id, answer string) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return
	}
	state := m.chatState(id)
	existing := strings.TrimRight(ggui.Peek(state.draft), " \t\n")
	if existing == "" {
		state.draft.Set(answer)
	} else {
		state.draft.Set(existing + "\n\n" + answer)
	}
	state.composerFocus.Focus()
}

// sideQuestionButton is the composer bar chip that opens the inline /btw
// side-question prompt without disturbing the composer's draft.
func sideQuestionButton(m *model, id string, state *chatState, disabled ggui.Readable[bool]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		capable, reason := m.sideQuestionCapable(id)
		if !capable {
			if reason == "" {
				return widget.Box()
			}
			// Hide cleanly when the agent has no side-question capability at all;
			// show a disabled hint only when the node's daemon is outdated.
			s, found := m.session(id)
			if !found || (!hasCap(s, protocol.CapSideQuestion) && !hasCap(s, protocol.CapChat)) {
				return widget.Box()
			}
			return ui.Tooltip(
				widget.Box(widget.Row(
					icon("sparkles").Size(14).Color(skin.ExtraMuted),
					widget.Text("/btw").Size(12).Color(skin.ExtraMuted).NoWrap(),
				).Gap(4).Align(widget.AlignCenter)).Pad(4, 6),
				reason,
			)
		}
		open := state.side.open.Get()
		glyphColor := skin.Muted
		if open {
			glyphColor = skin.AccentBright
		}
		chip := widget.Row(
			icon("sparkles").Size(14).Color(glyphColor),
			widget.Text("/btw").Size(12).Weight(ggui.WeightMedium).Color(glyphColor).NoWrap(),
		).Gap(4).Align(widget.AlignCenter)
		var btn ggui.Widget = ui.ButtonOf(chip, func() {
			if ggui.Peek(state.side.open) && ggui.Peek(state.side.asking) && !ggui.Peek(state.side.loading) && ggui.Peek(state.side.answer) == "" && ggui.Peek(state.side.err) == "" {
				m.dismissSideQuestion(id)
				return
			}
			m.openSideQuestion(id)
		}).Ghost().Pad(4, 6).Radius(8).Name(T("Ask a side question")).BindDisabled(disabled)
		if open {
			btn = &washed{child: btn, color: skin.AccentBright, radius: 8}
		}
		tip := T("Ask a side question (/btw) without interrupting the turn")
		if chord := m.shortcutChord("side_question"); chord != "" {
			tip += " (" + chordLabel(chord) + ")"
		}
		return ui.Tooltip(btn, tip)
	})
}

// sideQuestionCard shows the dismissible side-question input, loading indicator,
// or answer card above the composer in Chat. It is never part of the session's
// conversation history.
func sideQuestionCard(m *model, id string, state *chatState) ggui.Widget {
	sq := state.side
	return ggui.Reactive(func() ggui.Widget {
		if !sq.open.Get() {
			return widget.Box()
		}
		asking := sq.asking.Get()
		loading := sq.loading.Get()
		question := sq.question.Get()
		answer := sq.answer.Get()
		mode := sq.mode.Get()
		errText := sq.err.Get()

		headerItems := []ggui.Widget{
			icon("sparkles").Size(13).Color(skin.AccentBright),
			widget.Text(T("Side question")).Size(12.5).Weight(ggui.WeightSemibold).Color(skin.AccentBright).NoWrap(),
			widget.Box(widget.Text("/btw").Size(11).Font(ggui.DefaultMonoFont()).Color(skin.Muted).NoWrap()).
				Fill(skin.Surface2).Radius(4).Pad(1, 5),
		}
		if label := sideModeLabel(mode); label != "" {
			headerItems = append(headerItems,
				widget.Box(widget.Text(label).Size(11).Color(skin.Muted).NoWrap()).
					Fill(skin.Surface2).Radius(4).Pad(1, 5),
			)
		}
		headerItems = append(headerItems,
			widget.Expanded(widget.Text(T("Not added to session history")).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis()),
			ui.Tooltip(
				ui.ButtonOf(icon("x").Size(13).Color(skin.Muted), func() { m.dismissSideQuestion(id) }).
					Ghost().Pad(4).Radius(6).Name(T("Dismiss side question")),
				T("Dismiss"),
			),
		)
		header := widget.Row(headerItems...).Gap(6).Align(widget.AlignCenter)

		rows := []ggui.Widget{header}

		if asking {
			field := ui.TextField(sq.draft).Lines(1).
				Placeholder(T("Ask a quick side question without interrupting the turn…")).
				Name(T("Side question")).
				OnKey(func(ev ggui.KeyEvent) bool {
					if ev.Kind != ggui.KeyPress {
						return false
					}
					switch {
					case ev.Key == ggui.KeyEscape:
						if answer == "" && errText == "" && !loading {
							m.dismissSideQuestion(id)
						} else {
							sq.asking.Set(false)
						}
						return true
					case (ev.Key == ggui.KeyEnter || ev.Key == ggui.KeyNumpadEnter) && !ev.Mods.Shift && !ev.Mods.Alt:
						m.submitSideQuestion(id)
						return true
					}
					return false
				})
			sendDisabled := ggui.Map(sq.draft, func(v string) bool { return strings.TrimSpace(v) == "" })
			inputRow := widget.Row(
				widget.Expanded(sq.focus.Attach(field)),
				ui.Button(T("Ask"), func() { m.submitSideQuestion(id) }).Pad(6, 12).Radius(8).BindDisabled(sendDisabled),
			).Gap(8).Align(widget.AlignCenter)
			rows = append(rows, inputRow)
		}

		if question != "" && (!asking || loading || answer != "" || errText != "") {
			rows = append(rows, widget.Box(
				widget.Text(question).Size(12.5).Color(skin.Muted).Selectable(),
			).Fill(skin.Surface0).Radius(8).Pad(6, 10))
		}

		if loading {
			rows = append(rows, widget.Row(
				ui.Spinner().Size(13),
				muted(widget.Text(T("Asking side question…")).Size(12.5).NoWrap()),
				widget.Spacer(),
				ui.Button(T("Cancel"), func() { m.dismissSideQuestion(id) }).Ghost().Pad(4, 8).Radius(6),
			).Gap(8).Align(widget.AlignCenter))
		}

		if errText != "" {
			errRow := widget.Column(
				widget.Text(errText).Size(12.5).Color(skin.DotDown).Selectable(),
				widget.Row(
					ui.Button(T("Try again"), func() {
						if question != "" {
							m.askSideQuestion(id, question)
						} else {
							m.openSideQuestion(id)
						}
					}).Outline().Pad(4, 10).Radius(6),
					ui.Button(T("Ask another"), func() { m.openSideQuestion(id) }).Ghost().Pad(4, 10).Radius(6),
				).Gap(6).Align(widget.AlignCenter),
			).Gap(8).Stretch()
			rows = append(rows, errRow)
		}

		if answer != "" {
			info, _ := m.session(id)
			host := pageHost{m: m, id: id, doc: docInfo{Node: info.GetNodeId(), Path: joinPath(info.GetProject(), "side-question.md")}}
			body := widget.Box(widget.Scroll(mdview.Column(host, answer)).Shrink().Bar(skin.Surface3)).MaxHeight(260)
			rows = append(rows, body)

			copied := ggui.State(false)
			copyBtn := ggui.View(copied, func(done bool) ggui.Widget {
				lbl := T("Copy")
				glyph := "copy"
				if done {
					lbl = T("Copied")
					glyph = "check"
				}
				return ui.ButtonOf(
					widget.Row(icon(glyph).Size(13).Color(skin.Muted), widget.Text(lbl).Size(12).Color(skin.Muted).NoWrap()).Gap(5).Align(widget.AlignCenter),
					func() {
						ggui.CurrentClipboard().Write(answer)
						copied.Set(true)
						time.AfterFunc(1500*time.Millisecond, func() { m.post(func() { copied.Set(false) }) })
					},
				).Ghost().Pad(4, 8).Radius(6).Name(T("Copy"))
			})
			toComposerBtn := ui.ButtonOf(
				widget.Row(icon("send-horizontal").Size(13).Color(skin.Fg), widget.Text(T("Send to composer")).Size(12).NoWrap()).Gap(5).Align(widget.AlignCenter),
				func() { m.sendSideAnswerToComposer(id, answer) },
			).Outline().Pad(4, 10).Radius(6).Name(T("Send to composer"))
			askAnotherBtn := ui.Button(T("Ask another"), func() { m.openSideQuestion(id) }).Ghost().Pad(4, 8).Radius(6)
			actions := widget.Row(copyBtn, toComposerBtn, askAnotherBtn, widget.Spacer()).Gap(6).Align(widget.AlignCenter)
			rows = append(rows, actions)
		}

		card := widget.Box(widget.Column(rows...).Gap(8).Stretch()).
			Fill(skin.Surface1).Border(1, skin.Border).Radius(12).Pad(10, 12)
		return &lane{child: widget.Padding(card, 6, 0, 0, 0)}
	})
}

func sideModeLabel(mode string) string {
	switch mode {
	case protocol.SideQuestionModeFallback:
		return T("Read-only fallback")
	case protocol.SideQuestionModeAppServer:
		return T("Ephemeral thread")
	default:
		return ""
	}
}
