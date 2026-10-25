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
	cfg.Agents = map[string]agentspec.Agent{"claude": {Command: []string{"/bin/cat"}, Env: map[string]string{"CLAUDE_CONFIG_DIR": logs}}}
	if err := savePrefs(prefs{DaemonMode: "embedded", Language: "en", ShowComposer: false}.normalized()); err != nil {
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
	m.Composer.Set("draft survives switching")
	p.Frame()
	p.Tap(T("Chat"))
	probeUntil(t, p, "conversations", func() bool { return m.chats[id] != nil && ggui.Peek(m.chats[id].result).Loaded })
	result := ggui.Peek(m.chats[id].result)
	if result.Err != "" || len(result.Chat.Conversations) != 1 || result.Chat.Conversation != "" {
		t.Fatalf("discovery: %+v", result)
	}
	if _, ok := p.Find(T("Message")); !ok {
		t.Fatal("chat must show composer when setting is off")
	}
	p.Tap(T("Conversation"))
	label := result.Chat.Conversations[0].Updated.Local().Format("01/02 15:04") + " · conversation"
	p.Tap(label)
	probeUntil(t, p, "messages", func() bool { return len(ggui.Peek(m.chats[id].result).Chat.Messages) == 1 })
	if ggui.Peek(m.Composer) != "draft survives switching" {
		t.Fatal("lost draft")
	}
	m.Composer.Set("live input")
	m.sendComposer()
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
		t.Fatal("terminal composer ignored setting")
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
