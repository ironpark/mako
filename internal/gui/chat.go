package gui

import (
	"context"
	"maps"
	"reflect"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/yuin/goldmark/text"
)

type chatResult struct {
	Chat   protocol.Chat
	Err    string
	Loaded bool
}
type chatState struct {
	result     *ggui.StateValue[chatResult]
	selected   string
	loading    bool
	generation int
}

func (m *model) chatState(id string) *chatState {
	if state := m.chats[id]; state != nil {
		return state
	}
	state := &chatState{result: ggui.State(chatResult{}).WithEqual(func(a, b chatResult) bool { return reflect.DeepEqual(a, b) })}
	m.chats[id] = state
	return state
}

func (m *model) setChatMode(id string, chat bool) {
	if id == "" || isDocTab(id) {
		return
	}
	m.ChatModes.Update(func(v map[string]bool) map[string]bool { v = maps.Clone(v); v[id] = chat; return v })
}

func (m *model) toggleChat() {
	id := ggui.Peek(m.Selected)
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
	generation := state.generation
	req := protocol.ReadChat{NodeID: m.remoteID(info.NodeID), SessionID: id, Conversation: state.selected}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (protocol.Chat, error) {
		return c.ReadChat(ctx, m.key(), req)
	}, func(chat protocol.Chat, err error) {
		if m.chats[id] != state || generation != state.generation {
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

// sessionView switches only presentation. The attachment and terminal parser
// stay alive when chat is shown, so switching back has the current screen.
func sessionView(m *model, id string, group int) ggui.Widget {
	mode := ggui.Bind(func() bool { return m.ChatModes.Get()[id] }, func(chat bool) { m.focusGroup(group); m.setChatMode(id, chat) })
	toggle := ui.ToggleGroup[bool](mode).Options([]bool{true, false}).Format(func(chat bool) string {
		if chat {
			return T("Chat")
		}
		return T("Terminal")
	}).Name(T("Session view"))
	bar := ggui.Box(ggui.Row(toggle, ggui.Spacer()).Gap(8)).Pad(6, 12).Fill(tokSurface0)
	body := ggui.Key(ggui.Map(m.ChatModes, func(v map[string]bool) bool { return v[id] }), func(chat bool) ggui.Widget {
		if chat {
			return chatView(m, id, group)
		}
		focus := ggui.Peek(m.FocusGroup) == group
		term := newTerminalView(m.conns[id], func() prefs { return ggui.Peek(m.Prefs) }, focus).OnPress(func() { m.focusGroup(group) })
		return ggui.DropZone(term, func(d ggui.Dropped[pathDrag]) { term.setDropOver(false); term.typePaths([]string{d.Value.Path}) }).OnHover(term.setDropOver)
	})
	return ggui.Column(bar, hline(), ggui.Expanded(body)).Stretch()
}

func chatView(m *model, id string, group int) ggui.Widget {
	state := m.chatState(id)
	ggui.Effect(func() ggui.Cleanup {
		m.loadChat(id, state)
		stop := make(chan struct{})
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					m.post(func() {
						if ggui.Peek(m.ChatModes)[id] {
							m.loadChat(id, state)
						}
					})
				}
			}
		}()
		return func() { close(stop) }
	})
	header := ggui.View(state.result, func(v chatResult) ggui.Widget {
		var items []ggui.Widget
		if len(v.Chat.Conversations) > 0 || state.selected != "" {
			choices := []ggui.Widget{ui.MenuItem(T("Choose another conversation"), func() {
				state.generation++
				state.loading = false
				state.selected = ""
				state.result.Set(chatResult{})
				m.loadChat(id, state)
			})}
			for _, conversation := range v.Chat.Conversations {
				cid := conversation.ID
				label := conversation.Updated.Local().Format("01/02 15:04") + " · " + cid
				choices = append(choices, ui.MenuItem(label, func() {
					state.generation++
					state.loading = false
					state.selected = cid
					state.result.Set(chatResult{})
					m.loadChat(id, state)
				}))
			}
			items = append(items, ui.MenuOf(ggui.Text(T("Conversation")).Size(12), choices...).Name(T("Conversation")))
		}
		items = append(items, ggui.Spacer(), iconButton("refresh-cw", T("Refresh"), func() { m.loadChat(id, state) }))
		return ggui.Box(ggui.Row(items...).Gap(8)).Pad(4, 12)
	})
	scroll := &ggui.ScrollController{}
	bodyResult := ggui.Derived(func() chatResult {
		v := state.result.Get()
		v.Chat.Conversations = nil
		return v
	}).WithEqual(func(a, b chatResult) bool { return reflect.DeepEqual(a, b) })
	content := ggui.View(bodyResult, func(v chatResult) ggui.Widget {
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
			rows = append(rows, muted(ggui.Text(T("Choose this session's conversation above. If none is listed, send a message in Terminal and refresh."))))
		case len(v.Chat.Messages) == 0:
			rows = append(rows, muted(ggui.Text(T("No messages yet. The conversation updates as the agent saves it."))))
		}
		if v.Chat.Truncated {
			rows = append(rows, muted(ggui.Text(T("Showing recent messages; older or long content is omitted.")).Size(12)))
		}
		info, _ := m.session(id)
		for _, message := range v.Chat.Messages {
			label := T("Assistant")
			if message.Role == "user" {
				label = T("You")
			} else if message.Role == "tool" {
				label = T("Tool")
			}
			var body ggui.Widget
			if message.Role == "assistant" {
				r := &mdRenderer{m: m, id: id, doc: docInfo{Node: info.NodeID, Path: joinPath(info.Project, "conversation.md")}, src: []byte(message.Text)}
				root := markdown.Parser().Parse(text.NewReader(r.src))
				body = ggui.Column(r.blocks(root)...).Gap(10).Stretch()
			} else {
				body = ggui.Text(message.Text).Size(14).Selectable()
			}
			bubble := ggui.Box(ggui.Column(ggui.Text(label).Size(12).Color(tokMuted).Weight(ggui.WeightSemibold), body).Gap(8).Stretch()).Pad(14, 16).Radius(10).Fill(tokSurface1)
			rows = append(rows, bubble)
		}
		if len(v.Chat.Messages) > 0 && scroll.MaxOffset()-scroll.Offset() < 32 {
			rows = append(rows, &revealOnce{child: ggui.Box().Height(1), done: func() {}})
		}
		return ggui.Box(ggui.Column(rows...).Gap(14).Stretch()).Pad(18, 24).MaxWidth(900)
	})
	feed := ggui.Scroll(ggui.Align(content).Top()).Controller(scroll).Bar(tokSurface3)
	footer := ggui.Box(ggui.Row(
		ggui.Expanded(muted(ggui.Text(T("Messages go to the live session. Use Terminal for approvals and special keys.")).Size(12))),
		ui.Button(T("Latest"), func() { scroll.ScrollTo(scroll.MaxOffset()) }).Ghost(),
	).Gap(8)).Pad(6, 12)
	return ggui.Pointer(ggui.Column(header, ggui.Expanded(feed), footer).Stretch()).OnDown(func(ggui.PointerEvent) { m.focusGroup(group) })
}
