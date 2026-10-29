package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestChatSwitchKeepsLiveSessionAndComposer(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	project, logs := t.TempDir(), t.TempDir()
	dir := filepath.Join(logs, "projects", "project")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(map[string]any{"type": "user", "cwd": project, "sessionId": "conversation", "message": map[string]any{"role": "user", "content": "hello from the log"}})
	path := filepath.Join(dir, "conversation.jsonl")
	if err := os.WriteFile(path, append(record, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.P2P.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	cfg.Agents = map[string]agentspec.Agent{"claude": {Command: []string{"/bin/sh", "-c", `exec 3>>"$TEST_LOG_PATH"; exec cat`}, Env: map[string]string{"CLAUDE_CONFIG_DIR": logs, "TEST_LOG_PATH": path}}}
	if err := savePrefs(prefs{DaemonMode: "embedded", Language: "en"}.normalized()); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")
	ctx, cancel := context.WithCancel(t.Context())
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1100, 760))
	p.Setup(func() { setup(m); m.start(ctx, p.Post) })
	t.Cleanup(func() { cancel(); m.shutdown(); p.Close() })
	probeUntil(t, p, "daemon", func() bool { return ggui.Peek(m.Conn).OK })
	m.launch("claude", "", project)
	probeUntil(t, p, "attachment", func() bool { return len(ggui.Peek(m.Attached)) == 1 })
	id := ggui.Peek(m.Selected)
	conn := m.conns[id]
	m.setComposerDraft(id, "draft survives switching")
	p.Frame()
	p.Tap(T("Chat"))
	probeUntil(t, p, "conversations", func() bool { return m.chats[id] != nil && len(ggui.Peek(m.chats[id].result).Chat.GetMessages()) == 1 })
	result := ggui.Peek(m.chats[id].result)
	if result.Err != "" || result.Chat.GetConversation() != "conversation" {
		t.Fatalf("discovery: %+v", result)
	}
	if _, ok := p.Find(T("Message")); !ok {
		t.Fatal("chat must show its composer")
	}
	if _, ok := p.Find(T("Conversation")); ok {
		t.Fatal("chat still requires a conversation picker")
	}
	if m.composerDraft(id) != "draft survives switching" {
		t.Fatal("lost draft")
	}
	m.setComposerDraft(id, "live input")
	m.sendComposer(id)
	probeUntil(t, p, "live terminal output in chat mode", func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		text, _ := conn.vt.FormatString(gostty.FormatOptions{Format: gostty.FormatterFormatPlain})
		return strings.Contains(text, "live input")
	})
	p.Tap(T("Terminal"))
	p.Frame()
	if m.conns[id] != conn || ggui.Peek(m.ChatModes)[id] {
		t.Fatal("switch replaced the connection")
	}
	if _, ok := p.Find(T("Message")); ok {
		t.Fatal("terminal mode must not show the chat composer")
	}
	m.setChatMode(id, true)
	p.Frame()
	if got := ggui.Peek(m.chats[id].result).Chat.GetConversation(); got != "conversation" {
		t.Fatalf("lost conversation: %q", got)
	}
	// A document cannot be changed into a session view by the shortcut.
	m.Selected.Set(docTabID("node", "/example.txt"))
	m.toggleChat()
	if ggui.Peek(m.ChatModes)[docTabID("node", "/example.txt")] {
		t.Fatal("toggled a document")
	}
}

func TestChatModesArePerSession(t *testing.T) {
	m := newModel(config.Default(), "")
	m.showTab("a")
	m.showTab("b")
	m.setChatMode("a", true)
	m.splitTab("a", ggui.Peek(m.FocusGroup), dropRight)
	if !ggui.Peek(m.ChatModes)["a"] || ggui.Peek(m.ChatModes)["b"] {
		t.Fatal("mode followed the pane rather than session")
	}
	m.closeTab("a")
	if _, ok := ggui.Peek(m.ChatModes)["a"]; ok {
		t.Fatal("closed tab retained view state")
	}
}

// The composer keeps its own height; the feed above it gets the rest of the
// pane instead of collapsing to nothing.
func TestChatComposerLeavesRoomForTheFeed(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	m.Selected.Set("s")
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Column(ggui.Expanded(ggui.Text("FEEDMARKER")), composer(m, "s", m.chatState("s"))).Stretch()
	}, ggui.Sz(900, 600))
	defer p.Close()
	p.Frame()
	feed, ok := p.Semantics().Find(ggui.RoleText, "FEEDMARKER")
	if !ok || feed.Rect.Size.H < 300 {
		t.Fatalf("feed height %v, want the pane above the composer", feed.Rect.Size)
	}
	if _, ok := p.Semantics().Find(ggui.RoleTextField, T("Message")); !ok {
		t.Fatal("the composer is missing")
	}
}

// Tool rows name the tool and its argument, fold their output away, and
// fold independently of one another.
func TestChatToolRowsFold(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	m.Selected.Set("s")
	state := m.chatState("s")
	state.loading = true
	state.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Conversation: "c",
		Messages: []*protocol.ChatMessage{
			{Role: "assistant", Text: "Running the tool."},
			{Role: "tool", Id: "t1", Tool: "Bash", Detail: "go test ./...", Text: `{"command":"go test ./..."}`, Result: "OUTPUT_MARKER"},
			{Role: "tool", Id: "t2", Tool: "Read", Detail: "main.go", Text: `{"file_path":"main.go"}`, Result: "SECOND_MARKER"},
			{Role: "user", Text: "thanks"},
		}}})
	p := ggui.ProbeBuilder(func() ggui.Widget { return chatView(m, "s", 1) }, ggui.Sz(900, 700))
	defer p.Close()
	p.Frame()
	page := p.Semantics().String()
	if !strings.Contains(page, "Bash") || !strings.Contains(page, "go test ./...") || !strings.Contains(page, "Read") {
		t.Fatalf("tool rows missing:\n%s", page)
	}
	if strings.Contains(page, "OUTPUT_MARKER") || strings.Contains(page, "SECOND_MARKER") {
		t.Fatal("a folded tool showed its output")
	}
	row, ok := p.Find("Bash  go test ./...")
	if !ok {
		t.Fatalf("no Bash row:\n%s", page)
	}
	p.Click(row.Center())
	p.Frame()
	page = p.Semantics().String()
	if !strings.Contains(page, "OUTPUT_MARKER") {
		t.Fatal("the opened tool hid its output")
	}
	if strings.Contains(page, "SECOND_MARKER") {
		t.Fatal("opening one tool opened another")
	}
}

// A plain shell has no conversation, so the shortcut leaves it in Terminal.
func TestShellHasNoChat(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "shell"}})
	m.Selected.Set("s")
	if m.chatCapable("s") {
		t.Fatal("a shell counts as chat-capable")
	}
	m.toggleChat()
	if ggui.Peek(m.ChatModes)["s"] {
		t.Fatal("the shortcut switched a shell to chat")
	}
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "c", Agent: "claude"}})
	if !m.chatCapable("c") {
		t.Fatal("a claude session does not count as chat-capable")
	}
}

// A click anywhere on the view switch switches to the other view, on the
// side in view as much as on the other.
func TestViewSwitchTogglesWhereverClicked(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	p := ggui.ProbeBuilder(func() ggui.Widget { return ggui.Center(viewSwitch(m, "s", 0)) }, ggui.Sz(200, 60))
	defer p.Close()
	p.Frame()
	sw, ok := p.Find(T("Chat"))
	if !ok {
		t.Fatal("the switch is not named for the view it goes to")
	}
	r := sw.Rect
	for i, at := range []ggui.Point{
		{X: r.Origin.X + r.Size.W/4, Y: r.Origin.Y + r.Size.H/2},   // the terminal's side, in view
		{X: r.Origin.X + r.Size.W/4, Y: r.Origin.Y + r.Size.H/2},   // again, now the other side
		{X: r.Origin.X + r.Size.W*3/4, Y: r.Origin.Y + r.Size.H/2}, // the chat's side, not in view
		{X: r.Origin.X + r.Size.W/2, Y: r.Origin.Y + 1},            // the track's edge
	} {
		before := ggui.Peek(m.ChatModes)["s"]
		p.Click(at)
		p.Frame()
		if ggui.Peek(m.ChatModes)["s"] == before {
			t.Fatalf("click %d at %v did not switch the view", i, at)
		}
	}
}

// A message's copy button shows in its bottom-right corner, inside it,
// while the pointer is over the message, its selectable text too; it
// takes no room.
func TestCopyButtonShowsInTheMessageHovered(t *testing.T) {
	msg := &protocol.ChatMessage{Role: "user", Text: "copy me please"}
	content := ggui.Box(ggui.Text(msg.GetText()).Size(14).Selectable()).Pad(10, 14)
	w := &withCopy{content: content, button: copyButton(nil, msg.GetText())}
	p := ggui.NewProbe(ggui.Column(w, ggui.Box().Height(60)).Stretch(), ggui.Sz(400, 200))
	defer p.Close()
	p.Frame()
	if _, ok := p.Find(T("Copy")); ok {
		t.Fatal("the button shows with the pointer away")
	}
	// The text sits inside the box's padding, at its top-left.
	p.Move(ggui.Pt(30, 18))
	p.Frame()
	btn, ok := p.Find(T("Copy"))
	if !ok {
		t.Fatal("the button is hidden with the pointer over the text")
	}
	// The column stretches the message across the probe; its height is
	// the text's and padding's.
	msgH := content.Layout(ggui.Loose(ggui.Sz(400, 200)), ggui.Env{}).H
	if b := btn.Rect; b.Origin.Y < 0 || b.Origin.Y+b.Size.H > msgH || b.Origin.X+b.Size.W > 400 {
		t.Fatalf("the button %v is outside the message, %v tall", b, msgH)
	}
	p.Move(btn.Center())
	p.Frame()
	if _, ok := p.Find(T("Copy")); !ok {
		t.Fatal("the button went as the pointer reached it")
	}
	p.Move(ggui.Pt(390, 190))
	p.Frame()
	if _, ok := p.Find(T("Copy")); ok {
		t.Fatal("the button stays with the pointer away")
	}
}

func TestALongChatDrawsOnlyTheTurnsInView(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	m.Selected.Set("s")
	state := m.chatState("s")
	state.loading = true
	var messages []*protocol.ChatMessage
	for i := range 2000 {
		messages = append(messages, &protocol.ChatMessage{Role: "user", Text: fmt.Sprintf("turn %d", i)})
	}
	// The same words twice, with no ID to tell them apart, are two turns.
	messages = append(messages, &protocol.ChatMessage{Role: "user", Text: "again"}, &protocol.ChatMessage{Role: "user", Text: "again"})
	state.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Conversation: "c", Messages: messages}})
	p := ggui.ProbeBuilder(func() ggui.Widget { return chatView(m, "s", 1) }, ggui.Sz(900, 700))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	page := p.Semantics().String()
	if n := strings.Count(page, "turn "); n == 0 || n > 60 {
		t.Fatalf("%d of 2000 turns drawn, want only those near the window:\n%s", n, page)
	}
	if strings.Count(page, "again") != 2 {
		t.Fatalf("the latest turns, said alike, are not both at the foot of the feed:\n%s", page)
	}
}

// A message names the images it hands over by their paths, as words of
// their own: a relative one is the project's.
func TestImagesAreNamedByTheirPaths(t *testing.T) {
	got := imageRefs("look at shots/a.png and (/tmp/b.JPG), not notes.md or a.png.txt; shots/a.png again", "/repo")
	want := []string{"/repo/shots/a.png", "/tmp/b.JPG"}
	if !slices.Equal(got, want) {
		t.Fatalf("named %q, want %q", got, want)
	}
}

// A subagent at work, over the composer, opens its own conversation in the
// pane, which the bar over it leaves for the session's again.
func TestASubagentOpensItsConversation(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "n", Revision: protocol.Revision}})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", NodeId: "n", Agent: "claude", Project: "/tmp", State: protocol.StateRunning}})
	m.Selected.Set("s")
	state := m.chatState("s")
	state.loading = true
	state.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Conversation: "c",
		Messages: []*protocol.ChatMessage{{Role: "user", Text: "look around"}},
		Agents:   []*protocol.SubAgent{{Id: "a1", Running: true, Description: "Find the tests", Log: "/logs/agent-a1.jsonl"}},
	}})
	sub := m.agentState("s", "a1")
	sub.loading = true // the test has no daemon
	sub.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Messages: []*protocol.ChatMessage{{Role: "user", Text: "Find the tests."}}}})
	p := ggui.ProbeBuilder(func() ggui.Widget { return chatView(m, "s", 1) }, ggui.Sz(900, 700))
	defer p.Close()
	p.Frame()
	row, ok := p.Semantics().Find(ggui.RoleText, "Find the tests")
	if !ok {
		t.Fatalf("no running subagent:\n%s", p.Semantics().String())
	}
	p.Click(row.Rect.Origin.Add(ggui.Pt(4, row.Rect.Size.H/2)))
	p.Frame()
	if got := ggui.Peek(state.agent); got != "a1" {
		t.Fatalf("the pane shows agent %q, want a1", got)
	}
	if _, ok := p.Semantics().Find(ggui.RoleText, "Find the tests."); !ok {
		t.Fatalf("the subagent's conversation is not shown:\n%s", p.Semantics().String())
	}
	back, ok := p.Semantics().Find(ggui.RoleButton, T("Back to the session"))
	if !ok {
		t.Fatalf("no way back:\n%s", p.Semantics().String())
	}
	p.Click(back.Rect.Origin.Add(ggui.Pt(back.Rect.Size.W/2, back.Rect.Size.H/2)))
	p.Frame()
	if ggui.Peek(state.agent) != "" {
		t.Fatal("Back left the subagent's conversation")
	}
	if _, ok := p.Semantics().Find(ggui.RoleText, "look around"); !ok {
		t.Fatalf("the session's conversation is not back:\n%s", p.Semantics().String())
	}
}

// A message sent shows at once, as sent, until the log has it: said after
// the latest user message of when it was sent, or followed there by as
// many as were waiting before it. One the log never has goes in time.
func TestASentMessageShowsUntilTheLogHasIt(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	state := m.chatState("s")
	read := func(texts ...string) {
		var msgs []*protocol.ChatMessage
		for _, text := range texts {
			msgs = append(msgs, &protocol.ChatMessage{Role: "user", Text: text}, &protocol.ChatMessage{Role: "assistant", Text: "ok"})
		}
		state.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Messages: msgs}})
		settlePending(state)
	}
	shown := func() []string {
		var out []string
		for _, p := range ggui.Peek(state.pending) {
			out = append(out, p.Text)
		}
		return out
	}
	read("yes")
	m.notePending("s", "yes")
	m.notePending("s", "then this")
	if got := shown(); !slices.Equal(got, []string{"yes", "then this"}) {
		t.Fatalf("pending %q", got)
	}
	if turns := pendingTurns(ggui.Peek(state.pending)); len(turns) != 2 || !turns[0].Sending {
		t.Fatalf("turns %+v", turns)
	}
	read("yes", "yes") // the same words again: the first is in
	if got := shown(); !slices.Equal(got, []string{"then this"}) {
		t.Fatalf("after the first was logged, pending %q", got)
	}
	read("yes", "yes", "then this, as the agent wrote it down")
	if got := shown(); len(got) != 0 {
		t.Fatalf("after both were logged, pending %q", got)
	}
	m.notePending("s", "/model")
	p := ggui.Peek(state.pending)
	p[0].At = time.Now().Add(-time.Minute)
	settlePending(state)
	if got := shown(); len(got) != 0 {
		t.Fatalf("a command the log never has is still pending: %q", got)
	}
}
