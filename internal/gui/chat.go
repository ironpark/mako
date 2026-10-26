package gui

import (
	"cmp"
	"context"
	"hash/fnv"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transcript"
	"github.com/yuin/goldmark/text"
)

// chatLaneWidth is the width the transcript and the composer share.
const chatLaneWidth = 900

type chatResult struct {
	Chat   protocol.Chat
	Err    string
	Loaded bool
}
type chatState struct {
	result  *ggui.StateValue[chatResult]
	draft   *ggui.StateValue[string]
	tools   *ggui.StateValue[map[string]bool] // open tool rows, by message key
	loading bool
}

func (m *model) chatState(id string) *chatState {
	if state := m.chats[id]; state != nil {
		return state
	}
	state := &chatState{
		result: ggui.State(chatResult{}).WithEqual(func(a, b chatResult) bool { return reflect.DeepEqual(a, b) }),
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
	req := protocol.ReadChat{NodeID: m.remoteID(info.NodeID), SessionID: id}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (protocol.Chat, error) {
		return c.ReadChat(ctx, m.key(), req)
	}, func(chat protocol.Chat, err error) {
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
	return ok && transcript.Supported(s.Agent)
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
		term := newTerminalView(m.conns[id], func() prefs { return ggui.Peek(m.Prefs) }, focus).OnPress(func() { m.focusGroup(group) })
		body := ggui.DropZone(term, func(d ggui.Dropped[pathDrag]) { term.setDropOver(false); term.typePaths([]string{d.Value.Path}) }).OnHover(term.setDropOver)
		return ggui.Column(approvalBar(m, id), ggui.Expanded(ggui.Stack(body, usageOverlay(m, id, nil)).Expand())).Stretch()
	})
}

// viewSwitch switches a session between its terminal and its chat: a
// switch between the two glyphs, the one in view in the foreground colour.
// A plain shell has no conversation, so it has none.
func viewSwitch(m *model, id string, group int) ggui.Widget {
	mode := ggui.Bind(func() bool { return m.ChatModes.Get()[id] }, func(chat bool) { m.focusGroup(group); m.setChatMode(id, chat) })
	sw := ui.Switch(mode, "")
	sw.SetName(T("Session view"))
	side := func(glyph, label string, chat bool) ggui.Widget {
		g := ggui.Reactive(func() ggui.Widget {
			return icon(glyph).Size(14).Color(pick(mode.Get() == chat, tokFg, tokExtraMuted))
		})
		return ui.Tooltip(ui.ButtonOf(g, func() { mode.Set(chat) }).Ghost().Pad(3).Radius(4).Name(label), label)
	}
	return ggui.Row(side("square-terminal", T("Terminal"), false), sw, side("messages-square", T("Chat"), true)).Gap(4).Align(ggui.AlignCenter)
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
		case !v.Chat.Supported:
			rows = append(rows, muted(ggui.Text(T("Chat is available for Claude and Codex. Use Terminal for this agent."))))
		case v.Chat.Conversation == "":
			rows = append(rows, muted(ggui.Text(T("Waiting for this agent to save its conversation. It will connect automatically."))))
		case len(v.Chat.Messages) == 0:
			rows = append(rows, muted(ggui.Text(T("No messages yet. The conversation updates as the agent saves it."))))
		}
		if v.Chat.Truncated {
			rows = append(rows, ggui.Box(muted(ggui.Text(T("Showing recent messages; older or long content is omitted.")).Size(11.5))).
				Fill(tokSurface1).Radius(6).Pad(4, 8))
		}
		info, _ := m.session(id)
		rows = append(rows, chatTurns(m, id, info, state, v.Chat.Messages)...)
		if len(v.Chat.Messages) > 0 && scroll.MaxOffset()-scroll.Offset() < 32 {
			rows = append(rows, &revealOnce{child: ggui.Box().Height(1), done: func() {}})
		}
		return ggui.Box(ggui.Column(rows...).Gap(14).Stretch()).Pad(18, 24).MaxWidth(chatLaneWidth)
	})
	feed := ggui.Scroll(ggui.Align(content).Top()).Controller(scroll).Bar(tokSurface3)
	chatUsage := ggui.Map(state.result, func(v chatResult) protocol.Usage {
		if v.Chat.Usage == nil {
			return protocol.Usage{}
		}
		return *v.Chat.Usage
	})
	usage := ggui.View(chatUsage, func(u protocol.Usage) ggui.Widget {
		if u == (protocol.Usage{}) {
			return ggui.Box()
		}
		return ui.Tooltip(muted(ggui.Text(usageLine(u)).Size(12).NoWrap()), usageDetail(u))
	})
	footer := ggui.Box(ggui.Row(
		ggui.Expanded(muted(ggui.Text(T("Messages go to the live session. Use Terminal for special keys.")).Size(12).NoWrap().Ellipsis())),
		usage,
		iconButton("refresh-cw", T("Refresh"), func() { m.loadChat(id, state) }),
		ui.Button(T("Latest"), func() { scroll.ScrollTo(scroll.MaxOffset()) }).Ghost(),
	).Gap(8)).Pad(6, 12)
	// The composer belongs to Chat alone, as in ovis: Terminal mode's grid
	// is its own input surface, and a file view has no session to type into.
	return ggui.Pointer(ggui.Column(ggui.Expanded(ggui.Stack(feed, usageOverlay(m, id, chatUsage)).Expand()), footer, approvalCard(m, id), composer(m, id, state.draft)).Stretch()).
		OnDown(func(ggui.PointerEvent) { m.focusGroup(group) })
}

// chatTurns draws the conversation: the user on the right, the agent's prose
// as a page, and each run of tools as one quiet card whose rows fold. A
// long run folds into one line naming its tools, all but the last run,
// which is the agent's work in hand.
func chatTurns(m *model, id string, info protocol.SessionInfo, state *chatState, messages []protocol.ChatMessage) []ggui.Widget {
	var rows []ggui.Widget
	lastRun := -1
	for i := range messages {
		if messages[i].Role == "tool" && (i == 0 || messages[i-1].Role != "tool") {
			lastRun = i
		}
	}
	for i := 0; i < len(messages); {
		if messages[i].Role == "tool" {
			start := i
			var tools []ggui.Widget
			var names []string
			for i < len(messages) && messages[i].Role == "tool" {
				tools = append(tools, toolRow(state, messages[i]))
				names = append(names, toolName(messages[i]))
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
	header := ui.ButtonOf(ggui.Row(
		icon("layers").Size(14).Color(tokMuted),
		ggui.Text(Tf("%d tool calls", len(names))).Size(12.5).NoWrap(),
		ggui.Expanded(muted(ggui.Text(strings.Join(distinct, ", ")).Size(12).NoWrap().Ellipsis())),
		ggui.View(open, func(o bool) ggui.Widget {
			return icon("chevron-down").Size(13).Color(tokExtraMuted).Rotate(pick(o, 0.0, -math.Pi/2))
		}),
	).Gap(8).Stretch(), func() { open.Set(!ggui.Untrack(open.Get)) }).Ghost().Pad(4, 6).Radius(6).Name(Tf("%d tool calls", len(names)))
	return ggui.Column(header, ggui.If(open, func() ggui.Widget { return body })).Gap(2).Stretch()
}

// toolName is the name a tool row shows.
func toolName(msg protocol.ChatMessage) string {
	name, _, _, _, _ := toolParts(msg)
	return name
}

func chatTurn(m *model, id string, info protocol.SessionInfo, msg protocol.ChatMessage) ggui.Widget {
	// A message's copy button shows while the pointer is over it.
	hover := ggui.State(false)
	copyRow := func(right bool) ggui.Widget {
		btn := ggui.If(hover, func() ggui.Widget { return copyButton(m, msg.Text) })
		row := ggui.Row(btn, ggui.Spacer())
		if right {
			row = ggui.Row(ggui.Spacer(), btn)
		}
		return ggui.Box(row.Stretch()).Height(28)
	}
	if msg.Role == "user" {
		bubble := ggui.Box(ggui.Text(msg.Text).Size(14).Selectable()).
			Fill(tokSurface2).Radius(12).Pad(10, 14).MaxWidth(680)
		return ggui.Pointer(ggui.Column(
			ggui.Row(ggui.Spacer(), bubble).Stretch(),
			copyRow(true),
		).Gap(2).Stretch()).OnHover(hover.Set)
	}
	r := &mdRenderer{m: m, id: id, doc: docInfo{Node: info.NodeID, Path: joinPath(info.Project, "conversation.md")}, src: []byte(msg.Text)}
	root := markdown.Parser().Parse(text.NewReader(r.src))
	blocks := append(r.blocks(root), copyRow(false))
	return ggui.Pointer(ggui.Column(blocks...).Gap(10).Stretch()).OnHover(hover.Set)
}

// copyButton copies a message as the agent wrote it, Markdown and all, and
// shows a check for a moment after.
func copyButton(m *model, text string) ggui.Widget {
	copied := ggui.State(false)
	return ggui.View(copied, func(done bool) ggui.Widget {
		return iconButton(pick(done, "check", "copy"), T("Copy"), func() {
			ggui.CurrentClipboard().Write(text)
			copied.Set(true)
			time.AfterFunc(1500*time.Millisecond, func() { m.post(func() { copied.Set(false) }) })
		})
	})
}

// toolRow is one tool call: a glyph, its name and argument summary, and a
// fold with the arguments and what it printed.
func toolRow(state *chatState, msg protocol.ChatMessage) ggui.Widget {
	name, detail, args, result, failed := toolParts(msg)
	open := state.toolOpen(chatMessageKey(msg), failed)
	glyph := icon(toolGlyph(name)).Size(14).Color(tokMuted)
	if failed {
		glyph = glyph.Color(tokDotDown)
	}
	label := name
	if detail != "" {
		label += "  " + detail
	}
	header := ui.ButtonOf(ggui.Row(
		glyph,
		ggui.Text(name).Size(12.5).Font(ggui.DefaultMonoFont()).NoWrap(),
		ggui.Expanded(muted(ggui.Text(detail).Size(12).NoWrap().Ellipsis())),
		ggui.View(open, func(o bool) ggui.Widget {
			rotation := -math.Pi / 2
			if o {
				rotation = 0
			}
			return icon("chevron-down").Size(13).Color(tokExtraMuted).Rotate(rotation)
		}),
	).Gap(8).Stretch(), func() { open.Set(!ggui.Untrack(open.Get)) }).Ghost().Pad(4, 6).Radius(6).Name(label)
	return ggui.Column(header, ggui.If(open, func() ggui.Widget { return toolBody(args, result, failed) })).Gap(2).Stretch()
}

func toolBody(args, result string, failed bool) ggui.Widget {
	if failed && result == "" {
		result = T("The tool failed without output.")
	}
	var rows []ggui.Widget
	if args != "" {
		rows = append(rows, toolLabel(T("Arguments")), toolBlock(args, false))
	}
	if result != "" {
		label := T("Output")
		if failed {
			label = T("Error")
		}
		rows = append(rows, toolLabel(label), toolBlock(result, failed))
	}
	if len(rows) == 0 {
		return ggui.Padding(muted(ggui.Text(T("No output")).Size(12)), 4, 6, 6, 6)
	}
	return ggui.Padding(ggui.Column(rows...).Gap(4).Stretch(), 4, 6, 6, 18)
}

func toolLabel(s string) ggui.Widget { return ggui.Text(s).Size(11).Color(tokExtraMuted) }

func toolBlock(text string, failed bool) ggui.Widget {
	t := ggui.Text(strings.TrimRight(text, "\n")).Font(ggui.DefaultMonoFont()).Size(12).
		LineHeight(1.4).Selectable()
	if failed {
		t = t.Color(tokDotDown)
	}
	return ggui.Box(t).Fill(tokSurface0).Border(1, tokBorder).Radius(6).Pad(8, 10)
}

// toolParts reads a tool message: a call with its result, or output whose
// call is not in the window.
func toolParts(msg protocol.ChatMessage) (name, detail, args, result string, failed bool) {
	if msg.Output {
		return cmp.Or(msg.Tool, T("Output")), cmp.Or(msg.Detail, transcript.OneLine(msg.Text, 120)), "", msg.Text, msg.Error
	}
	return cmp.Or(msg.Tool, T("Tool")), msg.Detail, msg.Text, msg.Result, msg.Error
}

// chatMessageKey keeps a tool row's fold across refreshes: the log's own ID,
// or a hash of a message without one.
func chatMessageKey(msg protocol.ChatMessage) string {
	if msg.ID != "" {
		return msg.ID
	}
	h := fnv.New64a()
	for _, s := range []string{msg.Role, msg.Tool, msg.Text} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

func toolGlyph(name string) string {
	switch strings.ToLower(name) {
	case "bash", "shell", "exec_command", "exec", "write_stdin":
		return "square-terminal"
	case "read", "write", "edit", "multiedit", "notebookedit", "apply_patch":
		return "file-text"
	case "grep", "glob", "search":
		return "radar"
	case "webfetch", "websearch", "fetch":
		return "network"
	case "task", "agent":
		return "list"
	}
	return "square-dot"
}
