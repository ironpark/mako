//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
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

// An agent waits on another node's for its turn to end, as that node's
// state says, but not for its output.
func TestWaitingOnAnotherNodesAgent(t *testing.T) {
	a := meshDaemon(t, "wait-a")
	b := meshDaemon(t, "wait-b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	_, server := messagingSession(t, a, "server-agent", "product")
	web, _ := messagingSession(t, b, "web-agent", "product")
	web.SetStatus(protocol.AgentWorking)
	waitFor(t, "the other node's agent at work", func() bool {
		return protocol.AtWork(a.remoteAgent("project:product", "web-agent").GetStatus())
	})

	got, err := a.watchProjectAgent(server, watchAgentArgs{SessionID: "web-agent"})
	if err != nil || got.WatchID == "" || got.AlreadyMatched {
		t.Fatalf("watching its turn: %+v %v", got, err)
	}
	time.AfterFunc(300*time.Millisecond, func() { web.SetStatus(protocol.AgentDone) })
	waitForWithin(t, 10*time.Second, "remote turn_ended watch notification never arrived", func() bool {
		inbox, _ := a.projectAgents.messages.pending(server.id)
		for _, m := range inbox {
			if m.Note && m.From == "web-agent" && strings.Contains(m.Text, got.WatchID) && strings.Contains(m.Text, "turn ended") {
				return true
			}
		}
		return false
	})
	if r := a.remoteAgent("project:product", "web-agent"); r.GetTurns() != 1 {
		t.Fatalf("turns as shared: %d", r.GetTurns())
	}
	if _, err := a.watchProjectAgent(server, watchAgentArgs{SessionID: "web-agent", Until: "output", Text: "ok"}); err == nil {
		t.Fatal("watched another node's output")
	}
}

// cloneSession runs a session on d whose agent takes messages, in a clone
// of the repository at remote, and in no project of several folders.
func cloneSession(t *testing.T, d *Daemon, id, remote string) *agentPeer {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	s, err := session.Start(session.Spec{
		ID: id, NodeID: d.config().NodeID, Agent: "claude", Project: dir,
		Command: []string{"sh", "-c", "sleep 30"}, Cols: 40, Rows: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	d.sessions.add(s)
	_, stop := d.projectAgents.server(t.Context(), "", id, "")
	t.Cleanup(stop)
	return d.projectAgents.activate(s)
}

// The agents of two clones of one repository, on two nodes, are one
// project's with no project made of them: they list and message each
// other. A clone of another repository is not theirs.
func TestAgentsOfClonesOfOneRepositoryMessageAcrossNodes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	a := meshDaemon(t, "clone-a")
	b := meshDaemon(t, "clone-b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	server := cloneSession(t, a, "server-agent", "git@github.com:acme/app.git")
	cloneSession(t, b, "web-agent", "https://github.com/acme/app.git")
	cloneSession(t, b, "other-agent", "https://github.com/acme/other.git")

	scope := a.projectScope(server.session.Info())
	if !strings.HasPrefix(scope, repoScopePrefix) {
		t.Fatalf("scope %q", scope)
	}
	waitFor(t, "the other node's clone", func() bool {
		r := a.remoteAgent(scope, "web-agent")
		return r != nil && r.GetAgentMessaging()
	})
	if a.remoteAgent(scope, "other-agent") != nil {
		t.Fatal("an agent of another repository is listed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := a.sendAgentMessage(ctx, server, "web-agent", "the API changed"); err != nil {
		t.Fatal(err)
	}
	if inbox, _ := b.projectAgents.messages.pending("web-agent"); len(inbox) != 1 || inbox[0].Text != "the API changed" {
		t.Fatalf("inbox %+v", inbox)
	}
	if _, err := a.sendAgentMessage(ctx, server, "other-agent", "hello"); err == nil {
		t.Fatal("reached an agent of another repository")
	}
}

func spawnableMeshDaemon(t *testing.T, name string, profiles []agentspec.Profile, peers ...string) *Daemon {
	t.Helper()
	command, root := fakeAgent(t, "claude")
	return meshDaemonWith(t, name, func(cfg *config.Config) {
		cfg.Workspace.SyncAgents = new(false)
		cfg.Agents = map[string]agentspec.Agent{
			"claude": {
				Command:  []string{command},
				Env:      map[string]string{"MAKO_FAKE_AGENT": "claude", "MAKO_FAKE_ROOT": root, "CLAUDE_CONFIG_DIR": root},
				Profiles: profiles,
			},
		}
	}, peers...)
}

// An agent in a shared project discovers another node of the same project
// (even before any session runs there), inspects its profiles, spawns a remote
// child there, exchanges messages both ways immediately without waiting for
// gossip, watches its turns, and stops it; out-of-scope folders, disallowed
// profiles, and non-owners are refused.
func TestSpawnAndCommunicateAcrossNodesInSharedProject(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	a := spawnableMeshDaemon(t, "spawn-a", nil)
	b := spawnableMeshDaemon(t, "spawn-b", []agentspec.Profile{
		{Name: "linux-worker", Description: "Runs Linux builds and tests", Model: "sonnet"},
		{Name: "manual-only", Description: "Not for agents", AllowAgent: new(false)},
	}, p2pAddr(a))
	c := spawnableMeshDaemon(t, "spawn-c", nil, p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	letIn(t, c, a)
	letIn(t, a, c)

	dirA := t.TempDir()
	dirB1 := t.TempDir()
	dirB2 := t.TempDir()
	dirBOther := t.TempDir()
	a.projects.set([]*protocol.ProjectScope{{Id: "product", Name: "Product", Paths: []string{dirA}}})
	b.projects.set([]*protocol.ProjectScope{{Id: "product", Name: "Product", Paths: []string{dirB1, dirB2}}})
	a.pokeStatus()
	b.pokeStatus()

	parent, err := a.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: dirA})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := a.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: dirA})
	if err != nil {
		t.Fatal(err)
	}
	parentPeer := a.projectAgents.peer(parent.ID())
	siblingPeer := a.projectAgents.peer(sibling.ID())
	if parentPeer == nil || siblingPeer == nil {
		t.Fatal("missing agent peers")
	}

	// Wait for mutual node/project/session visibility (b has NO sessions yet!).
	waitFor(t, "mutual project discovery across nodes", func() bool {
		if b.remoteAgent("project:product", parent.ID()) == nil {
			return false
		}
		listed, err := a.listProjectAgents(parentPeer)
		if err != nil {
			return false
		}
		return slices.ContainsFunc(listed.Nodes, func(n projectNode) bool {
			return n.ID == b.config().NodeID && n.Name == "spawn-b" && n.Reachable && n.Spawnable && slices.Contains(n.Folders, dirB2)
		})
	})

	// list_agent_profiles without args summarizes other project nodes and hides manual-only.
	profiles, err := a.listAgentProfiles(parentPeer, listAgentProfilesArgs{})
	if err != nil {
		t.Fatal(err)
	}
	rawProfiles, _ := json.Marshal(profiles)
	if strings.Contains(string(rawProfiles), "manual-only") {
		t.Fatalf("disallowed remote profile leaked: %s", rawProfiles)
	}
	if !slices.ContainsFunc(profiles.Nodes, func(n remoteNodeInfo) bool {
		return n.Name == "spawn-b" && n.Spawnable && slices.ContainsFunc(n.Presets, func(p spawnablePreset) bool {
			return p.Target == "claude:linux-worker" && p.Model == "sonnet"
		})
	}) {
		t.Fatalf("remote node presets missing from list_agent_profiles: %+v", profiles.Nodes)
	}

	// list_agent_profiles(node="spawn-b") returns spawn-b's presets and folders directly.
	remoteProfiles, err := a.listAgentProfiles(parentPeer, listAgentProfilesArgs{Node: "spawn-b"})
	if err != nil {
		t.Fatal(err)
	}
	if remoteProfiles.Node != "spawn-b" || remoteProfiles.NodeID != b.config().NodeID || !slices.Contains(remoteProfiles.Folders, dirB2) {
		t.Fatalf("unexpected remote profile metadata: %+v", remoteProfiles)
	}
	if !slices.ContainsFunc(remoteProfiles.Presets, func(p spawnablePreset) bool { return p.Target == "claude:linux-worker" }) {
		t.Fatalf("missing linux-worker preset: %+v", remoteProfiles.Presets)
	}

	// Security checks before spawning: disallowed profile, out-of-scope folder, node without project.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if _, err := a.spawnProjectAgent(ctx, parentPeer, spawnAgentArgs{Node: "spawn-b", Agent: "claude", Profile: "manual-only", Prompt: "nope"}); err == nil {
		t.Fatal("spawned disallowed remote profile")
	}
	if _, err := a.spawnProjectAgent(ctx, parentPeer, spawnAgentArgs{Node: "spawn-b", Folder: dirBOther, Agent: "claude", Prompt: "escape"}); err == nil {
		t.Fatal("spawned into folder outside project on remote node")
	}
	if _, err := a.spawnProjectAgent(ctx, parentPeer, spawnAgentArgs{Node: "spawn-c", Agent: "claude", Prompt: "wrong node"}); err == nil {
		t.Fatal("spawned onto a node that does not have this project")
	}

	// Spawn a remote child on spawn-b in dirB2 using linux-worker profile.
	childSummary, err := a.spawnProjectAgent(ctx, parentPeer, spawnAgentArgs{
		Node:    "spawn-b",
		Folder:  dirB2,
		Agent:   "claude",
		Profile: "linux-worker",
		Name:    "Remote Worker",
		Prompt:  "Run the Linux test suite",
	})
	if err != nil {
		t.Fatalf("remote spawn failed: %v", err)
	}
	if childSummary.Node != "spawn-b" || childSummary.NodeID != b.config().NodeID || childSummary.ParentID != parent.ID() ||
		childSummary.Folder != dirB2 || childSummary.Profile != "linux-worker" || childSummary.Name != "Remote Worker" || !childSummary.Messaging {
		t.Fatalf("unexpected remote child summary: %+v", childSummary)
	}

	// Verify the child session exists on b with cross-node parent metadata and env vars.
	child, ok := b.sessions.get(childSummary.ID)
	if !ok {
		t.Fatalf("child session %s not found on node b", childSummary.ID)
	}
	childPeer := b.projectAgents.peer(child.ID())
	if childPeer == nil {
		t.Fatal("remote child has no MCP peer on node b")
	}
	b.sessionMu.Lock()
	saved := b.saved[child.ID()]
	b.sessionMu.Unlock()
	if saved.ParentID != parent.ID() || saved.ParentNodeID != a.config().NodeID {
		t.Fatalf("saved parent metadata mismatch: %+v", saved)
	}
	if childPeer.parent != parent.ID() || childPeer.parentNode != a.config().NodeID {
		t.Fatalf("remote child peer missing parent metadata: parent=%q parentNode=%q", childPeer.parent, childPeer.parentNode)
	}
	if instr := b.agentInstructions(childPeer); !strings.Contains(instr, "on spawn-a") {
		t.Fatalf("remote child instructions missing parent node: %q", instr)
	}

	// Immediate two-way messaging right after spawn (before waiting for NodeState gossip).
	sentToChild, err := a.sendAgentMessage(ctx, parentPeer, child.ID(), "Also run race detector")
	if err != nil {
		t.Fatalf("immediate parent->remote-child send failed: %v", err)
	}
	childInbox, _ := b.projectAgents.messages.pending(child.ID())
	if len(childInbox) != 1 || childInbox[0].ID != sentToChild.ID || childInbox[0].From != parent.ID() {
		t.Fatalf("unexpected child inbox: %+v", childInbox)
	}
	if prompt := b.agentPrompt(childInbox); !strings.Contains(prompt, "on spawn-a") {
		t.Fatalf("child prompt did not identify parent's node: %q", prompt)
	}

	sentToParent, err := b.sendAgentMessage(ctx, childPeer, parent.ID(), "Linux tests passed")
	if err != nil {
		t.Fatalf("immediate remote-child->parent send failed: %v", err)
	}
	parentInbox, _ := a.projectAgents.messages.pending(parent.ID())
	if len(parentInbox) != 1 || parentInbox[0].ID != sentToParent.ID || parentInbox[0].From != child.ID() {
		t.Fatalf("unexpected parent inbox: %+v", parentInbox)
	}
	if prompt := a.agentPrompt(parentInbox); !strings.Contains(prompt, "Remote Worker") || !strings.Contains(prompt, "on spawn-b") {
		t.Fatalf("parent prompt did not identify child name & node: %q", prompt)
	}

	// Wait for gossip so parent activity tracking, ReadProjectAgents, and watch_agent observe live state.
	child.SetStatus(protocol.AgentWorking)
	parent.SetStatus(protocol.AgentDone)
	waitFor(t, "remote child keeps parent working", func() bool {
		return parent.Info().GetStatus() == protocol.AgentWorking
	})

	// ReadProjectAgents on a shows the remote child with ParentSessionId set.
	mon, err := a.readProjectAgents(&protocol.ReadProjectAgents{Path: dirA})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(mon.GetAgents(), func(pa *protocol.ProjectAgent) bool {
		return pa.GetSession().GetId() == child.ID() && pa.GetParentSessionId() == parent.ID() && pa.GetMessaging()
	}) {
		t.Fatalf("ReadProjectAgents missing remote child parent linkage: %+v", mon.GetAgents())
	}

	// watch_agent + mako-msg wait --all-children observe remote child finishing its turn.
	watchRes, err := a.watchProjectAgent(parentPeer, watchAgentArgs{SessionID: child.ID()})
	if err != nil || watchRes.WatchID == "" || watchRes.AlreadyMatched {
		t.Fatalf("watch remote child failed: %+v %v", watchRes, err)
	}
	cliWaitDone := make(chan agentCLIResponse, 1)
	go func() {
		cliWaitDone <- a.cliWait(ctx, parentPeer, agentCLIRequest{
			WatchChildren: true,
			AfterID:       sentToParent.ID,
			TimeoutSec:    10,
		})
	}()
	time.AfterFunc(300*time.Millisecond, func() { child.SetStatus(protocol.AgentDone) })

	waitForWithin(t, 10*time.Second, "watch notification for remote child", func() bool {
		inbox, _ := a.projectAgents.messages.pending(parent.ID())
		for _, m := range inbox {
			if m.Note && m.From == child.ID() && strings.Contains(m.Text, watchRes.WatchID) && strings.Contains(m.Text, "turn ended") {
				return true
			}
		}
		return false
	})
	select {
	case resp := <-cliWaitDone:
		if !resp.OK || (resp.Reason != "turn_ended" && resp.Reason != "message") {
			t.Fatalf("cliWait --all-children unexpected response: %+v", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cliWait --all-children did not wake on remote child completion")
	}
	waitFor(t, "finished remote child releases parent activity", func() bool {
		return parent.Info().GetStatus() == protocol.AgentDone
	})

	// A sibling cannot stop a remote child it did not spawn; the parent can.
	if _, err := a.stopProjectAgent(ctx, siblingPeer, child.ID()); err == nil {
		t.Fatal("sibling stopped another agent's remote child")
	}
	stopped, err := a.stopProjectAgent(ctx, parentPeer, child.ID())
	if err != nil || !stopped.Stopped {
		t.Fatalf("parent failed to stop remote child: %+v %v", stopped, err)
	}
	if _, stillRunning := b.sessions.get(child.ID()); stillRunning {
		t.Fatal("remote child still registered on b after stop_agent")
	}
}

// In clones of one git repository (with no explicit workspace project), an
// agent on one node can spawn a child on another node that holds a clone and
// communicate with it bidirectionally.
func TestSpawnAndCommunicateAcrossNodesInRepoClones(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("MAKO_HOME", t.TempDir())
	initClone := func(remote string) string {
		dir := t.TempDir()
		for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
		return dir
	}

	a := spawnableMeshDaemon(t, "repo-a", nil)
	b := spawnableMeshDaemon(t, "repo-b", nil, p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)

	dirA := initClone("git@github.com:acme/shared-app.git")
	dirB := initClone("https://github.com/acme/shared-app.git")

	parent, err := a.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: dirA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: dirB}); err != nil {
		t.Fatal(err)
	}
	parentPeer := a.projectAgents.peer(parent.ID())
	scope := a.projectScope(parent.Info())
	if !strings.HasPrefix(scope, repoScopePrefix) {
		t.Fatalf("expected repo scope, got %q", scope)
	}

	waitFor(t, "repo clone discovery across nodes", func() bool {
		if b.remoteAgent(scope, parent.ID()) == nil {
			return false
		}
		listed, err := a.listProjectAgents(parentPeer)
		if err != nil {
			return false
		}
		return slices.ContainsFunc(listed.Nodes, func(n projectNode) bool {
			return n.Name == "repo-b" && n.Spawnable && len(n.Folders) > 0
		})
	})

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	childSummary, err := a.spawnProjectAgent(ctx, parentPeer, spawnAgentArgs{
		Node:   "repo-b",
		Name:   "Clone Worker",
		Prompt: "Verify clone build",
	})
	if err != nil {
		t.Fatalf("repo clone remote spawn failed: %v", err)
	}
	if childSummary.Node != "repo-b" || !childSummary.Messaging {
		t.Fatalf("unexpected repo clone child: %+v", childSummary)
	}
	childPeer := b.projectAgents.peer(childSummary.ID)
	if childPeer == nil {
		t.Fatal("child peer missing on repo-b")
	}
	if _, err := b.sendAgentMessage(ctx, childPeer, parent.ID(), "Clone build green"); err != nil {
		t.Fatalf("child->parent message in repo clone failed: %v", err)
	}
	if inbox, _ := a.projectAgents.messages.pending(parent.ID()); len(inbox) != 1 || inbox[0].Text != "Clone build green" {
		t.Fatalf("parent inbox: %+v", inbox)
	}
	if _, err := a.stopProjectAgent(ctx, parentPeer, childSummary.ID); err != nil {
		t.Fatalf("stop remote repo child failed: %v", err)
	}
}
