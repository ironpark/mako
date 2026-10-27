package pi

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// session is a pi log: its header, a prompt, a call and its result, a
// command the user ran, the reply and a compaction.
const session = `{"type":"session","version":3,"id":"s1","timestamp":"2026-10-28T01:00:00.000Z","cwd":"/w"}
{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-28T01:00:01.000Z","message":{"role":"system","content":"","timestamp":1}}
{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-10-28T01:00:01.000Z","message":{"role":"user","content":"Run the tests","timestamp":1}}
{"type":"message","id":"a3","parentId":"a2","timestamp":"2026-10-28T01:00:02.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Running."},{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"go test ./..."}},{"type":"toolCall","id":"c2","name":"edit","arguments":{"path":"/w/a.go","edits":[{"oldText":"a","newText":"b"}]}}],"usage":{"input":100,"output":20,"cacheRead":1000,"cacheWrite":50},"timestamp":2}}
{"type":"message","id":"a4","parentId":"a3","timestamp":"2026-10-28T01:00:03.000Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"bash","content":[{"type":"text","text":"FAIL\n\nCommand exited with code 1"}],"isError":true,"timestamp":3}}
{"type":"message","id":"a5","parentId":"a4","timestamp":"2026-10-28T01:00:04.000Z","message":{"role":"bashExecution","command":"ls","output":"a.go\n","exitCode":0,"timestamp":4}}
{"type":"message","id":"a6","parentId":"a5","timestamp":"2026-10-28T01:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Done."}],"usage":{"input":10,"output":5,"cacheRead":1200,"cacheWrite":0},"timestamp":5}}
{"type":"compaction","id":"a7","parentId":"a6","timestamp":"2026-10-28T01:00:06.000Z","summary":"What was done.","firstKeptEntryId":"a6","tokensBefore":5000,"usage":{"input":300,"output":40,"cacheRead":0,"cacheWrite":0}}
`

// A log reads as the chat: the prompt, prose, each call in its kind's
// form with its result, a command the user ran, and a summary.
func TestSessionReadsAsTheChat(t *testing.T) {
	a := New()
	dec := a.NewDecoder()
	var got []*protocol.ChatMessage
	for line := range strings.Lines(session) {
		got = append(got, dec.Line([]byte(line))...)
	}
	if len(got) != 8 {
		t.Fatalf("got %d messages: %v", len(got), got)
	}
	if got[0].GetRole() != "user" || got[0].GetText() != "Run the tests" || got[1].GetText() != "Running." {
		t.Errorf("prompt and prose: %v %v", got[0], got[1])
	}
	if c := got[2]; c.GetKind() != protocol.ToolShell || c.GetId() != "c1" {
		t.Errorf("command %v", c)
	}
	if c := got[3]; c.GetKind() != protocol.ToolMultiEdit {
		t.Errorf("edit %v", c)
	}
	if r := got[4]; !r.GetOutput() || r.GetId() != "c1" || !r.GetError() {
		t.Errorf("result %v", r)
	}
	if out, code := a.Output(protocol.ToolShell, got[4].GetText()); out != "FAIL" || code != 1 {
		t.Errorf("output %q %d", out, code)
	}
	if b := got[5]; b.GetKind() != protocol.ToolShell || b.GetResult() != "a.go\n" || b.ExitCode == nil || b.GetExitCode() != 0 {
		t.Errorf("user's command %v", b)
	}
	if got[6].GetText() != "Done." || got[7].GetKind() != protocol.ToolSummary {
		t.Errorf("reply %v, summary %v", got[6], got[7])
	}

	var u protocol.Usage
	count := a.NewUsage()
	for line := range strings.Lines(session) {
		count.Count([]byte(line), &u)
	}
	if u.Input != 100+50+10+300 || u.CachedInput != 2200 || u.Output != 65 || u.Context != 1210 {
		t.Errorf("usage %+v", &u)
	}
	id, cwd, created, done := a.Identity([]byte(strings.SplitN(session, "\n", 2)[0]))
	if id != "s1" || cwd != "/w" || created.IsZero() || !done {
		t.Errorf("identity %q %q %v", id, cwd, created)
	}
}

// A log is found by its ID whatever time names it, and only in a
// working directory's folder: omp's subagents are a folder deeper.
func TestLogName(t *testing.T) {
	a := NewOMP()
	pattern := a.Place().Pattern("/h/.omp/agent", a.LogName("s1"))
	for path, want := range map[string]bool{
		"/h/.omp/agent/sessions/-w/2026-10-28T01-00-00-000Z_s1.jsonl":     true,
		"/h/.omp/agent/sessions/-w/2026-10-28T01-00-00-000Z_s12.jsonl":    false,
		"/h/.omp/agent/sessions/-w/2026-10-28T01-00-00-000Z_s1/sub.jsonl": false,
	} {
		if got, _ := filepath.Match(pattern, path); got != want {
			t.Errorf("%s: %v", path, got)
		}
	}
}

// pi starts a session with the ID mako picks and resumes it by that ID;
// omp picks its own and resumes it so too.
func TestFlags(t *testing.T) {
	if got, ok := New().Pin([]string{"pi"}, "s1"); !ok || !slices.Equal(got, []string{"pi", "--session-id", "s1"}) {
		t.Errorf("pin %q", got)
	}
	if got := New().Resume([]string{"pi", "--model", "m", "-c", "do it"}, "s1"); !slices.Equal(got, []string{"pi", "--session", "s1", "--model", "m"}) {
		t.Errorf("resume %q", got)
	}
	if _, ok := NewOMP().Pin([]string{"omp"}, "s1"); ok {
		t.Error("omp pinned")
	}
	if got := NewOMP().Resume([]string{"omp", "--fork", "x", "--model", "m"}, "s1"); !slices.Equal(got, []string{"omp", "--resume", "s1", "--model", "m"}) {
		t.Errorf("resume %q", got)
	}
	var d agent.Driver = New()
	if _, ok := d.(agent.Servers); !ok {
		t.Error("pi takes no servers")
	}
	if _, ok := agent.Driver(NewOMP()).(agent.Servers); ok {
		t.Error("omp takes servers")
	}
}

// The extension mako writes runs its hook on the agent's events with the
// input Claude Code's hooks get, once written for every launch alike.
func TestTheExtensionRunsTheHook(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	events := filepath.Join(dir, "events")
	_ = os.Mkdir(events, 0o755)
	// Each event to a file of its own, as the hooks run at once.
	hook := "cat > \"$(mktemp '" + events + "/e.XXXXXX')\""
	a := New()
	l := agent.Launch{Command: []string{"pi", "-c"}}
	ok := a.WithHooks(&l, hook, "", dir)
	got := l.Command
	if !ok || len(got) != 4 || got[1] != "--extension" || got[3] != "-c" {
		t.Fatalf("command %q", got)
	}
	again := agent.Launch{Command: []string{"pi"}}
	if a.WithHooks(&again, hook, "", dir); again.Command[2] != got[2] {
		t.Errorf("a second extension %s", again.Command[2])
	}
	// The agent, as far as the extension asks of it.
	driver := filepath.Join(dir, "driver.mjs")
	_ = os.WriteFile(driver, []byte(`
const { default: load } = await import(process.argv[2]);
const handlers = {};
load({ on: (event, fn) => { handlers[event] = fn; } });
const ctx = { sessionManager: { getSessionId: () => "s1", getSessionFile: () => "/l/s1.jsonl" } };
handlers.session_start({}, ctx);
handlers.tool_execution_end({ toolName: "bash" }, ctx);
handlers.agent_settled({}, ctx);
`), 0o644)
	if b, err := exec.Command(node, driver, got[2]).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	var lines []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		lines = nil
		files, _ := filepath.Glob(filepath.Join(events, "e.*"))
		for _, f := range files {
			if body, _ := os.ReadFile(f); len(body) > 0 {
				lines = append(lines, string(body))
			}
		}
		if len(lines) == 3 {
			break
		}
	}
	for _, want := range []string{"SessionStart", "PostToolUse", "Stop"} {
		if !slices.ContainsFunc(lines, func(l string) bool {
			ev, ok := agent.CommonHooks{}.Event([]byte(l))
			return ok && strings.Contains(l, want) && ev.SessionID == "s1" && ev.Transcript == "/l/s1.jsonl"
		}) {
			t.Errorf("no %s in %q", want, lines)
		}
	}
}
