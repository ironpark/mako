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
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transcript"
	"github.com/yuin/goldmark/text"
)

// chatLaneWidth is the width the transcript and the composer share.
const chatLaneWidth = 900

type chatResult struct {
	Chat   *protocol.Chat
	Err    string
	Loaded bool
}
type chatState struct {
	result  *ggui.StateValue[chatResult]
	draft   *ggui.StateValue[string]
	tools   *ggui.StateValue[map[string]bool] // open tool rows, by message key
	loading bool
	// sent is what the composer sent, oldest first, for ↑ and ↓ to bring
	// back; recall is the one shown, len(sent) for none.
	sent   []string
	recall int
}

func (m *model) chatState(id string) *chatState {
	if state := m.chats[id]; state != nil {
		return state
	}
	state := &chatState{
		result: ggui.State(chatResult{}).WithEqual(deepEqual),
		draft:  ggui.State(""),
		tools:  ggui.State(map[string]bool{}).WithEqual(maps.Equal),
	}
	m.chats[id] = state
	return state
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
	if state.loading || m.chats[id] != state {
		return
	}
	info, ok := m.session(id)
	if !ok {
		return
	}
	state.loading = true
	req := &protocol.ReadChat{NodeId: m.remoteID(info.GetNodeId()), SessionId: id}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Chat, error) {
		return c.ReadChat(ctx, m.key(), req)
	}, func(chat *protocol.Chat, err error) {
		if m.chats[id] != state {
			return
		}
		state.loading = false
		next := chatResult{Chat: chat, Loaded: true}
		if err != nil {
			next.Chat = ggui.Peek(state.result).Chat
			next.Err = err.Error()
		}
		state.result.Set(next)
	})
}

// chatCapable reports whether a session's agent has a native conversation to
// show. A plain shell has none, so it keeps its terminal alone.
func (m *model) chatCapable(id string) bool {
	s, ok := m.session(id)
	return ok && transcript.Supported(s.GetAgent())
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
			OnLink(func(target, cwd string) { m.followLink(id, target, cwd) })
		body := ggui.DropZone(term, func(d ggui.Dropped[pathDrag]) { term.setDropOver(false); term.typePaths([]string{d.Value.Path}) }).OnHover(term.setDropOver)
		return ggui.Column(approvalBar(m, id), ggui.Expanded(ggui.Stack(body, usageOverlay(m, id, nil)).Expand())).Stretch()
	})
}

// viewSwitch switches a session between its terminal and its chat: a
// pill whose track holds the two glyphs, a light thumb sliding behind the
// one in view. A click anywhere on it switches to the other, which is what
// it is named for. A plain shell has no conversation, so it has none.
func viewSwitch(m *model, id string, group int) ggui.Widget {
	mode := ggui.Bind(func() bool { return m.ChatModes.Get()[id] }, func(chat bool) { m.focusGroup(group); m.setChatMode(id, chat) })
	side := func(glyph string, chat bool) ggui.Widget {
		g := ggui.Reactive(func() ggui.Widget {
			// Black on the light thumb in view; the other quiet on the track.
			var c color.Color = tokExtraMuted
			if mode.Get() == chat {
				c = viewThumbInk()
			}
			return icon(glyph).Size(14).Color(c)
		})
		return ggui.Box(ggui.Center(g)).Size(viewSideWidth+viewToggleInset, viewSideHeight+2*viewToggleInset)
	}
	toggle := &viewToggle{right: mode, child: ggui.Row(side("square-terminal", false), side("messages-square", true))}
	button := ui.ButtonOf(toggle, func() { mode.Set(!ggui.Untrack(mode.Get)) }).Variant(viewSideButton).Pad(0).
		Radius((viewSideHeight + 2*viewToggleInset) / 2).
		BindName(ggui.Map(mode, func(chat bool) string { return pick(chat, T("Terminal"), T("Chat")) }))
	return ui.Tooltip(button, T("Toggle chat and terminal"))
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
	return tokSurface0
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
	content := ggui.View(state.result, func(v chatResult) ggui.Widget {
		var rows []ggui.Widget
		if v.Err != "" {
			rows = append(rows, ggui.Text(v.Err).Size(13).Color(tokMuted).Selectable())
		}
		switch {
		case !v.Loaded:
			rows = append(rows, muted(ggui.Text(T("Loading conversation…"))))
		case v.Err != "":
		case !v.Chat.GetSupported():
			rows = append(rows, muted(ggui.Text(T("Chat is available for Claude and Codex. Use Terminal for this agent."))))
		case v.Chat.GetConversation() == "":
			rows = append(rows, muted(ggui.Text(T("Waiting for this agent to save its conversation. It will connect automatically."))))
		case len(v.Chat.GetMessages()) == 0:
			rows = append(rows, muted(ggui.Text(T("No messages yet. The conversation updates as the agent saves it."))))
		}
		if v.Chat.GetTruncated() {
			rows = append(rows, ggui.Box(muted(ggui.Text(T("Showing recent messages; older or long content is omitted.")).Size(11.5))).
				Fill(tokSurface1).Radius(6).Pad(4, 8))
		}
		info, _ := m.session(id)
		rows = append(rows, chatTurns(m, id, info, state, v.Chat.GetMessages(), agentsByCall(v.Chat.GetAgents()))...)
		if len(v.Chat.GetMessages()) > 0 && scroll.MaxOffset()-scroll.Offset() < 32 {
			rows = append(rows, &revealOnce{child: ggui.Box().Height(1), done: func() {}})
		}
		return ggui.Box(ggui.Column(rows...).Gap(14).Stretch()).Pad(18, 24).MaxWidth(chatLaneWidth)
	})
	feed := ggui.Scroll(ggui.Align(content).Top()).Controller(scroll).Bar(tokSurface3)
	chatUsage := ggui.Map(state.result, func(v chatResult) *protocol.Usage {
		return v.Chat.GetUsage()
	}).WithEqual(protocol.Same)
	usage := ggui.View(chatUsage, func(u *protocol.Usage) ggui.Widget {
		if u.IsZero() {
			return ggui.Box()
		}
		return usageTip(m, u, muted(ggui.Text(usageLine(u)).Size(12).NoWrap()))
	})
	footer := ggui.Box(ggui.Row(
		ggui.Expanded(muted(ggui.Text(T("Messages go to the live session. Use Terminal for special keys.")).Size(12).NoWrap().Ellipsis())),
		usage,
		iconButton("refresh-cw", T("Refresh"), func() { m.loadChat(id, state) }),
		ui.Button(T("Latest"), func() { scroll.ScrollTo(scroll.MaxOffset()) }).Ghost(),
	).Gap(8)).Pad(6, 12)
	// The composer belongs to Chat alone, as in ovis: Terminal mode's grid
	// is its own input surface, and a file view has no session to type into.
	return ggui.Pointer(ggui.Column(ggui.Expanded(ggui.Stack(feed, usageOverlay(m, id, chatUsage)).Expand()), footer, approvalCard(m, id), runningAgents(m, id, state), composer(m, id, state.draft)).Stretch()).
		OnDown(func(ggui.PointerEvent) { m.focusGroup(group) })
}

// chatTurns draws the conversation: the user on the right, the agent's prose
// as a page, and each run of tools as one quiet card whose rows fold. A
// long run folds into one line naming its tools, all but the last run,
// which is the agent's work in hand.
func chatTurns(m *model, id string, info *protocol.SessionInfo, state *chatState, messages []*protocol.ChatMessage, agents map[string]*protocol.SubAgent) []ggui.Widget {
	var rows []ggui.Widget
	lastRun := -1
	for i := range messages {
		if messages[i].GetRole() == "tool" && (i == 0 || messages[i-1].GetRole() != "tool") {
			lastRun = i
		}
	}
	for i := 0; i < len(messages); {
		if messages[i].GetRole() == "tool" {
			start := i
			var tools []ggui.Widget
			var names []string
			for i < len(messages) && messages[i].GetRole() == "tool" {
				tools = append(tools, toolRow(m, id, state, messages[i], agents[messages[i].GetId()]))
				name, _, _, _, _ := toolParts(messages[i])
				names = append(names, name)
				i++
			}
			var body ggui.Widget = ggui.Column(tools...).Gap(2).Stretch()
			if len(tools) >= foldRun {
				body = toolRun(state, chatMessageKey(messages[start]), names, start != lastRun, body)
			}
			rows = append(rows, ggui.Box(body).Fill(tokSurface1).Border(1, tokBorder).Radius(10).Pad(6, 8))
			continue
		}
		rows = append(rows, chatTurn(m, id, info, messages[i]))
		i++
	}
	return rows
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
		return ggui.Row(ggui.Spacer(), &withCopy{content: bubble, button: copyButton(m, msg.GetText())}).Stretch()
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
	var glyph ggui.Widget = icon(toolGlyph(name)).Size(14).Color(pick(failed, tokDotDown, tokMuted))
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
		rows = append(rows, ggui.Padding(agentProgress(agent), 0, 6, 2, 28))
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
			icon(toolGlyph(a.GetTool())).Size(12).Color(tokExtraMuted),
			ggui.Expanded(ggui.Text(strings.TrimSpace(a.GetTool()+"  "+a.GetDetail())).Size(11.5).Color(tokExtraMuted).NoWrap().Ellipsis()))
	}
	return ggui.Row(line...).Gap(6).Align(ggui.AlignCenter)
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
			rows[i] = ggui.Row(
				ui.Spinner().Size(13),
				ggui.Text(title).Size(12.5).Weight(ggui.WeightMedium).NoWrap(),
				ggui.Expanded(agentProgress(a)),
				ggui.View(m.Minute, func(now time.Time) ggui.Widget {
					if a.GetStarted() == 0 {
						return ggui.Box()
					}
					return muted(ggui.Text(untilText(now.Sub(time.Unix(a.GetStarted(), 0)))).Size(11.5).NoWrap())
				}),
			).Gap(8).Align(ggui.AlignCenter)
		}
		return ggui.Box(ggui.Box(ggui.Column(rows...).Gap(6).Stretch()).
			Fill(tokSurface1).Border(1, tokBorder).Radius(10).Pad(8, 12)).Pad(0, 16)
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
		return cmp.Or(msg.GetTool(), T("Output")), cmp.Or(msg.GetDetail(), transcript.OneLine(msg.GetText(), 120)), "", msg.GetText(), msg.GetError()
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
