package transcript

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ironpark/sanbo/internal/protocol"
)

func writeLog(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeConversationIdentityAndContent(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "custom-claude")
	env := map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": root}
	info := protocol.SessionInfo{Agent: "claude", Project: "/project"}
	first := `{"type":"user","cwd":"/project","sessionId":"first","uuid":"u1","message":{"role":"user","content":"안녕"}}` + "\n"
	reply := `{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"**hello**"},{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}` + "\n"
	result := `{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"package main"}]}]}}` + "\n"
	path := filepath.Join(root, "projects", "-project", "first.jsonl")
	writeLog(t, path, first+reply+reply+result+`{"type":"assistant"`)
	writeLog(t, filepath.Join(root, "projects", "-project", "second.jsonl"), strings.ReplaceAll(first, "first", "second"))
	writeLog(t, filepath.Join(root, "projects", "-other", "other.jsonl"), strings.ReplaceAll(strings.ReplaceAll(first, "first", "other"), "/project", "/other"))
	list, err := Read(t.Context(), info, env, "")
	if err != nil || len(list.Conversations) != 2 || list.Conversation != "" || len(list.Messages) != 0 {
		t.Fatalf("must not guess by cwd: %+v, %v", list, err)
	}
	chat, err := Read(t.Context(), info, env, "first")
	if err != nil || len(chat.Messages) != 4 || chat.Messages[0].Text != "안녕" || chat.Messages[2].Role != "tool" || chat.Messages[3].Text != "package main" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	info.Command = []string{"claude", "--session-id=first"}
	chat, err = Read(t.Context(), info, env, "")
	if err != nil || chat.Conversation != "first" {
		t.Fatalf("explicit session ID: %+v, %v", chat, err)
	}
	if _, err = Read(t.Context(), info, env, "other"); err == nil {
		t.Fatal("read another project's conversation")
	}
	if _, err = Read(t.Context(), info, env, "../first"); err == nil {
		t.Fatal("accepted a path as a conversation ID")
	}
}

func TestCodexUsesResponseItemsWithoutDuplicateEvents(t *testing.T) {
	home := t.TempDir()
	body := `{"type":"session_meta","payload":{"id":"thread","cwd":"/project","source":"cli"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_message","message":"duplicated"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"private instructions"}]}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"ls"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call_output","output":"main.go"}}` + "\n"
	path := filepath.Join(home, "sessions", "2026", "10", "26", "rollout-thread.jsonl")
	writeLog(t, path, body)
	info := protocol.SessionInfo{Agent: "codex", Project: "/project", Command: []string{"codex", "resume", "thread"}}
	chat, err := Read(t.Context(), info, map[string]string{"CODEX_HOME": home}, "")
	if err != nil || len(chat.Messages) != 4 || chat.Messages[0].Text != "question" || chat.Messages[1].Text != "answer" || chat.Messages[3].Role != "tool" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
}

func TestTailLimitsAndIncompleteAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	message := func(s string) string {
		data, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": s}})
		return string(data) + "\n"
	}
	writeLog(t, path, message(strings.Repeat("x", maxRead))+message(strings.Repeat("한", 20000))+message("last"))
	messages, truncated, err := readMessages(path, "claude")
	if err != nil || !truncated || len(messages) != 2 || messages[1].Text != "last" || !utf8.ValidString(messages[0].Text) {
		t.Fatalf("tail: count %d, truncated %v, err %v", len(messages), truncated, err)
	}
	writeLog(t, path, message("first")+strings.TrimSuffix(message("next"), "\n"))
	messages, _, err = readMessages(path, "claude")
	if err != nil || len(messages) != 1 {
		t.Fatalf("partial append: %+v, %v", messages, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n")
	_ = f.Close()
	messages, _, err = readMessages(path, "claude")
	if err != nil || len(messages) != 2 {
		t.Fatalf("completed append: %+v, %v", messages, err)
	}
}

func TestUnsupportedAndCancelled(t *testing.T) {
	chat, err := Read(t.Context(), protocol.SessionInfo{Agent: "shell"}, nil, "")
	if err != nil || chat.Supported {
		t.Fatalf("shell: %+v %v", chat, err)
	}
	root := t.TempDir()
	writeLog(t, filepath.Join(root, "projects", "p", "id.jsonl"), "{}\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Read(ctx, protocol.SessionInfo{Agent: "claude"}, map[string]string{"CLAUDE_CONFIG_DIR": root}, ""); err == nil {
		t.Fatal("ignored cancellation")
	}
}
