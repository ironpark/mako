package gui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty"
	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

func TestChatSwitchKeepsLiveSessionAndComposer(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
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
	cfg.Listen = "127.0.0.1:0"
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
	probeUntil(t, p, "conversations", func() bool { return m.chats[id] != nil && len(ggui.Peek(m.chats[id].result).Chat.Messages) == 1 })
	result := ggui.Peek(m.chats[id].result)
	if result.Err != "" || result.Chat.Conversation != "conversation" {
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
	if got := ggui.Peek(m.chats[id].result).Chat.Conversation; got != "conversation" {
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
	t.Setenv("SANBO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]protocol.SessionInfo{{ID: "s", Agent: "claude", Project: "/tmp"}})
	m.Selected.Set("s")
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Column(ggui.Expanded(ggui.Text("FEEDMARKER")), composer(m, "s", ggui.State(""))).Stretch()
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
	t.Setenv("SANBO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]protocol.SessionInfo{{ID: "s", Agent: "claude", Project: "/tmp"}})
	m.Selected.Set("s")
	state := m.chatState("s")
	state.loading = true
	state.result.Set(chatResult{Loaded: true, Chat: protocol.Chat{Supported: true, Conversation: "c",
		Messages: []protocol.ChatMessage{
			{Role: "assistant", Text: "Running the tool."},
			{Role: "tool", ID: "t1", Tool: "Bash", Detail: "go test ./...", Text: `{"command":"go test ./..."}`, Result: "OUTPUT_MARKER"},
			{Role: "tool", ID: "t2", Tool: "Read", Detail: "main.go", Text: `{"file_path":"main.go"}`, Result: "SECOND_MARKER"},
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
	t.Setenv("SANBO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]protocol.SessionInfo{{ID: "s", Agent: "shell"}})
	m.Selected.Set("s")
	if m.chatCapable("s") {
		t.Fatal("a shell counts as chat-capable")
	}
	m.toggleChat()
	if ggui.Peek(m.ChatModes)["s"] {
		t.Fatal("the shortcut switched a shell to chat")
	}
	m.Sessions.Set([]protocol.SessionInfo{{ID: "c", Agent: "claude"}})
	if !m.chatCapable("c") {
		t.Fatal("a claude session does not count as chat-capable")
	}
}
