package transcript

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
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
	info := &protocol.SessionInfo{Agent: "claude", Project: "/project"}
	first := `{"type":"user","cwd":"/project","sessionId":"first","uuid":"u1","message":{"role":"user","content":"안녕"}}` + "\n"
	reply := `{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"**hello**"},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"main.go"}}]}}` + "\n"
	result := `{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"package main"}]}]}}` + "\n"
	path := filepath.Join(root, "projects", "-project", "first.jsonl")
	writeLog(t, path, first+reply+reply+result+`{"type":"assistant"`)
	writeLog(t, filepath.Join(root, "projects", "-project", "second.jsonl"), strings.ReplaceAll(first, "first", "second"))
	writeLog(t, filepath.Join(root, "projects", "-other", "other.jsonl"), strings.ReplaceAll(strings.ReplaceAll(first, "first", "other"), "/project", "/other"))
	info.ConversationId = "first"
	chat, err := Read(t.Context(), info, env)
	if err != nil || len(chat.GetMessages()) != 3 || chat.GetMessages()[0].GetText() != "안녕" || chat.GetMessages()[1].GetRole() != "assistant" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	// The tool's output belongs to its call, named and summarised for the
	// chat's tool row.
	tool := chat.GetMessages()[2]
	if tool.GetRole() != "tool" || tool.GetTool() != "Read" || tool.GetDetail() != "main.go" || tool.GetResult() != "package main" || tool.GetOutput() {
		t.Fatalf("tool message: %+v", tool)
	}
	// A bound ID from another project has no log here: it waits rather than
	// reading that project's history.
	info.ConversationId = "other"
	if chat, err = Read(t.Context(), info, env); err != nil || len(chat.GetMessages()) != 0 {
		t.Fatalf("read another project's conversation: %+v, %v", chat, err)
	}
	info.ConversationId = "../first"
	if _, err = Read(t.Context(), info, env); err == nil {
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
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"ls","call_id":"c1"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"main.go"}}` + "\n"
	path := filepath.Join(home, "sessions", "2026", "10", "26", "rollout-thread.jsonl")
	writeLog(t, path, body)
	info := &protocol.SessionInfo{Agent: "codex", Project: "/project", ConversationId: "thread"}
	chat, err := Read(t.Context(), info, map[string]string{"CODEX_HOME": home})
	if err != nil || len(chat.GetMessages()) != 3 || chat.GetMessages()[0].GetText() != "question" || chat.GetMessages()[1].GetText() != "answer" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	tool := chat.GetMessages()[2]
	if tool.GetRole() != "tool" || tool.GetTool() != "exec_command" || tool.GetResult() != "main.go" {
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
	claude, _ := logsOf("claude")
	messages, truncated, err := readMessages(path, claude)
	if err != nil || !truncated || len(messages) != 2 || messages[1].GetText() != "last" || !utf8.ValidString(messages[0].GetText()) {
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
	chat, err := Read(t.Context(), &protocol.SessionInfo{Agent: "shell"}, nil)
	if err != nil || chat.GetSupported() {
		t.Fatalf("shell: %+v %v", chat, err)
	}
	root := t.TempDir()
	writeLog(t, filepath.Join(root, "projects", "p", "id.jsonl"), "{}\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Read(ctx, &protocol.SessionInfo{Agent: "claude", ConversationId: "id"}, map[string]string{"CLAUDE_CONFIG_DIR": root}); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestEnvKeepsOnlyWhereLogsAre(t *testing.T) {
	env := Env([]string{"HOME=/h", "OPENAI_API_KEY=secret", "PATH=/bin"}, map[string]string{"CODEX_HOME": "/c", "TOKEN": "t"})
	if len(env) != 2 || env["HOME"] != "/h" || env["CODEX_HOME"] != "/c" {
		t.Fatalf("env=%v", env)
	}
}

func TestLogOfTrustsOnlyTheAgentsOwnPlace(t *testing.T) {
	env := map[string]string{"CLAUDE_CONFIG_DIR": "/c"}
	id := "0b6f4d1e-2f7a-4c55-9a43-2c4f5f6e7a8b"
	for path, want := range map[string]bool{
		"/c/projects/-p/" + id + ".jsonl":         true,
		"/c/projects/-p/../-q/" + id + ".jsonl":   true, // cleaned, still a project
		"/c/projects/-p/other.jsonl":              false,
		"/c/" + id + ".jsonl":                     false,
		"/elsewhere/projects/-p/" + id + ".jsonl": false,
		"projects/-p/" + id + ".jsonl":            false,
	} {
		if got := LogOf("claude", env, id, path); got != want {
			t.Errorf("LogOf(%s)=%v", path, got)
		}
	}
	if LogOf("claude", env, "../x", "/c/projects/-p/../x.jsonl") || LogOf("codex", map[string]string{"CODEX_HOME": "/c"}, id, "/c/sessions/2026/10/26/rollout-"+id+".jsonl") {
		t.Fatal("invalid ID or rollout log trusted")
	}
}

func TestUpgradeStructuresOlderToolMessages(t *testing.T) {
	chat := protocol.Chat{Messages: []*protocol.ChatMessage{
		{Role: "assistant", Text: "Running it."},
		{Role: "tool", Text: "Bash\n{\"command\":\"go test ./...\"}"},
		{Role: "tool", Text: "ok  \tpkg\t0.1s"},
		{Role: "tool", Id: "t1", Tool: "Read", Text: "{}"},
	}}
	Upgrade(&chat)
	m := chat.GetMessages()
	if m[0].GetRole() != "assistant" || m[1].GetTool() != "Bash" || m[1].GetDetail() != "go test ./..." ||
		!m[2].GetOutput() || m[2].GetText() != "ok  \tpkg\t0.1s" || m[3].GetTool() != "Read" || m[3].GetId() != "t1" {
		t.Fatalf("upgraded=%+v", m)
	}
}

func TestCacheRereadsOnlyAChangedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects", "p", "c.jsonl")
	line := func(text string) string {
		return `{"type":"user","sessionId":"c","cwd":"/p","message":{"role":"user","content":"` + text + `"}}` + "\n"
	}
	writeLog(t, path, line("first"))
	info := &protocol.SessionInfo{Agent: "claude", ConversationId: "c"}
	var cache Cache
	chat, err := cache.ReadBound(info, path, Span{})
	if err != nil || len(chat.GetMessages()) != 1 {
		t.Fatalf("chat=%+v %v", chat, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(line("second"))
	f.Close()
	if chat, err = cache.ReadBound(info, path, Span{}); err != nil || len(chat.GetMessages()) != 2 {
		t.Fatalf("appended chat=%+v %v", chat, err)
	}
	info.ConversationId = "other"
	if _, err := cache.ReadBound(info, path, Span{}); err == nil {
		t.Fatal("a cached read skipped the identity check")
	}
}

// readMessages is the latest messages of a log, as most tests want them.
func readMessages(path string, a agent.Logs) ([]*protocol.ChatMessage, bool, error) {
	w, err := readWindow(path, a, Span{})
	return w.messages, w.truncated, err
}

// The latest window says where it starts in the log, and a read before
// there pages back from it, a window at a time, to the beginning.
func TestWindowsPageBackFromWhereTheyStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	var body strings.Builder
	offsets := []int64{}
	for i := range 300 {
		offsets = append(offsets, int64(body.Len()))
		fmt.Fprintf(&body, `{"type":"user","uuid":"u%d","message":{"role":"user","content":"m%d"}}`+"\n", i, i)
	}
	writeLog(t, path, body.String())

	latest, err := readWindow(path, claudeLogs(t), Span{})
	if err != nil || len(latest.messages) != 200 || !latest.truncated {
		t.Fatalf("latest: %d messages, truncated %v, %v", len(latest.messages), latest.truncated, err)
	}
	if latest.start != offsets[100] || latest.messages[0].GetText() != "m100" {
		t.Fatalf("latest starts at %d with %q, want %d", latest.start, latest.messages[0].GetText(), offsets[100])
	}
	older, err := readWindow(path, claudeLogs(t), Span{Before: latest.start})
	if err != nil || older.start != 0 || len(older.messages) != 100 || older.messages[99].GetText() != "m99" {
		t.Fatalf("before the latest: start %d, %d messages, %v", older.start, len(older.messages), err)
	}
	// A gap between two reads, from inside a record: the next record on.
	gap, err := readWindow(path, claudeLogs(t), Span{After: offsets[40] + 3, Before: offsets[45]})
	if err != nil || gap.start != offsets[41] || len(gap.messages) != 4 || gap.messages[0].GetText() != "m41" {
		t.Fatalf("gap: start %d, %d messages, %v", gap.start, len(gap.messages), err)
	}
}

// claudeLogs is Claude's logs, which most tests read.
func claudeLogs(t *testing.T) agent.Logs {
	a, ok := logsOf("claude")
	if !ok {
		t.Fatal("no claude driver")
	}
	return a
}
