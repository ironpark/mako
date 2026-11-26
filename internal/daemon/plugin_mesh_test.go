package daemon

import (
	"log/slog"
	"testing"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
)

// TestPluginAgentsParentSessionAndOwnership verifies that a plugin-started agent
// with an explicit ParentSessionID records that parent session in savedSession.ParentID,
// while still tracking PluginID for Running/Stop ownership checks and JSON round-tripping.
func TestPluginAgentsParentSessionAndOwnership(t *testing.T) {
	d, parent := projectMCPDaemon(t)
	a := pluginAgents{d}

	child, err := a.Start(t.Context(), "agent-map", plugin.AgentStart{
		Agent:           "claude",
		Root:            t.TempDir(),
		Prompt:          "inspect subagent topology",
		Name:            "Subagent Child",
		ParentSessionID: parent.ID(),
	})
	if err != nil {
		t.Fatalf("Start subagent: %v", err)
	}

	d.sessionMu.Lock()
	saved := d.saved[child]
	d.sessionMu.Unlock()
	if saved.ParentID != parent.ID() {
		t.Fatalf("saved.ParentID = %q, want %q", saved.ParentID, parent.ID())
	}
	if saved.PluginID != "agent-map" {
		t.Fatalf("saved.PluginID = %q, want %q", saved.PluginID, "agent-map")
	}

	// Verify listSessions() exposes the parent session ID on SessionInfo.
	var found *protocol.SessionInfo
	for _, s := range d.listSessions() {
		if s.GetId() == child {
			found = s
			break
		}
	}
	if found == nil || found.GetParentSessionId() != parent.ID() {
		t.Fatalf("listSessions() child = %+v, want ParentSessionId=%q", found, parent.ID())
	}

	// Ownership checks: owning plugin counts and can stop; other plugins cannot.
	if got := a.Running("agent-map"); got != 1 {
		t.Fatalf("Running(agent-map) = %d, want 1", got)
	}
	if got := a.Running("other"); got != 0 {
		t.Fatalf("Running(other) = %d, want 0", got)
	}
	if err := a.Stop("other", child); err == nil {
		t.Fatal("other plugin unexpectedly stopped child session")
	}

	// JSON round-trip preserves PluginID & ParentID.
	raw, err := saved.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var roundTrip savedSession
	if err := roundTrip.UnmarshalJSON(raw); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if roundTrip.PluginID != "agent-map" || roundTrip.ParentID != parent.ID() {
		t.Fatalf("roundTrip = %+v, want PluginID=agent-map ParentID=%s", roundTrip, parent.ID())
	}

	if err := a.Stop("agent-map", child); err != nil {
		t.Fatalf("Stop(agent-map): %v", err)
	}
	if got := a.Running("agent-map"); got != 0 {
		t.Fatalf("Running(agent-map) after stop = %d, want 0", got)
	}
}

// TestDaemonPluginMeshSessionsDedup verifies pluginMeshSessions includes local
// sessions and deduplicates by ID.
func TestDaemonPluginMeshSessionsDedup(t *testing.T) {
	d, s := projectMCPDaemon(t)
	got := d.pluginMeshSessions()
	count := 0
	for _, item := range got {
		if item.GetId() == s.ID() {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected session %q once in pluginMeshSessions, got %d (%+v)", s.ID(), count, got)
	}
}

// TestPluginAgentsOwnershipPreservedOnAttach verifies that resuming a saved
// plugin-started subagent via attachSession (startSessionWithParent with existed == true)
// preserves savedSession.PluginID, Running(id), and Stop ownership.
func TestPluginAgentsOwnershipPreservedOnAttach(t *testing.T) {
	d, parent := projectMCPDaemon(t)
	a := pluginAgents{d}

	childID, err := a.Start(t.Context(), "agent-map", plugin.AgentStart{
		Agent:           "claude",
		Root:            t.TempDir(),
		Prompt:          "inspect subagent topology",
		Name:            "Resumable Subagent",
		ParentSessionID: parent.ID(),
	})
	if err != nil {
		t.Fatalf("Start subagent: %v", err)
	}
	childSess, ok := d.sessions.get(childID)
	if !ok {
		t.Fatalf("started child session %q not live", childID)
	}
	waitConversation(t, childSess)

	// Simulate daemon restart (or sleep -> attach) on the same state dir and config.
	cfg := d.config()
	dir := d.dir
	d.sessions.closeAll()

	d2 := New(cfg, slog.New(slog.DiscardHandler))
	d2.dir, d2.ctx, d2.mcp, d2.hookBinary = dir, t.Context(), nil, "/bin/makod"
	if err := d2.loadSessions(dir); err != nil {
		t.Fatalf("loadSessions: %v", err)
	}
	t.Cleanup(d2.sessions.closeAll)

	if _, err := d2.attachSession(t.Context(), childID); err != nil {
		t.Fatalf("attachSession(%q): %v", childID, err)
	}

	d2.sessionMu.Lock()
	afterAttach := d2.saved[childID]
	d2.sessionMu.Unlock()
	if afterAttach.PluginID != "agent-map" {
		t.Fatalf("after attach saved.PluginID = %q, want %q", afterAttach.PluginID, "agent-map")
	}
	if afterAttach.ParentID != parent.ID() {
		t.Fatalf("after attach saved.ParentID = %q, want %q", afterAttach.ParentID, parent.ID())
	}

	a2 := pluginAgents{d2}
	if got := a2.Running("agent-map"); got != 1 {
		t.Fatalf("Running(agent-map) after attach = %d, want 1", got)
	}
	if got := a2.Running("other"); got != 0 {
		t.Fatalf("Running(other) after attach = %d, want 0", got)
	}
	if err := a2.Stop("other", childID); err == nil {
		t.Fatal("other plugin unexpectedly stopped re-attached child session")
	}
	if err := a2.Stop("agent-map", childID); err != nil {
		t.Fatalf("Stop(agent-map) after attach: %v", err)
	}
	if got := a2.Running("agent-map"); got != 0 {
		t.Fatalf("Running(agent-map) after stop = %d, want 0", got)
	}
}
