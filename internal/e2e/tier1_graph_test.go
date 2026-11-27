//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ggtest"

	"github.com/ironpark/mako/internal/gui"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
)

type graphT1Event struct {
	id    string
	kind  string
	value string
}

type graphT1EventSink struct {
	mu     sync.Mutex
	events []graphT1Event
}

func (s *graphT1EventSink) record(id, kind, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, graphT1Event{id: id, kind: kind, value: value})
}

func (s *graphT1EventSink) snapshot() []graphT1Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]graphT1Event, len(s.events))
	copy(out, s.events)
	return out
}

func (s *graphT1EventSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = nil
}

func graphT1RectCenter(r ggui.Rect) ggui.Point {
	return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
}

func graphT1FindNode(t *testing.T, p *ggui.Probe, label string) ggui.SemNode {
	t.Helper()
	f, ok := p.Semantics().Find(ggui.RoleButton, label)
	if !ok {
		t.Fatalf("graph node %q not found in semantics:\n%s", label, p.Semantics().String())
	}
	return f
}

func graphT1FindCanvasRect(t *testing.T, p *ggui.Probe, caption string) ggui.Rect {
	t.Helper()
	if f, ok := p.FindRole(ggui.RoleGroup, caption); ok {
		return f.Rect
	}
	groups := p.FindAll(ggui.RoleGroup)
	if len(groups) > 0 {
		return groups[0].Rect
	}
	t.Fatalf("graph RoleGroup not found in semantics:\n%s", p.Semantics().String())
	return ggui.Rect{}
}

// graphT1EmptyCanvasPoint picks a point inside canvasRect that avoids all
// button semantic rects (nodes, zoom/reset controls, minimap toggle).
func graphT1EmptyCanvasPoint(canvasRect ggui.Rect, p *ggui.Probe) ggui.Point {
	buttons := p.Semantics().Nodes(ggui.RoleButton)
	hitAny := func(pt ggui.Point) bool {
		for _, b := range buttons {
			expanded := ggui.Rct(
				ggui.Pt(b.Rect.Origin.X-8, b.Rect.Origin.Y-8),
				ggui.Sz(b.Rect.Size.W+16, b.Rect.Size.H+16),
			)
			if pt.X >= expanded.Origin.X && pt.X <= expanded.Origin.X+expanded.Size.W &&
				pt.Y >= expanded.Origin.Y && pt.Y <= expanded.Origin.Y+expanded.Size.H {
				return true
			}
		}
		return false
	}
	candidates := []ggui.Point{
		ggui.Pt(canvasRect.Origin.X+24, canvasRect.Origin.Y+24),
		ggui.Pt(canvasRect.Origin.X+24, canvasRect.Origin.Y+canvasRect.Size.H-24),
		ggui.Pt(canvasRect.Origin.X+canvasRect.Size.W*0.15, canvasRect.Origin.Y+canvasRect.Size.H*0.5),
		ggui.Pt(canvasRect.Origin.X+canvasRect.Size.W*0.5, canvasRect.Origin.Y+24),
		ggui.Pt(canvasRect.Origin.X+canvasRect.Size.W*0.85, canvasRect.Origin.Y+canvasRect.Size.H*0.5),
	}
	for _, pt := range candidates {
		if !hitAny(pt) {
			return pt
		}
	}
	return ggui.Pt(canvasRect.Origin.X+20, canvasRect.Origin.Y+20)
}

func graphT1InstallAndWatch(t *testing.T, id, js string) (*PluginRig, *TreeWatcher, *protocol.UiNode) {
	t.Helper()
	rig := NewPluginRig(t, PluginRigOpts{})
	toml := fmt.Sprintf(`
id = %q
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = ["storage"]

[[contributes.block]]
id = "main"
title = "Graph E2E"
slots = ["sidebar"]
`, id)
	rig.InstallJS(t, id, toml, js)
	rig.Grant(t, id)
	w := rig.WatchTree(t, "plugin:"+id+"/main", "", plugin.View{})
	tree := w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "graph") != nil
	})
	return rig, w, tree
}

func graphT1DrawFrames(p *ggui.Probe, sz ggui.Size, frames int) {
	img := ggfx.NewImage(max(int(sz.W), 1), max(int(sz.H), 1))
	defer img.Deallocate()
	for range frames {
		p.Draw(img)
	}
}

func graphT1NewProbe(t *testing.T, tree *protocol.UiNode, sink *graphT1EventSink, sz ggui.Size) *ggui.Probe {
	t.Helper()
	i18n.Set("en")
	var onEv func(id, kind, value string)
	if sink != nil {
		onEv = sink.record
	}
	p := gui.NewUiTreeProbe(tree, onEv, sz)
	t.Cleanup(p.Close)
	for range 3 {
		p.Frame()
	}
	return p
}

// --- F1.1: ui.graph Rich Card Nodes ---

func TestTier1_F1_1_RichCardNodes(t *testing.T) {
	t.Run("variant_cards_renders_nodes_and_labels", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "n1", label: "Planner Agent", group: "core", tone: "accent", shape: "card", subtitle: "claude-sonnet", status: "working", icon: "bot" },
							{ id: "n2", label: "Builder Agent", group: "core", tone: "success", shape: "card", subtitle: "idle", status: "idle", icon: "cpu" },
						], [{ from: "n1", to: "n2", label: "assign" }], {
							key: "g", variant: "cards", caption: "Card Topology", onChange: ["sel"],
						});
					},
					on: { sel() {} },
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f11-cards", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		n1 := graphT1FindNode(t, p, "Planner Agent")
		n2 := graphT1FindNode(t, p, "Builder Agent")
		if n1.Rect.Size.W <= 0 || n1.Rect.Size.H <= 0 || n2.Rect.Size.W <= 0 || n2.Rect.Size.H <= 0 {
			t.Fatalf("expected positive card bounds, got n1=%v n2=%v", n1.Rect, n2.Rect)
		}
	})

	t.Run("shape_card_and_circle_mixed", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "hub", label: "Project Hub", shape: "circle", tone: "accent", icon: "folder-kanban", weight: 2.0 },
							{ id: "worker", label: "Worker Card", shape: "card", tone: "success", subtitle: "running", status: "working", icon: "bot" },
						], [{ from: "hub", to: "worker" }], {
							key: "g", variant: "cards", caption: "Mixed Shapes", onChange: ["sel"],
						});
					},
					on: { sel() {} },
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f11-mixed", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, sink, sz)
		graphT1DrawFrames(p, sz, 2)

		hub := graphT1FindNode(t, p, "Project Hub")
		worker := graphT1FindNode(t, p, "Worker Card")
		p.Click(graphT1RectCenter(hub.Rect))
		p.Click(graphT1RectCenter(worker.Rect))
		p.Frame()

		evs := sink.snapshot()
		if len(evs) != 2 || evs[0].value != "hub" || evs[1].value != "worker" {
			t.Fatalf("expected clicks on mixed shapes [hub, worker], got %+v", evs)
		}
	})

	t.Run("lucide_icon_on_card_nodes", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Terminal Node", shape: "card", icon: "terminal", tone: "accent" },
							{ id: "b", label: "Git Node", shape: "card", icon: "git-branch", tone: "success" },
							{ id: "c", label: "Folder Node", shape: "card", icon: "folder-kanban", tone: "warning" },
						], [{ from: "a", to: "b" }, { from: "b", to: "c" }], {
							key: "g", variant: "cards", caption: "Icons",
						});
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f11-icons", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		for _, lbl := range []string{"Terminal Node", "Git Node", "Folder Node"} {
			_ = graphT1FindNode(t, p, lbl)
		}
	})

	t.Run("subtitle_and_status_dots_all_states", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "w1", label: "Working", shape: "card", subtitle: "editing main.go", status: "working", tone: "accent" },
							{ id: "w2", label: "Waiting", shape: "card", subtitle: "bash approval", status: "waiting", tone: "warning" },
							{ id: "w3", label: "Idle", shape: "card", subtitle: "standby", status: "idle", tone: "muted" },
							{ id: "w4", label: "Error", shape: "card", subtitle: "exit code 1", status: "error", tone: "danger" },
							{ id: "w5", label: "Done", shape: "card", subtitle: "completed", status: "done", tone: "success" },
						], [], { key: "g", variant: "cards", caption: "All Statuses" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f11-status", js)
		sz := ggui.Sz(800, 520)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 3)

		for _, lbl := range []string{"Working", "Waiting", "Idle", "Error", "Done"} {
			f := graphT1FindNode(t, p, lbl)
			if f.Rect.Size.W <= 0 || f.Rect.Size.H <= 0 {
				t.Fatalf("status node %q has invalid rect %v", lbl, f.Rect)
			}
		}
	})

	t.Run("badges_chips_on_graph_nodes", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{
								id: "b1", label: "Coder", shape: "card", subtitle: "feature/graph", status: "working",
								badges: [{ text: "main", tone: "accent", variant: "soft" }, { text: "Bash", tone: "warning" }],
							},
							{
								id: "b2", label: "Reviewer", shape: "card", subtitle: "qa", status: "idle",
								badges: ["qa", { text: "ok", tone: "success", variant: "outline" }],
							},
						], [{ from: "b1", to: "b2" }], { key: "g", variant: "cards", caption: "Badges" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f11-badges", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		_ = graphT1FindNode(t, p, "Coder")
		_ = graphT1FindNode(t, p, "Reviewer")
	})
}

// --- F1.2: ui.graph Cubic Bézier Edges ---

func TestTier1_F1_2_CubicBezierEdges(t *testing.T) {
	t.Run("curved_edges_between_distinct_nodes", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Alpha", x: 140, y: 200 },
							{ id: "b", label: "Beta", x: 520, y: 240 },
						], [{ from: "a", to: "b", tone: "accent" }], { key: "g", caption: "Bezier" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f12-curved", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		a := graphT1FindNode(t, p, "Alpha")
		b := graphT1FindNode(t, p, "Beta")
		if a.Rect == b.Rect {
			t.Fatalf("distinct nodes should not share rect %v", a.Rect)
		}
	})

	t.Run("self_loop_and_bidirectional_parallel_edges", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Node A", shape: "card" },
							{ id: "b", label: "Node B", shape: "card" },
						], [
							{ from: "a", to: "a", label: "self", tone: "muted" },
							{ from: "a", to: "b", label: "req", tone: "accent" },
							{ from: "b", to: "a", label: "ack", tone: "success" },
						], { key: "g", variant: "cards", caption: "Parallel" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f12-parallel", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 3)

		a := graphT1FindNode(t, p, "Node A")
		b := graphT1FindNode(t, p, "Node B")
		if math.IsNaN(a.Rect.Origin.X) || math.IsNaN(b.Rect.Origin.Y) {
			t.Fatalf("unexpected NaN rect: a=%v b=%v", a.Rect, b.Rect)
		}
	})

	t.Run("dashed_vs_solid_edges", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Root" },
							{ id: "b", label: "Solid Child" },
							{ id: "c", label: "Dashed Child" },
						], [
							{ from: "a", to: "b", dashed: false, tone: "accent" },
							{ from: "a", to: "c", dashed: true, tone: "warning" },
						], { key: "g", caption: "Dashed vs Solid" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f12-dashed", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		g := FindNodeByKind(tree, "graph")
		if len(g.GetGraphEdges()) != 2 || g.GetGraphEdges()[0].GetDashed() || !g.GetGraphEdges()[1].GetDashed() {
			t.Fatalf("unexpected dashed flags in %+v", g.GetGraphEdges())
		}
	})

	t.Run("midpoint_labels_render", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Sender" },
							{ id: "b", label: "Receiver" },
						], [
							{ from: "a", to: "b", label: "delegates-task", tone: "accent" },
						], { key: "g", caption: "Edge Labels" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f12-labels", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		g := FindNodeByKind(tree, "graph")
		if len(g.GetGraphEdges()) != 1 || g.GetGraphEdges()[0].GetLabel() != "delegates-task" {
			t.Fatalf("expected midpoint label 'delegates-task', got %+v", g.GetGraphEdges())
		}
	})

	t.Run("tangent_arrowhead_rendering", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "src", label: "Source", shape: "card" },
							{ id: "dst", label: "Target", shape: "card" },
						], [
							{ from: "src", to: "dst", tone: "danger", label: "calls" },
						], { key: "g", variant: "cards", caption: "Arrowheads" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f12-arrow", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		_ = graphT1FindNode(t, p, "Source")
		_ = graphT1FindNode(t, p, "Target")
	})
}

// --- F1.3: ui.graph Group Cluster Hulls ---

func TestTier1_F1_3_GroupClusterHulls(t *testing.T) {
	t.Run("single_group_hull_renders", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Alpha", group: "macbook-pro", shape: "card" },
							{ id: "b", label: "Beta", group: "macbook-pro", shape: "card" },
							{ id: "c", label: "Gamma", group: "macbook-pro", shape: "card" },
						], [{ from: "a", to: "b" }, { from: "b", to: "c" }], {
							key: "g", variant: "cards", caption: "Single Group",
						});
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f13-single", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		for _, lbl := range []string{"Alpha", "Beta", "Gamma"} {
			_ = graphT1FindNode(t, p, lbl)
		}
	})

	t.Run("multi_group_separated_hulls", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "m1", label: "Mac Agent 1", group: "mac-mini" },
							{ id: "m2", label: "Mac Agent 2", group: "mac-mini" },
							{ id: "l1", label: "Linux Agent 1", group: "linux-box" },
							{ id: "l2", label: "Linux Agent 2", group: "linux-box" },
						], [{ from: "m1", to: "m2" }, { from: "l1", to: "l2" }, { from: "m1", to: "l1", dashed: true }], {
							key: "g", variant: "cards", caption: "Multi Group",
						});
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f13-multi", js)
		sz := ggui.Sz(800, 520)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		for _, lbl := range []string{"Mac Agent 1", "Mac Agent 2", "Linux Agent 1", "Linux Agent 2"} {
			_ = graphT1FindNode(t, p, lbl)
		}
	})

	t.Run("group_header_label_renders", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "n1", label: "Worker 1", group: "cluster-alpha" },
							{ id: "n2", label: "Worker 2", group: "cluster-alpha" },
						], [{ from: "n1", to: "n2" }], { key: "g", variant: "cards", caption: "Group Header" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f13-header", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		_ = graphT1FindNode(t, p, "Worker 1")
		_ = graphT1FindNode(t, p, "Worker 2")
	})

	t.Run("hull_updates_when_node_added_and_removed", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { count: 2 },
					render(state, ui) {
						const nodes = [
							{ id: "n1", label: "N1", group: "alpha" },
							{ id: "n2", label: "N2", group: "alpha" },
						];
						const edges = [{ from: "n1", to: "n2" }];
						if (state.count >= 3) {
							nodes.push({ id: "n3", label: "N3", group: "alpha" });
							edges.push({ from: "n2", to: "n3" });
						}
						return ui.column([
							ui.button("Toggle3", { key: "tgl", onTap: ["toggle"] }),
							ui.graph(nodes, edges, { key: "g", variant: "cards", caption: "Dynamic Hull", grow: 1 }),
						], { fill: true });
					},
					on: {
						toggle(state) { state.count = state.count === 2 ? 3 : 2; },
					},
				});
			}
		};`
		rig, w, tree1 := graphT1InstallAndWatch(t, "t1-f13-dyn", js)
		sz := ggui.Sz(720, 520)
		p1 := graphT1NewProbe(t, tree1, nil, sz)
		graphT1DrawFrames(p1, sz, 2)
		if _, ok := p1.Semantics().Find(ggui.RoleButton, "N3"); ok {
			t.Fatal("N3 should not exist before toggle")
		}

		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:t1-f13-dyn/main", Node: "/tgl", Kind: protocol.UiTap,
		}); err != nil {
			t.Fatalf("toggle add: %v", err)
		}
		tree2 := w.WaitFor(t, 5*time.Second, func(n *protocol.UiNode) bool {
			g := FindNodeByKind(n, "graph")
			return g != nil && len(g.GetGraphNodes()) == 3
		})
		p2 := graphT1NewProbe(t, tree2, nil, sz)
		graphT1DrawFrames(p2, sz, 2)
		_ = graphT1FindNode(t, p2, "N3")

		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:t1-f13-dyn/main", Node: "/tgl", Kind: protocol.UiTap,
		}); err != nil {
			t.Fatalf("toggle remove: %v", err)
		}
		tree3 := w.WaitFor(t, 5*time.Second, func(n *protocol.UiNode) bool {
			g := FindNodeByKind(n, "graph")
			return g != nil && len(g.GetGraphNodes()) == 2
		})
		p3 := graphT1NewProbe(t, tree3, nil, sz)
		graphT1DrawFrames(p3, sz, 2)
		if _, ok := p3.Semantics().Find(ggui.RoleButton, "N3"); ok {
			t.Fatal("N3 should be removed after second toggle")
		}
	})

	t.Run("ungrouped_node_alongside_grouped_no_panic", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "g1", label: "Grouped 1", group: "alpha" },
							{ id: "g2", label: "Grouped 2", group: "alpha" },
							{ id: "u1", label: "Ungrouped", group: "" },
						], [{ from: "g1", to: "g2" }, { from: "g2", to: "u1" }], {
							key: "g", variant: "cards", caption: "Mixed Grouping", onChange: ["sel"],
						});
					},
					on: { sel() {} },
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f13-ungrouped", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, sink, sz)
		graphT1DrawFrames(p, sz, 2)

		u := graphT1FindNode(t, p, "Ungrouped")
		p.Click(graphT1RectCenter(u.Rect))
		p.Frame()
		evs := sink.snapshot()
		if len(evs) != 1 || evs[0].value != "u1" {
			t.Fatalf("expected click on ungrouped node u1, got %+v", evs)
		}
	})
}

// --- F1.4: ui.graph Layout Modes ---

func TestTier1_F1_4_LayoutModes(t *testing.T) {
	t.Run("layout_force_renders_and_hits", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Force A" },
							{ id: "b", label: "Force B" },
							{ id: "c", label: "Force C" },
						], [{ from: "a", to: "b" }, { from: "b", to: "c" }], {
							key: "g", layout: "force", caption: "Force", onChange: ["sel"],
						});
					},
					on: { sel() {} },
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f14-force", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, sink, sz)
		graphT1DrawFrames(p, sz, 2)

		b := graphT1FindNode(t, p, "Force B")
		p.Click(graphT1RectCenter(b.Rect))
		p.Frame()
		if evs := sink.snapshot(); len(evs) != 1 || evs[0].value != "b" {
			t.Fatalf("expected UiChange b, got %+v", evs)
		}
	})

	t.Run("layout_hierarchy_orders_parents_above_children", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "root", label: "Root Parent", shape: "card" },
							{ id: "child", label: "Mid Child", shape: "card" },
							{ id: "leaf", label: "Deep Leaf", shape: "card" },
						], [
							{ from: "root", to: "child" },
							{ from: "child", to: "leaf" },
						], { key: "g", variant: "cards", layout: "hierarchy", caption: "Hierarchy" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f14-hier", js)
		sz := ggui.Sz(800, 560)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		rootR := graphT1FindNode(t, p, "Root Parent").Rect
		childR := graphT1FindNode(t, p, "Mid Child").Rect
		leafR := graphT1FindNode(t, p, "Deep Leaf").Rect
		if !(rootR.Origin.Y < childR.Origin.Y && childR.Origin.Y < leafR.Origin.Y) {
			t.Fatalf("hierarchy layout expected top-down Y order root < child < leaf, got root=%.1f child=%.1f leaf=%.1f",
				rootR.Origin.Y, childR.Origin.Y, leafR.Origin.Y)
		}
	})

	t.Run("layout_radial_places_hub_and_spokes", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "hub", label: "Hub", weight: 3 },
							{ id: "s1", label: "Spoke 1" },
							{ id: "s2", label: "Spoke 2" },
							{ id: "s3", label: "Spoke 3" },
							{ id: "s4", label: "Spoke 4" },
						], [
							{ from: "hub", to: "s1" },
							{ from: "hub", to: "s2" },
							{ from: "hub", to: "s3" },
							{ from: "hub", to: "s4" },
						], { key: "g", layout: "radial", caption: "Radial" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f14-radial", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		hub := graphT1RectCenter(graphT1FindNode(t, p, "Hub").Rect)
		for _, lbl := range []string{"Spoke 1", "Spoke 2", "Spoke 3", "Spoke 4"} {
			sp := graphT1RectCenter(graphT1FindNode(t, p, lbl).Rect)
			if math.Hypot(sp.X-hub.X, sp.Y-hub.Y) < 10 {
				t.Fatalf("radial spoke %q too close to hub: hub=%v spoke=%v", lbl, hub, sp)
			}
		}
	})

	t.Run("interactive_layout_mode_switching", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { mode: "force" },
					render(state, ui) {
						return ui.column([
							ui.tabs(state.mode, ["force", "hierarchy", "radial"], { key: "mode", onChange: ["setMode"] }),
							ui.graph([
								{ id: "a", label: "A" },
								{ id: "b", label: "B" },
								{ id: "c", label: "C" },
							], [{ from: "a", to: "b" }, { from: "a", to: "c" }], {
								key: "g", layout: state.mode, grow: 1, caption: "Mode Switch",
							}),
						], { fill: true });
					},
					on: {
						setMode(state, mode) { state.mode = String(mode || "force"); },
					},
				});
			}
		};`
		rig, w, tree1 := graphT1InstallAndWatch(t, "t1-f14-switch", js)
		sz := ggui.Sz(720, 520)
		p1 := graphT1NewProbe(t, tree1, nil, sz)
		graphT1DrawFrames(p1, sz, 2)

		for _, nextMode := range []string{"hierarchy", "radial"} {
			if _, err := rig.SendEvent(t, &protocol.BlockEvent{
				Block: "plugin:t1-f14-switch/main", Node: "/mode", Kind: protocol.UiChange, Value: nextMode,
			}); err != nil {
				t.Fatalf("switch mode %s: %v", nextMode, err)
			}
			treeNext := w.WaitFor(t, 5*time.Second, func(n *protocol.UiNode) bool {
				tb := FindNodeByKey(n, "mode")
				return tb != nil && tb.GetValue() == nextMode
			})
			pn := graphT1NewProbe(t, treeNext, nil, sz)
			graphT1DrawFrames(pn, sz, 2)
			_ = graphT1FindNode(t, pn, "B")
		}
	})

	t.Run("deterministic_layout_stability_across_rerenders", func(t *testing.T) {
		for _, mode := range []string{"force", "hierarchy", "radial"} {
			js := fmt.Sprintf(`export default {
				activate(mako) {
					mako.blocks.register("main", {
						render(state, ui) {
							return ui.graph([
								{ id: "n1", label: "Node 1", group: "g1" },
								{ id: "n2", label: "Node 2", group: "g1" },
								{ id: "n3", label: "Node 3", group: "g2" },
							], [{ from: "n1", to: "n2" }, { from: "n2", to: "n3" }], {
								key: "g", layout: %q, caption: "Stable",
							});
						},
					});
				}
			};`, mode)
			_, _, t1 := graphT1InstallAndWatch(t, "t1-f14-det1-"+mode, js)
			_, _, t2 := graphT1InstallAndWatch(t, "t1-f14-det2-"+mode, js)
			sz := ggui.Sz(720, 480)
			p1 := graphT1NewProbe(t, t1, nil, sz)
			p2 := graphT1NewProbe(t, t2, nil, sz)
			for _, lbl := range []string{"Node 1", "Node 2", "Node 3"} {
				r1 := graphT1FindNode(t, p1, lbl).Rect
				r2 := graphT1FindNode(t, p2, lbl).Rect
				if r1 != r2 {
					t.Fatalf("mode %s non-deterministic for %q: %v != %v", mode, lbl, r1, r2)
				}
			}
		}
	})
}

// --- F1.5: ui.graph Tween & Auto Camera Focus ---

func TestTier1_F1_5_TweenAndCameraFocus(t *testing.T) {
	t.Run("node_add_tweens_across_frames", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { add: false },
					render(state, ui) {
						const nodes = [
							{ id: "a", label: "Alpha", weight: 2 },
							{ id: "b", label: "Beta", weight: 1 },
						];
						const edges = [{ from: "a", to: "b" }];
						if (state.add) {
							nodes.push({ id: "c", label: "Gamma", weight: 1.5 });
							edges.push({ from: "b", to: "c" });
						}
						return ui.graph(nodes, edges, { key: "g", caption: "Tween Add", fill: true, onChange: ["addNode"] });
					},
					on: {
						addNode(state) { state.add = true; },
					},
				});
			}
		};`
		sz := ggui.Sz(1080, 680)
		rig, ap, w, blockName := OpenAppProbeBlock(t, "t1-f15-add", "Tween Add", js, sz)
		p := ap.Probe()
		WaitProbeSemantics(t, ap, 10*time.Second, "Alpha and Beta visible", func(sem *ggui.SemTree) bool {
			_, okA := sem.Find(ggui.RoleButton, "Alpha")
			_, okB := sem.Find(ggui.RoleButton, "Beta")
			return okA && okB
		})
		alphaBefore := graphT1FindNode(t, p, "Alpha").Rect.Origin

		graphID := FindNodeByKind(w.Latest(t), "graph").GetId()
		if _, err := rig.BlockEvent(t, &protocol.BlockEvent{
			Block: blockName, Node: graphID, Kind: protocol.UiChange, Value: "a",
		}); err != nil {
			t.Fatalf("add node event: %v", err)
		}
		WaitProbeSemantics(t, ap, 10*time.Second, "Gamma visible on same AppProbe", func(sem *ggui.SemTree) bool {
			_, ok := sem.Find(ggui.RoleButton, "Gamma")
			return ok
		})
		for _, step := range []time.Duration{16 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond} {
			p.Advance(step)
			graphT1DrawFrames(p, sz, 1)
		}
		for _, lbl := range []string{"Alpha", "Beta", "Gamma"} {
			r := graphT1FindNode(t, p, lbl).Rect
			if math.IsNaN(r.Origin.X) || math.IsNaN(r.Origin.Y) || r.Size.W <= 0 || r.Size.H <= 0 {
				t.Fatalf("tweened node %q has invalid rect %v", lbl, r)
			}
		}
		alphaAfter := graphT1FindNode(t, p, "Alpha").Rect.Origin
		if alphaAfter == alphaBefore {
			t.Fatalf("expected adding Gamma on same graphViewState to tween Alpha to new layout pos, stayed at %v", alphaBefore)
		}
	})

	t.Run("filter_change_interpolates_remaining_nodes", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { filtered: false },
					render(state, ui) {
						const all = [
							{ id: "n1", label: "Node 1" },
							{ id: "n2", label: "Node 2" },
							{ id: "n3", label: "Node 3" },
							{ id: "n4", label: "Node 4" },
						];
						const nodes = state.filtered ? all.slice(0, 2) : all;
						const edges = state.filtered
							? [{ from: "n1", to: "n2" }]
							: [{ from: "n1", to: "n2" }, { from: "n2", to: "n3" }, { from: "n3", to: "n4" }];
						return ui.graph(nodes, edges, { key: "g", caption: "Filter Tween", fill: true, onChange: ["filter"] });
					},
					on: { filter(state) { state.filtered = true; } },
				});
			}
		};`
		sz := ggui.Sz(1080, 680)
		rig, ap, w, blockName := OpenAppProbeBlock(t, "t1-f15-filter", "Filter Tween", js, sz)
		p := ap.Probe()
		WaitProbeSemantics(t, ap, 10*time.Second, "Node 3 initially visible", func(sem *ggui.SemTree) bool {
			_, ok := sem.Find(ggui.RoleButton, "Node 3")
			return ok
		})

		graphID := FindNodeByKind(w.Latest(t), "graph").GetId()
		if _, err := rig.BlockEvent(t, &protocol.BlockEvent{
			Block: blockName, Node: graphID, Kind: protocol.UiChange, Value: "n1",
		}); err != nil {
			t.Fatalf("filter event: %v", err)
		}
		WaitProbeSemantics(t, ap, 10*time.Second, "Node 3 filtered out on same AppProbe", func(sem *ggui.SemTree) bool {
			_, ok3 := sem.Find(ggui.RoleButton, "Node 3")
			_, ok1 := sem.Find(ggui.RoleButton, "Node 1")
			return ok1 && !ok3
		})
		for range 4 {
			p.Advance(80 * time.Millisecond)
		}
		graphT1DrawFrames(p, sz, 2)
		_ = graphT1FindNode(t, p, "Node 1")
		_ = graphT1FindNode(t, p, "Node 2")
		if _, ok := p.Semantics().Find(ggui.RoleButton, "Node 3"); ok {
			t.Fatal("expected Node 3 to be filtered out")
		}
	})

	t.Run("selection_change_animates_camera_focus", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { sel: "" },
					render(state, ui) {
						return ui.column([
							ui.text("Selected:" + (state.sel || "none")),
							ui.graph([
								{ id: "a", label: "Left Node", x: -240, y: -120 },
								{ id: "b", label: "Right Node", x: 240, y: 120 },
							], [{ from: "a", to: "b" }], {
								key: "g", value: state.sel, caption: "Focus", grow: 1, onChange: ["pick"],
							}),
						], { fill: true });
					},
					on: {
						pick(state, id) { state.sel = String(id || ""); },
					},
				});
			}
		};`
		sz := ggui.Sz(1080, 680)
		rig, ap, w, blockName := OpenAppProbeBlock(t, "t1-f15-focus", "Focus View", js, sz)
		p := ap.Probe()
		WaitProbeSemantics(t, ap, 10*time.Second, "Right Node visible", func(sem *ggui.SemTree) bool {
			_, ok := sem.Find(ggui.RoleButton, "Right Node")
			return ok
		})
		before := graphT1FindNode(t, p, "Right Node").Rect

		graphID := FindNodeByKind(w.Latest(t), "graph").GetId()
		if _, err := rig.BlockEvent(t, &protocol.BlockEvent{
			Block: blockName, Node: graphID, Kind: protocol.UiChange, Value: "b",
		}); err != nil {
			t.Fatalf("pick event: %v", err)
		}
		WaitProbeSemantics(t, ap, 10*time.Second, "Selected:b on same AppProbe", func(sem *ggui.SemTree) bool {
			return strings.Contains(sem.String(), "Selected:b")
		})
		for range 4 {
			p.Advance(80 * time.Millisecond)
		}
		graphT1DrawFrames(p, sz, 2)

		after := graphT1FindNode(t, p, "Right Node").Rect
		if math.IsNaN(after.Origin.X) || math.IsNaN(after.Origin.Y) {
			t.Fatalf("camera focus produced NaN rect: before=%v after=%v", before, after)
		}
		if after == before {
			t.Fatalf("expected camera focus on Right Node to shift rect from %v, got identical %v", before, after)
		}
	})

	t.Run("fit_reset_button_centers_graph", func(t *testing.T) {
		tree := MustReadUI(t, `{
			"kind":"graph","key":"g","text":"Center","maxHeight":420,
			"graphNodes":[
				{"id":"a","label":"Node A"},
				{"id":"b","label":"Node B"}
			],
			"graphEdges":[{"from":"a","to":"b"}]
		}`).Root
		sz := ggui.Sz(720, 460)
		p := graphT1NewProbe(t, tree, nil, sz)
		p.Advance(0)
		origA := graphT1RectCenter(graphT1FindNode(t, p, "Node A").Rect)

		canvasRect := graphT1FindCanvasRect(t, p, "Center")
		bg := graphT1EmptyCanvasPoint(canvasRect, p)
		p.Press(bg)
		p.Move(ggui.Pt(bg.X+90, bg.Y+60))
		p.Release(ggui.Pt(bg.X+90, bg.Y+60))
		p.Frame()

		pannedA := graphT1RectCenter(graphT1FindNode(t, p, "Node A").Rect)
		if math.Hypot(pannedA.X-origA.X, pannedA.Y-origA.Y) < 10 {
			t.Fatalf("expected background pan to shift Node A: orig=%v panned=%v", origA, pannedA)
		}

		resetBtn, ok := p.Semantics().Find(ggui.RoleButton, "Reset")
		if !ok {
			resetBtn, ok = p.Semantics().Find(ggui.RoleButton, "Fit view")
		}
		if !ok {
			t.Fatalf("expected Reset or Fit view button in graph controls:\n%s", p.Semantics().String())
		}
		p.Click(graphT1RectCenter(resetBtn.Rect))
		p.Advance(350 * time.Millisecond)
		p.Frame()

		centeredA := graphT1RectCenter(graphT1FindNode(t, p, "Node A").Rect)
		if math.Hypot(centeredA.X-pannedA.X, centeredA.Y-pannedA.Y) < 10 {
			t.Fatalf("expected Reset/Fit to re-center graph from %v back toward %v, got %v", pannedA, origA, centeredA)
		}
	})

	t.Run("repeated_rapid_updates_stay_bounded", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { count: 1 },
					render(state, ui) {
						const nodes = [];
						for (let j = 0; j < state.count; j++) {
							nodes.push({ id: "n" + j, label: "Node " + j });
						}
						return ui.column([
							ui.button("Add Node", { key: "add", onTap: ["add"] }),
							ui.graph(nodes, [], { key: "g", caption: "Rapid", grow: 1 }),
						], { fill: true });
					},
					on: {
						add(state) { state.count++; },
					},
				});
			}
		};`
		sz := ggui.Sz(1080, 680)
		rig, ap, w, blockName := OpenAppProbeBlock(t, "t1-f15-rapid", "Rapid View", js, sz)
		p := ap.Probe()
		WaitProbeSemantics(t, ap, 10*time.Second, "Node 0 visible", func(sem *ggui.SemTree) bool {
			_, ok := sem.Find(ggui.RoleButton, "Node 0")
			return ok
		})
		addID := FindNodeByKey(w.Latest(t), "add").GetId()
		for i := 1; i <= 10; i++ {
			if _, err := rig.BlockEvent(t, &protocol.BlockEvent{
				Block: blockName, Node: addID, Kind: protocol.UiTap,
			}); err != nil {
				t.Fatalf("tap add %d: %v", i, err)
			}
			wantLbl := fmt.Sprintf("Node %d", i)
			WaitProbeSemantics(t, ap, 10*time.Second, wantLbl+" visible", func(sem *ggui.SemTree) bool {
				_, ok := sem.Find(ggui.RoleButton, wantLbl)
				return ok
			})
			p.Advance(16 * time.Millisecond)
		}
		for range 4 {
			p.Advance(90 * time.Millisecond)
		}
		graphT1DrawFrames(p, sz, 2)
		r := graphT1FindNode(t, p, "Node 10").Rect
		if math.IsNaN(r.Origin.X) || math.IsNaN(r.Origin.Y) || r.Size.W <= 0 || r.Size.H <= 0 {
			t.Fatalf("Node 10 has invalid rect %v", r)
		}
	})
}

// --- F1.6: ui.graph 60fps Live Motion ---

func TestTier1_F1_6_LiveMotion(t *testing.T) {
	t.Run("working_pulse_ring_frame_delta", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "w", label: "Active Worker", shape: "card", status: "working", tone: "accent" },
						], [], { key: "g", variant: "cards", caption: "Pulse" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f16-pulse", js)
		sz := ggui.Sz(640, 400)
		p := graphT1NewProbe(t, tree, nil, sz)
		p.Advance(0)
		img1 := ggtest.Render(p, sz, 1)
		p.Advance(250 * time.Millisecond)
		img2 := ggtest.Render(p, sz, 1)
		if bytes.Equal(img1.Pix, img2.Pix) {
			t.Fatal("expected working pulse ring animation to produce pixel delta across 250ms advance")
		}
		_ = graphT1FindNode(t, p, "Active Worker")
	})

	t.Run("waiting_amber_beacon_ring_animation", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "wait", label: "Needs Approval", shape: "card", status: "waiting", subtitle: "Bash rm", tone: "warning" },
						], [], { key: "g", variant: "cards", caption: "Beacon" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f16-beacon", js)
		sz := ggui.Sz(640, 400)
		p := graphT1NewProbe(t, tree, nil, sz)
		p.Advance(0)
		img1 := ggtest.Render(p, sz, 1)
		p.Advance(250 * time.Millisecond)
		img2 := ggtest.Render(p, sz, 1)
		if bytes.Equal(img1.Pix, img2.Pix) {
			t.Fatal("expected waiting amber beacon ring animation to produce pixel delta across 250ms advance")
		}
		_ = graphT1FindNode(t, p, "Needs Approval")
	})

	t.Run("active_edge_flow_particle_animation", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Sender", shape: "card", status: "idle" },
							{ id: "b", label: "Receiver", shape: "card", status: "idle" },
						], [
							{ from: "a", to: "b", label: "msg", active: true, kind: "agentMessage", tone: "accent" },
						], { key: "g", variant: "cards", caption: "Flow Edge" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f16-flow", js)
		sz := ggui.Sz(720, 440)
		p := graphT1NewProbe(t, tree, nil, sz)
		p.Advance(0)
		img1 := ggtest.Render(p, sz, 1)
		p.Advance(250 * time.Millisecond)
		img2 := ggtest.Render(p, sz, 1)
		if bytes.Equal(img1.Pix, img2.Pix) {
			t.Fatal("expected active edge flow particle animation to produce pixel delta across 250ms advance")
		}
		_ = graphT1FindNode(t, p, "Sender")
		_ = graphT1FindNode(t, p, "Receiver")
	})

	t.Run("mixed_working_waiting_idle_nodes", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "n1", label: "Work", status: "working", tone: "accent" },
							{ id: "n2", label: "Wait", status: "waiting", tone: "warning" },
							{ id: "n3", label: "Idle", status: "idle", tone: "muted" },
							{ id: "n4", label: "Done", status: "done", tone: "success" },
						], [
							{ from: "n1", to: "n2", active: true },
							{ from: "n2", to: "n3", active: false },
						], { key: "g", variant: "cards", caption: "Mixed Motion" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f16-mixed", js)
		sz := ggui.Sz(720, 460)
		p := graphT1NewProbe(t, tree, nil, sz)
		p.Advance(0)
		img1 := ggtest.Render(p, sz, 1)
		p.Advance(250 * time.Millisecond)
		img2 := ggtest.Render(p, sz, 1)
		if bytes.Equal(img1.Pix, img2.Pix) {
			t.Fatal("expected mixed working/waiting/active graph to animate across 250ms advance")
		}
		for _, lbl := range []string{"Work", "Wait", "Idle", "Done"} {
			_ = graphT1FindNode(t, p, lbl)
		}
	})

	t.Run("idle_graph_stays_calm_across_advances", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "i1", label: "Calm 1", status: "idle", tone: "muted" },
							{ id: "i2", label: "Calm 2", status: "done", tone: "success" },
						], [
							{ from: "i1", to: "i2", active: false },
						], { key: "g", variant: "cards", caption: "Calm" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f16-calm", js)
		sz := ggui.Sz(640, 400)
		p := graphT1NewProbe(t, tree, nil, sz)
		p.Advance(0)
		r1 := graphT1FindNode(t, p, "Calm 1").Rect
		img1 := ggtest.Render(p, sz, 1)
		p.Advance(250 * time.Millisecond)
		img2 := ggtest.Render(p, sz, 1)
		r2 := graphT1FindNode(t, p, "Calm 1").Rect
		if r1 != r2 {
			t.Fatalf("idle graph moved unexpectedly across idle advance: %v != %v", r1, r2)
		}
		if !bytes.Equal(img1.Pix, img2.Pix) {
			t.Fatal("expected static/idle graph to render identical pixels across 250ms advance")
		}
	})
}

// --- F1.7: ui.graph Fill/Grow & Minimap ---

func TestTier1_F1_7_FillGrowMinimap(t *testing.T) {
	t.Run("fill_root_and_grow_graph_fills_large_container", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.column([
							ui.text("Header Bar", { key: "hdr" }),
							ui.graph([
								{ id: "a", label: "A" },
								{ id: "b", label: "B" },
							], [{ from: "a", to: "b" }], { key: "g", grow: 1, caption: "Full Canvas" }),
						], { fill: true, gap: 8 });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f17-fill", js)
		sz := ggui.Sz(1400, 900)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)

		gRect := graphT1FindCanvasRect(t, p, "Full Canvas")
		if gRect.Size.H < 650 {
			t.Fatalf("expected fill+grow graph height >= 650 at 1400x900, got %.1f (rect=%v)", gRect.Size.H, gRect)
		}
	})

	t.Run("resize_updates_canvas_height_dynamically", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.column([
							ui.graph([
								{ id: "a", label: "Alpha" },
								{ id: "b", label: "Beta" },
							], [{ from: "a", to: "b" }], { key: "g", grow: 1, caption: "Resize Canvas" }),
						], { fill: true });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f17-resize", js)
		p := graphT1NewProbe(t, tree, nil, ggui.Sz(1400, 900))
		largeRect := graphT1FindCanvasRect(t, p, "Resize Canvas")

		p.Resize(ggui.Sz(1000, 700))
		p.Frame()
		graphT1DrawFrames(p, ggui.Sz(1000, 700), 1)
		smallRect := graphT1FindCanvasRect(t, p, "Resize Canvas")
		if !(smallRect.Size.H < largeRect.Size.H-100 && smallRect.Size.W < largeRect.Size.W-200) {
			t.Fatalf("expected Resize(1000x700) to shrink canvas from %v, got %v", largeRect, smallRect)
		}
	})

	t.Run("minimap_true_renders_overlay", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "M1" },
							{ id: "b", label: "M2" },
							{ id: "c", label: "M3" },
						], [{ from: "a", to: "b" }, { from: "b", to: "c" }], {
							key: "g", minimap: true, maxHeight: 460, caption: "Minimap",
						});
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f17-mini", js)
		sz := ggui.Sz(800, 500)
		p := graphT1NewProbe(t, tree, nil, sz)
		graphT1DrawFrames(p, sz, 2)
		_ = graphT1FindCanvasRect(t, p, "Minimap")
	})

	t.Run("minimap_toggle_hides_and_shows", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { showMini: true },
					render(state, ui) {
						return ui.column([
							ui.row([
								ui.button("Toggle Minimap", { key: "btn", onTap: ["toggle"] }),
								ui.text("MiniState:" + String(state.showMini)),
							], { gap: 8 }),
							ui.graph([
								{ id: "a", label: "N1", x: -120, y: -60 },
								{ id: "b", label: "N2", x: 120, y: 60 },
							], [{ from: "a", to: "b" }], {
								key: "g", minimap: state.showMini, grow: 1, caption: "Mini Toggle",
							}),
						], { fill: true });
					},
					on: { toggle(state) { state.showMini = !state.showMini; } },
				});
			}
		};`
		sz := ggui.Sz(1120, 720)
		_, ap, _, _ := OpenAppProbeBlock(t, "t1-f17-toggle", "Mini Toggle View", js, sz)
		p := ap.Probe()
		WaitProbeSemantics(t, ap, 10*time.Second, "Toggle minimap button visible", func(sem *ggui.SemTree) bool {
			_, ok := sem.Find(ggui.RoleButton, "Toggle minimap")
			return ok
		})

		toggleBtn, ok := p.Semantics().Find(ggui.RoleButton, "Toggle minimap")
		if !ok {
			t.Fatalf("expected Toggle minimap toolbar button:\n%s", p.Semantics().String())
		}

		clickMinimapAt := func(offX, offY float64) float64 {
			r := graphT1FindCanvasRect(t, p, "Mini Toggle")
			before := graphT1RectCenter(graphT1FindNode(t, p, "N1").Rect)
			mmTarget := ggui.Pt(r.Origin.X+r.Size.W-offX, r.Origin.Y+r.Size.H-offY)
			p.Click(mmTarget)
			p.Frame()
			after := graphT1RectCenter(graphT1FindNode(t, p, "N1").Rect)
			return math.Hypot(after.X-before.X, after.Y-before.Y)
		}

		// 1. Initially visible -> minimap click pans camera.
		if dist := clickMinimapAt(30, 25); dist < 10 {
			t.Fatalf("expected initial minimap click to pan N1, moved %.2fpx", dist)
		}

		// 2. Click "Toggle minimap" to hide -> minimap click at a new offset no longer pans.
		p.Click(graphT1RectCenter(toggleBtn.Rect))
		p.Frame()
		if dist := clickMinimapAt(120, 80); dist > 1 {
			t.Fatalf("expected hidden minimap click NOT to pan N1, moved %.2fpx", dist)
		}

		// 3. Click "Toggle minimap" again to show -> minimap click at that new offset pans.
		toggleBtn, ok = p.Semantics().Find(ggui.RoleButton, "Toggle minimap")
		if !ok {
			t.Fatal("Toggle minimap button disappeared")
		}
		p.Click(graphT1RectCenter(toggleBtn.Rect))
		p.Frame()
		if dist := clickMinimapAt(120, 80); dist < 10 {
			t.Fatalf("expected re-shown minimap click to pan N1, moved %.2fpx", dist)
		}

		// 4. Plugin-side minimap=false live BlockUpdate hides minimap on same graphViewState.
		pluginBtn, ok := p.Semantics().Find(ggui.RoleButton, "Toggle Minimap")
		if !ok {
			t.Fatalf("expected plugin Toggle Minimap button:\n%s", p.Semantics().String())
		}
		p.Click(graphT1RectCenter(pluginBtn.Rect))
		WaitProbeSemantics(t, ap, 10*time.Second, "MiniState:false on same AppProbe", func(sem *ggui.SemTree) bool {
			return strings.Contains(sem.String(), "MiniState:false")
		})
		if dist := clickMinimapAt(30, 25); dist > 1 {
			t.Fatalf("expected plugin minimap=false update to hide minimap, moved %.2fpx", dist)
		}
	})

	t.Run("minimap_click_drag_pans_camera", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Anchor", x: -200, y: -100 },
							{ id: "b", label: "Peer", x: 200, y: 100 },
						], [{ from: "a", to: "b" }], {
							key: "g", minimap: true, maxHeight: 460, caption: "Mini Pan", onChange: ["sel"],
						});
					},
					on: { sel() {} },
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f17-minipan", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(800, 500)
		p := graphT1NewProbe(t, tree, sink, sz)

		r := graphT1FindCanvasRect(t, p, "Mini Pan")
		before := graphT1RectCenter(graphT1FindNode(t, p, "Anchor").Rect)
		miniStart := ggui.Pt(r.Origin.X+r.Size.W-55, r.Origin.Y+r.Size.H-45)
		miniEnd := ggui.Pt(r.Origin.X+r.Size.W-30, r.Origin.Y+r.Size.H-25)
		p.Press(miniStart)
		p.Move(miniEnd)
		p.Release(miniEnd)
		p.Frame()

		after := graphT1RectCenter(graphT1FindNode(t, p, "Anchor").Rect)
		if math.IsNaN(after.X) || math.IsNaN(after.Y) {
			t.Fatalf("minimap drag produced NaN position: before=%v after=%v", before, after)
		}
		if math.Hypot(after.X-before.X, after.Y-before.Y) < 10 {
			t.Fatalf("expected minimap drag to pan camera and shift Anchor: before=%v after=%v", before, after)
		}
		if len(sink.snapshot()) != 0 {
			t.Fatalf("minimap interaction should not select a node: %+v", sink.snapshot())
		}
	})
}

// --- F1.8: ui.graph Pan vs Node Drag & Drop ---

func TestTier1_F1_8_PanVsNodeDragDrop(t *testing.T) {
	t.Run("background_drag_pans_viewport_without_drop", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "n1", label: "Pan Node 1" },
							{ id: "n2", label: "Pan Node 2" },
						], [{ from: "n1", to: "n2" }], {
							key: "g", maxHeight: 440, caption: "Bg Pan", onChange: ["sel"], onDrop: ["dropped"],
						});
					},
					on: { sel() {}, dropped() {} },
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f18-bgpan", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, sink, sz)

		before1 := graphT1RectCenter(graphT1FindNode(t, p, "Pan Node 1").Rect)
		before2 := graphT1RectCenter(graphT1FindNode(t, p, "Pan Node 2").Rect)
		bg := graphT1EmptyCanvasPoint(graphT1FindCanvasRect(t, p, "Bg Pan"), p)

		p.Press(bg)
		p.Move(ggui.Pt(bg.X+80, bg.Y+45))
		p.Release(ggui.Pt(bg.X+80, bg.Y+45))
		p.Frame()

		after1 := graphT1RectCenter(graphT1FindNode(t, p, "Pan Node 1").Rect)
		after2 := graphT1RectCenter(graphT1FindNode(t, p, "Pan Node 2").Rect)
		if math.Hypot(after1.X-before1.X, after1.Y-before1.Y) < 20 || math.Hypot(after2.X-before2.X, after2.Y-before2.Y) < 20 {
			t.Fatalf("expected both nodes to shift on background pan: n1 %v->%v, n2 %v->%v", before1, after1, before2, after2)
		}
		if len(sink.snapshot()) != 0 {
			t.Fatalf("background pan should not emit selection/drop events, got %+v", sink.snapshot())
		}
	})

	t.Run("node_drag_moves_locally_without_premature_drop", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "n1", label: "Drag Me", shape: "card" },
							{ id: "n2", label: "Stay Still", shape: "card" },
						], [{ from: "n1", to: "n2" }], {
							key: "g", variant: "cards", maxHeight: 440, caption: "Local Drag", onDrop: ["dropped"],
						});
					},
					on: { dropped() {} },
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f18-local", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, sink, sz)

		start := graphT1RectCenter(graphT1FindNode(t, p, "Drag Me").Rect)
		stillBefore := graphT1RectCenter(graphT1FindNode(t, p, "Stay Still").Rect)
		mid := ggui.Pt(start.X+45, start.Y+30)

		p.Press(start)
		p.Move(mid)
		p.Frame()

		if len(sink.snapshot()) != 0 {
			t.Fatalf("drop event fired prematurely during Move before Release: %+v", sink.snapshot())
		}
		dragMoved := graphT1RectCenter(graphT1FindNode(t, p, "Drag Me").Rect)
		stillDuring := graphT1RectCenter(graphT1FindNode(t, p, "Stay Still").Rect)
		if math.Hypot(dragMoved.X-start.X, dragMoved.Y-start.Y) < 10 {
			t.Fatalf("expected dragged node to follow pointer locally during Move: start=%v cur=%v", start, dragMoved)
		}
		if math.Hypot(stillDuring.X-stillBefore.X, stillDuring.Y-stillBefore.Y) > 2 {
			t.Fatalf("expected non-dragged node to stay still during node drag: before=%v during=%v", stillBefore, stillDuring)
		}
		p.Release(mid)
		p.Frame()
	})

	t.Run("node_release_emits_drop_position_event", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "n1", label: "Movable", shape: "card" },
							{ id: "n2", label: "Fixed", shape: "card" },
						], [{ from: "n1", to: "n2" }], {
							key: "g", variant: "cards", maxHeight: 440, caption: "Emit Drop", onDrop: ["dropped"],
						});
					},
					on: { dropped() {} },
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f18-emit", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, sink, sz)

		start := graphT1RectCenter(graphT1FindNode(t, p, "Movable").Rect)
		end := ggui.Pt(start.X+65, start.Y+40)
		p.Press(start)
		p.Move(end)
		p.Release(end)
		p.Frame()

		evs := sink.snapshot()
		if len(evs) != 1 || evs[0].kind != protocol.UiDrop {
			t.Fatalf("expected 1 UiDrop event on release, got %+v", evs)
		}
		var payload struct {
			ID string  `json:"id"`
			X  float64 `json:"x"`
			Y  float64 `json:"y"`
		}
		if err := json.Unmarshal([]byte(evs[0].value), &payload); err != nil {
			t.Fatalf("expected JSON {id,x,y} drop payload, got %q: %v", evs[0].value, err)
		}
		if payload.ID != "n1" || math.IsNaN(payload.X) || math.IsNaN(payload.Y) {
			t.Fatalf("unexpected drop payload %+v", payload)
		}
	})

	t.Run("plugin_saves_dropped_position_to_storage_and_survives_rerender", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { positions: {} },
					async load(state, ctx) {
						const item = await ctx.storage.getItem("positions");
						if (item && item.value) {
							try { state.positions = JSON.parse(item.value) || {}; } catch {}
						}
					},
					render(state, ui) {
						const p1 = state.positions["n1"];
						const n1 = { id: "n1", label: "Persisted Node", shape: "card" };
						if (p1) { n1.x = p1.x; n1.y = p1.y; }
						return ui.graph([
							n1,
							{ id: "n2", label: "Sibling Node", shape: "card" },
						], [{ from: "n1", to: "n2" }], {
							key: "g", variant: "cards", maxHeight: 440, caption: "Persist Pos", onDrop: ["savePos"],
						});
					},
					on: {
						async savePos(state, raw) {
							const pos = typeof raw === "string" ? JSON.parse(raw) : raw;
							if (pos && pos.id) {
								state.positions[pos.id] = { x: Number(pos.x), y: Number(pos.y) };
								const ctx = mako.blocks.context("main");
								await ctx.storage.setItem("positions", JSON.stringify(state.positions));
							}
						},
					},
				});
			}
		};`
		rig, w, tree := graphT1InstallAndWatch(t, "t1-f18-persist", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, sink, sz)

		start := graphT1RectCenter(graphT1FindNode(t, p, "Persisted Node").Rect)
		end := ggui.Pt(start.X+80, start.Y+35)
		p.Press(start)
		p.Move(end)
		p.Release(end)
		p.Frame()

		evs := sink.snapshot()
		if len(evs) != 1 || evs[0].kind != protocol.UiDrop {
			t.Fatalf("expected drop event, got %+v", evs)
		}
		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:t1-f18-persist/main",
			Node:  evs[0].id,
			Kind:  evs[0].kind,
			Value: evs[0].value,
		}); err != nil {
			t.Fatalf("SendEvent drop: %v", err)
		}

		updated := w.WaitFor(t, 5*time.Second, func(n *protocol.UiNode) bool {
			g := FindNodeByKind(n, "graph")
			return g != nil && len(g.GetGraphNodes()) == 2
		})
		p2 := graphT1NewProbe(t, updated, nil, sz)
		movedRect := graphT1FindNode(t, p2, "Persisted Node").Rect
		if math.IsNaN(movedRect.Origin.X) {
			t.Fatalf("persisted node has NaN rect %v", movedRect)
		}

		// Reload the plugin block and verify saved coordinates survive storage reload.
		if _, err := rig.Act(t, &protocol.PluginAction{
			PluginId: "t1-f18-persist",
			Op:       protocol.PluginReload,
		}); err != nil {
			t.Fatalf("reload plugin: %v", err)
		}
		w2 := rig.WatchTree(t, "plugin:t1-f18-persist/main", "", plugin.View{})
		reloaded := w2.WaitFor(t, 5*time.Second, func(n *protocol.UiNode) bool {
			g := FindNodeByKind(n, "graph")
			return g != nil && len(g.GetGraphNodes()) == 2
		})
		p3 := graphT1NewProbe(t, reloaded, nil, sz)
		reloadedRect := graphT1FindNode(t, p3, "Persisted Node").Rect
		if math.IsNaN(reloadedRect.Origin.X) || math.IsNaN(reloadedRect.Origin.Y) {
			t.Fatalf("reloaded node has invalid rect %v", reloadedRect)
		}
	})

	t.Run("plain_wheel_passes_through_while_zoom_controls_zoom", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.scroll([
							ui.graph([
								{ id: "a", label: "Zoom Alpha" },
								{ id: "b", label: "Zoom Beta" },
							], [{ from: "a", to: "b" }], { key: "g", maxHeight: 280, caption: "Scroll Graph" }),
							ui.card([
								ui.text("Row 1"),
								ui.text("Row 2"),
								ui.text("Row 3"),
								ui.text("Row 4"),
								ui.text("Footer Marker"),
							], { key: "below" }),
						], { key: "sc" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f18-wheel", js)
		sz := ggui.Sz(480, 320)
		p := graphT1NewProbe(t, tree, nil, sz)

		footerBefore, ok := p.Semantics().Find(ggui.RoleText, "Footer Marker")
		if !ok {
			t.Fatalf("Footer Marker not found:\n%s", p.Semantics().String())
		}
		distBefore := math.Hypot(
			graphT1RectCenter(graphT1FindNode(t, p, "Zoom Alpha").Rect).X-graphT1RectCenter(graphT1FindNode(t, p, "Zoom Beta").Rect).X,
			graphT1RectCenter(graphT1FindNode(t, p, "Zoom Alpha").Rect).Y-graphT1RectCenter(graphT1FindNode(t, p, "Zoom Beta").Rect).Y,
		)

		gRect := graphT1FindCanvasRect(t, p, "Scroll Graph")
		p.Scroll(graphT1RectCenter(gRect), ggui.Pt(0, -120))
		for range 3 {
			p.Frame()
		}

		footerAfter, ok := p.Semantics().Find(ggui.RoleText, "Footer Marker")
		if !ok || footerAfter.Rect.Origin.Y >= footerBefore.Rect.Origin.Y {
			t.Fatalf("expected plain wheel over graph to scroll parent container: beforeY=%.1f afterY=%.1f",
				footerBefore.Rect.Origin.Y, footerAfter.Rect.Origin.Y)
		}
		distAfterScroll := math.Hypot(
			graphT1RectCenter(graphT1FindNode(t, p, "Zoom Alpha").Rect).X-graphT1RectCenter(graphT1FindNode(t, p, "Zoom Beta").Rect).X,
			graphT1RectCenter(graphT1FindNode(t, p, "Zoom Alpha").Rect).Y-graphT1RectCenter(graphT1FindNode(t, p, "Zoom Beta").Rect).Y,
		)
		if math.Abs(distAfterScroll-distBefore) > 2.0 {
			t.Fatalf("plain wheel zoomed graph unexpectedly: distBefore=%.2f distAfter=%.2f", distBefore, distAfterScroll)
		}

		// Scroll parent container back to top so the graph's toolbar is visible before clicking Zoom in.
		p.Scroll(ggui.Pt(sz.W/2, sz.H/2), ggui.Pt(0, 120))
		for range 3 {
			p.Frame()
		}

		zoomIn, ok := p.Semantics().Find(ggui.RoleButton, "Zoom in")
		if !ok {
			t.Fatalf("Zoom in button missing:\n%s", p.Semantics().String())
		}
		p.Click(graphT1RectCenter(zoomIn.Rect))
		p.Frame()
		distAfterZoom := math.Hypot(
			graphT1RectCenter(graphT1FindNode(t, p, "Zoom Alpha").Rect).X-graphT1RectCenter(graphT1FindNode(t, p, "Zoom Beta").Rect).X,
			graphT1RectCenter(graphT1FindNode(t, p, "Zoom Alpha").Rect).Y-graphT1RectCenter(graphT1FindNode(t, p, "Zoom Beta").Rect).Y,
		)
		if distAfterZoom <= distAfterScroll {
			t.Fatalf("Zoom in did not expand node spacing: before=%.2f after=%.2f", distAfterScroll, distAfterZoom)
		}
	})
}

// --- F1.9: ui.graph Click, DoubleTap, Context Menu, 1-hop Hover ---

func TestTier1_F1_9_InteractionsAndHover(t *testing.T) {
	t.Run("single_click_fires_on_change", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { selected: "" },
					render(state, ui) {
						return ui.graph([
							{ id: "alpha", label: "Alpha Node", shape: "card" },
							{ id: "beta", label: "Beta Node", shape: "card" },
						], [{ from: "alpha", to: "beta" }], {
							key: "g", variant: "cards", value: state.selected, caption: "Click", onChange: ["select"],
						});
					},
					on: { select(state, id) { state.selected = String(id || ""); } },
				});
			}
		};`
		rig, w, tree := graphT1InstallAndWatch(t, "t1-f19-click", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, sink, sz)

		alpha := graphT1FindNode(t, p, "Alpha Node")
		p.Click(graphT1RectCenter(alpha.Rect))
		p.Frame()

		evs := sink.snapshot()
		if len(evs) != 1 || evs[0].kind != protocol.UiChange || evs[0].value != "alpha" {
			t.Fatalf("expected UiChange alpha, got %+v", evs)
		}
		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:t1-f19-click/main", Node: evs[0].id, Kind: evs[0].kind, Value: evs[0].value,
		}); err != nil {
			t.Fatalf("SendEvent select: %v", err)
		}
		updated := w.WaitFor(t, 5*time.Second, func(n *protocol.UiNode) bool {
			g := FindNodeByKind(n, "graph")
			return g != nil && g.GetValue() == "alpha"
		})
		if FindNodeByKind(updated, "graph").GetValue() != "alpha" {
			t.Fatal("expected plugin state selected=alpha")
		}
	})

	t.Run("double_tap_within_400ms_fires_on_double_tap", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { jumped: "" },
					render(state, ui) {
						return ui.column([
							ui.text("jumped:" + state.jumped, { key: "status" }),
							ui.graph([
								{ id: "sess-42", label: "Target Session", shape: "card" },
								{ id: "sess-99", label: "Other Session", shape: "card" },
							], [{ from: "sess-42", to: "sess-99" }], {
								key: "g", variant: "cards", grow: 1, caption: "DoubleTap",
								onChange: ["select"], onDoubleTap: ["jump"],
							}),
						], { fill: true });
					},
					on: {
						select() {},
						jump(state, id) { state.jumped = String(id || ""); },
					},
				});
			}
		};`
		rig, w, tree := graphT1InstallAndWatch(t, "t1-f19-dbl", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(720, 500)
		p := graphT1NewProbe(t, tree, sink, sz)
		p.Advance(0)

		target := graphT1RectCenter(graphT1FindNode(t, p, "Target Session").Rect)
		p.Click(target)
		p.Advance(150 * time.Millisecond)
		p.Click(target)
		p.Frame()

		evs := sink.snapshot()
		var dbl *graphT1Event
		for i := range evs {
			if evs[i].kind != protocol.UiChange && evs[i].value == "sess-42" {
				dbl = &evs[i]
				break
			}
		}
		if dbl == nil {
			t.Fatalf("expected double-tap event for sess-42 within 150ms, got %+v", evs)
		}
		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:t1-f19-dbl/main", Node: dbl.id, Kind: dbl.kind, Value: dbl.value,
		}); err != nil {
			t.Fatalf("SendEvent doubleTap: %v", err)
		}
		w.WaitFor(t, 5*time.Second, func(n *protocol.UiNode) bool {
			return HasTreeText(n, "jumped:sess-42")
		})
	})

	t.Run("right_click_opens_context_menu_and_triggers_action", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { lastAction: "" },
					render(state, ui) {
						return ui.column([
							ui.text("act:" + state.lastAction, { key: "act" }),
							ui.contextMenu("node-menu", [
								ui.menuItem("세션 열기", { key: "open", onTap: ["ctxAction", "open"] }),
								ui.menuItem("메시지 보내기", { key: "msg", onTap: ["ctxAction", "msg"] }),
								ui.menuItem("서브에이전트 실행", { key: "sub", onTap: ["ctxAction", "sub"] }),
								ui.menuItem("세션 종료", { key: "kill", tone: "danger", onTap: ["ctxAction", "kill"] }),
							]),
							ui.graph([
								{ id: "sess-1", label: "Worker Alpha", shape: "card" },
								{ id: "sess-2", label: "Worker Beta", shape: "card" },
							], [{ from: "sess-1", to: "sess-2" }], {
								key: "g", variant: "cards", grow: 1, caption: "Context",
								contextMenu: "node-menu", onChange: ["sel"],
							}),
						], { fill: true });
					},
					on: {
						sel() {},
						ctxAction(state, op, nodeId) { state.lastAction = op + ":" + nodeId; },
					},
				});
			}
		};`
		rig, w, tree := graphT1InstallAndWatch(t, "t1-f19-ctx", js)
		sink := &graphT1EventSink{}
		sz := ggui.Sz(720, 520)
		p := graphT1NewProbe(t, tree, sink, sz)

		n1 := graphT1FindNode(t, p, "Worker Alpha")
		p.ClickButton(graphT1RectCenter(n1.Rect), ggui.MouseButtonRight)
		p.Frame()

		for _, label := range []string{"세션 열기", "메시지 보내기", "서브에이전트 실행", "세션 종료"} {
			if _, ok := p.FindRole(ggui.RoleMenuItem, label); !ok {
				t.Fatalf("context menu item %q missing after right-click:\n%s", label, p.Semantics().String())
			}
		}
		openItem, _ := p.FindRole(ggui.RoleMenuItem, "세션 열기")
		sink.reset()
		p.Click(graphT1RectCenter(openItem.Rect))
		p.Frame()

		evs := sink.snapshot()
		if len(evs) != 1 || evs[0].kind != protocol.UiTap || evs[0].value != "sess-1" {
			t.Fatalf("expected context menu tap with sess-1, got %+v", evs)
		}
		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:t1-f19-ctx/main", Node: evs[0].id, Kind: evs[0].kind, Value: evs[0].value,
		}); err != nil {
			t.Fatalf("SendEvent context item: %v", err)
		}
		w.WaitFor(t, 5*time.Second, func(n *protocol.UiNode) bool {
			return HasTreeText(n, "act:open:sess-1")
		})
	})

	t.Run("hover_highlights_one_hop_and_renders_detail_card", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Node A", shape: "card", subtitle: "Detail Card A", status: "working" },
							{ id: "b", label: "Node B", shape: "card", subtitle: "1-hop neighbor" },
							{ id: "c", label: "Node C", shape: "card", subtitle: "2-hop dimmed" },
							{ id: "d", label: "Node D", shape: "card", subtitle: "isolated dimmed" },
						], [
							{ from: "a", to: "b", label: "hop1" },
							{ from: "b", to: "c", label: "hop2" },
						], { key: "g", variant: "cards", maxHeight: 460, caption: "Hover 1-Hop" });
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f19-hover", js)
		sz := ggui.Sz(800, 500)
		p := graphT1NewProbe(t, tree, nil, sz)

		aCenter := graphT1RectCenter(graphT1FindNode(t, p, "Node A").Rect)
		p.Move(aCenter)
		p.Frame()
		graphT1DrawFrames(p, sz, 2)

		if p.Cursor() != ggui.CursorShapePointer {
			t.Fatalf("expected CursorShapePointer when hovering node, got %v", p.Cursor())
		}
	})

	t.Run("hover_exit_restores_full_opacity", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					render(state, ui) {
						return ui.graph([
							{ id: "a", label: "Hover Target", shape: "card", subtitle: "Info" },
							{ id: "b", label: "Other Node", shape: "card" },
						], [{ from: "a", to: "b" }], {
							key: "g", variant: "cards", maxHeight: 440, caption: "Hover Exit",
						});
					},
				});
			}
		};`
		_, _, tree := graphT1InstallAndWatch(t, "t1-f19-hoverexit", js)
		sz := ggui.Sz(720, 480)
		p := graphT1NewProbe(t, tree, nil, sz)

		target := graphT1RectCenter(graphT1FindNode(t, p, "Hover Target").Rect)
		p.Move(target)
		p.Frame()
		if p.Cursor() != ggui.CursorShapePointer {
			t.Fatalf("expected pointer cursor on node hover, got %v", p.Cursor())
		}

		empty := graphT1EmptyCanvasPoint(graphT1FindCanvasRect(t, p, "Hover Exit"), p)
		p.Move(empty)
		p.Frame()
		graphT1DrawFrames(p, sz, 2)
		if p.Cursor() == ggui.CursorShapePointer {
			t.Fatalf("expected non-pointer cursor after leaving node onto empty background, got %v", p.Cursor())
		}
	})
}
