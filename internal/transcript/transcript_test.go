package transcript

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
	reply := `{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"**hello**"},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"main.go"}}]}}` + "\n"
	result := `{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"package main"}]}]}}` + "\n"
	path := filepath.Join(root, "projects", "-project", "first.jsonl")
	writeLog(t, path, first+reply+reply+result+`{"type":"assistant"`)
	writeLog(t, filepath.Join(root, "projects", "-project", "second.jsonl"), strings.ReplaceAll(first, "first", "second"))
	writeLog(t, filepath.Join(root, "projects", "-other", "other.jsonl"), strings.ReplaceAll(strings.ReplaceAll(first, "first", "other"), "/project", "/other"))
	info.ConversationID = "first"
	chat, err := Read(t.Context(), info, env)
	if err != nil || len(chat.Messages) != 3 || chat.Messages[0].Text != "안녕" || chat.Messages[1].Role != "assistant" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	// The tool's output belongs to its call, named and summarised for the
	// chat's tool row.
	tool := chat.Messages[2]
	if tool.Role != "tool" || tool.Tool != "Read" || tool.Detail != "main.go" || tool.Result != "package main" || tool.Output {
		t.Fatalf("tool message: %+v", tool)
	}
	// A bound ID from another project has no log here: it waits rather than
	// reading that project's history.
	info.ConversationID = "other"
	if chat, err = Read(t.Context(), info, env); err != nil || len(chat.Messages) != 0 {
		t.Fatalf("read another project's conversation: %+v, %v", chat, err)
	}
	info.ConversationID = "../first"
	if _, err = Read(t.Context(), info, env); err == nil {
		t.Fatal("accepted a path as a conversation ID")
	}
}

func TestArgumentID(t *testing.T) {
	cases := []struct {
		agent string
		args  []string
		want  string
	}{
		{"claude", []string{"claude", "--session-id=one"}, "one"},
		{"claude", []string{"claude", "--session-id", "one"}, "one"},
		{"claude", []string{"claude", "--resume", "two"}, "two"},
		{"claude", []string{"claude", "-r=three"}, "three"},
		{"claude", []string{"claude", "--fork-session", "--resume", "two"}, ""},
		{"claude", []string{"claude", "--session-id=one", "--fork-session"}, "one"},
		{"codex", []string{"codex", "resume", "four"}, "four"},
		{"codex", []string{"codex", "--model", "x"}, ""},
	}
	for _, c := range cases {
		a, _ := Lookup(c.agent)
		if got := a.ArgumentID(c.args); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.agent, c.args, got, c.want)
		}
	}
}

func TestCodexUsesResponseItemsWithoutDuplicateEvents(t *testing.T) {
	home := t.TempDir()
	body := `{"type":"session_meta","payload":{"id":"thread","cwd":"/project","source":"cli"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_message","message":"duplicated"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"private instructions"}]}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"ls","call_id":"c1"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"main.go"}}` + "\n"
	path := filepath.Join(home, "sessions", "2026", "10", "26", "rollout-thread.jsonl")
	writeLog(t, path, body)
	info := protocol.SessionInfo{Agent: "codex", Project: "/project", ConversationID: "thread"}
	chat, err := Read(t.Context(), info, map[string]string{"CODEX_HOME": home})
	if err != nil || len(chat.Messages) != 3 || chat.Messages[0].Text != "question" || chat.Messages[1].Text != "answer" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	tool := chat.Messages[2]
	if tool.Role != "tool" || tool.Tool != "exec_command" || tool.Result != "main.go" {
		t.Fatalf("tool message: %+v", tool)
	}
}

func TestTailLimitsAndIncompleteAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	message := func(s string) string {
		data, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": s}})
		return string(data) + "\n"
	}
	writeLog(t, path, message(strings.Repeat("x", maxRead))+message(strings.Repeat("한", 20000))+message("last"))
	claude, _ := Lookup("claude")
	messages, truncated, err := readMessages(path, claude)
	if err != nil || !truncated || len(messages) != 2 || messages[1].Text != "last" || !utf8.ValidString(messages[0].Text) {
		t.Fatalf("tail: count %d, truncated %v, err %v", len(messages), truncated, err)
	}
	writeLog(t, path, message("first")+strings.TrimSuffix(message("next"), "\n"))
	messages, _, err = readMessages(path, claude)
	if err != nil || len(messages) != 1 {
		t.Fatalf("partial append: %+v, %v", messages, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n")
	_ = f.Close()
	messages, _, err = readMessages(path, claude)
	if err != nil || len(messages) != 2 {
		t.Fatalf("completed append: %+v, %v", messages, err)
	}
}

func TestUnsupportedAndCancelled(t *testing.T) {
	chat, err := Read(t.Context(), protocol.SessionInfo{Agent: "shell"}, nil)
	if err != nil || chat.Supported {
		t.Fatalf("shell: %+v %v", chat, err)
	}
	root := t.TempDir()
	writeLog(t, filepath.Join(root, "projects", "p", "id.jsonl"), "{}\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Read(ctx, protocol.SessionInfo{Agent: "claude", ConversationID: "id"}, map[string]string{"CLAUDE_CONFIG_DIR": root}); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestResumeKeepsSettingsNotPromptOrFork(t *testing.T) {
	for _, tc := range []struct {
		agent      string
		args, want []string
	}{
		{"claude", []string{"claude", "--model", "opus", "--settings", "settings.json", "--session-id", "old", "--fork-session", "prompt"}, []string{"claude", "--resume", "new", "--model", "opus", "--settings", "settings.json"}},
		{"codex", []string{"codex", "-c", "mcp_servers.test.command=tool", "--model", "test", "resume", "--last", "old", "prompt", "--image", "image.png"}, []string{"codex", "resume", "new", "-c", "mcp_servers.test.command=tool", "--model", "test"}},
	} {
		a, _ := Lookup(tc.agent)
		if got := a.Resume(tc.args, "new"); !slices.Equal(got, tc.want) {
			t.Fatalf("%s: %q want %q", tc.agent, got, tc.want)
		}
	}
}

func TestPinOnlyWhenNothingPicksAConversation(t *testing.T) {
	claude, _ := Lookup("claude")
	if !claude.Picks([]string{"claude", "--continue"}) || !claude.Picks([]string{"claude", "--resume=x"}) || claude.Picks([]string{"claude", "--model", "opus"}) {
		t.Fatal("claude pickers")
	}
	if got, ok := claude.Pin([]string{"claude"}, "id"); !ok || !slices.Equal(got, []string{"claude", "--session-id", "id"}) {
		t.Fatalf("pin=%q %v", got, ok)
	}
	codex, _ := Lookup("codex")
	if _, ok := codex.Pin([]string{"codex"}, "id"); ok {
		t.Fatal("codex picks its own ID")
	}
	if !EnvKey("CODEX_HOME") || !EnvKey("HOME") || EnvKey("OPENAI_API_KEY") {
		t.Fatal("env keys")
	}
}

func TestUpgradeStructuresOlderToolMessages(t *testing.T) {
	chat := protocol.Chat{Messages: []protocol.ChatMessage{
		{Role: "assistant", Text: "Running it."},
		{Role: "tool", Text: "Bash\n{\"command\":\"go test ./...\"}"},
		{Role: "tool", Text: "ok  \tpkg\t0.1s"},
		{Role: "tool", ID: "t1", Tool: "Read", Text: "{}"},
	}}
	Upgrade(&chat)
	m := chat.Messages
	if m[0].Role != "assistant" || m[1].Tool != "Bash" || m[1].Detail != "go test ./..." ||
		!m[2].Output || m[2].Text != "ok  \tpkg\t0.1s" || m[3].Tool != "Read" || m[3].ID != "t1" {
		t.Fatalf("upgraded=%+v", m)
	}
}

func TestCacheRereadsOnlyAChangedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects", "p", "c.jsonl")
	line := func(text string) string {
		return `{"type":"user","sessionId":"c","cwd":"/p","message":{"role":"user","content":"` + text + `"}}` + "\n"
	}
	writeLog(t, path, line("first"))
	info := protocol.SessionInfo{Agent: "claude", ConversationID: "c"}
	var cache Cache
	chat, err := cache.ReadBound(info, path)
	if err != nil || len(chat.Messages) != 1 {
		t.Fatalf("chat=%+v %v", chat, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(line("second"))
	f.Close()
	if chat, err = cache.ReadBound(info, path); err != nil || len(chat.Messages) != 2 {
		t.Fatalf("appended chat=%+v %v", chat, err)
	}
	info.ConversationID = "other"
	if _, err := cache.ReadBound(info, path); err == nil {
		t.Fatal("a cached read skipped the identity check")
	}
}
