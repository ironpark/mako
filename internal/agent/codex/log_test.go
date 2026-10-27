package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// In code mode a call is a script that runs the tools; the items Codex
// completes say what they did, each a row with what it gave back.
func TestCodeModeReadsAsTheItemsItRan(t *testing.T) {
	body := `{"type":"response_item","payload":{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"exec","input":"text(await tools.exec_command({cmd:\"ls\"}))"}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"exec-1","command":["/bin/zsh","-lc","ls"],"cwd":"file:///p","status":"completed","aggregated_output":"main.go\n","exit_code":0}}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"FileChange","id":"exec-2","changes":{"/p/a.go":{"type":"update","unified_diff":"--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"},"/p/b.go":{"type":"add","content":"package b\n"}},"status":"completed","stdout":"Success."}}}
{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"Script completed"}]}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"ContextCompaction","id":"c1"}}}
{"type":"compacted","payload":{"message":"What was done.","replacement_history":[]}}
`
	got := decode(body)
	if len(got) != 3 {
		t.Fatalf("got %d messages, want the command, the change and the summary: %v", len(got), got)
	}
	cmd, change, summary := got[0], got[1], got[2]
	if cmd.GetKind() != protocol.ToolShell || cmd.GetText() != "{\n  \"command\": \"ls\",\n  \"cwd\": \"/p\"\n}" || cmd.GetResult() != "main.go\n" || cmd.GetExitCode() != 0 || cmd.ExitCode == nil {
		t.Errorf("command %v", cmd)
	}
	if change.GetKind() != protocol.ToolPatch || change.GetDetail() != "/p/a.go, /p/b.go" || !strings.Contains(change.GetText(), `@@ -1 +1 @@\n-old\n+new`) || strings.Contains(change.GetText(), "+++ b/a.go") {
		t.Errorf("change %v", change)
	}
	if summary.GetKind() != protocol.ToolSummary || summary.GetText() != "What was done." {
		t.Errorf("summary %v", summary)
	}
}

// An agent spawn_agent starts is followed in its own rollout, found by the
// thread ID the call answers with, while its turn runs.
func TestSubagentsAreFollowedInTheirRollouts(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "sessions")
	parent := filepath.Join(sessions, "2026", "10", "28", "rollout-2026-10-28T01-00-00-parent.jsonl")
	child := filepath.Join(sessions, "2026", "10", "28", "rollout-2026-10-28T01-00-05-child1.jsonl")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(parent, "")
	write(child, `{"timestamp":"`+now+`","type":"session_meta","payload":{"id":"child1","agent_role":"explorer","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent","depth":1}}}}}
{"timestamp":"`+now+`","type":"event_msg","payload":{"type":"task_started"}}
{"timestamp":"`+now+`","type":"response_item","payload":{"type":"function_call","call_id":"c1","name":"exec_command","arguments":"{\"cmd\":\"go test ./...\"}"}}
`)
	messages := decode(`{"type":"response_item","payload":{"type":"function_call","call_id":"s1","name":"spawn_agent","namespace":"multi_agent_v1","arguments":"{\"message\":\"Run the tests.\"}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"s1","output":"{\"agent_id\":\"child1\",\"nickname\":\"Ada\"}"}}
`)
	// The window pairs the result with its call.
	messages[0].Result = messages[1].GetText()
	scan := New().NewSubAgents()
	agents := scan.Read(parent, messages[:1])
	if len(agents) != 1 {
		t.Fatalf("agents %v", agents)
	}
	a := agents[0]
	if a.GetId() != "child1" || a.GetCallId() != "s1" || !a.GetRunning() || a.GetKind() != "explorer" || a.GetDescription() != "Ada" ||
		a.GetToolCalls() != 1 || a.GetToolKind() != protocol.ToolShell || a.GetDetail() != "go test ./..." {
		t.Fatalf("agent %v", a)
	}
	f, _ := os.OpenFile(child, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"timestamp":"` + now + `","type":"event_msg","payload":{"type":"task_complete"}}` + "\n")
	f.Close()
	if agents := scan.Read(parent, messages[:1]); len(agents) != 1 || agents[0].GetRunning() {
		t.Fatalf("agent still running after its turn: %v", agents)
	}
}
