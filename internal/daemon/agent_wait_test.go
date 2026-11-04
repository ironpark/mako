//go:build unix

package daemon

import (
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// wait_agent ends with the turn the agent is on, at once for one that
// ended its turns, and early when it waits on the user, a message reaches
// the waiter, or the wait times out.
func TestWaitAgent(t *testing.T) {
	d, parent := projectMCPDaemon(t)
	client := projectMCPClient(t, d, parent)
	childInfo := callProjectTool[agentSummary](t, client, "spawn_agent", spawnAgentArgs{Prompt: "Review the tests"})
	child, _ := d.sessions.get(childInfo.ID)
	childClient := projectMCPClient(t, d, child)
	wait := func(args waitAgentArgs) waitAgentResult {
		args.SessionID = child.ID()
		return callProjectTool[waitAgentResult](t, client, "wait_agent", args)
	}

	child.SetStatus(protocol.AgentWorking)
	time.AfterFunc(300*time.Millisecond, func() { child.SetStatus(protocol.AgentDone) })
	if got := wait(waitAgentArgs{}); got.Reason != "turn_ended" || got.Status != protocol.AgentDone {
		t.Fatalf("a turn ending: %+v", got)
	}
	if got := wait(waitAgentArgs{}); got.Reason != "idle" {
		t.Fatalf("a turn ended before the wait: %+v", got)
	}

	child.SetStatus(protocol.AgentWorking)
	if got := wait(waitAgentArgs{Timeout: 1}); got.Reason != "timeout" || got.Status != protocol.AgentWorking {
		t.Fatalf("a turn going on: %+v", got)
	}
	time.AfterFunc(300*time.Millisecond, func() {
		callProjectTool[agentMessage](t, childClient, "send_message", sendMessageArgs{To: parent.ID(), Text: "halfway"})
	})
	if got := wait(waitAgentArgs{}); got.Reason != "message" {
		t.Fatalf("a message to the waiter: %+v", got)
	}
	child.SetStatus(protocol.AgentWaiting)
	if got := wait(waitAgentArgs{}); got.Reason != "waiting" {
		t.Fatalf("waiting on the user: %+v", got)
	}

	result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "wait_agent", Arguments: waitAgentArgs{SessionID: parent.ID()}})
	if err != nil || !result.IsError {
		t.Fatal("an agent waited on itself")
	}
}

// A note tells the parent of a child that waits on the user a while, once,
// unless the parent waits on it; and of a child that exits without
// reporting.
func TestTheParentHearsOfAChildStuckOrGone(t *testing.T) {
	d, parent := projectMCPDaemon(t)
	client := projectMCPClient(t, d, parent)
	childInfo := callProjectTool[agentSummary](t, client, "spawn_agent", spawnAgentArgs{Prompt: "Review the tests"})
	child, _ := d.sessions.get(childInfo.ID)
	notes := func() []agentMessage {
		inbox, _ := d.projectAgents.messages.pending(parent.ID())
		var out []agentMessage
		for _, m := range inbox {
			if m.Note {
				out = append(out, m)
			}
		}
		return out
	}

	now := time.Now()
	child.SetStatus(protocol.AgentWaiting)
	var w childWatch
	d.noteChild(child.Info(), parent.ID(), &w, now)
	release := d.projectAgents.waitOn(child.ID())
	d.noteChild(child.Info(), parent.ID(), &w, now.Add(noteWaitingAfter))
	release()
	if len(notes()) != 0 {
		t.Fatal("a parent waiting on its child was told it waits")
	}
	w = childWatch{}
	d.noteChild(child.Info(), parent.ID(), &w, now)
	d.noteChild(child.Info(), parent.ID(), &w, now.Add(noteWaitingAfter/2))
	if len(notes()) != 0 {
		t.Fatal("a short wait was told of")
	}
	d.noteChild(child.Info(), parent.ID(), &w, now.Add(noteWaitingAfter))
	d.noteChild(child.Info(), parent.ID(), &w, now.Add(2*noteWaitingAfter))
	if got := notes(); len(got) != 1 || got[0].From != child.ID() || !strings.Contains(got[0].Text, "waits on the user") {
		t.Fatalf("notes: %+v", got)
	}
	if prompt := d.agentPrompt(notes()); !strings.Contains(prompt, "Mako note") || strings.Contains(prompt, "Answer with") {
		t.Fatalf("a note typed in as a message: %q", prompt)
	}

	turnBegan := time.Now()
	callProjectTool[agentMessage](t, projectMCPClient(t, d, child), "send_message", sendMessageArgs{To: parent.ID(), Text: "Done"})
	d.noteExit(child.Info(), parent.ID(), turnBegan)
	if len(notes()) != 1 {
		t.Fatal("a child that reported was told of as gone")
	}
	d.noteExit(child.Info(), parent.ID(), time.Now())
	if got := notes(); len(got) != 2 || !strings.Contains(got[1].Text, "exited") {
		t.Fatalf("a child gone without a word: %+v", got)
	}
}

// What an agent prints is matched with its terminal's escapes removed.
func TestWaitingForOutput(t *testing.T) {
	s, err := session.Start(session.Spec{ID: "out", Command: []string{"sh", "-c", `sleep 0.3; printf '\033[1mall\033[0m \033]0;t\007tests pass'; sleep 5`}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-watchOutput(t.Context(), s, "all tests pass"):
	case <-time.After(3 * time.Second):
		t.Fatal("the text did not show")
	}
	if got := agent.StripANSI("\x1b[38;5;2mok\x1b[0m\x1b]8;;http://x\x1b\\link\x1b(B\x1b[>1u\x1bP+q\x1b\\\r\n"); got != "oklink\n" {
		t.Fatalf("stripped: %q", got)
	}
}
