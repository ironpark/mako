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

// watch_agent registers a non-blocking one-shot notification and pushes a Mako
// note into the caller's inbox once the watched condition triggers, returning
// immediately if the agent is already idle/waiting/exited.
func TestWatchAgent(t *testing.T) {
	d, parent := projectMCPDaemon(t)
	client := projectMCPClient(t, d, parent)
	childInfo := callProjectTool[agentSummary](t, client, "spawn_agent", spawnAgentArgs{Prompt: "Review the tests"})
	child, _ := d.sessions.get(childInfo.ID)
	watch := func(args watchAgentArgs) watchAgentResult {
		args.SessionID = child.ID()
		return callProjectTool[watchAgentResult](t, client, "watch_agent", args)
	}

	// 1. Registering while child is working returns immediately and pushes a note when turn ends.
	child.SetStatus(protocol.AgentWorking)
	got := watch(watchAgentArgs{})
	if got.WatchID == "" || got.AlreadyMatched || got.Until != "turn" {
		t.Fatalf("watch working child: %+v", got)
	}
	child.SetStatus(protocol.AgentDone)
	waitForWithin(t, 3*time.Second, "turn_ended watch notification never arrived", func() bool {
		inbox, _ := d.projectAgents.messages.pending(parent.ID())
		for _, m := range inbox {
			if m.Note && m.From == child.ID() && strings.Contains(m.Text, got.WatchID) && strings.Contains(m.Text, "turn ended") {
				return true
			}
		}
		return false
	})

	// 2. Watching an already-idle child matches immediately without registering a background watch.
	if idle := watch(watchAgentArgs{}); !idle.AlreadyMatched || idle.Reason != "idle" || idle.WatchID != "" {
		t.Fatalf("watch already-idle child: %+v", idle)
	}

	// 3. Watching a working child that transitions to waiting pushes a waiting note.
	child.SetStatus(protocol.AgentWorking)
	gotWait := watch(watchAgentArgs{})
	if gotWait.WatchID == "" || gotWait.AlreadyMatched {
		t.Fatalf("watch before waiting: %+v", gotWait)
	}
	child.SetStatus(protocol.AgentWaiting)
	waitForWithin(t, 3*time.Second, "waiting watch notification never arrived", func() bool {
		inbox, _ := d.projectAgents.messages.pending(parent.ID())
		for _, m := range inbox {
			if m.Note && strings.Contains(m.Text, gotWait.WatchID) && strings.Contains(m.Text, "waits on the user") {
				return true
			}
		}
		return false
	})

	// 4. Watching self is rejected.
	result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "watch_agent", Arguments: watchAgentArgs{SessionID: parent.ID()}})
	if err != nil || !result.IsError {
		t.Fatal("an agent watched itself")
	}

	// 5. Output watch triggers a pushed note when matching terminal text appears.
	outSess, err := session.Start(session.Spec{
		ID: "out-peer", NodeID: d.cfg.NodeID, Agent: "claude", Project: parent.Info().GetProject(),
		Command: []string{"sh", "-c", `sleep 0.3; printf '\033[1mREADY_TOKEN\033[0m\n'; sleep 5`},
		Cols:    80, Rows: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer outSess.Close()
	d.sessions.add(outSess)
	gotOut := callProjectTool[watchAgentResult](t, client, "watch_agent", watchAgentArgs{
		SessionID: outSess.ID(), Until: "output", Text: "READY_TOKEN",
	})
	if gotOut.WatchID == "" || gotOut.AlreadyMatched || gotOut.Until != "output" {
		t.Fatalf("output watch: %+v", gotOut)
	}
	waitForWithin(t, 5*time.Second, "output watch notification never arrived", func() bool {
		inbox, _ := d.projectAgents.messages.pending(parent.ID())
		for _, m := range inbox {
			if m.Note && m.From == outSess.ID() && strings.Contains(m.Text, gotOut.WatchID) && strings.Contains(m.Text, "READY_TOKEN") {
				return true
			}
		}
		return false
	})
}

// A note tells the parent of a child that waits on the user a while, once,
// unless the parent watches it; and of a child that exits without reporting.
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
		t.Fatal("a parent watching its child was told it waits")
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
	// Text longer than what is kept of the output is still found.
	long, err := session.Start(session.Spec{ID: "long", Command: []string{"sh", "-c", `sleep 0.3; head -c 200000 /dev/zero | tr '\0' x; printf end; sleep 5`}})
	if err != nil {
		t.Fatal(err)
	}
	defer long.Close()
	select {
	case <-watchOutput(t.Context(), long, strings.Repeat("x", 200000)+"end"):
	case <-time.After(5 * time.Second):
		t.Fatal("the long text did not show")
	}
	if got := agent.StripANSI("\x1b[38;5;2mok\x1b[0m\x1b]8;;http://x\x1b\\link\x1b(B\x1b[>1u\x1bP+q\x1b\\\r\n"); got != "oklink\n" {
		t.Fatalf("stripped: %q", got)
	}
}
