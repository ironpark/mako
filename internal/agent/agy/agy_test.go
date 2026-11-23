package agy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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

func TestWriteHooksPrunesStaleMakoEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Empty 0-byte hooks.json is handled cleanly.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeHooks(path, "/old/mako hook /s"); err != nil {
		t.Fatalf("empty file writeHooks: %v", err)
	}

	// Seed a user entry + stale mako entry, then write a new hook command.
	seed := `{"mine":{"Stop":[{"command":"true"}]},"mako-00000000":{"Stop":[{"command":"/stale/mako hook /s Stop"}]}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeHooks(path, "/new/mako hook /s"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if _, hasStale := doc["mako-00000000"]; hasStale {
		t.Fatalf("stale mako entry was not pruned: %s", body)
	}
	if doc["mine"] == nil || len(doc) != 2 {
		t.Fatalf("expected user entry + 1 current mako entry, got: %s", body)
	}

	// Even when the current mako-* entry already matches, stale mako-* entries are pruned.
	doc["mako-deadbeef"] = json.RawMessage(`{"Stop":[{"command":"/other/mako"}]}`)
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeHooks(path, "/new/mako hook /s"); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(path)
	doc = nil
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if _, hasStale := doc["mako-deadbeef"]; hasStale || len(doc) != 2 {
		t.Fatalf("stale mako-deadbeef not pruned when current entry existed: %s", body)
	}

	// Repeating without stale entries does not rewrite the file.
	before, _ := os.Stat(path)
	if err := writeHooks(path, "/new/mako hook /s"); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !os.SameFile(before, after) {
		t.Error("rewrote hooks.json unexpectedly")
	}
}

func TestWriteHooksConcurrent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(`{"mine":{"Stop":[{"command":"true"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	const n = 16
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- writeHooks(path, fmt.Sprintf("/bin/mako-%d hook /s", i%4))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent writeHooks error: %v", err)
		}
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("corrupt hooks.json after concurrent writes: %v\n%s", err, body)
	}
	if doc["mine"] == nil || len(doc) != 2 {
		t.Fatalf("expected mine + 1 mako-* entry after concurrent writes, got: %s", body)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "hooks.json" {
			t.Fatalf("unexpected leftover file %q", e.Name())
		}
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

func TestWithHooksStatusLine(t *testing.T) {
	home := t.TempDir()
	l := &agent.Launch{
		Command: []string{"agy"},
		Env:     map[string]string{"HOME": home},
	}
	if !New().WithHooks(l, "/bin/mako hook /s", "/bin/mako statusline /s", "") {
		t.Fatal("WithHooks returned false")
	}
	idx := slices.Index(l.Command, "--settings")
	if idx < 0 || idx+1 >= len(l.Command) {
		t.Fatalf("expected --settings in command: %q", l.Command)
	}
	var cfg struct {
		StatusLine struct {
			Type             string `json:"type"`
			Command          string `json:"command"`
			Enabled          bool   `json:"enabled"`
			StackWithDefault bool   `json:"stack_with_default"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal([]byte(l.Command[idx+1]), &cfg); err != nil {
		t.Fatalf("invalid --settings JSON %q: %v", l.Command[idx+1], err)
	}
	if cfg.StatusLine.Type != "command" || cfg.StatusLine.Command != "/bin/mako statusline /s" || !cfg.StatusLine.Enabled || !cfg.StatusLine.StackWithDefault {
		t.Fatalf("unexpected statusLine config: %+v", cfg.StatusLine)
	}

	// Existing --settings JSON without statusLine gets merged cleanly.
	l2 := &agent.Launch{
		Command: []string{"agy", "--settings", `{"verbosity":"high"}`},
		Env:     map[string]string{"HOME": home},
	}
	if !New().WithHooks(l2, "/bin/mako hook /s", "/bin/mako statusline /s", "") {
		t.Fatal("WithHooks returned false")
	}
	if len(l2.Command) != 3 {
		t.Fatalf("expected merged --settings in place, got %q", l2.Command)
	}
	var merged map[string]any
	if err := json.Unmarshal([]byte(l2.Command[2]), &merged); err != nil {
		t.Fatalf("merged --settings invalid JSON: %v", err)
	}
	if merged["verbosity"] != "high" || merged["statusLine"] == nil {
		t.Fatalf("expected verbosity preserved and statusLine merged: %+v", merged)
	}

	// Existing --settings file path without statusLine gets read and merged.
	customFile := filepath.Join(home, "custom.json")
	if err := os.WriteFile(customFile, []byte(`{"copyOnSelect":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l3 := &agent.Launch{
		Command: []string{"agy", "--settings=" + customFile},
		Env:     map[string]string{"HOME": home},
	}
	if !New().WithHooks(l3, "/bin/mako hook /s", "/bin/mako statusline /s", "") {
		t.Fatal("WithHooks returned false")
	}
	if !strings.HasPrefix(l3.Command[1], "--settings=") || !strings.Contains(l3.Command[1], `"copyOnSelect":false`) || !strings.Contains(l3.Command[1], `"statusLine"`) {
		t.Fatalf("expected file --settings merged, got %q", l3.Command)
	}

	// Explicit statusLine in --settings is respected and not overwritten.
	l4 := &agent.Launch{
		Command: []string{"agy", "--settings", `{"statusLine":{"type":"command","command":"echo hi"}}`},
		Env:     map[string]string{"HOME": home},
	}
	if !New().WithHooks(l4, "/bin/mako hook /s", "/bin/mako statusline /s", "") {
		t.Fatal("WithHooks returned false")
	}
	if l4.Command[2] != `{"statusLine":{"type":"command","command":"echo hi"}}` {
		t.Fatalf("overwrote explicit user statusLine: %q", l4.Command)
	}
}

func TestStatusLineParsesContextAndQuota(t *testing.T) {
	a := New()
	payload := `{
		"model": {"id": "Gemini 4 Neon (High)", "display_name": "Gemini 4 Neon (High)", "effort": "high"},
		"context_window": {
			"total_input_tokens": 88244,
			"total_output_tokens": 61074,
			"context_window_size": 1048576,
			"used_percentage": 14.24,
			"current_usage": {
				"input_tokens": 63382,
				"cache_creation_input_tokens": 100,
				"cache_read_input_tokens": 20857
			}
		},
		"quota": {
			"gemini-5h": {"remaining_fraction": 0.72, "reset_in_seconds": 3600},
			"gemini-weekly": {"remaining_fraction": 0.9378, "reset_time": "2026-07-06T07:50:32Z"},
			"claude-5h": {"remaining_fraction": 0.10, "reset_in_seconds": 1800},
			"gemini-daily": {"remaining_fraction": 0.50, "disabled": true}
		},
		"cycle_mode": "accept-edits",
		"cost": {"total_usd": 0.42},
		"conversation_id": "conv-99",
		"transcript_path": "/tmp/conv-99.jsonl",
		"agent_state": "working",
		"tool_confirmation_pending": true
	}`
	u := a.Status([]byte(payload))
	if u.GetModel() != "Gemini 4 Neon (High)" || u.GetEffort() != "high" || u.GetPermissionMode() != "acceptEdits" || u.GetCostUsd() != 0.42 {
		t.Fatalf("model/effort/mode/cost = %q / %q / %q / %v", u.GetModel(), u.GetEffort(), u.GetPermissionMode(), u.GetCostUsd())
	}
	if ev, ok := a.StatusHookEvent([]byte(payload)); !ok || ev.Kind != agent.Waiting || ev.SessionID != "conv-99" || ev.Transcript != "/tmp/conv-99.jsonl" {
		t.Fatalf("StatusHookEvent waiting = %+v (%v)", ev, ok)
	}
	if ev, ok := a.StatusHookEvent([]byte(`{"conversation_id":"conv-99","agent_state":"tool_use"}`)); !ok || ev.Kind != agent.Prompted {
		t.Fatalf("StatusHookEvent working = %+v (%v)", ev, ok)
	}
	if ev, ok := a.StatusHookEvent([]byte(`{"conversation_id":"conv-99","agent_state":"idle"}`)); !ok || ev.Kind != agent.Idle {
		t.Fatalf("StatusHookEvent idle = %+v (%v)", ev, ok)
	}
	if u.GetWindow() != 1048576 || u.GetContext() != 63382+100+20857 || u.GetInput() != 88244 || u.GetOutput() != 61074 {
		t.Fatalf("context/tokens = %+v", u)
	}
	if len(u.GetLimits()) != 2 {
		t.Fatalf("expected 2 active limits (5h + weekly, disabled skipped), got %+v", u.GetLimits())
	}
	var fiveH, week *protocol.RateLimit
	for _, l := range u.GetLimits() {
		switch l.GetMinutes() {
		case protocol.FiveHours:
			fiveH = l
		case protocol.Week:
			week = l
		}
	}
	if fiveH == nil || int(fiveH.GetUsed()+0.5) != 28 || fiveH.GetResets() == 0 {
		t.Fatalf("unexpected gemini-5h limit (should pick gemini over claude): %+v", fiveH)
	}
	wantReset, _ := time.Parse(time.RFC3339, "2026-07-06T07:50:32Z")
	if week == nil || int(week.GetUsed()+0.5) != 6 || week.GetResets() != wantReset.Unix() {
		t.Fatalf("unexpected gemini-weekly limit: %+v", week)
	}

	// UserStatusLine reads ~/.gemini/antigravity-cli/settings.json.
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"statusLine":{"type":"command","command":"~/.gemini/status.sh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := a.UserStatusLine(nil); got != "~/.gemini/status.sh" {
		t.Fatalf("UserStatusLine = %q", got)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"statusLine":{"type":"command","command":"~/.gemini/status.sh","enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := a.UserStatusLine(nil); got != "" {
		t.Fatalf("disabled UserStatusLine = %q, want empty", got)
	}
}

func TestEnsureSidecarAndDeliverMessage(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	if err := EnsureSidecar(env, "/bin/makod", []string{"agy-sidecar", "/run/agent-cli.sock"}); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(home, ".gemini", "config", "sidecars", "mako-bridge", "sidecar.json")
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["command"] != "/bin/makod" || manifest["restart_policy"] != "always" {
		t.Fatalf("unexpected sidecar manifest: %+v", manifest)
	}
	cfgPath := filepath.Join(home, ".gemini", "config", "config.json")
	cfgBody, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfgBody), `"mako-bridge"`) || !strings.Contains(string(cfgBody), `"enabled": true`) {
		t.Fatalf("expected mako-bridge enabled in config.json: %s", cfgBody)
	}

	// Explicit user disable in config.json must not be overwritten.
	if err := os.WriteFile(cfgPath, []byte(`{"sidecars":{"mako-bridge":{"enabled":false}},"other":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSidecar(env, "/bin/makod", []string{"agy-sidecar", "/run/agent-cli.sock"}); err != nil {
		t.Fatal(err)
	}
	cfgBody, _ = os.ReadFile(cfgPath)
	if !strings.Contains(string(cfgBody), `"enabled":false`) {
		t.Fatalf("EnsureSidecar overwrote explicit disable: %s", cfgBody)
	}

	// DeliverMessage prefers $HOME/.gemini/antigravity-cli/bin/agentapi.
	outFile := filepath.Join(home, "delivered.txt")
	binPath := filepath.Join(home, ".gemini", "antigravity-cli", "bin", "agentapi")
	if err := os.MkdirAll(filepath.Dir(binPath), 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + outFile + "\"\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := New().DeliverMessage(t.Context(), "conv-123", "Mako message from worker", "Build passed", env); err != nil {
		t.Fatalf("DeliverMessage: %v", err)
	}
	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "send-message\n--title=Mako message from worker\nconv-123\nBuild passed\n"
	if string(got) != want {
		t.Fatalf("agentapi args = %q, want %q", string(got), want)
	}
}

func TestIsSubAgentAndSubAgentHookFiltering(t *testing.T) {
	a := New()
	root := t.TempDir()
	parentID := "0492e25a-38a3-40ec-8f63-03cd7b76b08a"
	workerMetaID := "4aa27cb9-78c6-475e-be5a-8479b7a708c3"
	workerMemberID := "c0cd66a6-519d-49f2-bf3b-92ba3d120538"
	workerSenderID := "112e8e38-3691-4fa6-acfb-5f8b0fdb42e9"
	unrelatedID := "99999999-9999-4999-8999-999999999999"

	parentLog := a.LogPath(root, parentID)
	workerMetaLog := a.LogPath(root, workerMetaID)
	workerSenderLog := a.LogPath(root, workerSenderID)
	unrelatedLog := a.LogPath(root, unrelatedID)

	for _, p := range []string{parentLog, workerMetaLog, workerSenderLog, unrelatedLog} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	subDir := filepath.Join(root, "brain", parentID, ".system_generated", "subagents")
	swarmDir := filepath.Join(root, "brain", parentID, ".system_generated", "swarm")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(swarmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(subDir, workerMetaID+".json"), []byte(`{"conversationId":"`+workerMetaID+`","state":"SUBAGENT_STATE_ALIVE"}`), 0o644)
	_ = os.WriteFile(filepath.Join(swarmDir, "members.json"), []byte(`{"members":{"ui-schema":{"conversation_id":"`+workerMemberID+`"}}}`), 0o644)
	_ = os.WriteFile(workerSenderLog, []byte(`{"step_index":0,"source":"SYSTEM","type":"SYSTEM_MESSAGE","content":"[Message] sender=`+parentID+` brief"}`+"\n"), 0o644)
	_ = os.WriteFile(unrelatedLog, []byte(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nhello\n</USER_REQUEST>"}`+"\n"), 0o644)

	if !a.IsSubAgent(parentID, parentLog, workerMetaID, workerMetaLog) {
		t.Fatal("expected worker in subagents/*.json to be detected as subagent")
	}
	// Works even when parentLog is not yet bound by deriving brainDir from childLog.
	if !a.IsSubAgent(parentID, "", workerMetaID, workerMetaLog) {
		t.Fatal("expected detection from childLog when parentLog is empty")
	}
	if !a.IsSubAgent(parentID, parentLog, workerMemberID, "") {
		t.Fatal("expected worker in swarm/members.json to be detected as subagent")
	}
	if !a.IsSubAgent(parentID, parentLog, workerSenderID, workerSenderLog) {
		t.Fatal("expected worker with SYSTEM_MESSAGE sender=parentID to be detected as subagent")
	}
	if a.IsSubAgent(parentID, parentLog, unrelatedID, unrelatedLog) {
		t.Fatal("unrelated /new conversation was falsely treated as subagent")
	}
	if a.IsSubAgent(parentID, parentLog, parentID, parentLog) {
		t.Fatal("parent conversation itself must not be a subagent")
	}

	// Explicit subagent fields in Event payload are filtered safely.
	for _, raw := range []string{
		`{"hook_event_name":"SessionStart","conversationId":"sub","isSubagent":true}`,
		`{"hook_event_name":"PreInvocation","conversationId":"sub","agentId":"w1"}`,
		`{"hook_event_name":"Stop","conversationId":"sub","parentConversationId":"parent"}`,
	} {
		if ev, ok := a.Event([]byte(raw)); ok {
			t.Fatalf("subagent hook %s should be ignored, got %+v", raw, ev)
		}
	}
	if ev, ok := a.Event([]byte(`{"hook_event_name":"SubagentStart","conversationId":"sub","parentConversationId":"parent","agentId":"w1"}`)); !ok || ev.Kind != agent.ChildStarted || ev.SessionID != "parent" || ev.AgentID != "w1" {
		t.Fatalf("SubagentStart = %+v %v", ev, ok)
	}
}

func TestAgyNewSubAgents(t *testing.T) {
	a := New()
	root := t.TempDir()
	parentID := "0492e25a-38a3-40ec-8f63-03cd7b76b08a"
	aliveID := "112e8e38-3691-4fa6-acfb-5f8b0fdb42e9"
	killedID := "e0dc1918-d065-4209-b30d-ee13ffd36126"

	parentLog := a.LogPath(root, parentID)
	aliveLog := a.LogPath(root, aliveID)
	killedLog := a.LogPath(root, killedID)
	for _, p := range []string{parentLog, aliveLog, killedLog} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	subDir := filepath.Join(root, "brain", parentID, ".system_generated", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(subDir, aliveID+".json"), []byte(`{"conversationId":"`+aliveID+`","subagentDescriptor":{"typeName":"worker","role":"Swarm Worker (ui-schema)"},"state":"SUBAGENT_STATE_ALIVE","spawnStepIndex":10}`), 0o644)
	_ = os.WriteFile(filepath.Join(subDir, killedID+".json"), []byte(`{"conversationId":"`+killedID+`","subagentDescriptor":{"typeName":"worker","role":"Swarm Worker (old)"},"state":"SUBAGENT_STATE_KILLED","spawnStepIndex":2}`), 0o644)
	_ = os.WriteFile(aliveLog, []byte(
		`{"step_index":0,"source":"SYSTEM","type":"SYSTEM_MESSAGE","created_at":"2026-11-22T00:02:06Z","content":"brief"}`+"\n"+
			`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","tool_calls":[{"name":"run_command","args":{"CommandLine":"go test ./..."}}]}`+"\n",
	), 0o644)

	msgs := []*protocol.ChatMessage{
		{Role: "tool", Id: "10.0", Tool: "swarm", Text: `{"action":"add","name":"ui-schema"}`},
	}
	scan := a.NewSubAgents()
	got := scan.Read(parentLog, msgs)
	if len(got) != 1 {
		t.Fatalf("got %d subagents (%+v), want 1 alive subagent", len(got), got)
	}
	sub := got[0]
	if sub.GetId() != aliveID || !sub.GetRunning() || sub.GetCallId() != "10.0" || sub.GetDescription() != "Swarm Worker (ui-schema)" || sub.GetToolCalls() != 1 || sub.GetTool() != "run_command" || sub.GetDetail() != "go test ./..." || sub.GetLog() != aliveLog {
		t.Fatalf("unexpected subagent: %+v", sub)
	}
}

func TestAgyCompletedAndParkedSubAgentsNotRunning(t *testing.T) {
	a := New()
	root := t.TempDir()
	parentID := "5be8efb1-e3fd-4a9f-abd8-4a268de95c36"
	doneID := "6a4e3524-3e91-43e9-bff6-755e49a565ac"
	oldDoneID := "3d336a18-21f3-43d7-819d-19d41d1aab9d"
	parkedID := "4aa27cb9-78c6-475e-be5a-8479b7a708c3"
	staleID := "454232d2-fbd9-41a3-99d0-e1e46702a012"

	parentLog := a.LogPath(root, parentID)
	for _, id := range []string{parentID, doneID, oldDoneID, parkedID, staleID} {
		if err := os.MkdirAll(filepath.Dir(a.LogPath(root, id)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sysDir := filepath.Join(root, "brain", parentID, ".system_generated")
	subDir := filepath.Join(sysDir, "subagents")
	swarmDir := filepath.Join(sysDir, "swarm")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(swarmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMeta := func(id, typ, role string, step int) {
		_ = os.WriteFile(filepath.Join(subDir, id+".json"),
			[]byte(`{"conversationId":"`+id+`","subagentDescriptor":{"typeName":"`+typ+`","role":"`+role+`"},"state":"SUBAGENT_STATE_ALIVE","spawnStepIndex":`+strconv.Itoa(step)+`}`), 0o644)
	}
	writeMeta(doneID, "research", "Graphics Drivers Auditor", 37)
	writeMeta(oldDoneID, "research", "Older Auditor", 5) // spawn step scrolled out of messages
	writeMeta(parkedID, "worker", "Swarm Worker (monitor)", 40)
	writeMeta(staleID, "research", "Stale Auditor", 41)

	// Completed subagent: ALIVE in metadata, but ended turn with PLANNER_RESPONSE DONE and no tool_calls.
	doneLog := a.LogPath(root, doneID)
	_ = os.WriteFile(doneLog, []byte(
		`{"step_index":0,"source":"SYSTEM","type":"SYSTEM_MESSAGE","created_at":"2026-11-22T07:10:00Z","content":"audit"}`+"\n"+
			`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-22T07:11:00Z","tool_calls":[{"name":"send_message","args":{"Recipient":"p","Message":"done"}}]}`+"\n"+
			`{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-22T07:11:01Z","content":"Message sent"}`+"\n"+
			`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-22T07:11:02Z","content":"Audit complete."}`+"\n",
	), 0o644)
	// Same for oldDoneID whose spawn call is outside the current window.
	_ = os.WriteFile(a.LogPath(root, oldDoneID), []byte(
		`{"step_index":0,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-22T06:00:00Z","content":"Done."}`+"\n",
	), 0o644)
	// Parked swarm worker: called swarm_wait and has wake_mode != "working" in members.json.
	_ = os.WriteFile(a.LogPath(root, parkedID), []byte(
		`{"step_index":0,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-22T07:00:00Z","tool_calls":[{"name":"swarm_wait","args":{"since":1}}]}`+"\n"+
			`{"step_index":1,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-22T07:00:01Z","content":"parked"}`+"\n",
	), 0o644)
	_ = os.WriteFile(filepath.Join(swarmDir, "members.json"), []byte(
		`{"members":{"monitor":{"conversation_id":"`+parkedID+`","phase":"RUNNING","wake_mode":"anything","killed":false}}}`,
	), 0o644)
	// Stale subagent: mid-tool-call, but log file hasn't been touched in over StaleAgent.
	staleLog := a.LogPath(root, staleID)
	_ = os.WriteFile(staleLog, []byte(
		`{"step_index":0,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-22T05:00:00Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"sleep 999"}}]}`+"\n",
	), 0o644)
	oldTime := time.Now().Add(-agent.StaleAgent - time.Minute)
	_ = os.Chtimes(staleLog, oldTime, oldTime)
	_ = os.Chtimes(filepath.Join(subDir, staleID+".json"), oldTime, oldTime)

	msgs := []*protocol.ChatMessage{
		{Role: "tool", Id: "37.0", Tool: "invoke_subagent", Text: `{}`},
		{Role: "tool", Id: "40.0", Tool: "swarm", Text: `{}`},
		{Role: "tool", Id: "41.0", Tool: "invoke_subagent", Text: `{}`},
	}
	scan := a.NewSubAgents()
	got := scan.Read(parentLog, msgs)
	byID := map[string]*protocol.SubAgent{}
	for _, sa := range got {
		byID[sa.GetId()] = sa
	}
	if _, exists := byID[oldDoneID]; exists {
		t.Fatalf("completed subagent outside message window should be omitted: %+v", byID[oldDoneID])
	}
	for _, id := range []string{doneID, parkedID, staleID} {
		sa, ok := byID[id]
		if !ok {
			t.Fatalf("expected subagent %s in results for inline link, got %+v", id, got)
		}
		if sa.GetRunning() {
			t.Fatalf("subagent %s should not be Running: %+v", id, sa)
		}
	}

	// Sending a follow-up message to doneID resumes it as Running=true.
	f, err := os.OpenFile(doneLog, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"step_index":4,"source":"SYSTEM","type":"SYSTEM_MESSAGE","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `","content":"follow-up"}` + "\n")
	_ = f.Close()

	got = scan.Read(parentLog, msgs)
	for _, sa := range got {
		if sa.GetId() == doneID && !sa.GetRunning() {
			t.Fatalf("resumed subagent %s should be Running=true after follow-up message", doneID)
		}
	}
}
