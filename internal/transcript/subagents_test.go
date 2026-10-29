package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
)

// The agents a conversation's calls started are read from their own logs:
// one in the foreground, found by its prompt, runs while its call is open;
// one in the background, by its call, until its meta says it finished.
// Each says how many calls it has made and its latest, and a read of an
// unchanged session log still follows them while one runs.
func TestSubagentsAreFollowedInTheirOwnLogs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "projects", "p")
	path := filepath.Join(dir, "c.jsonl")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	head := `{"type":"user","sessionId":"c","cwd":"/p","message":{"role":"user","content":"look around"}}` + "\n"
	calls := `{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"fg","name":"Agent","input":{"description":"Find the tests","prompt":"Find the tests.","subagent_type":"Explore"}},` +
		`{"type":"tool_use","id":"bg","name":"Agent","input":{"description":"Review","prompt":"Review it.","run_in_background":true}}]}}` + "\n"
	launched := `{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"bg","content":"launched"}]}}` + "\n"
	writeLog(t, path, head+calls+launched)

	sub := filepath.Join(dir, "c", "subagents")
	agentLog := func(prompt string, tools ...string) string {
		s := `{"type":"user","isSidechain":true,"timestamp":"` + now + `","message":{"role":"user","content":"` + prompt + `"}}` + "\n"
		for _, cmd := range tools {
			s += `{"type":"assistant","isSidechain":true,"timestamp":"` + now + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"Bash","input":{"command":"` + cmd + `"}}]}}` + "\n"
		}
		return s
	}
	writeLog(t, filepath.Join(sub, "agent-f1.jsonl"), agentLog("Find the tests.", "ls", "go test ./..."))
	writeLog(t, filepath.Join(sub, "agent-f1.meta.json"), `{"agentType":"Explore","requestShape":"foreground"}`)
	writeLog(t, filepath.Join(sub, "agent-b1.jsonl"), agentLog("Review it.", "git diff"))
	writeLog(t, filepath.Join(sub, "agent-b1.meta.json"), `{"agentType":"general-purpose","description":"Review","toolUseId":"bg","requestShape":"background"}`)
	// An agent of another call, gone from the window, is left out.
	writeLog(t, filepath.Join(sub, "agent-old.jsonl"), agentLog("Old work."))
	writeLog(t, filepath.Join(sub, "agent-old.meta.json"), `{"toolUseId":"gone","requestShape":"background"}`)

	info := &protocol.SessionInfo{Agent: "claude", ConversationId: "c"}
	var cache Cache
	chat, err := cache.ReadBound(info, path, Span{})
	if err != nil {
		t.Fatal(err)
	}
	if fg := chat.GetMessages()[1]; !fg.GetPending() || chat.GetMessages()[2].GetPending() {
		t.Fatalf("pending: the open call %v, the answered one %v", fg.GetPending(), chat.GetMessages()[2].GetPending())
	}
	agents := map[string]*protocol.SubAgent{}
	for _, a := range chat.GetAgents() {
		agents[a.GetId()] = a
	}
	if len(agents) != 2 {
		t.Fatalf("agents %v, want the two this window's calls started", chat.GetAgents())
	}
	f1, b1 := agents["f1"], agents["b1"]
	if f1.GetCallId() != "fg" || !f1.GetRunning() || f1.GetToolCalls() != 2 || f1.GetTool() != "Bash" || f1.GetDetail() != "go test ./..." || f1.GetKind() != "Explore" || f1.GetDescription() != "Find the tests" {
		t.Fatalf("foreground agent %+v", f1)
	}
	if b1.GetCallId() != "bg" || !b1.GetRunning() || b1.GetToolCalls() != 1 || f1.GetStarted() == 0 {
		t.Fatalf("background agent %+v", b1)
	}

	// The background agent works on and finishes while the session's log
	// is still.
	f, err := os.OpenFile(filepath.Join(sub, "agent-b1.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(agentLog("", "go vet ./...")[len(agentLog("")):])
	f.Close()
	writeLog(t, filepath.Join(sub, "agent-b1.meta.json"), `{"toolUseId":"bg","requestShape":"background","finished":true}`)
	if chat, err = cache.ReadBound(info, path, Span{}); err != nil {
		t.Fatal(err)
	}
	for _, a := range chat.GetAgents() {
		if a.GetId() == "b1" && (a.GetRunning() || a.GetToolCalls() != 2 || a.GetDetail() != "go vet ./...") {
			t.Fatalf("finished background agent %+v", a)
		}
	}
}

// A subagent started after the conversation was first read, its folder
// not there yet, is found on a later read, and one whose meta changes is
// read again.
func TestSubagentsStartedLaterAreFound(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "projects", "p")
	path := filepath.Join(dir, "c.jsonl")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	writeLog(t, path, `{"type":"user","sessionId":"c","cwd":"/p","message":{"role":"user","content":"go"}}`+"\n"+
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"tool_use","id":"bg","name":"Agent","input":{"description":"Review","prompt":"Review it.","run_in_background":true}}]}}`+"\n")
	a := claudeLogs(t)
	scans := newSubAgents(a)
	messages, _, err := readMessages(path, a)
	if err != nil {
		t.Fatal(err)
	}
	read := func() []*protocol.SubAgent { return scans.Read(path, messages) }
	if agents := read(); len(agents) != 0 {
		t.Fatalf("agents %v before any started", agents)
	}
	sub := filepath.Join(dir, "c", "subagents")
	writeLog(t, filepath.Join(sub, "agent-b1.jsonl"), `{"type":"user","isSidechain":true,"timestamp":"`+now+`","message":{"role":"user","content":"Review it."}}`+"\n")
	writeLog(t, filepath.Join(sub, "agent-b1.meta.json"), `{"toolUseId":"bg","requestShape":"background"}`)
	if agents := read(); len(agents) != 1 || !agents[0].GetRunning() {
		t.Fatalf("agents %v, want the one started", agents)
	}
	writeLog(t, filepath.Join(sub, "agent-b1.meta.json"), `{"toolUseId":"bg","requestShape":"background","finished":true}`)
	if agents := read(); len(agents) != 1 || agents[0].GetRunning() {
		t.Fatalf("agents %v, want the one finished", agents)
	}
}

// A page read through the cache, before the latest messages, says what a
// read of its own does: its messages, the whole conversation's usage and
// the subagents its calls started, from scans the cache keeps.
func TestCachedPagesReadAsAReadOfTheirOwn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "projects", "p")
	path := filepath.Join(dir, "c.jsonl")
	var body strings.Builder
	body.WriteString(`{"type":"user","sessionId":"c","cwd":"/p","message":{"role":"user","content":"look around"}}` + "\n")
	body.WriteString(`{"type":"assistant","uuid":"a1","message":{"role":"assistant","usage":{"input_tokens":10,"output_tokens":5},"content":[` +
		`{"type":"tool_use","id":"fg","name":"Agent","input":{"description":"Find the tests","prompt":"Find the tests."}}]}}` + "\n")
	body.WriteString(`{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"fg","content":"found"}]}}` + "\n")
	for i := range 300 {
		fmt.Fprintf(&body, `{"type":"user","uuid":"m%d","message":{"role":"user","content":"m%d"}}`+"\n", i, i)
	}
	writeLog(t, path, body.String())
	sub := filepath.Join(dir, "c", "subagents")
	writeLog(t, filepath.Join(sub, "agent-f1.jsonl"), `{"type":"user","isSidechain":true,"message":{"role":"user","content":"Find the tests."}}`+"\n")
	writeLog(t, filepath.Join(sub, "agent-f1.meta.json"), `{"requestShape":"foreground"}`)

	info := &protocol.SessionInfo{Agent: "claude", ConversationId: "c"}
	var cache Cache
	latest, err := cache.ReadBound(info, path, Span{})
	if err != nil || latest.GetStart() == 0 {
		t.Fatalf("latest starts at %d, %v", latest.GetStart(), err)
	}
	span := Span{Before: latest.GetStart()}
	for range 2 { // the second from scans already done
		got, err := cache.ReadBound(info, path, span)
		if err != nil {
			t.Fatal(err)
		}
		want, err := ReadBound(info, path, span)
		if err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(got, want) {
			t.Fatalf("cached page %v\nwant %v", got, want)
		}
		if len(got.GetAgents()) != 1 || got.GetUsage().GetOutput() != 5 {
			t.Fatalf("page agents %v, usage %v", got.GetAgents(), got.GetUsage())
		}
	}
}
