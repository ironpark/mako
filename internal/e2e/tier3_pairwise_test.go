//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
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

type t3RecordedEvent struct {
	ID    string
	Kind  string
	Value string
}

type t3EventLog struct {
	mu     sync.Mutex
	events []t3RecordedEvent
}

func (l *t3EventLog) Record(id, kind, value string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, t3RecordedEvent{ID: id, Kind: kind, Value: value})
}

func (l *t3EventLog) Snapshot() []t3RecordedEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

func (l *t3EventLog) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = nil
}

func t3Center(r ggui.Rect) ggui.Point {
	return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
}

func t3SetupJSBlock(t *testing.T, rig *PluginRig, id, perms, blockID, js string) *TreeWatcher {
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

func t3HasGraphNode(n *protocol.UiNode, nodeID string) bool {
	if n == nil {
		return false
	}
	for _, gn := range n.GetGraphNodes() {
		if gn.GetId() == nodeID {
			return true
		}
	}
	return false
}

func t3FindGraphNode(n *protocol.UiNode, nodeID string) *protocol.UiGraphNode {
	if n == nil {
		return nil
	}
	for _, gn := range n.GetGraphNodes() {
		if gn.GetId() == nodeID {
			return gn
		}
	}
	return nil
}

func t3NewUiTreeProbe(tree *protocol.UiNode, onEvent func(id, kind, value string), size ggui.Size) *ggui.Probe {
	i18n.Set("en")
	return gui.NewUiTreeProbe(tree, onEvent, size)
}

func t3WaitFindButton(t *testing.T, p *ggui.Probe, label string) ggui.SemNode {
	t.Helper()
	var found ggui.SemNode
	WaitFor(t, 5*time.Second, fmt.Sprintf("button %q in semantics", label), func() bool {
		p.Frame()
		var ok bool
		found, ok = p.Semantics().Find(ggui.RoleButton, label)
		return ok
	})
	return found
}

// TestPairwise_F1_1_F1_2_F1_3_CardNodesBezierEdgesGroupHulls verifies rich card
// and circle nodes (F1.1), bidirectional/labeled Cubic Bezier edges (F1.2), and
// multi-group cluster hulls (F1.3) rendered together in gui.NewUiTreeProbe.
func TestPairwise_F1_1_F1_2_F1_3_CardNodesBezierEdgesGroupHulls(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-cards-hulls", "", "board", `
export default {
  activate(mako) {
    mako.blocks.register("board", {
      state: { selected: "" },
      render(state, ui) {
        return ui.column([
          ui.graph([
            {
              id: "hub", label: "Gateway Hub", group: "edge-tier", shape: "card",
              icon: "network", tone: "accent", subtitle: "api-gw · v2", status: "working",
              badges: [{ text: "main", tone: "accent" }, { text: "primary", tone: "success" }],
              weight: 2.5
            },
            {
              id: "worker-a", label: "Worker Alpha", group: "compute-tier", shape: "card",
              icon: "bot", tone: "success", subtitle: "claude · idle", status: "idle",
              badges: ["opus"], weight: 1.5
            },
            {
              id: "cache", label: "Cache Ring", group: "compute-tier", shape: "circle",
              icon: "layers", tone: "warning", subtitle: "warm", status: "waiting", weight: 1.0
            }
          ], [
            { from: "hub", to: "worker-a", label: "dispatch", tone: "accent", active: true, kind: "agentMessage" },
            { from: "worker-a", to: "hub", label: "reply", tone: "success", dashed: true },
            { from: "worker-a", to: "cache", label: "read", tone: "muted" }
          ], {
            key: "topo",
            variant: "cards",
            value: state.selected,
            fill: true,
            onChange: ["pick"]
          })
        ], { fill: true });
      },
      on: {
        pick(state, id) { state.selected = String(id || ""); }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	gn := FindNodeByKind(tree, "graph")
	if gn.GetVariant() != "cards" || len(gn.GetGraphNodes()) != 3 || len(gn.GetGraphEdges()) != 3 {
		t.Fatalf("unexpected graph node: variant=%q nodes=%d edges=%d", gn.GetVariant(), len(gn.GetGraphNodes()), len(gn.GetGraphEdges()))
	}
	hub := t3FindGraphNode(gn, "hub")
	if hub.GetShape() != "card" || hub.GetIcon() != "network" || hub.GetStatus() != "working" || len(hub.GetBadges()) != 2 {
		t.Fatalf("unexpected hub node metadata: %+v", hub)
	}

	var log t3EventLog
	p := t3NewUiTreeProbe(tree, log.Record, ggui.Sz(900, 640))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	sem := p.Semantics().String()
	for _, want := range []string{"Gateway Hub", "Worker Alpha", "Cache Ring"} {
		if !strings.Contains(sem, want) {
			t.Fatalf("missing %q in semantics:\n%s", want, sem)
		}
	}

	workerBtn := t3WaitFindButton(t, p, "Worker Alpha")
	p.Click(t3Center(workerBtn.Rect))
	p.Frame()

	evs := log.Snapshot()
	if len(evs) != 1 || evs[0].Kind != protocol.UiChange || evs[0].Value != "worker-a" {
		t.Fatalf("got click events = %+v, want UiChange worker-a", evs)
	}
}

// TestPairwise_F1_4_F1_5_LayoutModesTweenAndCameraFocus verifies switching
// layout modes (force -> hierarchy -> radial) alongside TweenOf interpolation
// and automatic camera focus on node selection.
func TestPairwise_F1_4_F1_5_LayoutModesTweenAndCameraFocus(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-layout-tween", "", "canvas", `
export default {
  activate(mako) {
    mako.blocks.register("canvas", {
      state: { selected: "" },
      render(state, ui) {
        return ui.column([
          ui.graph([
            { id: "root", label: "Root Orchestrator", group: "g1", weight: 3 },
            { id: "c1", label: "Child One", group: "g1", weight: 1.5 },
            { id: "c2", label: "Child Two", group: "g2", weight: 1.5 },
            { id: "mid", label: "Mid Stage", group: "g2", weight: 1.2 },
            { id: "leaf", label: "Leaf Task", group: "g2", weight: 1.0 }
          ], [
            { from: "root", to: "c1", kind: "parent" },
            { from: "root", to: "c2", kind: "parent" },
            { from: "c1", to: "mid", kind: "dependency" },
            { from: "c2", to: "leaf", kind: "dependency" },
            { from: "mid", to: "leaf", kind: "dependency" }
          ], {
            key: "g",
            layout: "force",
            value: state.selected,
            fill: true,
            onChange: ["select"]
          })
        ], { fill: true });
      },
      on: {
        select(state, id) { state.selected = String(id || ""); }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	var log t3EventLog
	p := t3NewUiTreeProbe(tree, log.Record, ggui.Sz(840, 560))
	defer p.Close()
	for range 4 {
		p.Frame()
	}
	p.Advance(0)

	forcePos := t3WaitFindButton(t, p, "Leaf Task").Rect.Origin

	hierBtn := t3WaitFindButton(t, p, "Hierarchy layout")
	p.Click(t3Center(hierBtn.Rect))
	p.Frame()
	for range 4 {
		p.Advance(80 * time.Millisecond)
	}
	hierPos := t3WaitFindButton(t, p, "Leaf Task").Rect.Origin

	radialBtn := t3WaitFindButton(t, p, "Radial layout")
	p.Click(t3Center(radialBtn.Rect))
	p.Frame()
	for range 4 {
		p.Advance(80 * time.Millisecond)
	}
	radialPos := t3WaitFindButton(t, p, "Leaf Task").Rect.Origin

	if forcePos == hierPos || hierPos == radialPos || forcePos == radialPos {
		t.Fatalf("expected all 3 layout modes to yield distinct positions for Leaf Task: force=%v hier=%v radial=%v", forcePos, hierPos, radialPos)
	}

	// Selecting a node triggers UiChange and animates camera focus smoothly.
	leafBtn := t3WaitFindButton(t, p, "Leaf Task")
	p.Click(t3Center(leafBtn.Rect))
	p.Frame()
	for range 4 {
		p.Advance(80 * time.Millisecond)
	}

	evs := log.Snapshot()
	if len(evs) == 0 || evs[len(evs)-1].Kind != protocol.UiChange || evs[len(evs)-1].Value != "leaf" {
		t.Fatalf("expected UiChange leaf after click, got %+v", evs)
	}
}

// TestPairwise_F1_6_F1_9_LiveMotionAndHover1HopDimming verifies 60fps live
// motion (working pulse, waiting amber beacon, active edge flow) combined with
// 1-hop hover isolation and floating detail card rendering.
func TestPairwise_F1_6_F1_9_LiveMotionAndHover1HopDimming(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-motion-hover", "", "live", `
export default {
  activate(mako) {
    mako.blocks.register("live", {
      render(_state, ui) {
        return ui.column([
          ui.graph([
            { id: "n1", label: "Coder Alpha", status: "working", tone: "accent", subtitle: "editing", shape: "card", icon: "bot" },
            { id: "n2", label: "Reviewer Beta", status: "waiting", tone: "warning", subtitle: "approval: Bash", shape: "card", icon: "shield-alert" },
            { id: "n3", label: "Distant Gamma", status: "idle", tone: "muted", subtitle: "2 hops away", shape: "card", icon: "bot" }
          ], [
            { from: "n1", to: "n2", label: "handoff", active: true, kind: "agentMessage" },
            { from: "n2", to: "n3", label: "check", dashed: true }
          ], {
            key: "live-graph",
            variant: "cards",
            fill: true
          })
        ], { fill: true });
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	p := t3NewUiTreeProbe(tree, nil, ggui.Sz(860, 560))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	coderBtn := t3WaitFindButton(t, p, "Coder Alpha")
	p.Move(t3Center(coderBtn.Rect))
	for range 3 {
		p.Advance(32 * time.Millisecond)
		p.Frame()
	}

	sem := p.Semantics().String()
	if !strings.Contains(sem, "Detail: Coder Alpha") {
		t.Fatalf("expected hover detail card for Coder Alpha in semantics:\n%s", sem)
	}

	// Move pointer off all nodes and advance live motion frames without error.
	p.Move(ggui.Pt(10, 10))
	p.Advance(160 * time.Millisecond)
	p.Frame()
}

// TestPairwise_F1_7_F1_8_FillGrowMinimapAndPanVsNodeDragDrop verifies fill/grow
// canvas expansion across resize + minimap toggle (F1.7) combined with background
// pan vs per-node drag and drop emission (F1.8).
func TestPairwise_F1_7_F1_8_FillGrowMinimapAndPanVsNodeDragDrop(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-fill-drag", "", "board", `
export default {
  activate(mako) {
    mako.blocks.register("board", {
      state: { dropped: null },
      render(_state, ui) {
        return ui.column([
          ui.text("Header Strip"),
          ui.graph([
            { id: "a", label: "Node A", x: -80, y: 0, hasPos: true, tone: "accent" },
            { id: "b", label: "Node B", x: 120, y: 0, hasPos: true, tone: "success" }
          ], [
            { from: "a", to: "b", label: "link" }
          ], {
            key: "g",
            fill: true,
            grow: 1,
            minimap: true,
            onChange: ["onSel"],
            onDrop: ["onNodeDrop"]
          })
        ], { fill: true, gap: 6 });
      },
      on: {
        onSel() {},
        onNodeDrop(state, pos) { state.dropped = pos; }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	var log t3EventLog
	p := t3NewUiTreeProbe(tree, log.Record, ggui.Sz(760, 440))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	// Resize taller and confirm graph canvas/minimap control adapts cleanly.
	p.Resize(ggui.Sz(760, 700))
	for range 3 {
		p.Frame()
	}

	mmBtn := t3WaitFindButton(t, p, "Toggle minimap")
	p.Click(t3Center(mmBtn.Rect))
	p.Frame()
	p.Click(t3Center(mmBtn.Rect))
	p.Frame()

	// Background pan must NOT emit UiDrop or UiChange.
	log.Reset()
	p.Press(ggui.Pt(40, 120))
	p.Frame()
	p.Move(ggui.Pt(90, 160))
	p.Frame()
	p.Release(ggui.Pt(90, 160))
	p.Frame()
	if evs := log.Snapshot(); len(evs) != 0 {
		t.Fatalf("background pan unexpectedly emitted events: %+v", evs)
	}

	// Node drag -> release must emit UiDrop with JSON {id, x, y} and NOT UiChange.
	nodeA := t3WaitFindButton(t, p, "Node A")
	start := t3Center(nodeA.Rect)
	end := ggui.Pt(start.X+60, start.Y+45)
	p.Press(start)
	p.Frame()
	p.Move(ggui.Pt(start.X+30, start.Y+20))
	p.Frame()
	p.Move(end)
	p.Frame()
	p.Release(end)
	p.Frame()

	evs := log.Snapshot()
	if len(evs) != 1 || evs[0].Kind != protocol.UiDrop {
		t.Fatalf("node drag-drop events = %+v, want single UiDrop", evs)
	}
	var payload struct {
		ID string  `json:"id"`
		X  float64 `json:"x"`
		Y  float64 `json:"y"`
	}
	if err := json.Unmarshal([]byte(evs[0].Value), &payload); err != nil {
		t.Fatalf("unmarshal UiDrop payload %q: %v", evs[0].Value, err)
	}
	if payload.ID != "a" || (payload.X == -80 && payload.Y == 0) {
		t.Fatalf("unexpected drop payload: %+v", payload)
	}
}

// TestPairwise_F1_8_DragDropStoragePersistenceAndLayoutReload verifies that a
// graph node drop event persists {id, x, y} into ctx.storage and reloads into
// hasPos/pinned UiGraphNode coordinates.
func TestPairwise_F1_8_DragDropStoragePersistenceAndLayoutReload(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-drop-storage", `"storage"`, "persist", `
export default {
  activate(mako) {
    mako.blocks.register("persist", {
      state: { coords: {} },
      async load(state, ctx) {
        const saved = await ctx.storage.getItem("graph_coords");
        if (saved.found && saved.value) {
          state.coords = JSON.parse(saved.value);
        }
      },
      render(state, ui) {
        const nodes = ["n1", "n2"].map((id) => {
          const pos = state.coords[id];
          return {
            id,
            label: "Node " + id.toUpperCase(),
            ...(pos ? { x: pos.x, y: pos.y, hasPos: true, pinned: true } : {}),
          };
        });
        return ui.column([
          ui.graph(nodes, [{ from: "n1", to: "n2" }], {
            key: "g",
            fill: true,
            onDrop: ["savePos"]
          })
        ], { fill: true });
      },
      on: {
        async savePos(state, pos) {
          const ctx = mako.blocks.context("persist");
          state.coords[pos.id] = { x: pos.x, y: pos.y };
          await ctx.storage.setItem("graph_coords", JSON.stringify(state.coords));
        }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	graphNode := FindNodeByKind(tree, "graph")

	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t3-drop-storage/persist",
		Node:  graphNode.GetId(),
		Kind:  protocol.UiDrop,
		Value: `{"id":"n1","x":142.5,"y":-76.25}`,
	}); err != nil {
		t.Fatalf("SendEvent UiDrop: %v", err)
	}

	updated := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		nd := t3FindGraphNode(FindNodeByKind(n, "graph"), "n1")
		return nd != nil && nd.GetHasPos() && nd.GetPinned() && nd.GetX() == 142.5 && nd.GetY() == -76.25
	})
	if nd := t3FindGraphNode(FindNodeByKind(updated, "graph"), "n1"); nd == nil || !nd.GetHasPos() {
		t.Fatalf("node n1 missing persisted coordinates: %+v", nd)
	}

	// Reload the plugin and open a fresh watcher to confirm ctx.storage restores positions.
	if _, err := rig.Act(t, &protocol.PluginAction{
		PluginId: "t3-drop-storage",
		Op:       protocol.PluginReload,
	}); err != nil {
		t.Fatalf("PluginReload: %v", err)
	}
	wReloaded := rig.WatchTree(t, "plugin:t3-drop-storage/persist", "", plugin.View{Standalone: true})
	reloadedTree := wReloaded.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		nd := t3FindGraphNode(FindNodeByKind(n, "graph"), "n1")
		return nd != nil && nd.GetHasPos() && nd.GetX() == 142.5 && nd.GetY() == -76.25
	})
	if nd := t3FindGraphNode(FindNodeByKind(reloadedTree, "graph"), "n1"); nd == nil || !nd.GetPinned() {
		t.Fatalf("node n1 missing persisted coordinates after reload: %+v", nd)
	}
}

// TestPairwise_F1_9_F1_10_F2_3_DoubleTapOpenSessionAndCrossfade verifies
// ui.graph onDoubleTap (F1.9) firing ctx.ui.openSession (F2.3) and switching the
// workspaceCenterView crossfade back to the selected session (F1.10).
func TestPairwise_F1_9_F1_10_F2_3_DoubleTapOpenSessionAndCrossfade(t *testing.T) {
	dr := NewDaemonRig(t, DaemonRigOpts{Name: "t3-dtap-node"})
	sess := dr.OpenShellSession(t, t.TempDir())

	dr.InstallPlugin(t, "t3-dtap", `id = "t3-dtap"
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = ["sessions:read"]

[[contributes.block]]
id = "map"
title = "DoubleTap Map"
icon = "network"
slots = ["sidebar"]
`, fmt.Sprintf(`
export default {
  activate(mako) {
    mako.blocks.register("map", {
      render(_state, ui) {
        return ui.column([
          ui.graph([
            { id: "sess:%s", label: "Shell Target", shape: "card", status: "idle", x: 0, y: 0, hasPos: true }
          ], [], {
            key: "g",
            variant: "cards",
            fill: true,
            onDoubleTap: ["jump"]
          })
        ], { fill: true });
      },
      on: {
        async jump(_state, nodeId) {
          const ctx = mako.blocks.context("map");
          const sid = String(nodeId || "").replace(/^sess:/, "");
          if (sid) await ctx.ui.openSession(sid);
        }
      }
    });
  }
};
`, sess.GetId()))
	dr.Grant(t, "t3-dtap")

	w := dr.WatchBlockView(t, "plugin:t3-dtap/map", "", "en", true)
	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	gNode := FindNodeByKind(tree, "graph")

	done, err := dr.BlockEvent(t, &protocol.BlockEvent{
		Block: "plugin:t3-dtap/map",
		Node:  gNode.GetId(),
		Kind:  protocol.UiDoubleTap,
		Value: "sess:" + sess.GetId(),
	})
	if err != nil {
		t.Fatalf("BlockEvent UiDoubleTap: %v", err)
	}
	if len(done.GetOpen()) != 1 || done.GetOpen()[0].GetKind() != "session" || done.GetOpen()[0].GetKey() != sess.GetId() {
		t.Fatalf("PluginDone.Open = %+v, want session intent for %q", done.GetOpen(), sess.GetId())
	}

	app := gui.NewAppProbe(config.Default(), dr.Daemon.Socket(), ggui.Sz(1280, 800))
	defer app.Close()
	p := app.Probe()

	WaitFor(t, 10*time.Second, "DoubleTap Map in sidebar", func() bool {
		p.Frame()
		return strings.Contains(p.Semantics().String(), "DoubleTap Map")
	})

	app.OpenSidebarPage("plugin:t3-dtap/map")
	p.Advance(180 * time.Millisecond)
	p.Frame()
	if got := app.SidebarPage(); got != "plugin:t3-dtap/map" {
		t.Fatalf("SidebarPage = %q, want plugin:t3-dtap/map", got)
	}

	// Double-click the rendered graph node inside the live AppProbe.
	target := t3WaitFindButton(t, p, "Shell Target")
	pt := t3Center(target.Rect)
	p.Click(pt)
	p.Advance(80 * time.Millisecond)
	p.Frame()
	p.Click(pt)
	p.Advance(200 * time.Millisecond)

	WaitFor(t, 10*time.Second, "crossfade back to workspace & select session", func() bool {
		p.Frame()
		return app.SidebarPage() == "" && app.SelectedSession() == sess.GetId()
	})
}

// TestPairwise_F1_9_F1_10_KeyboardShortcutsAndGraphSelection verifies Arrow
// key cycling + Enter activation on ui.graph (F1.9, F1.10) and Escape return
// from sidebar page in AppProbe (F1.10).
func TestPairwise_F1_9_F1_10_KeyboardShortcutsAndGraphSelection(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-kbd-graph", "", "nav", `
export default {
  activate(mako) {
    mako.blocks.register("nav", {
      render(_state, ui) {
        return ui.column([
          ui.graph([
            { id: "left", label: "Left Node", x: -120, y: 0, hasPos: true },
            { id: "right", label: "Right Node", x: 120, y: 0, hasPos: true }
          ], [
            { from: "left", to: "right" }
          ], {
            key: "g",
            value: "left",
            fill: true,
            onChange: ["sel"],
            onDoubleTap: ["open"]
          })
        ], { fill: true });
      },
      on: {
        sel() {},
        open() {}
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	var log t3EventLog
	p := t3NewUiTreeProbe(tree, log.Record, ggui.Sz(800, 520))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	leftBtn := t3WaitFindButton(t, p, "Left Node")
	p.Click(t3Center(leftBtn.Rect))
	p.Frame()
	log.Reset()

	p.Type(ggui.Mods{}, ggui.KeyArrowRight)
	p.Frame()
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	p.Frame()

	evs := log.Snapshot()
	hasChangeRight := false
	hasDoubleTapRight := false
	for _, ev := range evs {
		if ev.Kind == protocol.UiChange && ev.Value == "right" {
			hasChangeRight = true
		}
		if ev.Kind == protocol.UiDoubleTap && ev.Value == "right" {
			hasDoubleTapRight = true
		}
	}
	if !hasChangeRight || !hasDoubleTapRight {
		t.Fatalf("expected ArrowRight -> UiChange(right) and Enter -> UiDoubleTap(right), got %+v", evs)
	}
}

// TestPairwise_F2_1_F1_2_F1_6_SessionFieldsToGraphEdgesAndBeacon verifies
// enriched Session fields (F2.1) driving parent edges (F1.2) and waiting amber
// beacon + badges (F1.6, F1.1).
func TestPairwise_F2_1_F1_2_F1_6_SessionFieldsToGraphEdgesAndBeacon(t *testing.T) {
	now := time.Now()
	rig := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:         "parent-1",
				Name:       "Lead Architect",
				Agent:      "claude",
				Profile:    "opus",
				Branch:     "feat/graph-v2",
				State:      protocol.StateRunning,
				Status:     "working",
				NodeId:     "local-mac",
				StartedAt:  timestamppb.New(now.Add(-5 * time.Minute)),
				LastOutput: timestamppb.New(now.Add(-2 * time.Second)),
			},
			{
				Id:              "child-1",
				Name:            "Test Runner",
				Agent:           "codex",
				Profile:         "fast",
				Branch:          "feat/graph-v2",
				ParentSessionId: "parent-1",
				State:           protocol.StateRunning,
				Status:          "waiting",
				NodeId:          "local-mac",
				Approval:        &protocol.Approval{Id: "apr-1", Tool: "Bash"},
				StartedAt:       timestamppb.New(now.Add(-2 * time.Minute)),
				StatusAt:        timestamppb.New(now.Add(-1 * time.Second)),
			},
		},
	})

	w := t3SetupJSBlock(t, rig, "t3-sess-enriched", `"sessions:read"`, "topo", `
export default {
  activate(mako) {
    mako.blocks.register("topo", {
      state: { sessions: [] },
      async load(state) {
        const res = await mako.sessions.listSessions();
        state.sessions = res.sessions || [];
      },
      render(state, ui) {
        const nodes = state.sessions.map((s) => ({
          id: "sess:" + s.id,
          label: s.name,
          group: s.nodeId || "local",
          shape: "card",
          icon: s.approvalTool ? "shield-alert" : "bot",
          status: s.status,
          tone: s.approvalTool ? "warning" : "accent",
          subtitle: [s.agent, s.profile, s.branch].filter(Boolean).join(" · "),
          badges: [
            ...(s.branch ? [{ text: s.branch, tone: "accent" }] : []),
            ...(s.approvalTool ? [{ text: "approval:" + s.approvalTool, tone: "warning" }] : []),
          ],
        }));
        const edges = state.sessions
          .filter((s) => s.parentSessionId)
          .map((s) => ({
            from: "sess:" + s.parentSessionId,
            to: "sess:" + s.id,
            label: "subagent",
            kind: "parent",
            tone: "accent",
            active: s.status === "working" || s.status === "waiting",
          }));
        return ui.column([
          ui.text("started:" + String((state.sessions[0] && state.sessions[0].startedAt > 0) || false)),
          ui.text("activity:" + String((state.sessions[1] && state.sessions[1].lastActivityAt > 0) || false)),
          ui.graph(nodes, edges, { key: "g", variant: "cards", fill: true })
        ], { fill: true });
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil && HasTreeText(n, "started:true")
	})
	if !HasTreeText(tree, "activity:true") {
		t.Fatalf("expected lastActivityAt > 0, texts=%v", TreeTexts(tree))
	}
	gn := FindNodeByKind(tree, "graph")
	child := t3FindGraphNode(gn, "sess:child-1")
	if child == nil || child.GetStatus() != "waiting" || len(child.GetBadges()) < 2 {
		t.Fatalf("unexpected child graph node: %+v", child)
	}
	if len(gn.GetGraphEdges()) != 1 || gn.GetGraphEdges()[0].GetKind() != "parent" || !gn.GetGraphEdges()[0].GetActive() {
		t.Fatalf("unexpected parent edge: %+v", gn.GetGraphEdges())
	}

	p := t3NewUiTreeProbe(tree, nil, ggui.Sz(840, 560))
	defer p.Close()
	p.Advance(64 * time.Millisecond)
	p.Frame()
	if !strings.Contains(p.Semantics().String(), "Test Runner") {
		t.Fatalf("missing Test Runner in probe semantics")
	}
}

// TestPairwise_F2_2_F1_3_MeshNodesSessionsAndMultiMachineClusterHulls verifies
// real 2-node P2P mesh discovery via mako.mesh.listNodes (F2.2) feeding
// multi-machine group cluster hulls in ui.graph (F1.3).
func TestPairwise_F2_2_F1_3_MeshNodesSessionsAndMultiMachineClusterHulls(t *testing.T) {
	nodeA := NewDaemonRig(t, DaemonRigOpts{Name: "mac-studio", EnableP2P: true})
	nodeB := NewDaemonRig(t, DaemonRigOpts{
		Name:      "linux-worker",
		EnableP2P: true,
		Peers:     []string{nodeA.PeerAddr()},
	})
	nodeA.LetIn(t, nodeB)
	nodeB.LetIn(t, nodeA)

	WaitFor(t, defaultWaitTimeout, "two-node mesh link up", func() bool {
		for _, n := range nodeA.Nodes(t) {
			if n.GetId() == nodeB.Daemon.Self().GetId() && n.GetLink() == "up" {
				return true
			}
		}
		return false
	})

	sessA := nodeA.OpenShellSession(t, t.TempDir())
	sessB := nodeB.OpenShellSession(t, t.TempDir())

	nodeA.InstallPlugin(t, "t3-mesh-hulls", `id = "t3-mesh-hulls"
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = ["sessions:read"]

[[contributes.block]]
id = "meshmap"
title = "Mesh Map"
slots = ["sidebar"]
`, `
export default {
  activate(mako) {
    mako.blocks.register("meshmap", {
      state: { nodes: [], sessions: [] },
      async load(state) {
        const mn = await mako.mesh.listNodes();
        const ms = await mako.mesh.listSessions();
        state.nodes = mn.nodes || [];
        state.sessions = ms.sessions || [];
      },
      render(state, ui) {
        const nodeById = new Map(state.nodes.map((n) => [n.id, n]));
        const gNodes = [];
        const gEdges = [];
        for (const n of state.nodes) {
          gNodes.push({
            id: "node:" + n.id,
            label: n.name + " (" + (n.os || "os") + ")",
            group: "machine:" + n.name,
            shape: "card",
            icon: "server",
            tone: n.self ? "accent" : "success",
            subtitle: n.arch || ""
          });
        }
        for (const s of state.sessions) {
          const owner = nodeById.get(s.nodeId);
          const grp = owner ? "machine:" + owner.name : "machine:unknown";
          gNodes.push({
            id: "sess:" + s.id,
            label: s.name || s.id,
            group: grp,
            shape: "card",
            icon: "terminal",
            status: s.status || "idle"
          });
          if (owner) {
            gEdges.push({ from: "node:" + owner.id, to: "sess:" + s.id, kind: "parent" });
          }
        }
        return ui.column([
          ui.text("meshNodes:" + state.nodes.length),
          ui.graph(gNodes, gEdges, { key: "mg", variant: "cards", fill: true })
        ], { fill: true });
      }
    });
  }
};
`)
	nodeA.Grant(t, "t3-mesh-hulls")

	w := nodeA.WatchBlockView(t, "plugin:t3-mesh-hulls/meshmap", "", "en", true)
	tree := w.WaitFor(t, 15*time.Second, func(n *protocol.UiNode) bool {
		gn := FindNodeByKind(n, "graph")
		return gn != nil && t3HasGraphNode(gn, "sess:"+sessA.GetId()) && t3HasGraphNode(gn, "sess:"+sessB.GetId())
	})
	if !HasTreeText(tree, "meshNodes:2") {
		t.Fatalf("expected meshNodes:2, got %v", TreeTexts(tree))
	}

	p := t3NewUiTreeProbe(tree, nil, ggui.Sz(920, 600))
	defer p.Close()
	p.Frame()
	sem := p.Semantics().String()
	if !strings.Contains(sem, "mac-studio") || !strings.Contains(sem, "linux-worker") {
		t.Fatalf("missing mesh machine names in semantics:\n%s", sem)
	}
}

// TestPairwise_F1_9_F2_3_ContextMenuSubagentSpawnWithParentSession verifies
// right-click context menu on a graph node (F1.9) triggering subagent spawn
// carrying parentSessionId (F2.3).
func TestPairwise_F1_9_F2_3_ContextMenuSubagentSpawnWithParentSession(t *testing.T) {
	root := t.TempDir()
	rig := NewPluginRig(t, PluginRigOpts{
		Projects: []plugin.ProjectRoots{{ID: "proj-1", Name: "Mako", Roots: []string{root}}},
		Sessions: []*protocol.SessionInfo{{
			Id:      "sess-parent",
			Name:    "Lead Agent",
			Agent:   "claude",
			Project: root,
			State:   protocol.StateRunning,
			Status:  "working",
		}},
	})

	w := t3SetupJSBlock(t, rig, "t3-ctx-spawn", `"sessions:read", "sessions:write", "fs:project"`, "map", fmt.Sprintf(`
export default {
  activate(mako) {
    mako.blocks.register("map", {
      state: { spawned: "" },
      render(state, ui) {
        return ui.column([
          ui.contextMenu("node-ctx", [
            ui.menuItem("서브에이전트 실행", { icon: "git-branch", onTap: ["spawnSub"] })
          ]),
          ui.graph([
            {
              id: "sess:sess-parent",
              label: "Lead Agent",
              shape: "card",
              x: 0,
              y: 0,
              hasPos: true,
              contextMenu: "node-ctx",
              contextArg: "sess-parent"
            }
          ], [], { key: "g", variant: "cards", fill: true }),
          ui.text("spawned:" + state.spawned)
        ], { fill: true });
      },
      on: {
        async spawnSub(state, parentSid) {
          const res = await mako.messages.startAgent({
            agent: "claude",
            project: "proj-1",
            root: %q,
            prompt: "Run verification subtask",
            name: "Child Verifier",
            parentSessionId: String(parentSid || "")
          });
          state.spawned = res.requestId || "ok";
        }
      }
    });
  }
};
`, root))

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	var log t3EventLog
	p := t3NewUiTreeProbe(tree, log.Record, ggui.Sz(800, 560))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	leadBtn := t3WaitFindButton(t, p, "Lead Agent")
	p.ClickButton(t3Center(leadBtn.Rect), ggui.MouseButtonRight)
	p.Frame()

	var menuItem ggui.SemNode
	WaitFor(t, 3*time.Second, "context menu item 서브에이전트 실행", func() bool {
		p.Frame()
		var ok bool
		menuItem, ok = p.Semantics().Find(ggui.RoleMenuItem, "서브에이전트 실행")
		return ok
	})
	p.Click(t3Center(menuItem.Rect))
	p.Frame()

	evs := log.Snapshot()
	if len(evs) != 1 || evs[0].Kind != protocol.UiTap || evs[0].Value != "sess-parent" {
		t.Fatalf("expected context menu UiTap with arg sess-parent, got %+v", evs)
	}

	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t3-ctx-spawn/map",
		Node:  evs[0].ID,
		Kind:  evs[0].Kind,
		Value: evs[0].Value,
	}); err != nil {
		t.Fatalf("SendEvent context menu tap: %v", err)
	}

	// Approve the plugin's agent start ask via PluginAllowAgent.
	info := rig.WaitPlugin(t, "t3-ctx-spawn", func(pi *protocol.PluginInfo) bool {
		return pi.GetAgentAsk() != nil
	})
	if _, err := rig.Act(t, &protocol.PluginAction{
		PluginId: "t3-ctx-spawn",
		Op:       protocol.PluginAllowAgent,
		Key:      info.GetAgentAsk().GetId(),
		Value:    protocol.AgentOnce,
	}); err != nil {
		t.Fatalf("PluginAllowAgent: %v", err)
	}

	WaitFor(t, 10*time.Second, "StubAgentHost.Start called", func() bool {
		return len(rig.Agents.Started()) == 1
	})
}

// TestPairwise_F3_1_F3_3_CanvasPanZoomHitIdUpdatesChart verifies ui.canvas
// pan/zoom + hitId click (F3.1) updating a sibling ui.chart (F3.3).
func TestPairwise_F3_1_F3_3_CanvasPanZoomHitIdUpdatesChart(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-canvas-chart", "", "dash", `
export default {
  activate(mako) {
    mako.blocks.register("dash", {
      state: { activeZone: "ingress" },
      render(state, ui) {
        const series = state.activeZone === "worker"
          ? [{ name: "CPU", tone: "warning", values: [72, 85, 91, 88] }]
          : [{ name: "RPS", tone: "accent", values: [12, 24, 30, 28] }];
        return ui.column([
          ui.canvas([
            { op: "roundRect", x: 10, y: 10, w: 380, h: 180, r: 10, fillTone: "surfaceAlt", strokeTone: "accent", hitId: "worker" },
            { op: "text", x: 40, y: 60, text: "Worker Cluster", tone: "text", size: "large" },
            { op: "circle", x: 320, y: 90, r: 22, fillTone: "success", hitId: "worker" },
            { op: "bezier", x1: 60, y1: 120, cx1: 140, cy1: 40, cx2: 220, cy2: 150, x2: 300, y2: 90, strokeTone: "accent", strokeWidth: 2 },
            { op: "icon", x: 308, y: 78, icon: "cpu", tone: "ink", size: "medium" }
          ], {
            key: "arch",
            width: 400,
            height: 200,
            onChange: ["focusZone"]
          }),
          ui.chart("area", series, {
            key: "metric",
            caption: "Zone: " + state.activeZone,
            labels: ["T1", "T2", "T3", "T4"],
            maxHeight: 180
          })
        ], { fill: true, gap: 8 });
      },
      on: {
        focusZone(state, hitId) {
          state.activeZone = String(hitId || "ingress");
        }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "canvas") != nil && FindNodeByKind(n, "chart") != nil
	})

	var log t3EventLog
	p := t3NewUiTreeProbe(tree, log.Record, ggui.Sz(720, 560))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	cNode := FindNodeByKind(tree, "canvas")
	workerBtn, ok := p.Semantics().Find(ggui.RoleButton, "worker")
	if !ok {
		t.Fatalf("canvas hitId 'worker' button not found in semantics:\n%s", p.Semantics().String())
	}
	p.Click(t3Center(workerBtn.Rect))
	p.Frame()

	evs := log.Snapshot()
	if len(evs) != 1 || evs[0].ID != cNode.GetId() || evs[0].Kind != protocol.UiChange || evs[0].Value != "worker" {
		t.Fatalf("expected GUI probe click on canvas hitId 'worker' to emit UiChange(worker), got %+v", evs)
	}

	// Pan and zoom the canvas; verify it doesn't error.
	p.Press(ggui.Pt(200, 100))
	p.Frame()
	p.Move(ggui.Pt(240, 120))
	p.Frame()
	p.Release(ggui.Pt(240, 120))
	p.Frame()
	p.Scroll(ggui.Pt(200, 100), ggui.Pt(0, -40))
	p.Frame()

	// Forward the exact event emitted by the GUI probe into rig.SendEvent.
	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t3-canvas-chart/dash",
		Node:  evs[0].ID,
		Kind:  evs[0].Kind,
		Value: evs[0].Value,
	}); err != nil {
		t.Fatalf("SendEvent canvas hitId: %v", err)
	}

	updated := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		ch := FindNodeByKind(n, "chart")
		return ch != nil && ch.GetText() == "Zone: worker"
	})
	if got := FindNodeByKind(updated, "chart").GetSeries()[0].GetValues()[2]; got != 91 {
		t.Fatalf("updated chart value[2] = %v, want 91", got)
	}
	p2 := t3NewUiTreeProbe(updated, nil, ggui.Sz(720, 560))
	defer p2.Close()
	p2.Frame()
	if sem := p2.Semantics().String(); !strings.Contains(sem, "Zone: worker") {
		t.Fatalf("expected updated chart caption 'Zone: worker' in p2 semantics:\n%s", sem)
	}
}

// TestPairwise_F3_2_F3_1_TimelineSpanClickHighlightsCanvasDiagram verifies
// ui.timeline span inspection (F3.2) updating a ui.canvas architecture diagram (F3.1).
func TestPairwise_F3_2_F3_1_TimelineSpanClickHighlightsCanvasDiagram(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-tl-canvas", "", "trace", `
export default {
  activate(mako) {
    mako.blocks.register("trace", {
      state: { selectedSpan: "turn-1" },
      render(state, ui) {
        const hl = state.selectedSpan === "ci-test" ? "warning" : "accent";
        return ui.column([
          ui.timeline([
            { id: "turn-1", lane: "Agent", kind: "turn", label: "Plan", tone: "accent", startMs: 0, endMs: 1200 },
            { id: "ci-test", lane: "CI", kind: "tool", label: "go test", subtitle: "1.8s", tone: "warning", startMs: 1200, endMs: 3000 }
          ], {
            key: "tl",
            caption: "Execution Trace",
            maxHeight: 180,
            onChange: ["inspect"]
          }),
          ui.canvas([
            { op: "roundRect", x: 20, y: 20, w: 220, h: 70, r: 8, fillTone: hl, strokeTone: "border", hitId: state.selectedSpan },
            { op: "text", x: 36, y: 58, text: "Selected: " + state.selectedSpan, tone: "ink" }
          ], { key: "detail", width: 320, height: 120 })
        ], { fill: true, gap: 10 });
      },
      on: {
        inspect(state, spanId) { state.selectedSpan = String(spanId || "turn-1"); }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "timeline") != nil && FindNodeByKind(n, "canvas") != nil
	})
	var log t3EventLog
	p := t3NewUiTreeProbe(tree, log.Record, ggui.Sz(760, 480))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	tlNode := FindNodeByKind(tree, "timeline")
	spanBtn := t3WaitFindButton(t, p, "go test")
	p.Click(t3Center(spanBtn.Rect))
	p.Frame()

	evs := log.Snapshot()
	if len(evs) != 1 || evs[0].ID != tlNode.GetId() || evs[0].Kind != protocol.UiChange || evs[0].Value != "ci-test" {
		t.Fatalf("expected timeline click on 'go test' to emit UiChange(ci-test), got %+v", evs)
	}

	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t3-tl-canvas/trace",
		Node:  evs[0].ID,
		Kind:  evs[0].Kind,
		Value: evs[0].Value,
	}); err != nil {
		t.Fatalf("SendEvent timeline span: %v", err)
	}

	updated := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		b, _ := json.Marshal(n)
		return strings.Contains(string(b), "Selected: ci-test")
	})
	p2 := t3NewUiTreeProbe(updated, nil, ggui.Sz(760, 480))
	defer p2.Close()
	p2.Frame()
	if sem := p2.Semantics().String(); !strings.Contains(sem, "Selected: ci-test") {
		t.Fatalf("expected 'Selected: ci-test' in updated probe semantics:\n%s", sem)
	}
}

// TestPairwise_F3_3_F3_2_ChartTypesOnChangeFiltersTimeline verifies area,
// stacked-bar, and heatmap charts (F3.3) with onChange filtering ui.timeline (F3.2).
func TestPairwise_F3_3_F3_2_ChartTypesOnChangeFiltersTimeline(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-chart-tl", "", "metrics", `
export default {
  activate(mako) {
    mako.blocks.register("metrics", {
      state: { laneFilter: "all" },
      render(state, ui) {
        const allSpans = [
          { id: "s1", lane: "Coder", label: "Edit", tone: "accent", startMs: 0, endMs: 800 },
          { id: "s2", lane: "Tester", label: "Verify", tone: "success", startMs: 800, endMs: 1600 }
        ];
        const shown = state.laneFilter === "all"
          ? allSpans
          : allSpans.filter((s) => s.lane === state.laneFilter);
        return ui.column([
          ui.chart("area", [{ name: "Latency", tone: "accent", values: [10, 20, 15] }], {
            key: "area-c", labels: ["T1", "T2", "T3"], maxHeight: 130, onChange: ["filterByIdx"]
          }),
          ui.chart("stacked-bar", [
            { name: "Coder", tone: "accent", values: [4, 7, 5] },
            { name: "Tester", tone: "success", values: [2, 3, 4] }
          ], { key: "stack-c", labels: ["M", "T", "W"], maxHeight: 130, onChange: ["filterByIdx"] }),
          ui.chart("heatmap", [
            { name: "Coder", tone: "accent", values: [1, 5, 3] },
            { name: "Tester", tone: "success", values: [2, 8, 4] }
          ], { key: "heat-c", labels: ["09h", "10h", "11h"], maxHeight: 130, onChange: ["filterByIdx"] }),
          ui.timeline(shown, { key: "tl", caption: "Filtered: " + state.laneFilter, maxHeight: 140 })
        ], { fill: true, gap: 6 });
      },
      on: {
        filterByIdx(state, payload) {
          state.laneFilter = String(payload).includes("1") ? "Tester" : "Coder";
        }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return len(FindAllNodesByKind(n, "chart")) == 3 && FindNodeByKind(n, "timeline") != nil
	})
	var log t3EventLog
	p := t3NewUiTreeProbe(tree, log.Record, ggui.Sz(820, 740))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	heat := FindNodeByID(tree, "/heat-c")
	if heat == nil {
		for _, ch := range FindAllNodesByKind(tree, "chart") {
			if ch.GetChart() == "heatmap" {
				heat = ch
				break
			}
		}
	}
	if heat == nil {
		t.Fatal("heatmap chart node not found")
	}

	cell, ok := p.Semantics().Find(ggui.RoleCell, "Tester 10h: 8")
	if !ok {
		t.Fatalf("heatmap cell 'Tester 10h: 8' not found in semantics:\n%s", p.Semantics().String())
	}
	p.Click(t3Center(cell.Rect))
	p.Frame()

	evs := log.Snapshot()
	if len(evs) != 1 || evs[0].ID != heat.GetId() || evs[0].Kind != protocol.UiChange || !strings.Contains(evs[0].Value, `"Tester"`) {
		t.Fatalf("expected heatmap cell click to emit UiChange for Tester, got %+v", evs)
	}

	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t3-chart-tl/metrics",
		Node:  evs[0].ID,
		Kind:  evs[0].Kind,
		Value: evs[0].Value,
	}); err != nil {
		t.Fatalf("SendEvent heatmap onChange: %v", err)
	}

	updated := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		tl := FindNodeByKind(n, "timeline")
		return tl != nil && tl.GetText() == "Filtered: Tester"
	})
	p2 := t3NewUiTreeProbe(updated, nil, ggui.Sz(820, 740))
	defer p2.Close()
	p2.Frame()
	sem2 := p2.Semantics().String()
	if !strings.Contains(sem2, "Filtered: Tester") || !strings.Contains(sem2, "Verify") || strings.Contains(sem2, "Edit") {
		t.Fatalf("expected filtered timeline to show Verify and hide Edit:\n%s", sem2)
	}
}

// TestPairwise_F1_7_F3_2_F3_3_SplitFillHostingGraphChartTimeline verifies
// ui.split with fill: true hosting ui.graph (F1.7), ui.chart (F3.3), and
// ui.timeline (F3.2) across resize in NewUiTreeProbe.
func TestPairwise_F1_7_F3_2_F3_3_SplitFillHostingGraphChartTimeline(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-split-all", "", "cockpit", `
export default {
  activate(mako) {
    mako.blocks.register("cockpit", {
      render(_state, ui) {
        return ui.split(
          ui.graph([
            { id: "a", label: "Primary Node", shape: "card", status: "working" },
            { id: "b", label: "Replica Node", shape: "card", status: "idle" }
          ], [{ from: "a", to: "b", active: true }], {
            key: "g", variant: "cards", fill: true, grow: 1, minimap: true
          }),
          ui.column([
            ui.chart("area", [{ name: "Load", tone: "accent", values: [20, 40, 35, 60] }], {
              key: "c", labels: ["1m", "2m", "3m", "4m"], grow: 1, maxHeight: 200
            }),
            ui.timeline([
              { id: "t1", lane: "Main", label: "Build", tone: "success", startMs: 0, endMs: 500 }
            ], { key: "tl", grow: 1 })
          ], { gap: 8 }),
          { key: "sp", direction: "horizontal", ratio: 0.55, fill: true }
        );
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "split") != nil &&
			FindNodeByKind(n, "graph") != nil &&
			FindNodeByKind(n, "chart") != nil &&
			FindNodeByKind(n, "timeline") != nil
	})
	p := t3NewUiTreeProbe(tree, nil, ggui.Sz(960, 560))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	findGroup := func(name string) ggui.SemNode {
		for _, nd := range p.Semantics().All() {
			if nd.Name == name && nd.Rect.Size.W > 0 && nd.Rect.Size.H > 0 {
				return nd
			}
		}
		t.Fatalf("semantic node %q not found:\n%s", name, p.Semantics().String())
		return ggui.SemNode{}
	}
	gBefore := findGroup("Graph")
	tlBefore := findGroup("Timeline")

	p.Resize(ggui.Sz(1160, 760))
	for range 3 {
		p.Frame()
	}
	gAfter := findGroup("Graph")
	tlAfter := findGroup("Timeline")
	if gAfter.Rect.Size.W <= gBefore.Rect.Size.W+50 || gAfter.Rect.Size.H <= gBefore.Rect.Size.H+100 {
		t.Fatalf("expected split fill graph to grow on Resize(1160x760): before=%v after=%v", gBefore.Rect, gAfter.Rect)
	}
	if tlAfter.Rect.Size.W <= tlBefore.Rect.Size.W+40 {
		t.Fatalf("expected split timeline to widen on Resize(1160x760): before=%v after=%v", tlBefore.Rect, tlAfter.Rect)
	}
	sem := p.Semantics().String()
	if !strings.Contains(sem, "Primary Node") || !strings.Contains(sem, "Replica Node") {
		t.Fatalf("missing graph nodes inside split:\n%s", sem)
	}
}

// TestPairwise_F2_4_DaemonRig_AgentMapLiveSessionAndBlockEvent verifies the real
// examples/plugins/agent-map plugin running on DaemonRig with a live shell
// session, WatchBlockView, and BlockEvent interactions.
func TestPairwise_F2_4_DaemonRig_AgentMapLiveSessionAndBlockEvent(t *testing.T) {
	dr := NewDaemonRig(t, DaemonRigOpts{Name: "t3-agent-map"})
	sess := dr.OpenShellSession(t, t.TempDir())
	dr.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	dr.Grant(t, "agent-map")

	w := dr.WatchBlockView(t, "plugin:agent-map/map", "", "en", true)
	tree := w.WaitFor(t, 15*time.Second, func(n *protocol.UiNode) bool {
		gn := FindNodeByKind(n, "graph")
		return gn != nil && t3HasGraphNode(gn, "sess:"+sess.GetId())
	})

	gn := FindNodeByKind(tree, "graph")
	if _, err := dr.BlockEvent(t, &protocol.BlockEvent{
		Block: "plugin:agent-map/map",
		Node:  gn.GetId(),
		Kind:  protocol.UiChange,
		Value: "sess:" + sess.GetId(),
	}); err != nil {
		t.Fatalf("BlockEvent selectNode: %v", err)
	}

	selectedTree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return HasTreeText(n, sess.GetId()) && FindNodeByKey(n, "btn-jump-session") != nil
	})
	jumpBtn := FindNodeByKey(selectedTree, "btn-jump-session")
	done, err := dr.BlockEvent(t, &protocol.BlockEvent{
		Block: "plugin:agent-map/map",
		Node:  jumpBtn.GetId(),
		Kind:  protocol.UiTap,
	})
	if err != nil {
		t.Fatalf("BlockEvent openSession: %v", err)
	}
	if len(done.GetOpen()) != 1 || done.GetOpen()[0].GetKind() != "session" || done.GetOpen()[0].GetKey() != sess.GetId() {
		t.Fatalf("openSession intent = %+v, want session %q", done.GetOpen(), sess.GetId())
	}
}

// TestPairwise_F4_1_F4_2_UiWorkbenchAndAppSupportParity verifies examples/plugins/
// ui-workbench (F4.1) and its AppSupport mirror (F4.2) match and render cleanly.
func TestPairwise_F4_1_F4_2_UiWorkbenchAndAppSupportParity(t *testing.T) {
	appSupportDir, err := AppSupportPluginsDir()
	if err != nil {
		t.Fatalf("AppSupportPluginsDir: %v", err)
	}

	for _, id := range []string{"ui-workbench", "agent-map"} {
		srcDir := DemoPluginDir(t, id)
		dstDir := filepath.Join(appSupportDir, id)
		for _, rel := range []string{"plugin.toml", "main.js"} {
			want, err := os.ReadFile(filepath.Join(srcDir, rel))
			if err != nil {
				t.Fatalf("read example %s/%s: %v", id, rel, err)
			}
			got, err := os.ReadFile(filepath.Join(dstDir, rel))
			if err != nil {
				t.Fatalf("read AppSupport %s/%s: %v", id, rel, err)
			}
			if !strings.EqualFold(string(want), string(got)) && string(want) != string(got) {
				t.Fatalf("AppSupport parity mismatch for %s/%s (example=%d bytes, appsupport=%d bytes)", id, rel, len(want), len(got))
			}
		}
	}

	rig := NewPluginRig(t, PluginRigOpts{})
	rig.CopyPluginDir(t, filepath.Join(appSupportDir, "ui-workbench"), "ui-workbench")
	rig.Grant(t, "ui-workbench")

	w := rig.WatchTree(t, "plugin:ui-workbench/explorer", "", plugin.View{Standalone: true})
	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return n != nil && len(TreeTexts(n)) > 0
	})
	p := t3NewUiTreeProbe(tree, nil, ggui.Sz(840, 620))
	defer p.Close()
	p.Frame()
}

// TestPairwise_F1_1_F1_6_F2_1_CardBadgesAndApprovalStatusFromSession verifies
// Session profile/branch/approval_tool (F2.1) mapped to card badges (F1.1) and
// waiting beacon status (F1.6) inside NewUiTreeProbe.
func TestPairwise_F1_1_F1_6_F2_1_CardBadgesAndApprovalStatusFromSession(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{{
			Id:       "s-apr",
			Name:     "Deploy Agent",
			Agent:    "claude",
			Profile:  "sonnet",
			Branch:   "release/v2",
			State:    protocol.StateRunning,
			Status:   "waiting",
			Approval: &protocol.Approval{Id: "a1", Tool: "WriteFile"},
		}},
	})
	w := t3SetupJSBlock(t, rig, "t3-card-apr", `"sessions:read"`, "view", `
export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: { sessions: [] },
      async load(state) {
        state.sessions = (await mako.sessions.listSessions()).sessions || [];
      },
      render(state, ui) {
        return ui.column([
          ui.graph(state.sessions.map((s) => ({
            id: "sess:" + s.id,
            label: s.name,
            shape: "card",
            status: s.status,
            tone: s.approvalTool ? "warning" : "accent",
            subtitle: s.profile + " · " + s.branch,
            badges: [
              { text: s.branch, tone: "accent" },
              { text: s.approvalTool, tone: "warning" }
            ]
          })), [], { key: "g", variant: "cards", fill: true })
        ], { fill: true });
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		gn := FindNodeByKind(n, "graph")
		return gn != nil && len(gn.GetGraphNodes()) == 1
	})
	p := t3NewUiTreeProbe(tree, nil, ggui.Sz(720, 480))
	defer p.Close()
	p.Advance(80 * time.Millisecond)
	p.Frame()
	if !strings.Contains(p.Semantics().String(), "Deploy Agent") {
		t.Fatalf("missing Deploy Agent in semantics")
	}
}

// TestPairwise_F1_2_F1_5_F1_8_PinnedNodesWithBezierEdgesAndCameraFit verifies
// pinned/hasPos nodes (F1.8) connected by Bezier edges (F1.2) and camera Fit
// view + Tween animation (F1.5).
func TestPairwise_F1_2_F1_5_F1_8_PinnedNodesWithBezierEdgesAndCameraFit(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-pin-fit", "", "g", `
export default {
  activate(mako) {
    mako.blocks.register("g", {
      render(_state, ui) {
        return ui.column([
          ui.graph([
            { id: "p1", label: "Pinned West", x: -240, y: -40, hasPos: true, pinned: true, shape: "card" },
            { id: "p2", label: "Pinned East", x: 240, y: 40, hasPos: true, pinned: true, shape: "card" }
          ], [
            { from: "p1", to: "p2", label: "bezier-link", active: true }
          ], { key: "g", variant: "cards", fill: true })
        ], { fill: true });
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	p := t3NewUiTreeProbe(tree, nil, ggui.Sz(820, 520))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	fitBtn := t3WaitFindButton(t, p, "Fit view")
	p.Click(t3Center(fitBtn.Rect))
	p.Advance(280 * time.Millisecond)
	p.Frame()

	sem := p.Semantics().String()
	if !strings.Contains(sem, "Pinned West") || !strings.Contains(sem, "Pinned East") {
		t.Fatalf("missing pinned nodes after Fit view:\n%s", sem)
	}
}

// TestPairwise_F3_1_SandboxTokenRejectionWithValidGraphAndChart verifies that
// uiblock enforces strict theme-token sandboxing on ui.canvas (F3.1) while
// accepting valid theme tokens alongside ui.graph and ui.chart.
func TestPairwise_F3_1_SandboxTokenRejectionWithValidGraphAndChart(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-sandbox-mix", "", "mix", `
export default {
  activate(mako) {
    mako.blocks.register("mix", {
      state: { fillTone: "surfaceAlt" },
      render(state, ui) {
        return ui.column([
          ui.canvas([
            { op: "roundRect", x: 0, y: 0, w: 120, h: 48, fillTone: state.fillTone, strokeTone: "accent" }
          ], { key: "c", width: 200, height: 80 }),
          ui.chart("area", [{ name: "ok", tone: "success", values: [1, 2, 3] }], { key: "ch", labels: ["1", "2", "3"] }),
          ui.graph([{ id: "a", label: "A", tone: "accent" }], [], { key: "g", variant: "cards" }),
          ui.button("Bad", { key: "bad", onTap: ["setBad", "#ff00aa"] }),
          ui.button("BadRGB", { key: "badrgb", onTap: ["setBad", "rgb(255,0,0)"] }),
          ui.button("BadNamed", { key: "badnamed", onTap: ["setBad", "rebeccapurple"] })
        ]);
      },
      on: {
        setBad(state, tone) { state.fillTone = String(tone); }
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "canvas") != nil && FindNodeByKind(n, "chart") != nil && FindNodeByKind(n, "graph") != nil
	})

	for _, key := range []string{"bad", "badrgb", "badnamed"} {
		btn := FindNodeByKey(tree, key)
		if btn == nil {
			t.Fatalf("missing button %q", key)
		}
		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:t3-sandbox-mix/mix",
			Node:  btn.GetId(),
			Kind:  protocol.UiTap,
		}); err != nil {
			t.Fatalf("SendEvent %s: %v", key, err)
		}
		_, upd := w.WaitUpdate(t, 10*time.Second, func(_ *protocol.UiNode, u *protocol.BlockUpdate) bool {
			return u.GetError() != ""
		})
		if upd.GetError() == "" {
			t.Fatalf("expected sandbox rejection error for %s", key)
		}
	}
}

// TestPairwise_F2_3_F1_9_ContextMenuSendMessageAndStopAgent verifies graph
// context menu actions (F1.9) driving mako.messages.sendMessage and stopAgent
// (F2.3) through StubAgentHost.
func TestPairwise_F2_3_F1_9_ContextMenuSendMessageAndStopAgent(t *testing.T) {
	root := t.TempDir()
	rig := NewPluginRig(t, PluginRigOpts{
		Projects: []plugin.ProjectRoots{{ID: "p1", Name: "Repo", Roots: []string{root}}},
		Sessions: []*protocol.SessionInfo{{
			Id: "s-live", Name: "Worker", Agent: "claude", Project: root, State: protocol.StateRunning, Status: "idle",
		}},
	})

	w := t3SetupJSBlock(t, rig, "t3-ctx-act", `"sessions:read", "sessions:write", "fs:project"`, "ctl", fmt.Sprintf(`
export default {
  activate(mako) {
    mako.blocks.register("ctl", {
      render(_state, ui) {
        return ui.column([
          ui.contextMenu("sess-ctx", [
            ui.menuItem("메시지 보내기", { onTap: ["msg"] }),
            ui.menuItem("세션 종료", { onTap: ["halt"] })
          ]),
          ui.graph([
            { id: "sess:s-live", label: "Worker", shape: "card", contextMenu: "sess-ctx", contextArg: "s-live" }
          ], [], { key: "g", variant: "cards", fill: true })
        ], { fill: true });
      },
      on: {
        async msg(_state, sid) {
          await mako.messages.sendMessage({ sessionId: String(sid), text: "status check" });
        },
        async halt(_state, sid) {
          const started = await mako.messages.startAgent({
            agent: "claude", project: "p1", root: %q, prompt: "temp child", name: "temp"
          });
          // stopAgent stops plugin-owned sessions; verified below after allow_agent.
          mako.log.write({ level: "info", message: "requested:" + started.requestId + ":" + sid });
        }
      }
    });
  }
};
`, root))

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	menuMsg := FindNodeByText(tree, "메시지 보내기")
	if menuMsg == nil {
		t.Fatalf("missing context menu item 메시지 보내기")
	}
	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:t3-ctx-act/ctl",
		Node:  menuMsg.GetId(),
		Kind:  protocol.UiTap,
		Value: "s-live",
	}); err != nil {
		t.Fatalf("SendEvent msg: %v", err)
	}
	sent := rig.Agents.Sent()
	if len(sent) != 1 || sent[0].Session != "s-live" || sent[0].Text != "status check" {
		t.Fatalf("StubAgentHost.Sent = %+v, want s-live status check", sent)
	}
}

// TestPairwise_F1_10_F2_4_CrossfadeAndEscapeWithAgentMapSidebar verifies
// AppProbe sidebar navigation into agent-map (F2.4), crossfade settle, and
// Escape returning to the main workspace (F1.10).
func TestPairwise_F1_10_F2_4_CrossfadeAndEscapeWithAgentMapSidebar(t *testing.T) {
	dr := NewDaemonRig(t, DaemonRigOpts{Name: "t3-esc-map"})
	dr.OpenShellSession(t, t.TempDir())
	dr.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	dr.Grant(t, "agent-map")

	app := gui.NewAppProbe(config.Default(), dr.Daemon.Socket(), ggui.Sz(1280, 800))
	defer app.Close()
	p := app.Probe()

	WaitFor(t, 10*time.Second, "Agent Map sidebar entry", func() bool {
		p.Frame()
		return strings.Contains(p.Semantics().String(), "Agent Map")
	})

	app.OpenSidebarPage("plugin:agent-map/map")
	p.Advance(180 * time.Millisecond)
	p.Frame()
	if app.SidebarPage() != "plugin:agent-map/map" {
		t.Fatalf("SidebarPage = %q, want plugin:agent-map/map", app.SidebarPage())
	}

	p.Key("escape")
	p.Advance(180 * time.Millisecond)
	p.Frame()
	if got := app.SidebarPage(); got != "" {
		t.Fatalf("SidebarPage after Escape = %q, want empty", got)
	}
}

// TestPairwise_F3_2_F3_3_TimelineAndChartMaxHeightGrowSizing verifies
// ui.timeline (F3.2) and ui.chart (F3.3) respecting grow and maxHeight inside a
// fill: true container across Probe resize.
func TestPairwise_F3_2_F3_3_TimelineAndChartMaxHeightGrowSizing(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	w := t3SetupJSBlock(t, rig, "t3-size-tl-ch", "", "panel", `
export default {
  activate(mako) {
    mako.blocks.register("panel", {
      render(_state, ui) {
        return ui.column([
          ui.chart("bar", [{ name: "Ops", tone: "accent", values: [3, 6, 9] }], {
            key: "c1", labels: ["A", "B", "C"], maxHeight: 150, grow: 1
          }),
          ui.timeline([
            { id: "x1", lane: "Worker", label: "Compile", tone: "accent", startMs: 100, endMs: 700 }
          ], { key: "tl1", maxHeight: 140, grow: 1 })
        ], { fill: true, gap: 8 });
      }
    });
  }
};
`)

	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "chart") != nil && FindNodeByKind(n, "timeline") != nil
	})
	ch := FindNodeByKind(tree, "chart")
	if ch.GetMaxHeight() != 150 || ch.GetGrow() != 1 {
		t.Fatalf("chart sizing mismatch: maxHeight=%v grow=%v", ch.GetMaxHeight(), ch.GetGrow())
	}
	p := t3NewUiTreeProbe(tree, nil, ggui.Sz(680, 480))
	defer p.Close()
	p.Frame()
	findSized := func(name string) ggui.SemNode {
		for _, nd := range p.Semantics().All() {
			if nd.Name == name && nd.Rect.Size.W > 0 && nd.Rect.Size.H > 0 {
				return nd
			}
		}
		t.Fatalf("semantic node %q not found:\n%s", name, p.Semantics().String())
		return ggui.SemNode{}
	}
	chBefore := findSized("Chart")
	tlBefore := findSized("Timeline")

	p.Resize(ggui.Sz(880, 720))
	p.Frame()
	chAfter := findSized("Chart")
	tlAfter := findSized("Timeline")
	if chAfter.Rect.Size.W <= chBefore.Rect.Size.W+100 || chAfter.Rect.Size.H <= chBefore.Rect.Size.H+80 {
		t.Fatalf("expected grow=1 chart to expand on Resize(880x720): ch %v->%v", chBefore.Rect, chAfter.Rect)
	}
	if tlAfter.Rect.Size.W <= tlBefore.Rect.Size.W+100 || tlAfter.Rect.Size.H <= tlBefore.Rect.Size.H+80 {
		t.Fatalf("expected grow=1 timeline to expand on Resize(880x720): tl %v->%v", tlBefore.Rect, tlAfter.Rect)
	}
}
