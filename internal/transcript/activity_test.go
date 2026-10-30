package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

func TestActivityKeepsChildrenOutsideTheChatWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.jsonl")
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	var body strings.Builder
	body.WriteString(`{"type":"user","sessionId":"c","cwd":"/p","message":{"role":"user","content":"go"}}` + "\n")
	body.WriteString(`{"type":"assistant","uuid":"task","message":{"role":"assistant","content":[{"type":"tool_use","id":"bg","name":"Agent","input":{"prompt":"Review it."}}]}}` + "\n")
	body.WriteString(`{"type":"user","uuid":"started","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"bg","content":"launched"}]}}` + "\n")
	for i := range 300 {
		fmt.Fprintf(&body, `{"type":"user","uuid":"m%d","message":{"role":"user","content":"m%d"}}`+"\n", i, i)
	}
	writeLog(t, path, body.String())
	info := &protocol.SessionInfo{Agent: "claude", ConversationId: "c"}
	var activity Activity
	read := func() ActivityState {
		t.Helper()
		got, err := activity.Read(info, path)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if len(read().Agents) != 0 {
		t.Fatal("child exists before its log")
	}
	// The parent log does not change when the child starts or finishes.
	child := filepath.Join(dir, "c", "subagents", "agent-b.meta.json")
	writeLog(t, strings.TrimSuffix(child, ".meta.json")+".jsonl", `{"type":"user","timestamp":"`+stamp+`","message":{"role":"user","content":"Review it."}}`+"\n")
	writeLog(t, child, `{"toolUseId":"bg","requestShape":"background","finished":false}`)
	if got := read().Agents; len(got) != 1 || !got[0].GetRunning() {
		t.Fatalf("running child outside window: %v", got)
	}
	writeLog(t, child, `{"toolUseId":"bg","requestShape":"background","finished":true}`)
	if got := read().Agents; len(got) != 1 || got[0].GetRunning() {
		t.Fatalf("completed child: %v", got)
	}
}

func TestActivityReadsCodexCancellationAndResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	info := &protocol.SessionInfo{Agent: "codex", ConversationId: "c"}
	now := time.Now().UTC()
	head := `{"type":"session_meta","payload":{"id":"c","cwd":"/p"}}` + "\n"
	record := func(kind string, at time.Time) string {
		return fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":%q}}`+"\n", at.Format(time.RFC3339Nano), kind)
	}
	writeLog(t, path, head+record("task_started", now))
	var activity Activity
	for i, step := range []struct{ kind, want string }{
		{"", protocol.AgentWorking},
		{"turn_aborted", protocol.AgentIdle},
		{"task_started", protocol.AgentWorking},
		{"task_complete", protocol.AgentDone},
	} {
		if step.kind != "" {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString(record(step.kind, now.Add(time.Duration(i)*time.Second)))
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		got, err := activity.Read(info, path)
		if err != nil || got.Status != step.want {
			t.Fatalf("%s: %+v, %v", step.kind, got, err)
		}
	}
	// A replacement with the same size must reset the lifecycle scan.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), `"task_complete"`, `"turn_aborted" `))
	if err := os.WriteFile(path+".new", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	got, err := activity.Read(info, path)
	if err != nil || got.Status != protocol.AgentIdle {
		t.Fatalf("replaced log: %+v, %v", got, err)
	}
}
