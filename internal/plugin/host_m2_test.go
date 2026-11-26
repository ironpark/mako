package plugin

import (
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestHostSessionEnrichmentM2 verifies F2.1 fields on hostapi.Session:
// parentSessionId, parentNodeId, branch, profile, approvalTool, startedAt,
// and lastActivityAt (including zero-timestamp handling without NaN).
func TestHostSessionEnrichmentM2(t *testing.T) {
	started := timestamppb.New(time.UnixMilli(1700000001000))
	statusAt := timestamppb.New(time.UnixMilli(1700000004000))
	lastOutput := timestamppb.New(time.UnixMilli(1700000008000))
	lastMessage := timestamppb.New(time.UnixMilli(1700000012345))

	sessions := []*protocol.SessionInfo{
		{
			Id:              "child-1",
			Name:            "Child Worker",
			Agent:           "claude",
			Project:         "/work/mako",
			State:           protocol.StateRunning,
			Status:          protocol.AgentWaiting,
			NodeId:          "node-b",
			ParentSessionId: "root-1",
			ParentNodeId:    "node-a",
			Branch:          "feat/m2",
			Profile:         "architect",
			Approval:        &protocol.Approval{Id: "a1", Tool: "Write"},
			StartedAt:       started,
			StatusAt:        statusAt,
			LastOutput:      lastOutput,
			LastMessage:     lastMessage,
		},
		{
			Id:    "empty-ts",
			Name:  "Minimal",
			Agent: "shell",
			State: protocol.StateStopped,
		},
	}

	r := newRig(t, testLimits, nil, sessions...)
	r.write("m2-sess", map[string]string{
		ManifestName: manifest("m2-sess", []string{"sessions:read"}, ""),
		"main.js": `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const list = await mako.sessions.listSessions();
        const c = list.sessions.find((x) => x.id === "child-1");
        const e = list.sessions.find((x) => x.id === "empty-ts");
        s.out = JSON.stringify({
          parentSessionId: c.parentSessionId,
          parentNodeId: c.parentNodeId,
          branch: c.branch,
          profile: c.profile,
          approvalTool: c.approvalTool,
          startedAt: c.startedAt,
          lastActivityAt: c.lastActivityAt,
          zeroStarted: e.startedAt === 0 && !Number.isNaN(e.startedAt),
          zeroLast: e.lastActivityAt === 0 && !Number.isNaN(e.lastActivityAt),
          emptyApproval: e.approvalTool === "",
        });
      },
      render: (s, ui) => ui.text(s.out),
    });
  },
};`,
	})
	r.state("m2-sess", protocol.PluginWaiting)
	r.grant("m2-sess")

	w := r.watch("plugin:m2-sess/ui")
	w.until("enriched session fields", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, `"parentSessionId":"root-1"`) &&
			hasText(n, `"parentNodeId":"node-a"`) &&
			hasText(n, `"branch":"feat/m2"`) &&
			hasText(n, `"profile":"architect"`) &&
			hasText(n, `"approvalTool":"Write"`) &&
			hasText(n, `"startedAt":1700000001000`) &&
			hasText(n, `"lastActivityAt":1700000012345`) &&
			hasText(n, `"zeroStarted":true`) &&
			hasText(n, `"zeroLast":true`) &&
			hasText(n, `"emptyApproval":true`)
	})
}

// TestHostMeshServiceAndOpenSessionM2 verifies F2.2 & F2.3 in internal/plugin:
// mesh.listNodes(), mesh.listSessions(), permission check, remote openSession
// fallback to MeshSessions, and StartAgentRequest.parentSessionId propagation.
func TestHostMeshServiceAndOpenSessionM2(t *testing.T) {
	agents := &fakeAgents{}
	nodes := []*protocol.Node{
		{
			Id: "node-local", Name: "mac-studio", Os: "darwin", Arch: "arm64",
			Source: "self", Link: protocol.LinkUp, Trusted: true,
			Projects: []*protocol.ProjectScope{{Id: "p1", Name: "Mako"}},
		},
		{
			Id: "node-remote", Name: "linux-box", Os: "linux", Arch: "amd64",
			Link: protocol.LinkUp, Trusted: true,
			Projects: []*protocol.ProjectScope{{Id: "p2", Name: "Worker"}},
		},
	}
	localSessions := []*protocol.SessionInfo{
		{Id: "s-local", Name: "Local", Agent: "claude", Project: "/proj", State: protocol.StateRunning, NodeId: "node-local"},
	}
	meshSessions := []*protocol.SessionInfo{
		localSessions[0],
		{Id: "s-remote", Name: "Remote", Agent: "codex", Project: "/remote", State: protocol.StateRunning, NodeId: "node-remote"},
	}
	r := startRig(t, Config{
		Limits:       testLimits,
		NodeID:       "node-local",
		Sessions:     func() []*protocol.SessionInfo { return localSessions },
		Nodes:        func() []*protocol.Node { return nodes },
		MeshSessions: func() []*protocol.SessionInfo { return meshSessions },
		Projects:     func() []ProjectRoots { return []ProjectRoots{{ID: "p1", Name: "Mako", Roots: []string{"/proj"}}} },
		Agents:       agents,
	})

	r.write("m2-mesh", map[string]string{
		ManifestName: manifest("m2-mesh", []string{"sessions:read", "sessions:write"}, ""),
		"main.js": `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { out: "" },
      async load(s) {
        const nr = await mako.mesh.listNodes();
        const sr = await mako.mesh.listSessions();
        s.out = JSON.stringify({
          nodes: nr.nodes.map((n) => ({ id: n.id, self: n.self, os: n.os, proj: n.projects.length })),
          sessions: sr.sessions.map((x) => x.id),
        });
      },
      render: (s, ui) => ui.column([
        ui.text(s.out),
        ui.button("OpenRemote", { key: "open-rem", onTap: ["openRem"] }),
        ui.button("SpawnChild", { key: "spawn-child", onTap: ["spawnChild"] }),
      ]),
      on: {
        async openRem() {
          const ctx = mako.blocks.context("ui");
          await ctx.ui.openSession("s-remote");
        },
        async spawnChild() {
          await mako.messages.startAgent({
            agent: "claude",
            project: "p1",
            root: "/proj",
            prompt: "investigate mesh",
            parentSessionId: "s-local",
          });
        },
      },
    });
  },
};`,
	})
	r.state("m2-mesh", protocol.PluginWaiting)
	r.grant("m2-mesh")

	w := r.watch("plugin:m2-mesh/ui")
	tree := w.until("mesh snapshot", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, `"id":"node-local"`) &&
			hasText(n, `"id":"node-remote"`) &&
			hasText(n, `"s-remote"`)
	})

	// Open remote session (only in MeshSessions, not local Sessions) -> preserves node-remote.
	openBtn := findBySuffix(tree, "open-rem")
	done, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:m2-mesh/ui", Node: openBtn.GetId(), Kind: protocol.UiTap,
	})
	if err != nil {
		t.Fatalf("open remote session: %v", err)
	}
	if len(done.GetOpen()) != 1 || done.GetOpen()[0].GetKey() != "s-remote" || done.GetOpen()[0].GetNode() != "node-remote" {
		t.Fatalf("unexpected openSession intent: %+v", done.GetOpen())
	}

	// Spawn child agent with parentSessionId -> forwarded to AgentHost.Start after AllowAgent.
	spawnBtn := findBySuffix(tree, "spawn-child")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:m2-mesh/ui", Node: spawnBtn.GetId(), Kind: protocol.UiTap,
	}); err != nil {
		t.Fatalf("spawn child tap: %v", err)
	}
	info := r.info("m2-mesh", func(p *protocol.PluginInfo) bool { return p.GetAgentAsk() != nil })
	if _, err := r.act(&protocol.PluginAction{
		PluginId: "m2-mesh",
		Op:       protocol.PluginAllowAgent,
		Key:      info.GetAgentAsk().GetId(),
		Value:    protocol.AgentOnce,
	}); err != nil {
		t.Fatalf("allow agent: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for agents.starts() == 0 && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
	}
	agents.mu.Lock()
	defer agents.mu.Unlock()
	if len(agents.started) != 1 || agents.started[0].ParentSessionID != "s-local" {
		t.Fatalf("started = %+v, want ParentSessionID=s-local", agents.started)
	}
}

// TestHostStartAgentRejectsSpoofedParentSessionID verifies invalid parentSessionId values are refused.
func TestHostStartAgentRejectsSpoofedParentSessionID(t *testing.T) {
	agents := &fakeAgents{}
	r := agentsRig(t, agents)
	r.write("m2-bad-parent", map[string]string{
		ManifestName: manifest("m2-bad-parent", []string{"sessions:write"}, ""),
		"main.js": `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", {
      state: { err: "" },
      async load(s) {
        try {
          await mako.messages.startAgent({
            agent: "claude",
            project: "p1",
            root: "/proj",
            prompt: "hi",
            parentSessionId: "plugin:spoof",
          });
          s.err = "unexpected-ok";
        } catch (e) {
          s.err = "rejected:" + e.message;
        }
      },
      render: (s, ui) => ui.text(s.err),
    });
  },
};`,
	})
	r.state("m2-bad-parent", protocol.PluginWaiting)
	r.grant("m2-bad-parent")

	w := r.watch("plugin:m2-bad-parent/ui")
	w.until("invalid parent rejected", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		for _, line := range texts(n) {
			if strings.Contains(line, "rejected:") && strings.Contains(line, "parentSessionId") {
				return true
			}
		}
		return false
	})
}
