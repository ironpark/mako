package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// subagentLog is a subagent's log as Claude Code writes one: its prompt,
// the records around the API requests, thinking, calls, and results that
// carry whole files, in text blocks and as images.
func subagentLog(prompt string, calls int, at time.Time) string {
	stamp := func(i int) string { return at.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339Nano) }
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	big := strings.Repeat("func f() { return 1 }\n", 900) // 20 KB of a file read
	var b strings.Builder
	fmt.Fprintf(&b, `{"parentUuid":null,"isSidechain":true,"agentId":"a1","type":"user","timestamp":%q,"message":{"role":"user","content":%s}}`+"\n", stamp(0), q(prompt))
	fmt.Fprintf(&b, `{"type":"attachment","timestamp":%q,"attachment":{"type":"skill_listing","content":%s}}`+"\n", stamp(0), q(strings.Repeat("- a skill\n", 300)))
	fmt.Fprintf(&b, `{"type":"api-request-blob","timestamp":%q,"message":{"role":"user","content":[{"type":"text","text":%s},{"type":"text","text":"more"}]}}`+"\n", stamp(0), q(strings.Repeat("context ", 400)))
	for i := range calls {
		fmt.Fprintf(&b, `{"type":"assistant","isSidechain":true,"timestamp":%q,"message":{"id":"m%d","role":"assistant","content":[{"type":"thinking","thinking":%s,"signature":"sig"}]}}`+"\n", stamp(i+1), i, q(strings.Repeat("think ", 200)))
		var input string
		switch i % 4 {
		case 0:
			input = fmt.Sprintf(`{"command":"go test ./... -run T%d","description":"Run the tests"}`, i)
		case 1:
			input = fmt.Sprintf(`{"file_path":"/p/f%d.go"}`, i)
		case 2:
			input = fmt.Sprintf(`{"pattern":"func T%d","path":"/p"}`, i)
		default:
			input = fmt.Sprintf(`{"file_path":"/p/f%d.go","old_string":"a","new_string":"b"}`, i)
		}
		name := []string{"Bash", "Read", "Grep", "Edit"}[i%4]
		fmt.Fprintf(&b, `{"type":"assistant","isSidechain":true,"timestamp":%q,"message":{"id":"m%d","role":"assistant","content":[{"type":"text","text":"Next."},{"type":"tool_use","id":"t%d","name":%q,"input":%s}]}}`+"\n", stamp(i+1), i, i, name, input)
		if i%5 == 4 {
			fmt.Fprintf(&b, `{"type":"user","isSidechain":true,"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":[{"type":"text","text":"a picture"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}]}]}}`+"\n", stamp(i+1), i, strings.Repeat("iVBORw0KGgo", 2000))
		} else {
			fmt.Fprintf(&b, `{"type":"user","isSidechain":true,"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":%s}]},"toolUseResult":{"stdout":%s}}`+"\n", stamp(i+1), i, q(big), q(big))
		}
	}
	fmt.Fprintf(&b, `{"type":"assistant","isSidechain":true,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":"Done: it works."}]}}`+"\n", stamp(calls+1))
	return b.String()
}

// subagentFixture is a conversation at dir/c.jsonl whose calls started n
// subagents, alternately in the background, by their call, and in the
// foreground, by their prompt; its calls are all answered but the last.
func subagentFixture(tb testing.TB, dir string, n int) (string, []*protocol.ChatMessage) {
	tb.Helper()
	path := filepath.Join(dir, "c.jsonl")
	sub := filepath.Join(dir, "c", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		tb.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(sub, name), []byte(body), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	var messages []*protocol.ChatMessage
	for i := range n {
		id := fmt.Sprintf("toolu_%02d", i)
		prompt := fmt.Sprintf("Task %d. ", i) + strings.Repeat("Look into the allocation in the daemon and say what it costs. ", 50)
		in, _ := json.MarshalIndent(map[string]any{"description": fmt.Sprintf("Look %d", i), "prompt": prompt, "agent": "Explore"}, "", "  ")
		messages = append(messages, &protocol.ChatMessage{Role: "tool", Id: id, Tool: "Agent", Kind: protocol.ToolTask, Text: string(in), Pending: i == n-1})
		write(fmt.Sprintf("agent-a%02d.jsonl", i), subagentLog(prompt, 12, time.Now()))
		if i%2 == 0 {
			write(fmt.Sprintf("agent-a%02d.meta.json", i), fmt.Sprintf(`{"agentType":"general-purpose","description":"Look %d","toolUseId":%q,"requestShape":"background","finished":%v}`, i, id, i != n-2))
		} else {
			write(fmt.Sprintf("agent-a%02d.meta.json", i), `{"agentType":"Explore","requestShape":"foreground"}`)
		}
	}
	return path, messages
}

// parsedScan is what the scan read of a log before it decoded only what
// it keeps: every record's message whole, as the chat reads it.
func parsedScan(path string) agentScan {
	var s agentScan
	s.Tail.Read(path, func() { s = agentScan{} }, func(line []byte) {
		var r struct {
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(line, &r) != nil {
			return
		}
		for _, m := range agent.ParseMessage(r.Message, claudeTools) {
			switch {
			case m.GetRole() == "user" && s.prompt == "":
				s.prompt = strings.TrimSpace(m.GetText())
			case m.GetRole() == "tool" && !m.GetOutput():
				s.calls++
				s.tool, s.kind, s.detail = m.GetTool(), m.GetKind(), m.GetDetail()
			}
		}
	})
	return s
}

// A subagent's log is read to the same prompt, calls and latest call as
// when each of its records was parsed whole, however its prompt comes:
// as a string, in blocks folded together, after an image or a result,
// blank at first; and whatever records come between.
func TestAgentScanReadsWhatParsingAllOfItRead(t *testing.T) {
	at := time.Now()
	stamp := at.UTC().Format(time.RFC3339Nano)
	user := func(content string) string {
		return `{"type":"user","isSidechain":true,"timestamp":"` + stamp + `","message":{"role":"user","content":` + content + `}}` + "\n"
	}
	assistant := func(content string) string {
		return `{"type":"assistant","isSidechain":true,"timestamp":"` + stamp + `","message":{"role":"assistant","content":` + content + `}}` + "\n"
	}
	call := assistant(`[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]`)
	logs := map[string]string{
		"real-looking": subagentLog("Find the tests.", 13, at),
		"string":       user(`"  Find the tests.\n"`) + call,
		"blocks":       user(`[{"type":"text","text":"Find"},{"type":"thinking","thinking":"x"},{"type":"text","text":"  "},{"type":"text","text":"the tests."}]`) + call,
		"image first":  user(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},{"type":"text","text":"What is this?"}]`) + call,
		"result first": user(`[{"type":"tool_result","tool_use_id":"x","content":"out"},{"type":"text","text":"Then this."}]`) + call,
		"blank first":  user(`"   "`) + user(`[{"type":"text","text":" "}]`) + call + user(`"Later."`) + user(`"Not this."`),
		"call between": user(`[{"type":"text","text":"Before"},{"type":"tool_use","id":"u","name":"Read","input":{"file_path":"/a"}},{"type":"text","text":"after"}]`),
		"odd records": `{"type":"attachment","message":"not an object"}` + "\n" + `{"type":"system","message":{"role":"system","content":"sys"}}` + "\n" +
			user(`null`) + user(`7`) + `not json` + "\n" + user(`"Go."`) +
			assistant(`[{"type":"tool_use","id":"","name":"","input":{}},{"type":"tool_use","id":"n","name":""},{"type":"tool_use","id":"m","name":"mcp__x__y","input":{"q":1}}]`) +
			assistant(`"prose alone"`) + assistant(`[{"type":"text","text":"a \"tool_use\" in prose"}]`) +
			user(`[{"type":"tool_result","tool_use_id":"m","content":[{"type":"text","text":"done"}]}]`) +
			assistant(`[{"type":"tool_use","id":"k","name":"Agent","input":{"description":"Look","prompt":"Look."}}]`),
	}
	for name, body := range logs {
		path := filepath.Join(t.TempDir(), "agent-a.jsonl")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var got agentScan
		got.read(path)
		want := parsedScan(path)
		if got.prompt != want.prompt || got.calls != want.calls || got.tool != want.tool || got.kind != want.kind || got.detail != want.detail {
			t.Errorf("%s: read %q, %d calls, latest %s %s %q; parsed whole %q, %d, %s %s %q", name,
				got.prompt, got.calls, got.tool, got.kind, got.detail, want.prompt, want.calls, want.tool, want.kind, want.detail)
		}
		if !got.Started.Equal(want.Started) || !got.Updated.Equal(want.Updated) {
			t.Errorf("%s: written %v to %v, want %v to %v", name, got.Started, got.Updated, want.Started, want.Updated)
		}
	}
}

// MAKO_SUBAGENT_LOGS, a glob of subagents' logs, as
// ~/.claude/projects/*/*/subagents/agent-*.jsonl, compares the scan on
// real ones.
func TestAgentScanReadsRealLogsAsParsingAllOfThem(t *testing.T) {
	glob := os.Getenv("MAKO_SUBAGENT_LOGS")
	if glob == "" {
		t.Skip("MAKO_SUBAGENT_LOGS names no logs")
	}
	paths, _ := filepath.Glob(glob)
	for _, path := range paths {
		var got agentScan
		got.read(path)
		want := parsedScan(path)
		for range 200 {
			if got.detail == want.detail {
				break
			}
			// A tool's detail can be any of its arguments, as a map
			// ranges over them: SendMessage's.
			want.detail = parsedScan(path).detail
		}
		if got.prompt != want.prompt || got.calls != want.calls || got.tool != want.tool || got.kind != want.kind || got.detail != want.detail {
			t.Errorf("%s: read %d calls, latest %s %q; parsed whole %d, %s %q", path, got.calls, got.tool, got.detail, want.calls, want.tool, want.detail)
		}
	}
	t.Logf("%d logs", len(paths))
}

// byID is agents by their IDs.
func byID(agents []*protocol.SubAgent) map[string]*protocol.SubAgent {
	out := map[string]*protocol.SubAgent{}
	for _, a := range agents {
		out[a.GetId()] = a
	}
	return out
}

// A call's arguments are parsed once, and again when they change: a
// foreground agent found by its prompt is no longer once its call's
// prompt is another, and a description changed is read.
func TestTheCallsArgumentsAreReadAgainWhenTheyChange(t *testing.T) {
	path, messages := subagentFixture(t, t.TempDir(), 4)
	scans := &agentScans{}
	if a := byID(scans.Read(path, messages))["a01"]; a.GetDescription() != "Look 1" || a.GetCallId() != "toolu_01" {
		t.Fatalf("foreground agent %v", a)
	}
	if len(scans.tasks) != 4 {
		t.Fatalf("%d calls' arguments kept, want 4", len(scans.tasks))
	}
	var in agent.Task
	_ = json.Unmarshal([]byte(messages[1].Text), &in)
	in.Description = "Looked again"
	text, _ := json.Marshal(in)
	messages[1].Text = string(text)
	if a := byID(scans.Read(path, messages))["a01"]; a.GetDescription() != "Looked again" {
		t.Fatalf("foreground agent after its call's arguments changed: %v", a)
	}
	in.Prompt = "Another prompt."
	text, _ = json.Marshal(in)
	messages[1].Text = string(text)
	if a, ok := byID(scans.Read(path, messages))["a01"]; ok {
		t.Fatalf("an agent whose call's prompt is another's is still found by it: %v", a)
	}
	// A call gone takes its arguments with it.
	scans.Read(path, messages[2:])
	if _, ok := scans.tasks["toolu_00"]; ok || len(scans.tasks) != 2 {
		t.Fatalf("arguments kept of calls %v, want the two given", scans.tasks)
	}
}

// The arguments of calls read already are not parsed again: a read of a
// conversation with no subagents' folder allocates only for its stat,
// however many calls it has and however long their prompts.
func TestTheCallsArgumentsAreParsedOnce(t *testing.T) {
	dir := t.TempDir()
	path, messages := subagentFixture(t, dir, 20)
	if err := os.RemoveAll(filepath.Join(dir, "c")); err != nil {
		t.Fatal(err)
	}
	scans := &agentScans{}
	scans.Read(path, messages)
	n := testing.AllocsPerRun(50, func() { scans.Read(path, messages) })
	t.Logf("%v allocations", n)
	if n > 4 {
		t.Fatalf("%v allocations reading 20 calls read before", n)
	}
}

func BenchmarkSubAgentsRead(b *testing.B) {
	path, messages := subagentFixture(b, b.TempDir(), 20)
	scans := &agentScans{}
	if got := scans.Read(path, messages); len(got) != 20 {
		b.Fatalf("%d agents, want 20", len(got))
	}
	b.ReportAllocs()
	for b.Loop() {
		scans.Read(path, messages)
	}
}

func BenchmarkAgentScanRead(b *testing.B) {
	path := filepath.Join(b.TempDir(), "agent-a.jsonl")
	if err := os.WriteFile(path, []byte(subagentLog("Find the tests.", 40, time.Now())), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		var s agentScan
		s.read(path)
		if s.calls != 40 {
			b.Fatalf("%d calls", s.calls)
		}
	}
}
