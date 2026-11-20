package agy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// decode is what the decoder reads of the lines of body.
func decode(body string) []*protocol.ChatMessage {
	dec := New().NewDecoder()
	var out []*protocol.ChatMessage
	for line := range strings.Lines(body) {
		out = append(out, dec.Line([]byte(line))...)
	}
	return out
}

// A transcript reads as the user's words, the model's prose and calls, and
// each call's result, which follows the step that called it.
func TestTranscriptReadsAsTheChat(t *testing.T) {
	body := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-27T19:23:22Z","content":"<USER_REQUEST>\nRun cat a.txt\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: now.\n</ADDITIONAL_METADATA>"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","input_tokens":6979,"cache_read_tokens":0,"output_tokens":64,"thinking":"hmm","tool_calls":[{"name":"run_command","args":{"CommandLine":"cat a.txt","Cwd":"/p","toolSummary":"Read a.txt"}},{"name":"view_file","args":{"AbsolutePath":"/p/b.go","StartLine":3,"EndLine":4}}]}
{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","content":"Created At: x\nCompleted At: y\nCommand exited with code 0.\n\nCommand output:\nhello\r\n"}
{"step_index":3,"source":"MODEL","type":"GENERIC","status":"ERROR","content":"Created At: x\nCompleted At: y\nEncountered error in step execution: no such file"}
{"step_index":4,"source":"SYSTEM","type":"SYSTEM_MESSAGE","status":"DONE","content":"for the model"}
{"step_index":5,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","input_tokens":2209,"cache_read_tokens":4925,"output_tokens":189,"content":"It says hello."}
{"step_index":6,"source":"SYSTEM","type":"CHECKPOINT","status":"DONE","content":"# Resuming from a compaction"}
`
	got := decode(body)
	if len(got) != 7 {
		t.Fatalf("got %d messages: %v", len(got), got)
	}
	if got[0].GetRole() != "user" || got[0].GetText() != "Run cat a.txt" {
		t.Errorf("prompt %v", got[0])
	}
	cmd, read := got[1], got[2]
	if cmd.GetKind() != protocol.ToolShell || cmd.GetId() != "1.0" || cmd.GetDetail() == "" {
		t.Errorf("command %v", cmd)
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(read.GetText()), &args)
	if read.GetKind() != protocol.ToolRead || args["path"] != "/p/b.go" || args["offset"] != 3.0 || args["limit"] != 2.0 {
		t.Errorf("read %v", read)
	}
	if r := got[3]; !r.GetOutput() || r.GetId() != "1.0" || r.GetError() {
		t.Errorf("command's result %v", r)
	}
	if r := got[4]; !r.GetOutput() || r.GetId() != "1.1" || !r.GetError() {
		t.Errorf("read's result %v", r)
	}
	if got[5].GetRole() != "assistant" || got[6].GetKind() != protocol.ToolSummary {
		t.Errorf("reply %v, summary %v", got[5], got[6])
	}
	var u protocol.Usage
	count := New().NewUsage()
	for line := range strings.Lines(body) {
		count.Count([]byte(line), &u)
	}
	if u.Input != 6979+2209 || u.CachedInput != 4925 || u.Output != 64+189 || u.Context != 2209+4925 {
		t.Errorf("usage %+v", &u)
	}
}

// What agy writes around a result is left out: its times, a command's
// exit, a file's line numbers, the notes around an edit's diff.
func TestOutputsWithoutAgysWrapping(t *testing.T) {
	a := New()
	out, code := a.Output(protocol.ToolShell, "Created At: x\nCompleted At: y\n\nThe command exited with code 1.\nOutput:\nls: nope\r\n\n")
	if out != "ls: nope" || code != 1 {
		t.Errorf("command %q %d", out, code)
	}
	out, _ = a.Output(protocol.ToolRead, "Created At: x\nFile Path: `file:///p`\nShowing lines 1 to 2\nThe following code has been modified to include a line number before every line, in the format: <line_number>: <original_line>. Please note.\n1: package p\n2:\nThe above content does NOT show the entire file contents.")
	if out != "package p\n" {
		t.Errorf("read %q", out)
	}
	out, _ = a.Output(protocol.ToolEdit, "Created At: x\nThe following changes were made.\n[diff_block_start]\n@@ -1 +1 @@\n-a\n+b\n[diff_block_end]\n\nPlease note.")
	if out != "@@ -1 +1 @@\n-a\n+b" {
		t.Errorf("edit %q", out)
	}
}

// A transcript is its folder's conversation, of the workspace the history
// of prompts gives it; a path of any other shape is none.
func TestWhoseTranscript(t *testing.T) {
	root := t.TempDir()
	a := New()
	path := a.LogPath(root, "c1")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(path, []byte(`{"step_index":0,"created_at":"2026-10-27T19:23:22Z"}`+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "history.jsonl"), []byte(`{"display":"x","workspace":"/other","conversationId":"c0"}`+"\n"+`{"display":"y","workspace":"/w","conversationId":"c1"}`+"\n"), 0o644)
	id, cwd, created := a.Whose(path)
	if id != "c1" || cwd != "/w" || !created.Equal(time.Date(2026, 10, 27, 19, 23, 22, 0, time.UTC)) {
		t.Errorf("whose: %q %q %v", id, cwd, created)
	}
	if id, _, _ := a.Whose(filepath.Join(filepath.Dir(path), "transcript.jsonl")); id != "" {
		t.Errorf("the short transcript is %q's", id)
	}
	if matches, _ := filepath.Glob(a.LogPath(root, "*")); !slices.Equal(matches, []string{path}) {
		t.Errorf("every transcript %v", matches)
	}
	// A long prompt pasted in before is passed over.
	long := `{"display":"` + strings.Repeat("x", 5<<20) + `","workspace":"/other","conversationId":"c0"}` + "\n"
	_ = os.WriteFile(filepath.Join(root, "history.jsonl"), []byte(long+`{"display":"y","workspace":"/w","conversationId":"c1"}`+"\n"), 0o644)
	if _, cwd, _ := a.Whose(path); cwd != "/w" {
		t.Errorf("after a long prompt, the workspace is %q", cwd)
	}
}

// mako's entry joins the user's hooks, once, each event's command naming
// it; a file mako cannot read is left alone.
func TestHooksJoinTheUsersOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "hooks.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte("\xef\xbb\xbf"+`{"mine":{"Stop":[{"command":"true"}]}}`), 0o644)
	if err := writeHooks(path, "/bin/mako hook /s"); err != nil {
		t.Fatal(err)
	}
	// Written already, the file is left as it is.
	before, _ := os.Stat(path)
	if err := writeHooks(path, "/bin/mako hook /s"); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !os.SameFile(before, after) {
		t.Error("rewrote hooks it already had")
	}
	body, _ := os.ReadFile(path)
	var doc map[string]map[string][]map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) != 2 || doc["mine"] == nil {
		t.Fatalf("hooks %s", body)
	}
	for name, entry := range doc {
		if name == "mine" {
			continue
		}
		if entry["Stop"][0]["command"] != "/bin/mako hook /s Stop" {
			t.Errorf("stop %v", entry["Stop"])
		}
		if group := entry["PostToolUse"][0]; group["matcher"] != "*" || group["hooks"] == nil {
			t.Errorf("tool hooks %v", group)
		}
	}
	_ = os.WriteFile(path, []byte("not json"), 0o644)
	if writeHooks(path, "/bin/mako hook /s") == nil {
		t.Error("overwrote a file it could not read")
	}
}

// The hook's input reads as the event its command named.
func TestHookEvents(t *testing.T) {
	ev, ok := New().Event([]byte(`{"hook_event_name":"SessionStart","conversationId":"c1","transcriptPath":"/t"}`))
	if !ok || ev.Kind != agent.Started || ev.SessionID != "c1" || ev.Transcript != "/t" {
		t.Errorf("start %+v", ev)
	}
	if ev, ok := New().Event([]byte(`{"hook_event_name":"PreInvocation","conversationId":"c1"}`)); !ok || ev.Kind != agent.Prompted {
		t.Errorf("invocation %+v", ev)
	}
	if ev, ok := New().Event([]byte(`{"hookEventName":"Stop","conversationId":"c1"}`)); !ok || ev.Kind != agent.Stopped {
		t.Errorf("camelCase stop %+v", ev)
	}
	if _, ok := New().Event([]byte(`{"hook_event_name":"PostInvocation"}`)); ok {
		t.Error("an event mako does not follow")
	}
}

// Resuming keeps the settings and drops what started the conversation.
func TestResume(t *testing.T) {
	got := New().Resume([]string{"agy", "--model", "m", "--settings", "/s.json", "--add-dir", "/a", "-i", "do it", "--continue"}, "c1")
	want := []string{"agy", "--conversation", "c1", "--model", "m", "--settings", "/s.json", "--add-dir", "/a"}
	if !slices.Equal(got, want) {
		t.Errorf("resume %q", got)
	}
}

// WithHooks writes to the launch's HOME when overridden, for isolated homes.
func TestWithHooksHonorsLaunchHome(t *testing.T) {
	home := t.TempDir()
	l := &agent.Launch{
		Command: []string{"agy"},
		Env:     map[string]string{"HOME": home},
	}
	if !New().WithHooks(l, "/bin/mako hook /s", "", "") {
		t.Fatal("WithHooks returned false")
	}
	wantPath := filepath.Join(home, ".gemini", "config", "hooks.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected hooks.json at %s: %v", wantPath, err)
	}
}

// WithServers merges Mako's MCP servers into mcp_config.json while preserving
// user entries and passing built-in per-session credentials via launch Env.
func TestWithServers(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// Starts cleanly even when an empty 0-byte mcp_config.json already exists.
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	a := New()
	l1 := &agent.Launch{Command: []string{"agy"}, Env: map[string]string{"HOME": home}}
	cleanup1, err := a.WithServers(l1, []agent.Server{
		{
			ID: "mako", Command: "/bin/mako", Args: []string{"agent-mcp", "/run/agents.sock"},
			Env: map[string]string{"MAKO_SESSION": "s1", "MAKO_AGENT_MCP_TOKEN": "tok1"},
			URL: "http://127.0.0.1:9999/mcp", BearerToken: "tok1",
		},
		{ID: "remote", URL: "https://example.com/mcp", BearerToken: "ext-tok"},
	}, t.TempDir(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup1()

	if l1.Env["MAKO_SESSION"] != "s1" || l1.Env["MAKO_AGENT_MCP_TOKEN"] != "tok1" {
		t.Fatalf("launch env missing mako MCP credentials: %v", l1.Env)
	}

	// Add a user-configured server and verify a second session preserves it and
	// does not leak session tokens into mcp_config.json.
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	makoEntry := doc["mcpServers"]["mako"]
	if makoEntry["command"] != "/bin/mako" {
		t.Fatalf("mako entry: %v", makoEntry)
	}
	if _, hasEnv := makoEntry["env"]; hasEnv {
		t.Fatalf("mako entry should not write per-session env into shared mcp_config.json: %v", makoEntry)
	}
	remoteEntry := doc["mcpServers"]["remote"]
	if remoteEntry["serverUrl"] != "https://example.com/mcp" {
		t.Fatalf("remote entry: %v", remoteEntry)
	}

	// Write a user server alongside and verify session 2 keeps it and sets its own token in l2.Env.
	doc["mcpServers"]["user-tool"] = map[string]any{"command": "/usr/local/bin/user-mcp"}
	updated, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(cfgPath, append([]byte("\xef\xbb\xbf"), updated...), 0o600); err != nil {
		t.Fatal(err)
	}
	l2 := &agent.Launch{Command: []string{"agy"}, Env: map[string]string{"HOME": home}}
	cleanup2, err := a.WithServers(l2, []agent.Server{
		{
			ID: "mako", Command: "/bin/mako", Args: []string{"agent-mcp", "/run/agents.sock"},
			Env: map[string]string{"MAKO_SESSION": "s2", "MAKO_AGENT_MCP_TOKEN": "tok2"},
		},
	}, t.TempDir(), "s2")
	if err != nil {
		t.Fatal(err)
	}
	cleanup2()
	if l2.Env["MAKO_SESSION"] != "s2" || l2.Env["MAKO_AGENT_MCP_TOKEN"] != "tok2" {
		t.Fatalf("second launch env missing credentials: %v", l2.Env)
	}
	body, _ = os.ReadFile(cfgPath)
	var after map[string]map[string]map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")), &after); err != nil {
		t.Fatal(err)
	}
	if after["mcpServers"]["user-tool"] == nil || after["mcpServers"]["mako"] == nil {
		t.Fatalf("expected user-tool and mako preserved, got: %s", body)
	}
}

func TestScreenModeAndControls(t *testing.T) {
	a := New()
	for _, tc := range []struct {
		file string
		want string
	}{
		{"screen_idle.txt", "auto"},
		{"screen_done.txt", "acceptEdits"},
		{"screen_working.txt", ""},
		{"screen_permission.txt", ""},
	} {
		screen, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if got := a.ScreenMode(string(screen)); got != tc.want {
			t.Errorf("%s: ScreenMode = %q, want %q", tc.file, got, tc.want)
		}
	}
	if got := a.ScreenMode("? for shortcuts     plan · Gemini 4 Neon · high\n"); got != "plan" {
		t.Errorf("plan mode = %q", got)
	}
	if a.ModelCommand("gemini-4-neon-high") != "/model gemini-4-neon-high" || a.EffortCommand("high") != "/effort high" {
		t.Errorf("unexpected control commands")
	}
}

func TestCommandsAndInvokeSubagentTool(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	for _, dir := range []string{
		filepath.Join(home, ".gemini", "config", "skills", "my-user-skill"),
		filepath.Join(project, ".agents", "skills", "my-proj-skill"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+filepath.Base(dir)+"\ndescription: test skill\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmds := New().Commands(project, map[string]string{"HOME": home})
	has := func(name string) bool {
		return slices.ContainsFunc(cmds, func(c agent.Command) bool { return c.Name == name })
	}
	if !has("model") || !has("effort") || !has("my-user-skill") || !has("my-proj-skill") {
		t.Fatalf("missing expected commands: %+v", cmds)
	}

	kind, canon := New().Tool("invoke_subagent", json.RawMessage(`{"TypeName":"research","Role":"Code Explorer","Prompt":"Check MCP"}`))
	if kind != protocol.ToolTask || canon["prompt"] != "Check MCP" || canon["agent"] != "Code Explorer" {
		t.Fatalf("invoke_subagent tool: kind=%q canon=%+v", kind, canon)
	}
}
