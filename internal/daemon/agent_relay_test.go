//go:build unix

package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// messagingSession runs a session on d whose agent takes messages, as one
// given the MCP tools does, in a project of the ID project there.
func messagingSession(t *testing.T, d *Daemon, id, project string) (*session.Session, *agentPeer) {
	t.Helper()
	addSessionOf(t, d, id, "claude")
	s, _ := d.sessions.get(id)
	_, stop := d.projectAgents.server(t.Context(), "", id, "")
	t.Cleanup(stop)
	peer := d.projectAgents.activate(s)
	d.projects.set([]*protocol.ProjectScope{{Id: project, Paths: []string{s.Info().GetProject()}}})
	return s, peer
}

// The agents of one project on two nodes list and message each other, and
// the message names its sender's node; an agent of another project stays
// out of reach.
func TestAgentsOfOneProjectMessageAcrossNodes(t *testing.T) {
	a := meshDaemon(t, "relay-a")
	b := meshDaemon(t, "relay-b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	_, server := messagingSession(t, a, "server-agent", "product")
	messagingSession(t, b, "web-agent", "product")

	waitFor(t, "the other node's agent", func() bool {
		r := a.remoteAgent("project:product", "web-agent")
		return r != nil && r.GetAgentMessaging() && b.remoteAgent("project:product", "server-agent") != nil
	})
	listed, err := a.listProjectAgents(server)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(listed.Agents, func(s agentSummary) bool { return s.ID == "web-agent" && s.Node == "relay-b" && s.Messaging }) {
		t.Fatalf("not listed with its node: %+v", listed.Agents)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	msg, err := a.sendAgentMessage(ctx, server, "web-agent", "the API changed")
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := b.projectAgents.messages.pending("web-agent")
	if len(inbox) != 1 || inbox[0].ID != msg.ID || inbox[0].From != "server-agent" || inbox[0].Text != "the API changed" {
		t.Fatalf("inbox %+v, sent %+v", inbox, msg)
	}
	if prompt := b.agentPrompt(inbox); !strings.Contains(prompt, "on relay-a") {
		t.Fatalf("the prompt does not say the node: %q", prompt)
	}
	agents, err := b.readProjectAgents(&protocol.ReadProjectAgents{Path: mustSession(t, b, "web-agent").Info().GetProject()})
	if err != nil || len(agents.GetAgents()) != 2 || len(agents.GetMessages()) != 1 {
		t.Fatalf("monitor %v %v", agents, err)
	}

	// Another project on b: the agent there is not a.'s to reach.
	b.projects.set([]*protocol.ProjectScope{{Id: "other", Paths: []string{mustSession(t, b, "web-agent").Info().GetProject()}}})
	waitFor(t, "the agent's project to change", func() bool { return a.remoteAgent("project:product", "web-agent") == nil })
	if _, err := a.sendAgentMessage(ctx, server, "web-agent", "still there?"); err == nil {
		t.Fatal("reached an agent of another project")
	}
	if _, err := b.takeRelayedMessage(&protocol.RelayAgentMessage{FromSessionId: "server-agent", ToSessionId: "web-agent", Text: "forged", Scope: "project:other"}); err == nil {
		t.Fatal("took a message from an agent of another project")
	}
}

func mustSession(t *testing.T, d *Daemon, id string) *session.Session {
	t.Helper()
	s, ok := d.sessions.get(id)
	if !ok {
		t.Fatalf("no session %s", id)
	}
	return s
}
