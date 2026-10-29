package gui

import (
	"cmp"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/protocol"
)

// A subagent's conversation: a subagent in Chat, among those running over
// the composer or under the call that started it, opens its own
// conversation in the session's pane, read from its log on the session's
// node, with the way back to the session's over it.

// openAgent shows the conversation of session id's subagent agent in its
// Chat, or the session's own again for "".
func (m *model) openAgent(id, agent string) { m.chatState(id).agent.Set(agent) }

// agentState is the read of session id's subagent agent: the one last
// shown, kept to come back to with its folds as they were left, or a new
// one in its place.
func (m *model) agentState(id, agent string) *chatState {
	state := m.chatState(id)
	if state.sub == nil || state.subID != agent {
		state.sub, state.subID = newChatState(), agent
	}
	return state.sub
}

// loadAgentChat reads the conversation of session id's subagent agent.
func (m *model) loadAgentChat(id, agent string, sub *chatState) {
	m.readChat(id, agent, sub, func() bool { return m.chatState(id).sub == sub })
}

// agentOf is the subagent agent of the session's chat as last read, nil
// once it is not listed.
func agentOf(state *chatState, agent string) *protocol.SubAgent {
	for _, a := range state.result.Get().Chat.GetAgents() {
		if a.GetId() == agent {
			return a
		}
	}
	return nil
}

// agentChatView is the conversation of session id's subagent agent: a bar
// with the way back, what it was asked and how far it has got, over its
// turns, read again every few seconds while it is in view.
func agentChatView(m *model, id string, group int, agent string) ggui.Widget {
	state, sub := m.chatState(id), m.agentState(id, agent)
	ggui.Effect(func() ggui.Cleanup {
		m.loadAgentChat(id, agent, sub)
		return m.every(2*time.Second, func() {
			if a := ggui.Untrack(func() *protocol.SubAgent { return agentOf(state, agent) }); a.GetRunning() || !ggui.Peek(sub.result).Loaded {
				m.loadAgentChat(id, agent, sub)
			}
		})
	})
	back := ui.ButtonOf(ggui.Row(icon("arrow-left").Size(14).Color(tokMuted), muted(ggui.Text(T("Session")).Size(12.5))).Gap(6),
		func() { m.openAgent(id, "") }).Ghost().Pad(4, 8).Radius(6).Name(T("Back to the session"))
	bar := ggui.Reactive(func() ggui.Widget {
		a := agentOf(state, agent)
		title := cmp.Or(a.GetDescription(), a.GetKind(), T("Subagent"))
		var status ggui.Widget = muted(ggui.Text(T("Finished")).Size(11.5).NoWrap())
		if a.GetRunning() {
			status = ui.Spinner().Size(13)
		}
		return ggui.Box(ggui.Row(
			back,
			ggui.Expanded(ggui.Column(
				ggui.Text(title).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis(),
				agentProgress(a),
			).Gap(2).Stretch()),
			status,
		).Gap(10).Align(ggui.AlignCenter)).Pad(8, 12)
	})
	turns := ggui.Derived(func() []chatTurnItem {
		return chatTurns(sub.result.Get().Chat.GetMessages(), nil)
	}).WithEqual(deepEqual)
	head := ggui.Reactive(func() ggui.Widget {
		v := sub.result.Get()
		switch {
		case v.Err != "":
			return ggui.Text(v.Err).Size(13).Color(tokMuted).Selectable()
		case !v.Loaded:
			return muted(ggui.Text(T("Loading conversation…")))
		case len(v.Chat.GetMessages()) == 0:
			return muted(ggui.Text(T("No messages yet. The conversation updates as the agent saves it.")))
		}
		return ggui.Box()
	})
	feed := chatFeed(m, id, sub, turns, head)
	return ggui.Pointer(ggui.Column(
		ggui.Box(bar).Fill(tokSurface1),
		hline(),
		ggui.Expanded(feed),
	).Stretch()).OnDown(func(ggui.PointerEvent) { m.focusGroup(group) })
}

// agentLink makes w, with a chevron after it, open session id's subagent
// a when tapped; w alone on a node too old to read a subagent's log.
func agentLink(m *model, id string, a *protocol.SubAgent, w ggui.Widget) ggui.Widget {
	if !m.nodeHas(id, protocol.RevisionAgentChat) {
		return w
	}
	row := ggui.Row(ggui.Expanded(w), icon("chevron-right").Size(12).Color(tokExtraMuted)).Gap(6).Align(ggui.AlignCenter)
	return ui.Tooltip(ggui.Tap(row, func() { m.openAgent(id, a.GetId()) }).Cursor(ggui.CursorShapePointer), T("Open its conversation"))
}

// nodeHas reports whether the node session id runs on is of revision or
// later: this window's own daemon, or a peer as it says.
func (m *model) nodeHas(id string, revision int64) bool {
	info, ok := m.session(id)
	if !ok {
		return false
	}
	if self := ggui.Peek(m.Conn).Self; self.GetId() == info.GetNodeId() {
		return self.GetRevision() >= revision
	}
	for _, n := range ggui.Peek(m.Nodes) {
		if n.GetId() == info.GetNodeId() {
			return n.GetRevision() >= revision
		}
	}
	return false
}
