//go:build unix

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

func TestProjectMonitorKeepsAcknowledgedMessagesAndDoesNotConsumeInboxes(t *testing.T) {
	d, parent := projectMCPDaemon(t)
	child, err := d.spawnProjectAgent(t.Context(), d.projectAgents.peer(parent.ID()), spawnAgentArgs{Prompt: "review"})
	if err != nil {
		t.Fatal(err)
	}
	project := parent.Info().GetProject()
	msg, err := d.projectAgents.messages.send(parent.ID(), child.ID, "Review cancellation", projectScope(parent.Info()))
	if err != nil {
		t.Fatal(err)
	}
	req := &protocol.ReadProjectAgents{Path: project}
	read := func() *protocol.ProjectAgents {
		t.Helper()
		got, err := d.readProjectAgents(req)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := read()
	if len(got.GetAgents()) != 2 || len(got.GetMessages()) != 1 || got.GetMessages()[0].GetState() != "pending" {
		t.Fatalf("snapshot: %v", got)
	}
	for _, a := range got.GetAgents() {
		if a.GetSession().GetId() == child.ID && (a.GetParentSessionId() != parent.ID() || a.GetPendingMessages() != 1 || !a.GetMessaging()) {
			t.Fatalf("child: %v", a)
		}
		if len(a.GetSession().GetCommand()) != 0 {
			t.Fatal("monitor exposed runtime command")
		}
	}
	queued, err := d.projectAgents.messages.read(t.Context(), child.ID, 0, 0, 10, 0)
	if err != nil || len(queued) != 1 || queued[0].ID != msg.ID {
		t.Fatal("monitor consumed a message")
	}
	if _, err := d.projectAgents.messages.read(t.Context(), child.ID, 0, msg.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	got = read()
	if len(got.GetMessages()) != 1 || got.GetMessages()[0].GetState() != "acknowledged" {
		t.Fatal("acknowledgement removed monitor history")
	}
	for _, a := range got.GetAgents() {
		if a.GetPendingMessages() != 0 {
			t.Fatal("pending count did not update")
		}
	}
	var restored agentMessages
	if err := restored.load(d.dir); err != nil {
		t.Fatal(err)
	}
	_, history := restored.monitor(projectScope(parent.Info()), map[string]bool{parent.ID(): true}, 50)
	if len(history) != 1 || history[0].GetState() != "acknowledged" {
		t.Fatal("monitor history was not persisted")
	}
	if _, err := d.projectAgents.messages.send(parent.ID(), child.ID, "More context", projectScope(parent.Info())); err != nil {
		t.Fatal(err)
	}
	if err := d.closeSession(child.ID); err != nil {
		t.Fatal(err)
	}
	got = read()
	if len(got.GetAgents()) != 1 || len(got.GetMessages()) != 2 || got.GetMessages()[1].GetState() != "removed" {
		t.Fatalf("removed recipient: %v", got)
	}
}

func TestProjectMonitorBoundsHistoryAndSeparatesProjects(t *testing.T) {
	var store agentMessages
	for i := 0; i < maxAgentHistory+5; i++ {
		if _, err := store.send("a", "b", "review", "/a"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.send("x", "y", "private", "/b"); err != nil {
		t.Fatal(err)
	}
	pending, history := store.monitor("/a", map[string]bool{"a": true, "b": true}, maxAgentHistory+10)
	if len(history) != maxAgentHistory || pending["b"] != maxAgentHistory+5 || history[0].GetId() != 6 {
		t.Fatalf("bounded history: %d %v", len(history), pending)
	}
	_, history = store.monitor("/a", map[string]bool{}, 3)
	if len(history) != 3 || history[0].GetId() != maxAgentHistory+3 {
		t.Fatal("message limit did not return the latest messages")
	}
	_, history = store.monitor("/b", map[string]bool{}, 50)
	if len(history) != 1 || history[0].GetText() != "private" {
		t.Fatal("project histories mixed")
	}
}

func TestProjectMonitorRoutesToTheSessionsNode(t *testing.T) {
	a := meshDaemon(t, "monitor-a")
	b := meshDaemon(t, "monitor-b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	addSession(t, b, "remote-agent")
	s, _ := b.sessions.get("remote-agent")
	project := s.Info().GetProject()
	if _, err := b.projectAgents.messages.send("other", "remote-agent", "remote message", projectScope(s.Info())); err != nil {
		t.Fatal(err)
	}
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var got *protocol.ProjectAgents
	waitFor(t, "remote project monitor", func() bool {
		var err error
		got, err = c.ProjectAgents(ctx, &protocol.ReadProjectAgents{NodeId: b.cfg.NodeID, Path: project})
		return err == nil && len(got.GetAgents()) == 1
	})
	if got.GetAgents()[0].GetSession().GetId() != "remote-agent" || len(got.GetMessages()) != 1 || got.GetMessages()[0].GetText() != "remote message" {
		t.Fatalf("remote monitor: %v", got)
	}
}
