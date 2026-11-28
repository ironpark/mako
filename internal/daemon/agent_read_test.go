//go:build unix

package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironpark/mako/internal/protocol"
)

// A conversation groups into turns at each user message: what came before
// the first is a turn begun before the read, and the last is in progress
// while the agent works on it or a call of it is unanswered.
func TestAgentTurnsGroupAtUserMessages(t *testing.T) {
	msgs := []*protocol.ChatMessage{
		{Role: "tool", Tool: "Read", Detail: "/a.go", Result: "package a"},
		{Role: "assistant", Text: "Read it."},
		{Role: "user", Text: "Fix the tests"},
		{Role: "assistant", Text: "  "},
		{Role: "assistant", Text: "Running them."},
		{Role: "tool", Tool: "Bash", Kind: "command", Detail: "go test ./...", Result: "FAIL", Error: true},
		{Role: "user", Text: "Again"},
		{Role: "tool", Tool: "Bash", Detail: "go test ./...", Pending: true},
	}
	turns := agentTurns(msgs, false)
	if len(turns) != 3 {
		t.Fatalf("turns: %+v", turns)
	}
	if turns[0].Message != "" || len(turns[0].Parts) != 2 || turns[0].State != turnComplete {
		t.Fatalf("leading turn: %+v", turns[0])
	}
	second := turns[1]
	if second.Message != "Fix the tests" || len(second.Parts) != 2 || second.State != turnComplete {
		t.Fatalf("second turn: %+v", second)
	}
	if p := second.Parts[1]; p.Kind != partToolCall || p.Tool != "Bash" || p.Call != "command" || p.Detail != "go test ./..." || p.Result != "FAIL" || !p.Error {
		t.Fatalf("tool part: %+v", p)
	}
	if turns[2].State != turnInProgress {
		t.Fatal("a turn with an unanswered call is not in progress")
	}
	if agentTurns(msgs[:7], true)[2].State != turnInProgress {
		t.Fatal("the turn of an agent at work is not in progress")
	}
	if agentTurns(nil, true) != nil {
		t.Fatal("an empty conversation has turns")
	}
}

// Long texts are clipped, a turn keeps its latest parts, and the turns kept
// fit the budget, the latest always among them.
func TestAgentTurnsAreBounded(t *testing.T) {
	long := strings.Repeat("x", readAgentText*2)
	msgs := []*protocol.ChatMessage{{Role: "user", Text: long}}
	for i := range readAgentParts + 5 {
		msgs = append(msgs, &protocol.ChatMessage{Role: "assistant", Text: fmt.Sprintf("part %d", i)})
	}
	turns := agentTurns(msgs, false)
	if got := len([]rune(turns[0].Message)); got != readAgentText {
		t.Fatalf("message clipped to %d runes", got)
	}
	if len(turns[0].Parts) != readAgentParts || turns[0].OmittedParts != 5 || turns[0].Parts[readAgentParts-1].Text != fmt.Sprintf("part %d", readAgentParts+4) {
		t.Fatalf("parts kept: %d, omitted %d", len(turns[0].Parts), turns[0].OmittedParts)
	}

	big := func(n int) agentTurn {
		return agentTurn{Message: "m", Parts: []agentPart{{Kind: partText, Text: strings.Repeat("y", n)}, {Kind: partText, Text: "last"}}}
	}
	kept, dropped := fitTurns([]agentTurn{big(600), big(600), big(100)}, 700)
	if len(kept) != 1 || dropped != 2 || kept[0].Parts[0].Text != strings.Repeat("y", 100) {
		t.Fatalf("kept %d, dropped %d", len(kept), dropped)
	}
	kept, dropped = fitTurns([]agentTurn{big(2000)}, 1000)
	if len(kept) != 1 || dropped != 0 || len(kept[0].Parts) != 1 || kept[0].Parts[0].Text != "last" || kept[0].OmittedParts != 1 {
		t.Fatalf("an oversized latest turn was not cut to its latest parts: %+v", kept)
	}
}

// read_agent reads a sibling's session and latest turns, and nothing of an
// agent outside the project.
func TestProjectMCPReadAgent(t *testing.T) {
	d, caller := projectMCPDaemon(t)
	client := projectMCPClient(t, d, caller)
	sibling, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: caller.Info().GetProject()})
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.renameSession(sibling.ID(), "Reviewer"); err != nil {
		t.Fatal(err)
	}
	waitConversation(t, sibling)
	at := time.Now().UTC()
	line := func(v map[string]any) string {
		at = at.Add(time.Second)
		v["timestamp"] = at.Format(time.RFC3339Nano)
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(data) + "\n"
	}
	assistant := func(content ...map[string]any) string {
		return line(map[string]any{"type": "assistant", "message": map[string]any{"id": fmt.Sprintf("m%d", at.UnixNano()), "role": "assistant", "content": content}})
	}
	log := assistant(map[string]any{"type": "text", "text": "Looking at the tests."}, map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "go test ./..."}}) +
		line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}}}}) +
		assistant(map[string]any{"type": "text", "text": "All tests pass."}) +
		line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "Now review the docs"}}) +
		assistant(map[string]any{"type": "text", "text": "Reading the docs."})
	f, err := os.OpenFile(sibling.TranscriptPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(log); err != nil {
		t.Fatal(err)
	}
	f.Close()
	sibling.SetStatus(protocol.AgentWorking)

	var read readAgentResult
	waitFor(t, "the sibling's latest turn was not read", func() bool {
		read = callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID()})
		n := len(read.Turns)
		return n > 0 && read.Turns[n-1].Message == "Now review the docs"
	})
	s := read.Session
	if s.ID != sibling.ID() || s.Agent != "claude" || s.Name != "Reviewer" || s.Status != protocol.AgentWorking || !s.Messaging || s.Folder != caller.Info().GetProject() || s.Node != "" {
		t.Fatalf("session: %+v", s)
	}
	if len(read.Turns) != 2 || read.ChatUnavailable != "" {
		t.Fatalf("turns: %+v (%s)", read.Turns, read.ChatUnavailable)
	}
	first, last := read.Turns[0], read.Turns[1]
	if first.Message != "original conversation" || first.State != turnComplete || len(first.Parts) != 3 {
		t.Fatalf("first turn: %+v", first)
	}
	if p := first.Parts[1]; p.Kind != partToolCall || p.Tool != "Bash" || !strings.Contains(p.Detail, "go test") || p.Result != "ok" {
		t.Fatalf("tool call: %+v", p)
	}
	if last.State != turnInProgress || len(last.Parts) != 1 || last.Parts[0].Text != "Reading the docs." {
		t.Fatalf("last turn: %+v", last)
	}

	one := 1
	latest := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID(), Turns: &one})
	if len(latest.Turns) != 1 || latest.EarlierTurns != 1 || latest.Turns[0].Message != "Now review the docs" {
		t.Fatalf("turns=1: %+v", latest)
	}
	none := 0
	alone := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID(), Turns: &none})
	if len(alone.Turns) != 0 || alone.Session.ID != sibling.ID() || alone.Session.Usage != nil {
		t.Fatalf("turns=0 read the conversation: %+v", alone)
	}

	sibling.SetStatus(protocol.AgentIdle)
	if done := callProjectTool[readAgentResult](t, client, "read_agent", readAgentArgs{SessionID: sibling.ID()}); done.Turns[len(done.Turns)-1].State != turnComplete {
		t.Fatal("the last turn of an idle agent is still in progress")
	}

	tooMany := readAgentMaxTurns + 1
	for _, args := range []any{
		readAgentArgs{SessionID: other.ID()},
		readAgentArgs{SessionID: "no-such-session"},
		readAgentArgs{SessionID: ""},
		readAgentArgs{SessionID: sibling.ID(), Turns: &tooMany},
	} {
		result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "read_agent", Arguments: args})
		if err == nil && !result.IsError {
			t.Fatalf("read_agent accepted %+v", args)
		}
	}
}

// What an agent waits on reads from its approval, its questions, or the
// dialog in its terminal.
func TestAgentWaitingOf(t *testing.T) {
	if agentWaitingOf(&protocol.SessionInfo{}) != nil {
		t.Fatal("an agent waiting on nothing waits")
	}
	w := agentWaitingOf(&protocol.SessionInfo{Approval: &protocol.Approval{Tool: "Bash", Input: `{"command": "rm -rf build"}`}})
	if w == nil || w.Kind != "approval" || w.Tool != "Bash" || !strings.Contains(w.Detail, "rm -rf build") {
		t.Fatalf("approval: %+v", w)
	}
	w = agentWaitingOf(&protocol.SessionInfo{Approval: &protocol.Approval{Tool: "AskUserQuestion", Questions: []*protocol.Question{{Question: "Which database?"}}}})
	if w == nil || w.Kind != "question" || len(w.Questions) != 1 || w.Questions[0] != "Which database?" {
		t.Fatalf("question: %+v", w)
	}
	w = agentWaitingOf(&protocol.SessionInfo{Dialog: &protocol.ScreenDialog{Text: "Trust this folder?", Options: []string{"Yes", "No"}}})
	if w == nil || w.Kind != "dialog" || len(w.Options) != 2 {
		t.Fatalf("dialog: %+v", w)
	}
}
