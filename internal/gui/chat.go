package gui

import (
	"cmp"
	"context"
	"hash/fnv"
	"image/color"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transcript"
	"github.com/yuin/goldmark/text"
	"google.golang.org/protobuf/proto"
)

// chatLaneWidth is the width the transcript and the composer share.
const chatLaneWidth = 900

type chatResult struct {
	Chat   *protocol.Chat
	Err    string
	Loaded bool
}

// chatPages are messages read from further back than the latest, oldest
// first: from Start in the log up to End, where the latest messages began
// when they were last joined on.
type chatPages struct {
	Messages   []*protocol.ChatMessage
	Start, End int64
}

type chatState struct {
	result  *ggui.StateValue[chatResult]
	draft   *ggui.StateValue[string]
	tools   *ggui.StateValue[map[string]bool] // open tool rows, by message key
	loading bool
	// whole asks the next read for the whole chat, a patch having missed
	// what was on show.
	whole bool
	// older are the messages read from before the latest, for a reader who
	// scrolled back; paging while a read of them is out.
	older  *ggui.StateValue[chatPages]
	paging bool
	// sent is what the composer sent, oldest first, for ↑ and ↓ to bring
	// back; recall is the one shown, len(sent) for none.
	sent   []string
	recall int
	// menu lists the slash commands a draft begins.
	menu *commandMenu
	// agent is the subagent whose conversation the pane shows in place of
	// the session's, "" for none, and sub the read of the one shown last.
	agent *ggui.StateValue[string]
	sub   *chatState
	subID string
	// pending are the messages sent and not yet in the log.
	pending *ggui.StateValue[[]pendingMessage]
}

func (m *model) chatState(id string) *chatState {
	if state := m.chats[id]; state != nil {
		return state
	}
	state := newChatState()
	m.chats[id] = state
	return state
}

func newChatState() *chatState {
	return &chatState{
		result: ggui.State(chatResult{}).WithEqual(deepEqual),
		older:  ggui.State(chatPages{}).WithEqual(deepEqual),
		draft:  ggui.State(""),
		tools:  ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		menu:   newCommandMenu(),
		agent:  ggui.State(""),
		pending: ggui.State[[]pendingMessage](nil).WithEqual(func(a, b []pendingMessage) bool {
			return slices.Equal(a, b)
		}),
	}
}

// toolOpen follows one tool row's fold, defaulting to open for a failed
// tool so the error is in sight.
func (s *chatState) toolOpen(key string, openByDefault bool) ggui.Binding[bool] {
	return ggui.Bind(func() bool {
		if open, ok := s.tools.Get()[key]; ok {
			return open
		}
		return openByDefault
	}, func(open bool) {
		s.tools.Update(func(v map[string]bool) map[string]bool {
			v = maps.Clone(v)
			v[key] = open
			return v
		})
	})
}

func (m *model) setChatMode(id string, chat bool) {
	if !isSessionTab(id) {
		return
	}
	m.ChatModes.Update(func(v map[string]bool) map[string]bool { v = maps.Clone(v); v[id] = chat; return v })
}

func (m *model) toggleChat() {
	id := ggui.Peek(m.Selected)
	if !m.chatCapable(id) {
		return
	}
	m.setChatMode(id, !ggui.Peek(m.ChatModes)[id])
}

func (m *model) loadChat(id string, state *chatState) {
	if m.chats[id] != state {
		return
	}
	m.readChat(id, "", state, func() bool { return m.chats[id] == state })
}

// readChat reads into state the conversation of session id, or of its
// subagent agent, unless a read of it is out; a reply that comes once
// current reports the state gone is let go.
func (m *model) readChat(id, agent string, state *chatState, current func() bool) {
	info, ok := m.session(id)
	if state.loading || !ok {
		return
	}
	state.loading = true
	// What is on show is asked after by its digest, so a quiet
	// conversation is not sent again.
	shown := ggui.Peek(state.result)
	req := &protocol.ReadChat{NodeId: m.remoteID(info.GetNodeId()), SessionId: id, Agent: agent, Seen: shown.Chat.GetDigest(), Patch: true}
	if state.whole {
		req.Seen, state.whole = 0, false
	}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Chat, error) {
		return c.ReadChat(ctx, req)
	}, func(chat *protocol.Chat, err error) {
		if !current() {
			return
		}
		state.loading = false
		if err == nil && chat.GetUnchanged() {
			if shown.Err != "" {
				state.result.Set(chatResult{Chat: shown.Chat, Loaded: true})
			}
			settlePending(state) // one that waited too long goes
			return
		}
		if err == nil {
			var ok bool
			if chat, ok = patched(shown.Chat, chat); !ok {
				state.whole = true
				return
			}
		}
		next := chatResult{Chat: chat, Loaded: true}
		if err != nil {
			next.Chat = ggui.Peek(state.result).Chat
			next.Err = err.Error()
		}
		state.result.Set(next)
		settlePending(state)
		// The latest moved on past the older pages: what came between
		// joins them.
		if older := ggui.Peek(state.older); older.End > 0 && chat.GetStart() > older.End {
			m.readOlder(id, state, transcript.Span{After: older.End, Before: chat.GetStart()})
		}
	})
}

// patched is chat, a reply that may patch shown, made whole: the messages
// it keeps of shown's are shown's own, so their rows are not built again.
// It is false for a patch of another chat than shown.
func patched(shown, chat *protocol.Chat) (*protocol.Chat, bool) {
	if chat.GetBase() == 0 {
		return chat, true
	}
	old := shown.GetMessages()
	drop, keep := int(chat.GetDrop()), int(chat.GetKeep())
	if chat.GetBase() != shown.GetDigest() || drop < 0 || keep < 0 || drop+keep > len(old) {
		return nil, false
	}
	chat.Messages = slices.Concat(old[drop:drop+keep], chat.GetMessages())
	chat.Base, chat.Drop, chat.Keep = 0, 0, 0
	return chat, true
}

// loadOlder reads the page before what is loaded, for a reader near the
// top, keeping what is in view in place.
func (m *model) loadOlder(id string, state *chatState) {
	before := ggui.Peek(state.result).Chat.GetStart()
	if older := ggui.Peek(state.older); older.End > 0 {
		before = older.Start
	}
	if before <= 0 || state.paging {
		return
	}
	m.readOlder(id, state, transcript.Span{Before: before})
}

// readOlder reads a span of the log older than the latest, and puts it
// with the older pages: before them, or between them and the latest.
func (m *model) readOlder(id string, state *chatState, span transcript.Span) {
	info, ok := m.session(id)
	if !ok || state.paging {
		return
	}
	state.paging = true
	req := &protocol.ReadChat{NodeId: m.remoteID(info.GetNodeId()), SessionId: id, Before: span.Before, After: span.After}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Chat, error) {
		return c.ReadChat(ctx, req)
	}, func(chat *protocol.Chat, err error) {
		if m.chats[id] != state {
			return
		}
		state.paging = false
		older := ggui.Peek(state.older)
		switch {
		case err != nil:
		case span.After == 0: // a page before the rest
			if older.End == 0 {
				older.End = span.Before
			}
			older.Messages, older.Start = joinChat(chat.GetMessages(), older.Messages), chat.GetStart()
			state.older.Set(older)
		case chat.GetStart() == span.After: // the gap, whole
			older.Messages, older.End = joinChat(older.Messages, chat.GetMessages()), span.Before
			state.older.Set(older)
		default: // more came between than one read holds: start over
			state.older.Set(chatPages{})
		}
	})
}

// toLatest goes back to the latest messages alone, at the bottom.
func (m *model) toLatest(state *chatState, scroll *ggui.ScrollController) {
	scroll.ScrollTo(scroll.MaxOffset())
	state.older.Set(chatPages{})
}

// joinChat is older then newer, a result newer begins with put on the call
// in older it answers, as one read would have.
// chatMessages are the messages of a chat as read: the older pages read
// for a reader scrolled back, and the latest.
func chatMessages(latest chatResult, older chatPages) []*protocol.ChatMessage {
	if older.End > 0 {
		return joinChat(older.Messages, latest.Chat.GetMessages())
	}
	return latest.Chat.GetMessages()
}

func joinChat(older, newer []*protocol.ChatMessage) []*protocol.ChatMessage {
	out := slices.Clone(older)
	calls := map[string]int{}
	for i, msg := range out {
		if msg.GetRole() == "tool" && !msg.GetOutput() && msg.GetId() != "" {
			calls[msg.GetId()] = i
		}
	}
	for _, msg := range newer {
		if i, ok := calls[msg.GetId()]; ok && msg.GetRole() == "tool" && msg.GetOutput() {
			call := proto.CloneOf(out[i])
			call.Result, call.Error, call.Pending = msg.GetText(), msg.GetError(), false
			out[i] = call
			continue
		}
		out = append(out, msg)
	}
	return out
}

// chatCapable reports whether a session's agent has a native conversation to
// show. A plain shell has none, so it keeps its terminal alone.
func (m *model) chatCapable(id string) bool {
	s, ok := m.session(id)
	return ok && hasCap(s, protocol.CapChat)
}

// hasCap reports whether mako follows what c says of s's agent: as the
// session's daemon says, or as a node or this build knows the agent, from
// a daemon older than caps.
func hasCap(s *protocol.SessionInfo, c uint32) bool {
	caps := s.GetCaps()
	if caps == 0 {
		_, _, caps = agentLook(s.GetAgent())
	}
	return caps&c != 0
}

// sessionView switches only presentation. The attachment and terminal parser
// stay alive when chat is shown, so switching back has the current screen.
// The switch between them is in the pane's tab bar; see viewSwitch.
func sessionView(m *model, id string, group int) ggui.Widget {
	return ggui.Key(ggui.Map(m.ChatModes, func(v map[string]bool) bool { return v[id] }), func(chat bool) ggui.Widget {
		if chat {
			return chatView(m, id, group)
		}
		focus := ggui.Peek(m.FocusGroup) == group
		term := newTerminalView(m.conns[id], func() prefs { return ggui.Peek(m.Prefs) }, focus).OnPress(func() { m.focusGroup(group) }).
			OnDrop(func(paths []string, typed func([]string)) { m.handOver(id, paths, typed) }).
			OnPasteImage(func(png []byte, typed func([]string)) { m.pasteImage(id, png, typed) }).
			OnRiskyPaste(m.confirmPaste).
			OnLink(func(target, cwd string) { m.followLink(id, target, cwd) })
		body := ggui.DropZone(term, func(d ggui.Dropped[pathDrag]) { term.setDropOver(false); term.typePaths([]string{d.Value.Path}) }).OnHover(term.setDropOver)
		return ggui.Column(approvalBar(m, id), ggui.Expanded(ggui.Stack(body, usageOverlay(m, id, nil, viewChip(m, id, group)), findBar(m, id, terminalFinder(m.conns[id]))).Expand())).Stretch()
	})
}

// viewSwitch switches a session between its terminal and its chat: a
// pill whose track holds the two glyphs, a light thumb sliding behind the
// one in view. A click anywhere on it switches to the other, which is what
// it is named for. A plain shell has no conversation, so it has none.
func viewSwitch(m *model, id string, group int) ggui.Widget {
	mode := ggui.Bind(func() bool { return m.ChatModes.Get()[id] }, func(chat bool) { m.focusGroup(group); m.setChatMode(id, chat) })
	return glyphSwitch(mode, "square-terminal", "messages-square", T("Chat"), T("Terminal"), T("Toggle chat and terminal"))
}

// glyphSwitch is a two-sided switch between the views mode picks, false
// for left's glyph and true for right's: a pill whose track holds the two
// glyphs, a light thumb sliding behind the one in view. A click anywhere
// on it switches to the other, which is what it is named for: toLeft
// while right's is in view, toRight while left's is.
func glyphSwitch(mode ggui.Binding[bool], left, right, toRight, toLeft, tip string) ggui.Widget {
	side := func(glyph string, on bool) ggui.Widget {
		g := ggui.Reactive(func() ggui.Widget {
			// Black on the light thumb in view; the other quiet on the track.
			var c color.Color = tokExtraMuted
			if mode.Get() == on {
				c = viewThumbInk()
			}
			return icon(glyph).Size(14).Color(c)
		})
		return ggui.Box(ggui.Center(g)).Size(viewSideWidth+viewToggleInset, viewSideHeight+2*viewToggleInset)
	}
	toggle := &viewToggle{right: mode, child: ggui.Row(side(left, false), side(right, true))}
	button := ui.ButtonOf(toggle, func() { mode.Set(!ggui.Untrack(mode.Get)) }).Variant(viewSideButton).Pad(0).
		Radius((viewSideHeight + 2*viewToggleInset) / 2).
		BindName(ggui.Map(mode, func(on bool) string { return pick(on, toLeft, toRight) }))
	return ui.Tooltip(button, tip)
}

// viewChip floats the view switch over a session's top-right corner, for a
// session that has a chat; nil for one that has none.
func viewChip(m *model, id string, group int) ggui.Widget {
	if !m.chatCapable(id) {
		return nil
	}
	return raisedChip(viewSwitch(m, id, group)).Radius((viewSideHeight+2*viewToggleInset)/2 + 3).Pad(3)
}

// The view switch's measures: each side, and the track's margin around
// the thumb.
const (
	viewSideWidth   = 26
	viewSideHeight  = 20
	viewToggleInset = 2
)

// viewSideButton is the view switch as a button: its own drawing alone,
// with no fill of the button's over it.
var viewSideButton = ui.NewVariant("view-switch", func(t uitheme.Theme) ui.ButtonStyle {
	return ui.ButtonStyle{Label: t.Fg}
})

// viewThumb is the colour of the view switch's thumb, near white in either
// theme, and viewThumbInk the glyph's on it.
func viewThumb() color.Color {
	if pal, ok := uitheme.Use().Get(paletteKey); ok && pal.Dark {
		return pal.Fg
	}
	return tokSolidBg
}

func viewThumbInk() color.Color {
	if pal, ok := uitheme.Use().Get(paletteKey); ok && pal.Dark {
		return pal.Surface0
	}
	return tokFg
}

// viewToggle draws a two-sided switch's track and its thumb, which slides
// to the right side while right holds, under its sides of equal width.
type viewToggle struct {
	right ggui.Readable[bool]
	child ggui.Widget

	track, thumb, border color.Color
	motion               time.Duration
}

var viewThumbSlot = ggui.NewSlot[*ggui.Motion]("view switch thumb")

func (v *viewToggle) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	theme := uitheme.From(env)
	v.track, v.border = theme.Muted, theme.Border
	v.thumb = ggui.ResolveColor(viewThumb(), env)
	v.motion = env.Motion(theme.MotionFast)
	return v.child.Layout(c, env)
}

func (v *viewToggle) Paint(dst *ggui.Canvas, r ggui.Rect) {
	right := ggui.Untrack(v.right.Get)
	k := dst.Ease(ggui.Anchor{Rect: r}, viewThumbSlot, pick(right, 1.0, 0.0), v.motion)
	dst.FillRoundRect(r, r.Size.H/2, v.track)
	thumb := ggui.Rct(r.Origin.Add(ggui.Pt(viewToggleInset+k*viewSideWidth, viewToggleInset)), ggui.Sz(viewSideWidth, viewSideHeight))
	dst.FillRoundRect(thumb, thumb.Size.H/2, v.thumb)
	dst.StrokeRoundRect(thumb, thumb.Size.H/2, 1, v.border)
	dst.Paint(v.child, r)
}

func chatView(m *model, id string, group int) ggui.Widget {
	state := m.chatState(id)
	ggui.Effect(func() ggui.Cleanup {
		m.loadChat(id, state)
		return m.every(2*time.Second, func() {
			if ggui.Peek(m.ChatModes)[id] {
				m.loadChat(id, state)
			}
		})
	})

	scroll := &ggui.ScrollController{}
	offset := ggui.State(0.0)
	loaded := ggui.Derived(func() []*protocol.ChatMessage {
		return chatMessages(state.result.Get(), state.older.Get())
	})
	turns := ggui.Derived(func() []chatTurnItem {
		turns := chatTurns(loaded.Get(), agentsByCall(state.result.Get().Chat.GetAgents()))
		return append(turns, pendingTurns(state.pending.Get())...)
	}).WithEqual(deepEqual)
	// What stands over the conversation: why it cannot be read, that it is
	// on its way, or that more of it is further back.
	head := ggui.Reactive(func() ggui.Widget {
		v, older := state.result.Get(), state.older.Get()
		start := v.Chat.GetStart()
		if older.End > 0 {
			start = older.Start
		}
		var rows []ggui.Widget
		if v.Err != "" {
			rows = append(rows, ggui.Text(v.Err).Size(13).Color(tokMuted).Selectable())
		}
		switch {
		case !v.Loaded:
			rows = append(rows, muted(ggui.Text(T("Loading conversation…"))))
		case v.Err != "":
		case !v.Chat.GetSupported():
			rows = append(rows, muted(ggui.Text(T("This agent keeps no conversation mako can read. Use Terminal for it."))))
		case v.Chat.GetConversation() == "":
			rows = append(rows, muted(ggui.Text(T("Waiting for this agent to save its conversation. It will connect automatically."))))
		case len(loaded.Get()) == 0:
			rows = append(rows, muted(ggui.Text(T("No messages yet. The conversation updates as the agent saves it."))))
		}
		if start > 0 {
			// More is further back. A feed too short to scroll reads it at
			// once, once it is laid out and its extent known.
			rows = append(rows, &onPaint{child: ggui.Center(ui.Spinner().Size(14)), fn: func(*ggui.Canvas, ggui.Rect) {
				if scroll.MaxOffset() == 0 {
					m.loadOlder(id, state)
				}
			}})
		}
		if len(rows) == 0 {
			return ggui.Box()
		}
		return ggui.Padding(ggui.Column(rows...).Gap(chatGap).Stretch(), 0, 0, chatGap, 0)
	})
	find := &chatFinder{turns: turns, scroll: scroll, marks: map[string]rowMark{}}
	feed := chatFeed(m, id, state, turns, head, find).Controller(scroll).BindOffset(offset)
	// Scrolling up near the top of what is loaded reads the log from
	// further back. Only up: the feed opens at the top before it shows the
	// latest at the bottom.
	last := 0.0
	ggui.Watch(offset, func(off float64) {
		if off < last && off < olderNear {
			m.loadOlder(id, state)
		}
		last = off
	})
	chatUsage := ggui.Map(state.result, func(v chatResult) *protocol.Usage {
		return v.Chat.GetUsage()
	}).WithEqual(protocol.Same)
	// The composer belongs to Chat alone, as in ovis: Terminal mode's grid
	// is its own input surface, and a file view has no session to type into.
	session := ggui.Pointer(ggui.Column(
		ggui.Expanded(ggui.Stack(feed, &afterPaint{fn: find.painted}, hostBadge(m, id), toLatestButton(m, state, scroll, offset),
			usageOverlay(m, id, chatUsage, viewChip(m, id, group)), findBar(m, id, find.finder())).Expand()),
		approvalCard(m, id), compactingRow(m, id), runningAgents(m, id, state), chatInput(m, id, group, state),
	).Stretch()).OnDown(func(ggui.PointerEvent) { m.focusGroup(group) })
	// A subagent opened shows its conversation in place of the session's,
	// which is built once and kept, scroll and draft and all, to come back
	// to.
	return ggui.Key(state.agent, func(agent string) ggui.Widget {
		if agent == "" {
			return session
		}
		return agentChatView(m, id, group, agent)
	})
}

// chatFeed is a conversation's turns of state, under head, in the lane: at
// the latest it stays there as the agent writes on; scrolled back, it
// stays where the reader is. A long conversation has thousands of turns:
// only those near the window are built and laid out, each drawn again only
// when it changes. Older ones coming in above keep what is in view in
// place. find, when there is one, finds in the feed.
func chatFeed(m *model, id string, state *chatState, turns ggui.Readable[[]chatTurnItem], head ggui.Widget, find *chatFinder) *ggui.ScrollWidget {
	list := ggui.EachKeyed(turns, func(t chatTurnItem) string { return t.Key }, func(row ggui.EachItem[chatTurnItem]) ggui.Widget {
		// A read of the conversation brings every message anew: a turn is
		// drawn again only when what it holds changed.
		turn := ggui.Map(row.Value, func(t chatTurnItem) chatTurnItem { return t }).WithEqual(deepEqual)
		w := ggui.View(turn, func(t chatTurnItem) ggui.Widget {
			info, _ := m.session(id)
			return chatTurnRow(m, id, info, state, t)
		})
		if find == nil {
			return w
		}
		return find.mark(row.Item.Key, w)
	}).Gap(chatGap).Stretch().EstimatedExtent(chatTurnEstimate)
	// A drag selects through the turns in view, from one message into the
	// next, and copies them as they read.
	area := ggui.SelectionArea(ggui.Column(head, list).Stretch())
	if find != nil {
		find.area = area
	}
	content := &lane{child: ggui.Box(area).Pad(18, 0)}
	return ggui.Scroll(content).FollowEnd().Bar(tokSurface3)
}

// olderNear is how near the top of the feed the reader comes before older
// messages are read, and toLatestFar how far from the bottom before the
// way back to the latest shows.
const (
	olderNear   = 400
	toLatestFar = 240
)

// hostBadge names the node the session runs on, over the feed's top-left
// corner.
func hostBadge(m *model, id string) ggui.Widget {
	name := ggui.Derived(func() string {
		s, ok := sessionIn(m.Sessions.Get(), id)
		if !ok {
			return ""
		}
		return nodeName(m.Nodes.Get(), s.GetNodeId())
	})
	return ggui.View(name, func(n string) ggui.Widget {
		if n == "" {
			return ggui.Box()
		}
		return ggui.Padding(overlayChip(icon("server").Size(13), muted(ggui.Text(n).Size(12).NoWrap())), 10, 0, 0, 14)
	})
}

// toLatestButton floats over the feed's foot while the reader is up the
// conversation, and takes them back down to the latest.
func toLatestButton(m *model, state *chatState, scroll *ggui.ScrollController, offset ggui.Readable[float64]) ggui.Widget {
	// A Map, so the button is built as the reader crosses the line, not on
	// every step of the scroll.
	far := ggui.Map(offset, func(o float64) bool { return ggui.Untrack(scroll.MaxOffset)-o >= toLatestFar })
	return ggui.View(far, func(far bool) ggui.Widget {
		if !far {
			return ggui.Box()
		}
		btn := ui.ButtonOf(icon("arrow-down").Size(16).Color(tokFg), func() { m.toLatest(state, scroll) }).
			Outline().Pad(8).Radius(18).Name(T("Latest"))
		return ggui.Padding(ggui.Align(ggui.Box(btn).Radius(18).Shadow(uitheme.Use().PanelShadow)).At(0.5, 1), 0, 0, 14, 0)
	})
}

// onPaint runs fn once, the first time it is painted: after the layout it
// is part of, which has the scroll's new extent.
type onPaint struct {
	child ggui.Widget
	fn    func(dst *ggui.Canvas, r ggui.Rect)
}

func (o *onPaint) Layout(c ggui.Constraints, env ggui.Env) ggui.Size { return o.child.Layout(c, env) }

func (o *onPaint) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(o.child, r)
	if o.fn != nil {
		o.fn(dst, r)
		o.fn = nil
	}
}

// lane centres its child in the width it is given, at most chatLaneWidth
// wide with laneGutter on each side: the transcript's column, which the
// composer and the cards under the feed share. Unlike Align it takes its
// child's height, as it must under a Scroll and in a Column.
type lane struct {
	child ggui.Widget
	size  ggui.Size
}

// laneGutter is the room on each side of the lane.
const laneGutter = 24

func (l *lane) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w := max(min(c.MaxW, chatLaneWidth)-2*laneGutter, 0)
	l.size = l.child.Layout(ggui.Constraints{MinW: w, MaxW: w, MaxH: c.MaxH}, env)
	return c.Constrain(ggui.Sz(c.MaxW, l.size.H))
}

func (l *lane) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(l.child, ggui.Rct(ggui.Pt(r.Origin.X+(r.Size.W-l.size.W)/2, r.Origin.Y), l.size))
}

// chatTurns draws the conversation: the user on the right, the agent's prose
// as a page, and each run of tools as one quiet card whose rows fold. A
// long run folds into one line naming its tools, all but the last run,
// which is the agent's work in hand.
func chatTurns(messages []*protocol.ChatMessage, agents map[string]*protocol.SubAgent) []chatTurnItem {
	var turns []chatTurnItem
	lastRun := -1
	for i := range messages {
		if messages[i].GetRole() == "tool" && (i == 0 || messages[i-1].GetRole() != "tool") {
			lastRun = i
		}
	}
	seen := map[string]int{}
	for i := 0; i < len(messages); {
		start := i
		t := chatTurnItem{Key: chatMessageKey(messages[i])}
		if messages[i].GetRole() == "tool" {
			t.Key = "run:" + t.Key
			t.Folded = start != lastRun
			for i < len(messages) && messages[i].GetRole() == "tool" {
				t.Messages = append(t.Messages, messages[i])
				t.Agents = append(t.Agents, agents[messages[i].GetId()])
				i++
			}
		} else {
			t.Messages = messages[i : i+1]
			i++
		}
		// The same words said twice, with no ID to tell them apart, are
		// two turns.
		if n := seen[t.Key]; n > 0 {
			seen[t.Key]++
			t.Key += "#" + strconv.Itoa(n)
		} else {
			seen[t.Key] = 1
		}
		turns = append(turns, t)
	}
	return turns
}

// chatTurnItem is one turn of the feed: a message, or a run of tool calls
// with the sub-agent each started, if any.
type chatTurnItem struct {
	Key      string
	Messages []*protocol.ChatMessage
	Agents   []*protocol.SubAgent
	Folded   bool // a long run folded to a line
	Sending  bool // a message sent, not yet in the log
}

// chatGap is the room between the feed's turns, and chatTurnEstimate how
// tall one is taken to be until it is laid out.
const (
	chatGap          = 14
	chatTurnEstimate = 120
)

// chatTurnRow draws a turn: the user's on the right, the agent's prose as
// a page, a run of tools as one quiet card whose rows fold.
func chatTurnRow(m *model, id string, info *protocol.SessionInfo, state *chatState, t chatTurnItem) ggui.Widget {
	if len(t.Messages) == 0 {
		return ggui.Box()
	}
	if t.Sending {
		label := ggui.Row(ggui.Spacer(), icon("clock").Size(11).Color(tokExtraMuted), ggui.Text(T("Sent")).Size(11).Color(tokExtraMuted)).Gap(4).Align(ggui.AlignCenter)
		return ggui.Column(ggui.Opacity(chatTurn(m, id, info, t.Messages[0]), 0.6), label).Gap(4).Stretch()
	}
	if t.Messages[0].GetRole() != "tool" {
		return chatTurn(m, id, info, t.Messages[0])
	}
	var tools []ggui.Widget
	var names []string
	for i, msg := range t.Messages {
		tools = append(tools, toolRow(m, id, state, msg, t.Agents[i]))
		name, _, _, _, _ := toolParts(msg)
		names = append(names, name)
	}
	var body ggui.Widget = ggui.Column(tools...).Gap(2).Stretch()
	if len(tools) >= foldRun {
		body = toolRun(state, chatMessageKey(t.Messages[0]), names, t.Folded, body)
	}
	return ggui.Box(body).Fill(tokSurface1).Border(1, tokBorder).Radius(10).Pad(6, 8)
}

// foldRun is how many tool calls in a row fold into one line.
const foldRun = 3

// toolRun is a long run of tool calls, folded to a line that counts them
// and names their tools.
func toolRun(state *chatState, key string, names []string, folded bool, body ggui.Widget) ggui.Widget {
	open := state.toolOpen("run:"+key, !folded)
	var distinct []string
	for _, n := range names {
		if !slices.Contains(distinct, n) {
			distinct = append(distinct, n)
		}
	}
	title := Tf("%d tool calls", len(names))
	header := foldHeader(open, icon("layers").Size(14).Color(tokMuted), ggui.Text(title).Size(12.5).NoWrap(), strings.Join(distinct, ", "), title)
	return ggui.Column(header, ggui.If(open, func() ggui.Widget { return body })).Gap(2).Stretch()
}

// foldHeader is the line a fold opens and closes from: a glyph, a title,
// a muted detail, and a chevron that turns as it opens.
func foldHeader(open ggui.Binding[bool], glyph, title ggui.Widget, detail, name string) ggui.Widget {
	return ui.ButtonOf(ggui.Row(
		glyph,
		title,
		ggui.Expanded(muted(ggui.Text(detail).Size(12).NoWrap().Ellipsis())),
		ggui.View(open, func(o bool) ggui.Widget {
			return icon("chevron-down").Size(13).Color(tokExtraMuted).Rotate(pick(o, 0.0, -math.Pi/2))
		}),
	).Gap(8).Stretch(), func() { open.Set(!ggui.Untrack(open.Get)) }).Ghost().Pad(4, 6).Radius(6).Name(name)
}

func chatTurn(m *model, id string, info *protocol.SessionInfo, msg *protocol.ChatMessage) ggui.Widget {
	if msg.GetRole() == "user" {
		bubble := ggui.Box(ggui.Text(msg.GetText()).Size(14).Selectable()).
			Fill(tokSurface2).Radius(12).Pad(10, 14).MaxWidth(680)
		var turn ggui.Widget = ggui.Row(ggui.Spacer(), &withCopy{content: bubble, button: copyButton(m, msg.GetText())}).Stretch()
		// The images it holds, and those it names by their paths, over it.
		named := imageRefs(msg.GetText(), info.GetProject())
		if len(msg.GetImages())+len(named) > 0 {
			thumbs := ggui.Row(ggui.Spacer(), ggui.Box(chatImages(m, info.GetNodeId(), msg.GetImages(), named)).MaxWidth(680)).Stretch()
			if strings.TrimSpace(msg.GetText()) == "" {
				return thumbs
			}
			turn = ggui.Column(thumbs, turn).Gap(6).Stretch()
		}
		return turn
	}
	r := &mdRenderer{m: m, id: id, doc: docInfo{Node: info.GetNodeId(), Path: joinPath(info.GetProject(), "conversation.md")}, src: []byte(msg.GetText())}
	root := markdown.Parser().Parse(text.NewReader(r.src))
	return &withCopy{content: ggui.Column(r.blocks(root)...).Gap(10).Stretch(), button: copyButton(m, msg.GetText())}
}

// withCopy is a message with its copy button over its bottom-right
// corner, inside it and taking no room of its own, while the pointer is
// over the message. Where the pointer is decides it, not hover: selectable
// text over the message takes the hover for itself.
// copyInset is how far the copy button sits in from the message's edges.
const copyInset = 4

type withCopy struct {
	content, button ggui.Widget
	buttonSize      ggui.Size
}

func (w *withCopy) Layout(cs ggui.Constraints, env ggui.Env) ggui.Size {
	size := w.content.Layout(cs, env)
	w.buttonSize = w.button.Layout(ggui.Loose(ggui.Sz(size.W, ggui.Unbounded)), env)
	return size
}

func (w *withCopy) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(w.content, r)
	at := ggui.Rct(ggui.Pt(r.Origin.X+r.Size.W-w.buttonSize.W-copyInset, r.Origin.Y+r.Size.H-w.buttonSize.H-copyInset), w.buttonSize)
	if p, ok := dst.Pointer(); ok && r.Contains(p) {
		dst.Paint(w.button, at)
	}
}

// copyButton copies a message as the agent wrote it, Markdown and all, and
// shows a check for a moment after.
func copyButton(m *model, text string) ggui.Widget {
	copied := ggui.State(false)
	return ggui.View(copied, func(done bool) ggui.Widget {
		glyph := icon(pick(done, "check", "copy")).Size(13).Color(tokMuted)
		return ui.Tooltip(ui.ButtonOf(glyph, func() {
			ggui.CurrentClipboard().Write(text)
			copied.Set(true)
			time.AfterFunc(1500*time.Millisecond, func() { m.post(func() { copied.Set(false) }) })
		}).Ghost().Pad(3).Radius(5).Name(T("Copy")), T("Copy"))
	})
}

// toolRow is one tool call: a glyph, its name and argument summary, and a
// fold with the arguments and what it printed. A call at work turns a
// spinner in place of its glyph; one that started a subagent says under
// it how far the agent has got.
func toolRow(m *model, id string, state *chatState, msg *protocol.ChatMessage, agent *protocol.SubAgent) ggui.Widget {
	name, detail, _, _, failed := toolParts(msg)
	open := state.toolOpen(chatMessageKey(msg), failed)
	var glyph ggui.Widget = icon(toolGlyph(msg.GetKind())).Size(14).Color(pick(failed, tokDotDown, tokMuted))
	if msg.GetPending() || agent.GetRunning() {
		still := glyph
		glyph = ggui.Reactive(func() ggui.Widget {
			if toolLive(m, id, msg, agent) {
				return ui.Spinner().Size(14)
			}
			return still
		})
	}
	label := name
	if detail != "" {
		label += "  " + detail
	}
	header := foldHeader(open, glyph, ggui.Text(name).Size(12.5).Font(ggui.DefaultMonoFont()).NoWrap(), detail, label)
	rows := []ggui.Widget{header}
	if agent != nil {
		rows = append(rows, ggui.Padding(agentLink(m, id, agent, agentProgress(agent)), 0, 6, 2, 28))
	}
	rows = append(rows, ggui.If(open, func() ggui.Widget { return toolBody(m, msg) }))
	return ggui.Column(rows...).Gap(2).Stretch()
}

// toolLive reports whether a call is at work: a subagent it started still
// running, or no answer yet while its session's agent works. A call left
// unanswered by an agent stopped under it is not.
func toolLive(m *model, id string, msg *protocol.ChatMessage, agent *protocol.SubAgent) bool {
	s, ok := sessionIn(m.Sessions.Get(), id)
	switch {
	case !ok || s.GetState() != protocol.StateRunning:
		return false
	case agent.GetRunning():
		return true
	case !msg.GetPending():
		return false
	}
	switch s.GetStatus() {
	case protocol.AgentIdle, protocol.AgentDone:
		return false
	}
	return true
}

func agentsByCall(agents []*protocol.SubAgent) map[string]*protocol.SubAgent {
	out := make(map[string]*protocol.SubAgent, len(agents))
	for _, a := range agents {
		if a.GetCallId() != "" {
			out[a.GetCallId()] = a
		}
	}
	return out
}

// agentProgress is how far a subagent has got: its sort, the calls it has
// made and its latest.
func agentProgress(a *protocol.SubAgent) ggui.Widget {
	var parts []string
	if a.GetKind() != "" {
		parts = append(parts, a.GetKind())
	}
	parts = append(parts, Tf("%d tool calls", a.GetToolCalls()))
	line := []ggui.Widget{muted(ggui.Text(strings.Join(parts, " · ")).Size(11.5).NoWrap())}
	if a.GetTool() != "" {
		line = append(line,
			icon(toolGlyph(a.GetToolKind())).Size(12).Color(tokExtraMuted),
			ggui.Expanded(ggui.Text(strings.TrimSpace(a.GetTool()+"  "+a.GetDetail())).Size(11.5).Color(tokExtraMuted).NoWrap().Ellipsis()))
	}
	return ggui.Row(line...).Gap(6).Align(ggui.AlignCenter)
}

// compactingRow says, over the composer, that session id's agent is
// compacting its conversation, while it does.
func compactingRow(m *model, id string) ggui.Widget {
	compacting := ggui.Derived(func() bool {
		s, ok := sessionIn(m.Sessions.Get(), id)
		return ok && s.GetState() == protocol.StateRunning && s.GetStatus() == protocol.AgentCompacting
	})
	return ggui.View(compacting, func(on bool) ggui.Widget {
		if !on {
			return ggui.Box()
		}
		return &lane{child: ggui.Box(ggui.Row(
			ui.Spinner().Size(13),
			muted(ggui.Text(T("Compacting conversation…")).Size(12.5).NoWrap()),
		).Gap(8).Align(ggui.AlignCenter)).Fill(tokSurface1).Border(1, tokBorder).Radius(10).Pad(8, 12)}
	})
}

// runningAgents lists, over the composer, the subagents of session id
// still at work: what each was asked, how many calls it has made, its
// latest, and how long it has run. Nothing shows while none runs, nor once
// the session has ended.
func runningAgents(m *model, id string, state *chatState) ggui.Widget {
	running := ggui.Derived(func() []*protocol.SubAgent {
		if s, ok := sessionIn(m.Sessions.Get(), id); !ok || s.GetState() != protocol.StateRunning {
			return nil
		}
		var out []*protocol.SubAgent
		for _, a := range state.result.Get().Chat.GetAgents() {
			if a.GetRunning() {
				out = append(out, a)
			}
		}
		return out
	}).WithEqual(func(a, b []*protocol.SubAgent) bool { return slices.EqualFunc(a, b, protocol.Same[*protocol.SubAgent]) })
	return ggui.View(running, func(agents []*protocol.SubAgent) ggui.Widget {
		if len(agents) == 0 {
			return ggui.Box()
		}
		rows := make([]ggui.Widget, len(agents))
		for i, a := range agents {
			title := cmp.Or(a.GetDescription(), a.GetKind(), T("Subagent"))
			rows[i] = agentLink(m, id, a, ggui.Row(
				ui.Spinner().Size(13),
				ggui.Text(title).Size(12.5).Weight(ggui.WeightMedium).NoWrap(),
				ggui.Expanded(agentProgress(a)),
				ggui.View(m.Minute, func(now time.Time) ggui.Widget {
					if a.GetStarted() == 0 {
						return ggui.Box()
					}
					return muted(ggui.Text(untilText(now.Sub(time.Unix(a.GetStarted(), 0)))).Size(11.5).NoWrap())
				}),
			).Gap(8).Align(ggui.AlignCenter))
		}
		return &lane{child: ggui.Box(ggui.Column(rows...).Gap(6).Stretch()).
			Fill(tokSurface1).Border(1, tokBorder).Radius(10).Pad(8, 12)}
	})
}

// toolFrame is the frame of a tool's code, output or command.
func toolFrame(w ggui.Widget) *ggui.BoxWidget {
	return ggui.Box(w).Fill(tokSurface0).Border(1, tokBorder).Radius(6)
}

func toolLabel(s string) ggui.Widget { return ggui.Text(s).Size(11).Color(tokExtraMuted) }

func toolBlock(text string, failed bool) ggui.Widget {
	t := ggui.Text(strings.TrimRight(text, "\n")).Font(ggui.DefaultMonoFont()).Size(12).
		LineHeight(1.4).Selectable()
	if failed {
		t = t.Color(tokDotDown)
	}
	return toolFrame(t).Pad(8, 10)
}

// toolParts reads a tool message: a call with its result, or output whose
// call is not in the window.
func toolParts(msg *protocol.ChatMessage) (name, detail, args, result string, failed bool) {
	if msg.GetOutput() {
		return cmp.Or(msg.GetTool(), T("Output")), cmp.Or(msg.GetDetail(), agent.OneLine(msg.GetText(), 120)), "", msg.GetText(), msg.GetError()
	}
	return cmp.Or(msg.GetTool(), T("Tool")), msg.GetDetail(), msg.GetText(), msg.GetResult(), msg.GetError()
}

// chatMessageKey keeps a tool row's fold across refreshes: the log's own ID,
// or a hash of a message without one.
func chatMessageKey(msg *protocol.ChatMessage) string {
	if msg.GetId() != "" {
		return msg.GetId()
	}
	h := fnv.New64a()
	for _, s := range []string{msg.GetRole(), msg.GetTool(), msg.GetText()} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 16)
}
