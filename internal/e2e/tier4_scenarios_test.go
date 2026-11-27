//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
)

type t4Event struct {
	ID    string
	Kind  string
	Value string
}

type t4EventSink struct {
	mu     sync.Mutex
	events []t4Event
}

func (s *t4EventSink) Record(id, kind, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, t4Event{ID: id, Kind: kind, Value: value})
}

func (s *t4EventSink) Snapshot() []t4Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

func (s *t4EventSink) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = nil
}

func t4Center(r ggui.Rect) ggui.Point {
	return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
}

func t4HasGraphNode(n *protocol.UiNode, id string) bool {
	if n == nil {
		return false
	}
	for _, gn := range n.GetGraphNodes() {
		if gn.GetId() == id {
			return true
		}
	}
	return false
}

func t4FindGraphNode(n *protocol.UiNode, id string) *protocol.UiGraphNode {
	if n == nil {
		return nil
	}
	for _, gn := range n.GetGraphNodes() {
		if gn.GetId() == id {
			return gn
		}
	}
	return nil
}

func t4HasEdge(n *protocol.UiNode, from, to string) bool {
	if n == nil {
		return false
	}
	for _, ge := range n.GetGraphEdges() {
		if ge.GetFrom() == from && ge.GetTo() == to {
			return true
		}
	}
	return false
}

func t4NewUiTreeProbe(tree *protocol.UiNode, onEvent func(id, kind, value string), size ggui.Size) *ggui.Probe {
	i18n.Set("en")
	return gui.NewUiTreeProbe(tree, onEvent, size)
}

func t4WaitFindButton(t *testing.T, p *ggui.Probe, label string) ggui.SemNode {
	t.Helper()
	var found ggui.SemNode
	WaitFor(t, 5*time.Second, fmt.Sprintf("button %q", label), func() bool {
		p.Frame()
		var ok bool
		found, ok = p.Semantics().Find(ggui.RoleButton, label)
		return ok
	})
	return found
}

func t4InstallJSBlock(t *testing.T, rig *PluginRig, id, perms, blockID, js string) *TreeWatcher {
	t.Helper()
	toml := fmt.Sprintf(`id = %q
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = [%s]

[[contributes.block]]
id = %q
title = %q
slots = ["sidebar"]
`, id, perms, blockID, id)
	rig.InstallJS(t, id, toml, js)
	rig.Grant(t, id)
	return rig.WatchTree(t, fmt.Sprintf("plugin:%s/%s", id, blockID), "", plugin.View{Standalone: true})
}

// Scenario 1: Multi-Machine Agent Topology
// Two peered DaemonRig mesh nodes with projects, parent agent, and subagent
// (via parent_session_id) rendered through real examples/plugins/agent-map and
// gui.NewUiTreeProbe.
func TestScenario_01_MultiMachineAgentTopology(t *testing.T) {
	nodeMac := NewDaemonRig(t, DaemonRigOpts{Name: "mac-studio", EnableP2P: true})
	nodeLinux := NewDaemonRig(t, DaemonRigOpts{
		Name:      "linux-builder",
		EnableP2P: true,
		Peers:     []string{nodeMac.PeerAddr()},
	})
	nodeMac.LetIn(t, nodeLinux)
	nodeLinux.LetIn(t, nodeMac)

	WaitFor(t, defaultWaitTimeout, "mesh peer link up", func() bool {
		for _, n := range nodeMac.Nodes(t) {
			if n.GetId() == nodeLinux.Daemon.Self().GetId() && n.GetLink() == "up" {
				return true
			}
		}
		return false
	})

	projMac := filepath.Join(t.TempDir(), "mako-core")
	projLinux := filepath.Join(t.TempDir(), "mako-runner")
	if err := os.MkdirAll(projMac, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projLinux, 0o755); err != nil {
		t.Fatal(err)
	}

	leadSess := nodeMac.OpenShellSession(t, projMac)
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	subSess, err := nodeMac.Client.Open(ctx, &protocol.OpenSession{
		Agent:           "shell",
		Project:         projMac,
		ParentSessionId: leadSess.GetId(),
		Name:            "sub-verifier",
		Cols:            80,
		Rows:            24,
	})
	if err != nil {
		t.Fatalf("open child session: %v", err)
	}
	t.Cleanup(func() {
		kctx, kcancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer kcancel()
		_ = nodeMac.Client.Kill(kctx, &protocol.CloseSession{SessionId: subSess.GetId()})
	})
	remoteSess := nodeLinux.OpenShellSession(t, projLinux)

	nodeMac.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	nodeMac.Grant(t, "agent-map")

	w := nodeMac.WatchBlockView(t, "plugin:agent-map/map", "", "en", true)
	tree := w.WaitFor(t, 15*time.Second, func(n *protocol.UiNode) bool {
		gn := FindNodeByKind(n, "graph")
		return gn != nil &&
			t4HasGraphNode(gn, "sess:"+leadSess.GetId()) &&
			t4HasGraphNode(gn, "sess:"+subSess.GetId()) &&
			t4HasGraphNode(gn, "sess:"+remoteSess.GetId())
	})

	gn := FindNodeByKind(tree, "graph")
	if !t4HasEdge(gn, "sess:"+leadSess.GetId(), "sess:"+subSess.GetId()) {
		t.Fatalf("missing parent->subagent edge %s -> %s in graph edges: %+v", leadSess.GetId(), subSess.GetId(), gn.GetGraphEdges())
	}

	p := t4NewUiTreeProbe(tree, nil, ggui.Sz(1080, 720))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	for _, want := range []string{"mac-studio", "linux-builder", "sub-verifier"} {
		if !strings.Contains(sem, want) {
			t.Fatalf("missing %q in multi-machine topology semantics:\n%s", want, sem)
		}
	}
}

// Scenario 2: Live Approval Waiting & Subagent Spawn Context Menu Flow
// Waiting session with approval_tool shows amber beacon + right-click context
// menu -> spawns subagent with parentSessionId -> sends message -> jumps to session.
func TestScenario_02_LiveApprovalWaitingAndSubagentSpawnContextMenu(t *testing.T) {
	root := t.TempDir()
	rig := NewPluginRig(t, PluginRigOpts{
		Projects: []plugin.ProjectRoots{{ID: "proj-app", Name: "Mako", Roots: []string{root}}},
		Sessions: []*protocol.SessionInfo{{
			Id:       "lead-1",
			Name:     "Approval Lead",
			Agent:    "claude",
			Profile:  "opus",
			Branch:   "feat/e2e",
			Project:  root,
			State:    protocol.StateRunning,
			Status:   "waiting",
			Approval: &protocol.Approval{Id: "apr-9", Tool: "Bash"},
		}},
	})

	w := t4InstallJSBlock(t, rig, "t4-approval-flow", `"sessions:read", "sessions:write", "fs:project"`, "map", fmt.Sprintf(`
export default {
  activate(mako) {
    mako.blocks.register("map", {
      state: { sessions: [], spawned: "" },
      async load(state) {
        state.sessions = (await mako.sessions.listSessions()).sessions || [];
      },
      render(state, ui) {
        const nodes = state.sessions.map((s) => ({
          id: "sess:" + s.id,
          label: s.name,
          shape: "card",
          icon: s.approvalTool ? "shield-alert" : "bot",
          status: s.status,
          tone: s.approvalTool ? "warning" : "accent",
          subtitle: s.approvalTool ? ("Approval: " + s.approvalTool) : s.agent,
          badges: s.approvalTool ? [{ text: s.approvalTool, tone: "warning" }] : [],
          contextMenu: "sess-menu",
          contextArg: s.id,
          x: 0,
          y: 0,
          hasPos: true
        }));
        return ui.column([
          ui.contextMenu("sess-menu", [
            ui.menuItem("세션 열기", { icon: "external-link", onTap: ["openTarget"] }),
            ui.menuItem("메시지 보내기", { icon: "message-square", onTap: ["pingTarget"] }),
            ui.menuItem("서브에이전트 실행", { icon: "git-branch", onTap: ["spawnChild"] })
          ]),
          ui.graph(nodes, [], {
            key: "topo",
            variant: "cards",
            fill: true,
            onDoubleTap: ["openTarget"]
          }),
          ui.text("spawned:" + state.spawned)
        ], { fill: true });
      },
      on: {
        async openTarget(_state, sid) {
          const ctx = mako.blocks.context("map");
          await ctx.ui.openSession(String(sid).replace(/^sess:/, ""));
        },
        async pingTarget(_state, sid) {
          await mako.messages.sendMessage({ sessionId: String(sid), text: "approved bash check" });
        },
        async spawnChild(state, sid) {
          const res = await mako.messages.startAgent({
            agent: "claude",
            project: "proj-app",
            root: %q,
            prompt: "Verify approval branch",
            name: "Verifier Sub",
            parentSessionId: String(sid)
          });
          state.spawned = res.requestId || "ok";
        }
      }
    });
  }
};
`, root))

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		gn := FindNodeByKind(n, "graph")
		return gn != nil && t4HasGraphNode(gn, "sess:lead-1")
	})
	nd := t4FindGraphNode(FindNodeByKind(tree, "graph"), "sess:lead-1")
	if nd.GetStatus() != "waiting" || len(nd.GetBadges()) == 0 {
		t.Fatalf("expected waiting node with approval badge, got %+v", nd)
	}

	var sink t4EventSink
	p := t4NewUiTreeProbe(tree, sink.Record, ggui.Sz(840, 560))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	leadBtn := t4WaitFindButton(t, p, "Approval Lead")
	p.ClickButton(t4Center(leadBtn.Rect), ggui.MouseButtonRight)
	p.Frame()

	var spawnItem ggui.SemNode
	WaitFor(t, 3*time.Second, "context menu 서브에이전트 실행", func() bool {
		p.Frame()
		var ok bool
		spawnItem, ok = p.Semantics().Find(ggui.RoleMenuItem, "서브에이전트 실행")
		return ok
	})
	p.Click(t4Center(spawnItem.Rect))
	p.Frame()

	evs := sink.Snapshot()
	if len(evs) != 1 || evs[0].Kind != protocol.UiTap || evs[0].Value != "lead-1" {
		t.Fatalf("context menu spawn event = %+v, want UiTap lead-1", evs)
	}

	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t4-approval-flow/map",
		Node:  evs[0].ID,
		Kind:  protocol.UiTap,
		Value: "lead-1",
	}); err != nil {
		t.Fatalf("SendEvent spawnChild: %v", err)
	}

	info := rig.WaitPlugin(t, "t4-approval-flow", func(pi *protocol.PluginInfo) bool {
		return pi.GetAgentAsk() != nil
	})
	if _, err := rig.Act(t, &protocol.PluginAction{
		PluginId: "t4-approval-flow",
		Op:       protocol.PluginAllowAgent,
		Key:      info.GetAgentAsk().GetId(),
		Value:    protocol.AgentAlways,
	}); err != nil {
		t.Fatalf("PluginAllowAgent: %v", err)
	}

	WaitFor(t, 10*time.Second, "StubAgentHost.Start recorded", func() bool {
		return len(rig.Agents.Started()) == 1
	})

	msgItem := FindNodeByText(tree, "메시지 보내기")
	if msgItem == nil {
		t.Fatal("missing 메시지 보내기 menu item")
	}
	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t4-approval-flow/map",
		Node:  msgItem.GetId(),
		Kind:  protocol.UiTap,
		Value: "lead-1",
	}); err != nil {
		t.Fatalf("SendEvent pingTarget: %v", err)
	}
	if sent := rig.Agents.Sent(); len(sent) != 1 || sent[0].Session != "lead-1" {
		t.Fatalf("expected Sent to lead-1, got %+v", sent)
	}

	gNode := FindNodeByKind(tree, "graph")
	done, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t4-approval-flow/map",
		Node:  gNode.GetId(),
		Kind:  protocol.UiDoubleTap,
		Value: "sess:lead-1",
	})
	if err != nil {
		t.Fatalf("SendEvent UiDoubleTap: %v", err)
	}
	if len(done.GetOpen()) != 1 || done.GetOpen()[0].GetKind() != "session" || done.GetOpen()[0].GetKey() != "lead-1" {
		t.Fatalf("expected session jump intent for lead-1, got %+v", done.GetOpen())
	}
}

// Scenario 3: Drag-Drop Layout Persistence & Reload Survival
// Drag node -> drop saves coordinates to ctx.storage -> reload plugin/watcher
// preserves pinned coordinates.
func TestScenario_03_DragDropLayoutPersistenceAndReloadSurvival(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t4InstallJSBlock(t, rig, "t4-drag-persist", `"storage"`, "layout", `
export default {
  activate(mako) {
    mako.blocks.register("layout", {
      state: { positions: {} },
      async load(state, ctx) {
        const res = await ctx.storage.getItem("positions");
        if (res.found && res.value) {
          state.positions = JSON.parse(res.value);
        }
      },
      render(state, ui) {
        const base = [
          { id: "alpha", label: "Alpha Service", x: -100, y: 0 },
          { id: "beta", label: "Beta Service", x: 100, y: 0 }
        ];
        const nodes = base.map((item) => {
          const saved = state.positions[item.id];
          return {
            id: item.id,
            label: item.label,
            shape: "card",
            x: saved ? saved.x : item.x,
            y: saved ? saved.y : item.y,
            hasPos: true,
            pinned: Boolean(saved)
          };
        });
        return ui.column([
          ui.graph(nodes, [{ from: "alpha", to: "beta" }], {
            key: "g",
            variant: "cards",
            fill: true,
            onDrop: ["persistDrop"]
          })
        ], { fill: true });
      },
      on: {
        async persistDrop(state, pos) {
          const ctx = mako.blocks.context("layout");
          state.positions[pos.id] = { x: pos.x, y: pos.y };
          await ctx.storage.setItem("positions", JSON.stringify(state.positions));
        }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	var sink t4EventSink
	p := t4NewUiTreeProbe(tree, sink.Record, ggui.Sz(840, 560))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	alphaBtn := t4WaitFindButton(t, p, "Alpha Service")
	start := t4Center(alphaBtn.Rect)
	end := ggui.Pt(start.X+90, start.Y+60)
	p.Press(start)
	p.Frame()
	p.Move(ggui.Pt(start.X+45, start.Y+30))
	p.Frame()
	p.Move(end)
	p.Frame()
	p.Release(end)
	p.Frame()

	evs := sink.Snapshot()
	if len(evs) != 1 || evs[0].Kind != protocol.UiDrop {
		t.Fatalf("expected single UiDrop event after node drag, got %+v", evs)
	}

	var dropPayload struct {
		ID string  `json:"id"`
		X  float64 `json:"x"`
		Y  float64 `json:"y"`
	}
	if err := json.Unmarshal([]byte(evs[0].Value), &dropPayload); err != nil {
		t.Fatalf("unmarshal drop payload: %v", err)
	}
	if dropPayload.ID != "alpha" {
		t.Fatalf("drop payload id = %q, want alpha", dropPayload.ID)
	}

	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t4-drag-persist/layout",
		Node:  evs[0].ID,
		Kind:  protocol.UiDrop,
		Value: evs[0].Value,
	}); err != nil {
		t.Fatalf("SendEvent UiDrop: %v", err)
	}

	w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		nd := t4FindGraphNode(FindNodeByKind(n, "graph"), "alpha")
		return nd != nil && nd.GetPinned() && nd.GetX() == dropPayload.X && nd.GetY() == dropPayload.Y
	})

	if _, err := rig.Act(t, &protocol.PluginAction{
		PluginId: "t4-drag-persist",
		Op:       protocol.PluginReload,
	}); err != nil {
		t.Fatalf("PluginReload: %v", err)
	}
	wAfter := rig.WatchTree(t, "plugin:t4-drag-persist/layout", "", plugin.View{Standalone: true})
	reloaded := wAfter.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		nd := t4FindGraphNode(FindNodeByKind(n, "graph"), "alpha")
		return nd != nil && nd.GetPinned() && nd.GetX() == dropPayload.X && nd.GetY() == dropPayload.Y
	})
	if nd := t4FindGraphNode(FindNodeByKind(reloaded, "graph"), "alpha"); nd == nil || !nd.GetHasPos() {
		t.Fatalf("persisted node coordinates lost after reload: %+v", nd)
	}
}

// Scenario 4: Keyboard-Only Topology Navigation & Workspace Return
// DaemonRig + AppProbe: OpenSidebarPage -> crossfade -> Arrow navigation ->
// Enter opens session -> reopen -> Esc returns to workspace.
func TestScenario_04_KeyboardOnlyTopologyNavigationAndWorkspaceReturn(t *testing.T) {
	dr := NewDaemonRig(t, DaemonRigOpts{Name: "t4-kbd-nav"})
	s1 := dr.OpenShellSession(t, t.TempDir())
	s2 := dr.OpenShellSession(t, t.TempDir())

	dr.InstallPlugin(t, "t4-kbd-map", `id = "t4-kbd-map"
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = ["sessions:read"]

[[contributes.block]]
id = "map"
title = "Keyboard Map"
icon = "network"
slots = ["sidebar"]
`, fmt.Sprintf(`
export default {
  activate(mako) {
    mako.blocks.register("map", {
      state: { selected: "sess:%s" },
      render(state, ui) {
        return ui.column([
          ui.graph([
            { id: "sess:%s", label: "First Session", shape: "card", x: -120, y: 0, hasPos: true },
            { id: "sess:%s", label: "Second Session", shape: "card", x: 120, y: 0, hasPos: true }
          ], [
            { from: "sess:%s", to: "sess:%s" }
          ], {
            key: "g",
            variant: "cards",
            value: state.selected,
            fill: true,
            onChange: ["sel"],
            onDoubleTap: ["open"]
          })
        ], { fill: true });
      },
      on: {
        sel(state, id) { state.selected = String(id || ""); },
        async open(_state, id) {
          const ctx = mako.blocks.context("map");
          await ctx.ui.openSession(String(id).replace(/^sess:/, ""));
        }
      }
    });
  }
};
`, s1.GetId(), s1.GetId(), s2.GetId(), s1.GetId(), s2.GetId()))
	dr.Grant(t, "t4-kbd-map")

	app := gui.NewAppProbe(config.Default(), dr.Daemon.Socket(), ggui.Sz(1280, 800))
	defer app.Close()
	p := app.Probe()

	WaitFor(t, 10*time.Second, "Keyboard Map sidebar menu ready", func() bool {
		p.Frame()
		return strings.Contains(p.Semantics().String(), "Keyboard Map")
	})

	app.OpenSidebarPage("plugin:t4-kbd-map/map")
	p.Advance(180 * time.Millisecond)
	p.Frame()

	firstBtn := t4WaitFindButton(t, p, "First Session")
	p.Click(t4Center(firstBtn.Rect))
	p.Frame()

	p.Type(ggui.Mods{}, ggui.KeyArrowRight)
	p.Frame()
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	p.Advance(200 * time.Millisecond)

	WaitFor(t, 10*time.Second, "Enter opened Second Session and returned to workspace", func() bool {
		p.Frame()
		return app.SidebarPage() == "" && app.SelectedSession() == s2.GetId()
	})

	// Re-open sidebar page and verify Esc exits back to workspace.
	app.OpenSidebarPage("plugin:t4-kbd-map/map")
	p.Advance(180 * time.Millisecond)
	p.Frame()
	if app.SidebarPage() != "plugin:t4-kbd-map/map" {
		t.Fatalf("expected sidebar page active before Esc, got %q", app.SidebarPage())
	}

	p.Key("escape")
	p.Advance(180 * time.Millisecond)
	p.Frame()
	if got := app.SidebarPage(); got != "" {
		t.Fatalf("expected Esc to return to workspace, got SidebarPage=%q", got)
	}
}

// Scenario 5: Agent Turn & CI Pipeline Timeline + Heatmap Dashboard
// ui.timeline span inspection drives ui.chart heatmap/area and ui.canvas updates.
func TestScenario_05_AgentTurnAndCIPipelineTimelineHeatmapDashboard(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t4InstallJSBlock(t, rig, "t4-ci-dashboard", "", "ci", `
export default {
  activate(mako) {
    mako.blocks.register("ci", {
      state: { activeSpan: "turn-plan" },
      render(state, ui) {
        const isCI = state.activeSpan === "ci-e2e";
        return ui.column([
          ui.timeline([
            { id: "turn-plan", lane: "Architect", kind: "turn", label: "Plan Spec", tone: "accent", startMs: 0, endMs: 1400 },
            { id: "sub-gen", lane: "Worker", kind: "subagent", label: "Implement", tone: "success", startMs: 1400, endMs: 4200 },
            { id: "ci-e2e", lane: "CI Runner", kind: "ci", label: "E2E Suite", tone: "warning", startMs: 4200, endMs: 7100 }
          ], {
            key: "tl",
            caption: "Pipeline Execution",
            maxHeight: 170,
            onChange: ["inspectSpan"]
          }),
          ui.split(
            ui.chart("heatmap", [
              { name: "Architect", tone: "accent", values: isCI ? [2, 3, 1] : [9, 6, 4] },
              { name: "CI Runner", tone: "warning", values: isCI ? [10, 12, 8] : [2, 1, 2] }
            ], {
              key: "hm",
              caption: "Stage Load (" + state.activeSpan + ")",
              labels: ["00m", "05m", "10m"],
              maxHeight: 190
            }),
            ui.canvas([
              { op: "roundRect", x: 12, y: 12, w: 280, h: 120, r: 8, fillTone: isCI ? "warning" : "accent", strokeTone: "border", hitId: state.activeSpan },
              { op: "text", x: 28, y: 54, text: "Inspect: " + state.activeSpan, tone: "ink", size: "large" },
              { op: "icon", x: 240, y: 36, icon: isCI ? "flask-conical" : "sparkles", tone: "ink" }
            ], { key: "stage-canvas", width: 320, height: 160 }),
            { key: "lower", direction: "horizontal", ratio: 0.55, grow: 1 }
          )
        ], { fill: true, gap: 10 });
      },
      on: {
        inspectSpan(state, spanId) { state.activeSpan = String(spanId || "turn-plan"); }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "timeline") != nil && FindNodeByKind(n, "chart") != nil && FindNodeByKind(n, "canvas") != nil
	})
	var sink t4EventSink
	p := t4NewUiTreeProbe(tree, sink.Record, ggui.Sz(960, 620))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	tlNode := FindNodeByKind(tree, "timeline")
	spanBtn := t4WaitFindButton(t, p, "E2E Suite")
	p.Click(t4Center(spanBtn.Rect))
	p.Frame()

	evs := sink.Snapshot()
	if len(evs) != 1 || evs[0].ID != tlNode.GetId() || evs[0].Kind != protocol.UiChange || evs[0].Value != "ci-e2e" {
		t.Fatalf("expected timeline click on 'E2E Suite' to emit UiChange(ci-e2e), got %+v", evs)
	}

	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t4-ci-dashboard/ci",
		Node:  evs[0].ID,
		Kind:  evs[0].Kind,
		Value: evs[0].Value,
	}); err != nil {
		t.Fatalf("SendEvent timeline span ci-e2e: %v", err)
	}

	updated := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		ch := FindNodeByKind(n, "chart")
		return ch != nil && ch.GetText() == "Stage Load (ci-e2e)"
	})
	p2 := t4NewUiTreeProbe(updated, nil, ggui.Sz(960, 620))
	defer p2.Close()
	p2.Frame()
	sem2 := p2.Semantics().String()
	if !strings.Contains(sem2, "Stage Load (ci-e2e)") || !strings.Contains(sem2, "Inspect: ci-e2e") {
		t.Fatalf("expected updated dashboard semantics with 'Stage Load (ci-e2e)' and 'Inspect: ci-e2e':\n%s", sem2)
	}
}

// Scenario 6: Interactive Vector Architecture Diagram in ui.canvas
// Exercises all 6 vector commands (roundRect, circle, line, bezier, text, icon)
// with hitId, pan/zoom in NewUiTreeProbe, and strict theme-token sandbox rejection.
func TestScenario_06_InteractiveVectorArchitectureDiagramInCanvas(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t4InstallJSBlock(t, rig, "t4-arch-canvas", "", "arch", `
export default {
  activate(mako) {
    mako.blocks.register("arch", {
      state: { focused: "gateway", badTone: "" },
      render(state, ui) {
        return ui.column([
          ui.text("Focused: " + state.focused),
          ui.canvas([
            { op: "roundRect", x: 20, y: 30, w: 150, h: 80, r: 10, fillTone: state.badTone || "surfaceAlt", strokeTone: "accent", hitId: "gateway" },
            { op: "icon", x: 34, y: 46, icon: "network", tone: "accent", size: "medium" },
            { op: "text", x: 64, y: 66, text: "Gateway", tone: "text" },
            { op: "line", x1: 170, y1: 70, x2: 240, y2: 70, strokeTone: "muted", strokeWidth: 2 },
            { op: "bezier", x1: 170, y1: 85, cx1: 200, cy1: 140, cx2: 230, cy2: 140, x2: 260, y2: 110, strokeTone: "success", strokeWidth: 2 },
            { op: "circle", x: 280, y: 80, r: 34, fillTone: "surface", strokeTone: "success", hitId: "worker" },
            { op: "text", x: 256, y: 84, text: "Worker", tone: "success" }
          ], {
            key: "diagram",
            width: 480,
            height: 220,
            onChange: ["focusNode"]
          }),
          ui.button("InjectBadHex", { key: "bad-hex", onTap: ["injectBad", "#123456"] })
        ], { fill: true, gap: 8 });
      },
      on: {
        focusNode(state, hitId) { state.focused = String(hitId || "gateway"); },
        injectBad(state, tone) { state.badTone = String(tone); }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "canvas") != nil
	})

	var sink t4EventSink
	p := t4NewUiTreeProbe(tree, sink.Record, ggui.Sz(640, 420))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	// Exercise pan and zoom gestures on the vector canvas.
	p.Press(ggui.Pt(200, 140))
	p.Frame()
	p.Move(ggui.Pt(230, 155))
	p.Frame()
	p.Release(ggui.Pt(230, 155))
	p.Frame()
	p.Scroll(ggui.Pt(200, 140), ggui.Pt(0, -60))
	p.Frame()

	cNode := FindNodeByKind(tree, "canvas")
	workerBtn := t4WaitFindButton(t, p, "worker")
	p.Click(t4Center(workerBtn.Rect))
	p.Frame()

	evs := sink.Snapshot()
	if len(evs) != 1 || evs[0].ID != cNode.GetId() || evs[0].Kind != protocol.UiChange || evs[0].Value != "worker" {
		t.Fatalf("expected canvas hitId click on 'worker' to emit UiChange(worker), got %+v", evs)
	}

	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t4-arch-canvas/arch",
		Node:  evs[0].ID,
		Kind:  evs[0].Kind,
		Value: evs[0].Value,
	}); err != nil {
		t.Fatalf("SendEvent canvas hitId worker: %v", err)
	}
	updated := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "Focused: worker")
	})
	p2 := t4NewUiTreeProbe(updated, nil, ggui.Sz(640, 420))
	defer p2.Close()
	p2.Frame()
	if sem2 := p2.Semantics().String(); !strings.Contains(sem2, "Focused: worker") {
		t.Fatalf("expected updated canvas probe semantics with 'Focused: worker':\n%s", sem2)
	}

	// Trigger strict sandbox color token rejection.
	badBtn := FindNodeByKey(tree, "bad-hex")
	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t4-arch-canvas/arch",
		Node:  badBtn.GetId(),
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatalf("SendEvent injectBad: %v", err)
	}
	_, upd := w.WaitUpdate(t, 10*time.Second, func(_ *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetError() != ""
	})
	if upd.GetError() == "" {
		t.Fatal("expected uiblock sandbox rejection for raw hex #123456")
	}
}

// Scenario 7: Hover 1-Hop Focus & Layout Mode Switching on Dense Graph
// 12-node, 16-edge multi-group graph switching force -> hierarchy -> radial,
// toggling minimap, and hovering a hub node to reveal floating detail card.
func TestScenario_07_Hover1HopFocusAndLayoutModeSwitchingOnDenseGraph(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t4InstallJSBlock(t, rig, "t4-dense-graph", "", "dense", `
export default {
  activate(mako) {
    mako.blocks.register("dense", {
      render(_state, ui) {
        const nodes = [
          { id: "hub", label: "Core Hub", group: "core", shape: "card", status: "working", tone: "accent", weight: 3 },
          { id: "c1", label: "Core Auth", group: "core", shape: "card", status: "idle", tone: "success" },
          { id: "c2", label: "Core Store", group: "core", shape: "card", status: "idle", tone: "success" },
          { id: "c3", label: "Core Bus", group: "core", shape: "card", status: "working", tone: "accent" },
          { id: "a1", label: "API Ingress", group: "api", shape: "card", status: "idle", tone: "accent" },
          { id: "a2", label: "API GraphQL", group: "api", shape: "card", status: "waiting", tone: "warning" },
          { id: "a3", label: "API WS", group: "api", shape: "card", status: "idle", tone: "success" },
          { id: "a4", label: "API AuthZ", group: "api", shape: "card", status: "idle", tone: "muted" },
          { id: "w1", label: "Worker 1", group: "workers", shape: "card", status: "working", tone: "accent" },
          { id: "w2", label: "Worker 2", group: "workers", shape: "card", status: "idle", tone: "success" },
          { id: "w3", label: "Worker 3", group: "workers", shape: "card", status: "done", tone: "muted" },
          { id: "w4", label: "Worker 4", group: "workers", shape: "card", status: "idle", tone: "muted" }
        ];
        const edges = [
          { from: "hub", to: "c1", active: true }, { from: "hub", to: "c2" }, { from: "hub", to: "c3", active: true },
          { from: "c1", to: "c2" }, { from: "c2", to: "c3" },
          { from: "hub", to: "a1" }, { from: "a1", to: "a2" }, { from: "a2", to: "a3" },
          { from: "a3", to: "a4" }, { from: "a1", to: "a4" },
          { from: "hub", to: "w1", active: true }, { from: "w1", to: "w2" }, { from: "w2", to: "w3" },
          { from: "w3", to: "w4" }, { from: "c3", to: "w1" }, { from: "a2", to: "w2", dashed: true }
        ];
        return ui.column([
          ui.graph(nodes, edges, {
            key: "dense",
            variant: "cards",
            layout: "force",
            minimap: true,
            fill: true
          })
        ], { fill: true });
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		gn := FindNodeByKind(n, "graph")
		return gn != nil && len(gn.GetGraphNodes()) == 12 && len(gn.GetGraphEdges()) == 16
	})

	p := t4NewUiTreeProbe(tree, nil, ggui.Sz(1120, 760))
	defer p.Close()
	for range 4 {
		p.Frame()
	}

	p.Click(t4Center(t4WaitFindButton(t, p, "Hierarchy layout").Rect))
	p.Advance(320 * time.Millisecond)
	p.Frame()

	p.Click(t4Center(t4WaitFindButton(t, p, "Radial layout").Rect))
	p.Advance(320 * time.Millisecond)
	p.Frame()

	p.Click(t4Center(t4WaitFindButton(t, p, "Toggle minimap").Rect))
	p.Frame()

	hubBtn := t4WaitFindButton(t, p, "Core Hub")
	p.Move(t4Center(hubBtn.Rect))
	for range 3 {
		p.Advance(32 * time.Millisecond)
		p.Frame()
	}
	if sem := p.Semantics().String(); !strings.Contains(sem, "Detail: Core Hub") {
		t.Fatalf("expected hover detail card for Core Hub:\n%s", sem)
	}
}

// Scenario 8: Live Session Lifecycle Streaming
// sessionStarted -> working -> agentMessage active edge -> waiting approval ->
// sessionEnded reflected live via WatchTree + protocol.ApplyUi.
func TestScenario_08_LiveSessionLifecycleStreaming(t *testing.T) {
	proj := t.TempDir()
	sLead := &protocol.SessionInfo{
		Id:      "s-lead",
		Name:    "Lead",
		Agent:   "claude",
		Project: proj,
		State:   protocol.StateRunning,
		Status:  "working",
	}
	rig := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{sLead},
	})
	rig.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	rig.Grant(t, "agent-map")

	w := rig.WatchTree(t, "plugin:agent-map/map", "", plugin.View{Standalone: true})
	w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return t4HasGraphNode(FindNodeByKind(n, "graph"), "sess:s-lead")
	})

	// Seed the initial session baseline after Grant + activate() subscribe() so
	// Manager.SessionsChanged (which treats the first post-subscribe listing as baseline)
	// emits sessionStarted when s-sub arrives next.
	rig.SetSessions([]*protocol.SessionInfo{sLead})

	// 1. Subagent starts working.
	rig.SetSessions([]*protocol.SessionInfo{
		sLead,
		{Id: "s-sub", Name: "SubWorker", Agent: "codex", Project: proj, ParentSessionId: "s-lead", State: protocol.StateRunning, Status: "working"},
	})
	w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return t4HasGraphNode(FindNodeByKind(n, "graph"), "sess:s-sub")
	})

	// 2. Inter-agent message creates live edge between s-lead and s-sub.
	rig.Mgr.AgentMessage(1, "s-lead", "s-sub", "Run integration suite", false)
	w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return t4HasEdge(FindNodeByKind(n, "graph"), "sess:s-lead", "sess:s-sub")
	})

	// 3. Subagent enters waiting approval state.
	rig.SetSessions([]*protocol.SessionInfo{
		{Id: "s-lead", Name: "Lead", Agent: "claude", Project: proj, State: protocol.StateRunning, Status: "working"},
		{
			Id: "s-sub", Name: "SubWorker", Agent: "codex", Project: proj,
			ParentSessionId: "s-lead", State: protocol.StateRunning, Status: "waiting",
			Approval: &protocol.Approval{Id: "apr-1", Tool: "Bash"},
		},
	})
	treeWaiting := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		nd := t4FindGraphNode(FindNodeByKind(n, "graph"), "sess:s-sub")
		return nd != nil && nd.GetTone() == "warning"
	})

	p := t4NewUiTreeProbe(treeWaiting, nil, ggui.Sz(1024, 680))
	defer p.Close()
	p.Advance(80 * time.Millisecond)
	p.Frame()

	// 4. Subagent exits -> state reflected as exited/muted.
	rig.SetSessions([]*protocol.SessionInfo{
		{Id: "s-lead", Name: "Lead", Agent: "claude", Project: proj, State: protocol.StateRunning, Status: "idle"},
		{Id: "s-sub", Name: "SubWorker", Agent: "codex", Project: proj, ParentSessionId: "s-lead", State: protocol.StateExited, Status: "done"},
	})
	w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		nd := t4FindGraphNode(FindNodeByKind(n, "graph"), "sess:s-sub")
		return nd != nil && nd.GetTone() == "muted"
	})
}

func t4CollectTappableNodes(n *protocol.UiNode) []*protocol.UiNode {
	if n == nil {
		return nil
	}
	var out []*protocol.UiNode
	if n.GetTap() {
		out = append(out, n)
	}
	for _, c := range n.GetChildren() {
		out = append(out, t4CollectTappableNodes(c)...)
	}
	return out
}

// Scenario 9: ui-workbench Multi-Surface Explorer & Viewer Tab Workflow
// Exercises real examples/plugins/ui-workbench explorer opening viewer tabs,
// rendering ui.canvas, ui.timeline, and ui.chart types with storage persistence.
func TestScenario_09_UiWorkbenchMultiSurfaceExplorerAndViewerTabWorkflow(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{
		Limits: plugin.Limits{
			CPU:     10 * time.Second,
			Wall:    20 * time.Second,
			Memory:  16 << 20,
			Updates: 20 * time.Millisecond,
			Events:  200,
		},
	})
	rig.CopyPluginDir(t, DemoPluginDir(t, "ui-workbench"), "ui-workbench")
	rig.Grant(t, "ui-workbench")

	wExp := rig.WatchTree(t, "plugin:ui-workbench/explorer", "", plugin.View{Standalone: true})
	expTree := wExp.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return n != nil && len(TreeTexts(n)) > 0
	})

	pExp := t4NewUiTreeProbe(expTree, nil, ggui.Sz(480, 680))
	defer pExp.Close()
	pExp.Frame()

	// Collect all viewer tab intents offered by explorer presets/buttons.
	var tabIntents []*protocol.PluginOpen
	seenKeys := map[string]bool{}
	for _, tapNode := range t4CollectTappableNodes(expTree) {
		done, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:ui-workbench/explorer",
			Node:  tapNode.GetId(),
			Kind:  protocol.UiTap,
		})
		if err != nil || done == nil {
			continue
		}
		for _, o := range done.GetOpen() {
			if o.GetKind() == "tab" && o.GetBlock() == "plugin:ui-workbench/viewer" && !seenKeys[o.GetKey()] {
				seenKeys[o.GetKey()] = true
				tabIntents = append(tabIntents, o)
			}
		}
	}
	if len(tabIntents) == 0 {
		tabIntents = append(tabIntents, &protocol.PluginOpen{
			Kind:  "tab",
			Block: "plugin:ui-workbench/viewer",
			Key:   "e2e-tab-1",
		})
	}

	var sawCanvas, sawTimeline, sawChart bool
	inspectTree := func(n *protocol.UiNode) {
		if FindNodeByKind(n, "canvas") != nil {
			sawCanvas = true
		}
		if FindNodeByKind(n, "timeline") != nil {
			sawTimeline = true
		}
		if FindNodeByKind(n, "chart") != nil {
			sawChart = true
		}
		pv := t4NewUiTreeProbe(n, nil, ggui.Sz(960, 700))
		pv.Frame()
		pv.Close()
	}

	for _, intent := range tabIntents {
		viewOpts := plugin.View{
			Standalone: true,
			Instance:   intent.GetKey(),
			Params:     intent.GetParams(),
		}
		wView := rig.WatchTree(t, "plugin:ui-workbench/viewer", "", viewOpts)
		viewTree := wView.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
			return n != nil && len(TreeTexts(n)) > 0
		})
		inspectTree(viewTree)

		if tabs := FindNodeByKind(viewTree, "tabs"); tabs != nil && len(tabs.GetOptions()) > 1 {
			for _, opt := range tabs.GetOptions() {
				if _, err := rig.SendEvent(t, &protocol.BlockEvent{
					Block:    "plugin:ui-workbench/viewer",
					Node:     tabs.GetId(),
					Kind:     protocol.UiChange,
					Value:    opt,
					Instance: viewOpts.Instance,
				}); err != nil {
					t.Fatalf("switch viewer tab %q: %v", opt, err)
				}
				cur := wView.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
					tb := FindNodeByKind(n, "tabs")
					return tb != nil && tb.GetValue() == opt
				})
				inspectTree(cur)
			}
		}
	}

	if !sawCanvas || !sawTimeline || !sawChart {
		t.Fatalf("expected ui-workbench viewer tabs to showcase ui.canvas (%v), ui.timeline (%v), and ui.chart (%v)",
			sawCanvas, sawTimeline, sawChart)
	}
}

// Scenario 10: Installed AppSupport Plugin Parity & Smoke Execution
// Verifies /Users/ironpark/Library/Application Support/mako/plugins/ matches
// examples/plugins/ and boots & renders every synced plugin cleanly in PluginRig.
func TestScenario_10_InstalledAppSupportPluginParityAndSmokeExecution(t *testing.T) {
	appSupportDir, err := AppSupportPluginsDir()
	if err != nil {
		t.Fatalf("AppSupportPluginsDir: %v", err)
	}
	entries, err := os.ReadDir(appSupportDir)
	if err != nil {
		t.Fatalf("read AppSupport plugins dir %q: %v", appSupportDir, err)
	}

	var synced []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		exDir := DemoPluginDir(t, e.Name())
		if info, statErr := os.Stat(exDir); statErr == nil && info.IsDir() {
			synced = append(synced, e.Name())
		}
	}
	if !slices.Contains(synced, "agent-map") || !slices.Contains(synced, "ui-workbench") {
		t.Fatalf("AppSupport synced plugins = %v, must include agent-map and ui-workbench", synced)
	}

	for _, id := range synced {
		srcDir := DemoPluginDir(t, id)
		dstDir := filepath.Join(appSupportDir, id)
		err := filepath.WalkDir(srcDir, func(srcPath string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(srcDir, srcPath)
			if err != nil || rel == "." {
				return err
			}
			if d.IsDir() || d.Type()&os.ModeSymlink != 0 || d.Name() == ".DS_Store" {
				return nil
			}
			dstPath := filepath.Join(dstDir, rel)
			wantBytes, err := os.ReadFile(srcPath)
			if err != nil {
				return fmt.Errorf("read example %s: %w", srcPath, err)
			}
			gotBytes, err := os.ReadFile(dstPath)
			if err != nil {
				return fmt.Errorf("read appsupport %s: %w", dstPath, err)
			}
			if !bytes.Equal(wantBytes, gotBytes) {
				return fmt.Errorf("parity mismatch in %s/%s (example=%dB, appsupport=%dB)", id, rel, len(wantBytes), len(gotBytes))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("plugin parity check failed for %q: %v", id, err)
		}
	}

	// Smoke-boot and render every synced AppSupport plugin in an isolated PluginRig.
	projRoot := t.TempDir()
	rig := NewPluginRig(t, PluginRigOpts{
		Projects: []plugin.ProjectRoots{{ID: "smoke-proj", Name: "Smoke", Roots: []string{projRoot}}},
		Sessions: []*protocol.SessionInfo{{
			Id:        "smoke-s1",
			Name:      "Smoke Session",
			Agent:     "claude",
			Project:   projRoot,
			State:     protocol.StateRunning,
			Status:    "working",
			StartedAt: timestamppb.Now(),
		}},
	})
	for _, id := range synced {
		rig.CopyPluginDir(t, filepath.Join(appSupportDir, id), id)
		rig.Grant(t, id)
		info := rig.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
			return p.GetState() != "" && p.GetState() != protocol.PluginWaiting
		})
		if info.GetState() == protocol.PluginFailed {
			t.Fatalf("synced plugin %q failed to boot: %s", id, info.GetError())
		}
		for _, blk := range info.GetBlocks() {
			w := rig.WatchTree(t, blk.GetName(), "", plugin.View{Standalone: true})
			tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
				return n != nil
			})
			p := t4NewUiTreeProbe(tree, nil, ggui.Sz(840, 560))
			p.Frame()
			p.Close()
		}
	}
}
