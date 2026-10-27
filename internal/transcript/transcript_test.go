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
	claude, _ := Lookup("claude")
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
	env := Env([]string{"HOME=/h", "OPENAI_API_KEY=secret", "PATH=/bin"}, map[string]string{"CODEX_HOME": "/c", "TOKEN": "t"})
	if len(env) != 2 || env["HOME"] != "/h" || env["CODEX_HOME"] != "/c" {
		t.Fatalf("env=%v", env)
	}
}

func TestClaudeHooksAreSettings(t *testing.T) {
	claude, _ := Lookup("claude")
	got, ok := claude.Hook([]string{"claude", "--model", "opus"}, "'/bin/makod' hook '/run/h.sock'", "'/bin/makod' statusline '/run/h.sock'")
	if !ok || len(got) != 5 || got[3] != "--settings" {
		t.Fatalf("hook=%q %v", got, ok)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct{ Type, Command string }
		}
		StatusLine struct{ Type, Command string }
	}
	if err := json.Unmarshal([]byte(got[4]), &settings); err != nil {
		t.Fatal(err)
	}
	if h := settings.Hooks["SessionStart"]; len(h) != 1 || len(h[0].Hooks) != 1 || h[0].Hooks[0].Type != "command" || h[0].Hooks[0].Command != "'/bin/makod' hook '/run/h.sock'" {
		t.Fatalf("settings=%s", got[4])
	}
	if sl := settings.StatusLine; sl.Type != "command" || sl.Command != "'/bin/makod' statusline '/run/h.sock'" {
		t.Fatalf("no status line: %s", got[4])
	}
	// Claude's handling of a second --settings is undocumented.
	if _, ok := claude.Hook([]string{"claude", "--settings=/mine.json"}, "h", "s"); ok {
		t.Fatal("hook replaced the user's own settings")
	}
	if h := settings.Hooks["PermissionRequest"]; len(h) != 1 || h[0].Hooks[0].Command != "'/bin/makod' hook '/run/h.sock'" {
		t.Fatalf("no approval hook: %s", got[4])
	}
	if ev, ok := claude.ParseHook([]byte(`{"hook_event_name":"SessionStart","session_id":"s","transcript_path":"/p"}`)); !ok || ev.SessionID != "s" || ev.TranscriptPath != "/p" {
		t.Fatalf("parse=%+v %v", ev, ok)
	}
	if ev, ok := claude.ParseHook([]byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`)); !ok || ev.Tool != "Bash" || string(ev.ToolInput) != `{"command":"ls"}` {
		t.Fatalf("parse=%+v %v", ev, ok)
	}
	for _, other := range []string{`{"hook_event_name":"SessionStart","session_id":"s","agent_id":"sub"}`, `not json`, `{}`} {
		if _, ok := claude.ParseHook([]byte(other)); ok {
			t.Errorf("%s taken", other)
		}
	}
}

// Codex takes its hooks as config overrides, which it runs only when
// trusted by the hash it computes of each; these are hashes Codex itself
// recorded on trusting the same hooks.
func TestCodexHooksAreTrustedOverrides(t *testing.T) {
	cmd := "if [ -f '/Users/ironpark/.orca/agent-hooks/codex-hook.sh' ] && [ -r '/Users/ironpark/.orca/agent-hooks/codex-hook.sh' ] && [ -x '/Users/ironpark/.orca/agent-hooks/codex-hook.sh' ]; then /bin/sh '/Users/ironpark/.orca/agent-hooks/codex-hook.sh'; else { command -p cat 2>/dev/null || cat; } >/dev/null 2>&1 || :; fi"
	for name, want := range map[string]string{
		"SessionStart":      "sha256:9a0e1518079e2e24e2cf7d47a4deecd885a9089d5eed8522d16ca7334925b4b9",
		"Stop":              "sha256:062e403078366fd27b0501522dcdd66a9ce9488df3c009329e1de067bfbbfa14",
		"PermissionRequest": "sha256:bf384f5375f7e816fb8d77e977f5b09a37135ae9c136b8908693762f102fe69e",
	} {
		if got := codexHookHash(hookEvent{Name: name, Timeout: 10}, cmd); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}

	codex, _ := Lookup("codex")
	got, ok := codex.Hook([]string{"codex", "resume", "abc"}, `'/bin/san"bo' hook '/run/h.sock'`, "")
	if !ok || got[0] != "codex" || !slices.Equal(got[len(got)-2:], []string{"resume", "abc"}) {
		t.Fatalf("hook=%q %v", got, ok)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		`hooks.Stop=[{hooks=[{type="command",command="'/bin/san\"bo' hook '/run/h.sock'",timeout=10}]}]`,
		`hooks.PermissionRequest=[{hooks=[{type="command",command="'/bin/san\"bo' hook '/run/h.sock'",timeout=86400}]}]`,
		`"/<session-flags>/config.toml:permission_request:0:0"={trusted_hash="sha256:`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %s in %q", want, got)
		}
	}
	if ev, ok := codex.ParseHook([]byte(`{"hook_event_name":"Stop","session_id":"s","transcript_path":null}`)); !ok || ev.Event != "Stop" {
		t.Fatalf("parse=%+v %v", ev, ok)
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
	chat, err := cache.ReadBound(info, path)
	if err != nil || len(chat.GetMessages()) != 1 {
		t.Fatalf("chat=%+v %v", chat, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(line("second"))
	f.Close()
	if chat, err = cache.ReadBound(info, path); err != nil || len(chat.GetMessages()) != 2 {
		t.Fatalf("appended chat=%+v %v", chat, err)
	}
	info.ConversationId = "other"
	if _, err := cache.ReadBound(info, path); err == nil {
		t.Fatal("a cached read skipped the identity check")
	}
}
