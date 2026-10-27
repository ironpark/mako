package opencode

import (
	"cmp"
	"encoding/json"
	"net/url"
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

// The plugin and the servers join the configuration OpenCode reads from
// its environment, the user's own kept.
func TestConfigJoinsTheUsers(t *testing.T) {
	t.Setenv(configEnv, `{"plugin":["mine"],"theme":"dark"}`)
	l := agent.Launch{Command: []string{"opencode", "-c"}}
	if !New().WithHooks(&l, "/bin/mako hook /s", "", t.TempDir()) {
		t.Fatal("not hooked")
	}
	if _, err := New().WithServers(&l, []agent.Server{{ID: "gh", Command: "gh-mcp", Args: []string{"stdio"}, Env: map[string]string{"T": "x"}}}, "", ""); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(l.Command, []string{"opencode", "-c"}) {
		t.Errorf("command %q", l.Command)
	}
	var doc struct {
		Plugin []string
		Theme  string
		MCP    map[string]struct {
			Type        string
			Command     []string
			Environment map[string]string
		}
	}
	if err := json.Unmarshal([]byte(l.Env[configEnv]), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Plugin) != 2 || doc.Plugin[0] != "mine" || !strings.HasPrefix(doc.Plugin[1], "file:///") || doc.Theme != "dark" {
		t.Errorf("config %s", l.Env[configEnv])
	}
	if s := doc.MCP["gh"]; s.Type != "local" || !slices.Equal(s.Command, []string{"gh-mcp", "stdio"}) || s.Environment["T"] != "x" {
		t.Errorf("servers %s", l.Env[configEnv])
	}
	t.Setenv(configEnv, "not json")
	if New().WithHooks(&agent.Launch{Command: []string{"opencode"}}, "h", "", t.TempDir()) {
		t.Error("replaced a configuration it could not read")
	}
}

// The plugin, run as OpenCode runs it, writes the session down, what was
// before it first saw it and what comes after, and runs the hook on its
// events; mako reads the log back as the chat.
func TestThePluginWritesTheSessionDown(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	events := filepath.Join(dir, "events")
	_ = os.Mkdir(events, 0o755)
	l := agent.Launch{Command: []string{"opencode"}}
	if !New().WithHooks(&l, "cat > \"$(mktemp '"+events+"/e.XXXXXX')\"", "", dir) {
		t.Fatal("not hooked")
	}
	var doc struct{ Plugin []string }
	_ = json.Unmarshal([]byte(l.Env[configEnv]), &doc)
	u, _ := url.Parse(doc.Plugin[0])

	// OpenCode, as far as the plugin asks of it: a session that has a
	// prompt and a reply, then a subagent's, then the next turn.
	driver := filepath.Join(dir, "driver.mjs")
	_ = os.WriteFile(driver, []byte(`
const mod = await import(process.argv[2]);
const client = { session: { messages: async () => ({ data: [
	{ info: { id: "m1", role: "user" }, parts: [{ id: "p1", messageID: "m1", type: "text", text: "Hi" }] },
	{ info: { id: "m2", role: "assistant", time: { completed: 1 }, tokens: { input: 10, output: 2, reasoning: 1, cache: { read: 100, write: 5 } } },
	  parts: [{ id: "p2", messageID: "m2", type: "text", text: "Hello.", time: { end: 1 } }] },
] }) } };
const hooks = await mod.MakoPlugin({ client, directory: "/w" });
const ev = (type, properties) => hooks.event({ event: { type, properties } });
await ev("session.created", { sessionID: "ses_1", info: { id: "ses_1", directory: "/w", time: { created: 1700000000000 } } });
await ev("session.created", { sessionID: "ses_sub", info: { id: "ses_sub", parentID: "ses_1" } });
await ev("message.updated", { sessionID: "ses_sub", info: { id: "x1", role: "user" } });
await ev("session.status", { sessionID: "ses_1", status: { type: "busy" } });
await ev("message.updated", { sessionID: "ses_1", info: { id: "m3", role: "user" } });
await ev("message.part.updated", { sessionID: "ses_1", part: { id: "p3", messageID: "m3", type: "text", text: "Run ls" } });
await ev("message.updated", { sessionID: "ses_1", info: { id: "m4", role: "assistant" } });
await ev("message.part.updated", { sessionID: "ses_1", part: { id: "p4", messageID: "m4", type: "tool", tool: "bash", callID: "c1", state: { status: "running", input: { command: "ls" } } } });
await ev("message.part.updated", { sessionID: "ses_1", part: { id: "p4", messageID: "m4", type: "tool", tool: "bash", callID: "c1", state: { status: "completed", input: { command: "ls" }, output: "a.go\n", metadata: { exit: 0 } } } });
await ev("message.part.updated", { sessionID: "ses_1", part: { id: "p5", messageID: "m4", type: "text", text: "Done.", time: { start: 1 } } });
await ev("message.part.updated", { sessionID: "ses_1", part: { id: "p5", messageID: "m4", type: "text", text: "Done.", time: { start: 1, end: 2 } } });
await hooks["tool.execute.after"]({ sessionID: "ses_1", tool: "bash" });
await ev("session.status", { sessionID: "ses_1", status: { type: "idle" } });
`), 0o644)
	cmd := exec.Command(node, driver, u.Path)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+data)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}

	path := filepath.Join(New().Place().Root(map[string]string{"XDG_DATA_HOME": data}), "opencode", "mako", "ses_1.jsonl")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "ses_sub.jsonl")); err == nil {
		t.Error("a subagent's session was written down")
	}
	a := New()
	if id, cwd, _, _ := a.Identity([]byte(strings.SplitN(string(body), "\n", 2)[0])); id != "ses_1" || cwd != "/w" {
		t.Errorf("identity %q %q", id, cwd)
	}
	dec := a.NewDecoder()
	var got []*protocol.ChatMessage
	var u2 protocol.Usage
	count := a.NewUsage()
	for line := range strings.Lines(string(body)) {
		got = append(got, dec.Line([]byte(line))...)
		count.Count([]byte(line), &u2)
	}
	var texts []string
	for _, m := range got {
		texts = append(texts, m.GetRole()+":"+cmp.Or(m.GetText(), m.GetTool()))
	}
	if len(got) != 5 || got[0].GetText() != "Hi" || got[2].GetText() != "Run ls" || got[3].GetKind() != protocol.ToolShell || got[3].GetResult() != "a.go\n" || got[3].GetExitCode() != 0 || got[3].ExitCode == nil || got[4].GetText() != "Done." {
		t.Errorf("chat %q\n%s", texts, body)
	}
	if u2.Input != 15 || u2.CachedInput != 100 || u2.Output != 3 {
		t.Errorf("usage %+v", &u2)
	}

	var hooked []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		hooked = nil
		files, _ := filepath.Glob(filepath.Join(events, "e.*"))
		for _, f := range files {
			if b, _ := os.ReadFile(f); len(b) > 0 {
				ev, ok := agent.CommonHooks{}.Event(b)
				if ok && ev.SessionID == "ses_1" && ev.Transcript == filepath.Join(data, "opencode", "mako", "ses_1.jsonl") {
					hooked = append(hooked, string(b))
				}
			}
		}
		if len(hooked) == 4 {
			break
		}
	}
	for _, want := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
		if !slices.ContainsFunc(hooked, func(h string) bool { return strings.Contains(h, `"`+want+`"`) }) {
			t.Errorf("no %s in %q", want, hooked)
		}
	}
}

// A session is resumed by its ID, the prompt it started with left out.
func TestResume(t *testing.T) {
	got := New().Resume([]string{"opencode", "-m", "x/y", "--prompt", "go", "-c"}, "ses_1")
	if !slices.Equal(got, []string{"opencode", "--session", "ses_1", "-m", "x/y"}) {
		t.Errorf("resume %q", got)
	}
}
