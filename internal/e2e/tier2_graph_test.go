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
	"github.com/ironpark/mako/internal/protocol"
)

type graphT2Event struct {
	id    string
	kind  string
	value string
}

type graphT2EventSink struct {
	mu     sync.Mutex
	events []graphT2Event
}

func (s *graphT2EventSink) record(id, kind, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, graphT2Event{id: id, kind: kind, value: value})
}

func (s *graphT2EventSink) snapshot() []graphT2Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]graphT2Event, len(s.events))
	copy(out, s.events)
	return out
}

func (s *graphT2EventSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = nil
}

func graphT2RectCenter(r ggui.Rect) ggui.Point {
	return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
}

func graphT2FindNode(t *testing.T, p *ggui.Probe, label string) ggui.SemNode {
	t.Helper()
	f, ok := p.Semantics().Find(ggui.RoleButton, label)
	if !ok {
		t.Fatalf("graph node %q not found in semantics:\n%s", label, p.Semantics().String())
	}
	return f
}

func graphT2FindCanvasRect(t *testing.T, p *ggui.Probe, caption string) ggui.Rect {
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

func graphT2EmptyCanvasPoint(canvasRect ggui.Rect, p *ggui.Probe) ggui.Point {
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
	}
	for _, pt := range candidates {
		if !hitAny(pt) {
			return pt
		}
	}
	return ggui.Pt(canvasRect.Origin.X+20, canvasRect.Origin.Y+20)
}

func graphT2NewProbe(t *testing.T, tree *protocol.UiNode, sink *graphT2EventSink, sz ggui.Size) *ggui.Probe {
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

func graphT2DrawFrames(p *ggui.Probe, sz ggui.Size, frames int) {
	w := max(int(sz.W), 1)
	h := max(int(sz.H), 1)
	img := ggfx.NewImage(w, h)
	defer img.Deallocate()
	for range frames {
		p.Draw(img)
	}
}

func graphT2AssertFiniteNodes(t *testing.T, p *ggui.Probe) {
	t.Helper()
	for _, b := range p.Semantics().Nodes(ggui.RoleButton) {
		if math.IsNaN(b.Rect.Origin.X) || math.IsNaN(b.Rect.Origin.Y) ||
			math.IsInf(b.Rect.Origin.X, 0) || math.IsInf(b.Rect.Origin.Y, 0) {
			t.Fatalf("non-finite button rect %+v for %q", b.Rect, b.Name)
		}
	}
}

// --- F1.1 Boundary & Negative Tests ---

func TestTier2_F1_1_RichCardNodeBoundaries(t *testing.T) {
	t.Run("unknown_shape_rejected_by_uiblock", func(t *testing.T) {
		MustRejectUI(t, `{
			"kind":"graph","variant":"cards",
			"graphNodes":[{"id":"a","label":"A","shape":"hexagon"}]
		}`)
	})

	t.Run("unknown_variant_rejected_by_uiblock", func(t *testing.T) {
		MustRejectUI(t, `{
			"kind":"graph","variant":"bubbles",
			"graphNodes":[{"id":"a","label":"A"}]
		}`)
	})

	t.Run("overlong_badges_and_label_rejected", func(t *testing.T) {
		longLabel := strings.Repeat("x", 129)
		MustRejectUI(t, fmt.Sprintf(`{"kind":"graph","graphNodes":[{"id":"a","label":%q}]}`, longLabel))

		longBadge := strings.Repeat("b", 33)
		MustRejectUI(t, fmt.Sprintf(`{"kind":"graph","graphNodes":[{"id":"a","label":"A","badges":[{"text":%q}]}]}`, longBadge))

		tooManyBadges := make([]map[string]any, 9)
		for i := range tooManyBadges {
			tooManyBadges[i] = map[string]any{"text": fmt.Sprintf("b%d", i)}
		}
		bJSON, _ := json.Marshal(tooManyBadges)
		MustRejectUI(t, fmt.Sprintf(`{"kind":"graph","graphNodes":[{"id":"a","label":"A","badges":%s}]}`, string(bJSON)))

		MustRejectUI(t, `{"kind":"graph","graphNodes":[{"id":"a","label":"A\u202eB"}]}`)
	})

	t.Run("missing_or_empty_icon_graceful", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","text":"Empty Icon",
			"graphNodes":[
				{"id":"a","label":"No Icon","shape":"card","icon":""},
				{"id":"b","label":"Omitted Icon","shape":"card"}
			]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(640, 420))
		graphT2DrawFrames(p, ggui.Sz(640, 420), 2)
		_ = graphT2FindNode(t, p, "No Icon")
		_ = graphT2FindNode(t, p, "Omitted Icon")

		MustRejectUI(t, `{"kind":"graph","graphNodes":[{"id":"a","label":"Bad","icon":"Not An Icon!"}]}`)
	})

	t.Run("exceeding_max_graph_nodes_rejected", func(t *testing.T) {
		makeNodes := func(n int) string {
			list := make([]map[string]any, 0, n)
			for i := range n {
				list = append(list, map[string]any{
					"id":    fmt.Sprintf("n%d", i),
					"label": fmt.Sprintf("N%d", i),
				})
			}
			b, _ := json.Marshal(list)
			return fmt.Sprintf(`{"kind":"graph","graphNodes":%s}`, string(b))
		}
		_ = MustReadUI(t, makeNodes(120))
		MustRejectUI(t, makeNodes(121))
	})
}

// --- F1.2 Boundary & Negative Tests ---

func TestTier2_F1_2_BezierEdgeBoundaries(t *testing.T) {
	t.Run("edge_referencing_nonexistent_node_rejected", func(t *testing.T) {
		MustRejectUI(t, `{
			"kind":"graph",
			"graphNodes":[{"id":"a","label":"A"}],
			"graphEdges":[{"from":"a","to":"missing"}]
		}`)
		MustRejectUI(t, `{
			"kind":"graph",
			"graphNodes":[{"id":"a","label":"A"}],
			"graphEdges":[{"from":"missing","to":"a"}]
		}`)
	})

	t.Run("self_edge_does_not_nan_or_crash", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","text":"Self Edge",
			"graphNodes":[{"id":"a","label":"Solo","shape":"card"}],
			"graphEdges":[{"from":"a","to":"a","label":"loop","tone":"accent"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(640, 420))
		graphT2DrawFrames(p, ggui.Sz(640, 420), 3)
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("exceeding_max_graph_edges_rejected", func(t *testing.T) {
		nodes := make([]map[string]any, 0, 25)
		for i := range 25 {
			nodes = append(nodes, map[string]any{"id": fmt.Sprintf("n%d", i), "label": fmt.Sprintf("N%d", i)})
		}
		makeTree := func(edgeCount int) string {
			edges := make([]map[string]any, 0, edgeCount)
			for i := range edgeCount {
				edges = append(edges, map[string]any{
					"from": fmt.Sprintf("n%d", i%25),
					"to":   fmt.Sprintf("n%d", (i*7+1)%25),
				})
			}
			nb, _ := json.Marshal(nodes)
			eb, _ := json.Marshal(edges)
			return fmt.Sprintf(`{"kind":"graph","graphNodes":%s,"graphEdges":%s}`, string(nb), string(eb))
		}
		_ = MustReadUI(t, makeTree(400))
		MustRejectUI(t, makeTree(401))
	})

	t.Run("empty_edge_label_accepted_and_invalid_tone_rejected", func(t *testing.T) {
		_ = MustReadUI(t, `{
			"kind":"graph",
			"graphNodes":[{"id":"a","label":"A"},{"id":"b","label":"B"}],
			"graphEdges":[{"from":"a","to":"b","label":"","tone":""}]
		}`)
		MustRejectUI(t, `{
			"kind":"graph",
			"graphNodes":[{"id":"a","label":"A"},{"id":"b","label":"B"}],
			"graphEdges":[{"from":"a","to":"b","tone":"#ff0000"}]
		}`)
		MustRejectUI(t, fmt.Sprintf(`{
			"kind":"graph",
			"graphNodes":[{"id":"a","label":"A"},{"id":"b","label":"B"}],
			"graphEdges":[{"from":"a","to":"b","label":%q}]
		}`, strings.Repeat("e", 129)))
	})

	t.Run("coincident_node_positions_no_divide_by_zero", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","text":"Coincident",
			"graphNodes":[
				{"id":"a","label":"Co A","x":120,"y":120},
				{"id":"b","label":"Co B","x":120,"y":120}
			],
			"graphEdges":[{"from":"a","to":"b","label":"same-pos","tone":"accent"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(640, 420))
		graphT2DrawFrames(p, ggui.Sz(640, 420), 3)
		graphT2AssertFiniteNodes(t, p)
	})
}

// --- F1.3 Boundary & Negative Tests ---

func TestTier2_F1_3_GroupHullBoundaries(t *testing.T) {
	t.Run("all_nodes_ungrouped_renders_cleanly", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","text":"No Groups",
			"graphNodes":[
				{"id":"a","label":"U1","group":""},
				{"id":"b","label":"U2","group":""},
				{"id":"c","label":"U3"}
			],
			"graphEdges":[{"from":"a","to":"b"},{"from":"b","to":"c"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(640, 420))
		graphT2DrawFrames(p, ggui.Sz(640, 420), 2)
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("single_node_group_hull", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","text":"Singleton Group",
			"graphNodes":[
				{"id":"a","label":"SoloGrp","group":"singleton"},
				{"id":"b","label":"Plain","group":""}
			],
			"graphEdges":[{"from":"a","to":"b"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(640, 420))
		graphT2DrawFrames(p, ggui.Sz(640, 420), 2)
		_ = graphT2FindNode(t, p, "SoloGrp")
	})

	t.Run("group_name_64_byte_boundary_and_65_byte_rejected", func(t *testing.T) {
		g64 := strings.Repeat("g", 64)
		g65 := strings.Repeat("g", 65)
		_ = MustReadUI(t, fmt.Sprintf(`{"kind":"graph","graphNodes":[{"id":"a","label":"A","group":%q}]}`, g64))
		MustRejectUI(t, fmt.Sprintf(`{"kind":"graph","graphNodes":[{"id":"a","label":"A","group":%q}]}`, g65))
	})

	t.Run("mixed_empty_and_named_groups", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","text":"Mixed Groups",
			"graphNodes":[
				{"id":"n1","label":"E1","group":""},
				{"id":"n2","label":"A1","group":"alpha"},
				{"id":"n3","label":"A2","group":"alpha"},
				{"id":"n4","label":"B1","group":"beta"}
			],
			"graphEdges":[{"from":"n1","to":"n2"},{"from":"n2","to":"n3"},{"from":"n3","to":"n4"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(720, 460))
		graphT2DrawFrames(p, ggui.Sz(720, 460), 2)
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("max_120_nodes_in_12_groups_renders_without_panic", func(t *testing.T) {
		nodes := make([]map[string]any, 0, 120)
		edges := make([]map[string]any, 0, 119)
		for i := range 120 {
			nodes = append(nodes, map[string]any{
				"id":    fmt.Sprintf("n%d", i),
				"label": fmt.Sprintf("Node %d", i),
				"group": fmt.Sprintf("group-%d", i%12),
			})
			if i > 0 {
				edges = append(edges, map[string]any{
					"from": fmt.Sprintf("n%d", i-1),
					"to":   fmt.Sprintf("n%d", i),
				})
			}
		}
		nb, _ := json.Marshal(nodes)
		eb, _ := json.Marshal(edges)
		tr := MustReadUI(t, fmt.Sprintf(`{"kind":"graph","text":"120 Nodes 12 Groups","graphNodes":%s,"graphEdges":%s}`, string(nb), string(eb)))
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(1024, 720))
		graphT2DrawFrames(p, ggui.Sz(1024, 720), 2)
		graphT2AssertFiniteNodes(t, p)
	})
}

// --- F1.4 Boundary & Negative Tests ---

func TestTier2_F1_4_LayoutModeBoundaries(t *testing.T) {
	t.Run("invalid_layout_bogus_rejected", func(t *testing.T) {
		MustRejectUI(t, `{"kind":"graph","layout":"bogus","graphNodes":[{"id":"a","label":"A"}]}`)
		MustRejectUI(t, `{"kind":"graph","layout":"grid","graphNodes":[{"id":"a","label":"A"}]}`)
	})

	t.Run("empty_graph_zero_nodes_in_all_layouts", func(t *testing.T) {
		for _, mode := range []string{"force", "hierarchy", "radial"} {
			tr := MustReadUI(t, fmt.Sprintf(`{"kind":"graph","layout":%q,"text":"Empty","graphNodes":[],"graphEdges":[]}`, mode))
			p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(640, 400))
			graphT2DrawFrames(p, ggui.Sz(640, 400), 2)
		}
	})

	t.Run("cyclic_graph_in_hierarchy_layout_no_infinite_loop", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","layout":"hierarchy","text":"Cycle",
			"graphNodes":[
				{"id":"a","label":"Cycle A"},
				{"id":"b","label":"Cycle B"},
				{"id":"c","label":"Cycle C"}
			],
			"graphEdges":[
				{"from":"a","to":"b"},
				{"from":"b","to":"c"},
				{"from":"c","to":"a"},
				{"from":"a","to":"a"}
			]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(720, 480))
		graphT2DrawFrames(p, ggui.Sz(720, 480), 2)
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("disconnected_components_in_radial_layout", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","layout":"radial","text":"Disconnected Radial",
			"graphNodes":[
				{"id":"a1","label":"Comp1 A"},
				{"id":"a2","label":"Comp1 B"},
				{"id":"b1","label":"Comp2 A"},
				{"id":"b2","label":"Comp2 B"},
				{"id":"solo","label":"Orphan"}
			],
			"graphEdges":[{"from":"a1","to":"a2"},{"from":"b1","to":"b2"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(720, 480))
		graphT2DrawFrames(p, ggui.Sz(720, 480), 2)
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("rapid_toggling_between_all_3_modes_no_crash", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { mode: "force" },
					render(state, ui) {
						return ui.column([
							ui.row([
								ui.radio(state.mode, ["force", "hierarchy", "radial"], {
									key: "mode", direction: "horizontal", onChange: ["setMode"],
								}),
								ui.text("Mode:" + state.mode),
							], { gap: 8 }),
							ui.graph([
								{ id: "a", label: "N1" },
								{ id: "b", label: "N2" },
								{ id: "c", label: "N3" },
								{ id: "d", label: "N4" },
							], [
								{ from: "a", to: "b" },
								{ from: "a", to: "c" },
								{ from: "b", to: "d" },
								{ from: "c", to: "d" },
							], {
								key: "g", layout: state.mode, caption: "Rapid Modes", grow: 1,
							}),
						], { fill: true });
					},
					on: {
						setMode(state, v) { state.mode = String(v || "force"); },
					},
				});
			}
		};`
		sz := ggui.Sz(1080, 680)
		rig, ap, w, blockName := OpenAppProbeBlock(t, "t2-f14-rapid", "Rapid", js, sz)
		p := ap.Probe()
		WaitProbeSemantics(t, ap, 10*time.Second, "Mode:force visible", func(sem *ggui.SemTree) bool {
			return strings.Contains(sem.String(), "Mode:force")
		})
		modeID := FindNodeByKey(w.Latest(t), "mode").GetId()
		seenPos := map[string]ggui.Point{}
		seenPos["force"] = graphT2FindNode(t, p, "N4").Rect.Origin

		for _, mode := range []string{"hierarchy", "radial", "force", "radial", "hierarchy", "force"} {
			if _, err := rig.BlockEvent(t, &protocol.BlockEvent{
				Block: blockName, Node: modeID, Kind: protocol.UiChange, Value: mode,
			}); err != nil {
				t.Fatalf("send mode %s: %v", mode, err)
			}
			wantTxt := "Mode:" + mode
			WaitProbeSemantics(t, ap, 10*time.Second, wantTxt+" on same AppProbe", func(sem *ggui.SemTree) bool {
				return strings.Contains(sem.String(), wantTxt)
			})
			for range 4 {
				p.Advance(80 * time.Millisecond)
			}
			if mm, ok := p.Semantics().Find(ggui.RoleButton, "Toggle minimap"); ok {
				p.Click(graphT2RectCenter(mm.Rect))
			}
			p.Frame()
			seenPos[mode] = graphT2FindNode(t, p, "N4").Rect.Origin
		}
		if seenPos["force"] == seenPos["hierarchy"] || seenPos["hierarchy"] == seenPos["radial"] {
			t.Fatalf("expected rapid mode switching to update layout on probe, got positions %+v", seenPos)
		}
		graphT2DrawFrames(p, sz, 2)
		graphT2AssertFiniteNodes(t, p)
	})
}

// --- F1.5 Boundary & Negative Tests ---

func TestTier2_F1_5_TweenCameraBoundaries(t *testing.T) {
	t.Run("selecting_nonexistent_value_handled_cleanly", func(t *testing.T) {
		MustRejectUI(t, `{
			"kind":"graph","value":"does-not-exist",
			"graphNodes":[{"id":"a","label":"A"}]
		}`)

		// Renderer resilience when a UiNode arrives with stale/nonexistent Value.
		raw := &protocol.UiNode{
			Id:        "/g",
			Kind:      "graph",
			Text:      "Missing Value",
			Value:     "nonexistent-node",
			MaxHeight: 360,
			GraphNodes: []*protocol.UiGraphNode{
				{Id: "a", Label: "Alpha"},
				{Id: "b", Label: "Beta"},
			},
		}
		p := graphT2NewProbe(t, raw, nil, ggui.Sz(640, 400))
		graphT2DrawFrames(p, ggui.Sz(640, 400), 2)
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("zero_and_tiny_viewport_during_fit_focus", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","text":"Tiny Viewport","value":"a","maxHeight":200,
			"graphNodes":[{"id":"a","label":"A"},{"id":"b","label":"B"}],
			"graphEdges":[{"from":"a","to":"b"}]
		}`)
		p := gui.NewUiTreeProbe(tr.Root, nil, ggui.Sz(0, 0))
		defer p.Close()
		p.Frame()
		p.Advance(16 * time.Millisecond)

		p.Resize(ggui.Sz(80, 80))
		p.Frame()
		graphT2DrawFrames(p, ggui.Sz(80, 80), 2)
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("extreme_node_coordinates_bounded_cleanly", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","text":"Extreme Coords","maxHeight":400,
			"graphNodes":[
				{"id":"a","label":"Far Left","x":-100000,"y":100000},
				{"id":"b","label":"Far Right","x":100000,"y":-100000}
			],
			"graphEdges":[{"from":"a","to":"b"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(720, 440))
		graphT2DrawFrames(p, ggui.Sz(720, 440), 2)
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("advance_zero_and_10s_tween_completion", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","text":"Long Advance","maxHeight":400,
			"graphNodes":[{"id":"a","label":"A"},{"id":"b","label":"B"}],
			"graphEdges":[{"from":"a","to":"b"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(720, 440))
		p.Advance(0)
		p.Advance(10 * time.Second)
		graphT2DrawFrames(p, ggui.Sz(720, 440), 2)
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("removing_selected_node_mid_tween_stays_safe", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { removed: false },
					render(state, ui) {
						const nodes = [{ id: "a", label: "Keep" }];
						const edges = [];
						if (!state.removed) {
							nodes.push({ id: "b", label: "Remove" });
							edges.push({ from: "a", to: "b" });
						}
						return ui.column([
							ui.button("Remove Selected", { key: "rm", onTap: ["rm"] }),
							ui.graph(nodes, edges, {
								key: "g", value: state.removed ? "" : "b", caption: "Mid Tween Remove", grow: 1,
							}),
						], { fill: true });
					},
					on: { rm(state) { state.removed = true; } },
				});
			}
		};`
		sz := ggui.Sz(1080, 680)
		rig, ap, w, blockName := OpenAppProbeBlock(t, "t2-f15-rm", "Mid Tween Remove", js, sz)
		p := ap.Probe()
		WaitProbeSemantics(t, ap, 10*time.Second, "Keep and Remove visible", func(sem *ggui.SemTree) bool {
			_, okK := sem.Find(ggui.RoleButton, "Keep")
			_, okR := sem.Find(ggui.RoleButton, "Remove")
			return okK && okR
		})
		p.Advance(32 * time.Millisecond)

		rmID := FindNodeByKey(w.Latest(t), "rm").GetId()
		if _, err := rig.BlockEvent(t, &protocol.BlockEvent{
			Block: blockName, Node: rmID, Kind: protocol.UiTap,
		}); err != nil {
			t.Fatalf("remove selected event: %v", err)
		}
		WaitProbeSemantics(t, ap, 10*time.Second, "Remove disappears on same AppProbe", func(sem *ggui.SemTree) bool {
			_, okK := sem.Find(ggui.RoleButton, "Keep")
			_, okR := sem.Find(ggui.RoleButton, "Remove")
			return okK && !okR
		})
		for range 4 {
			p.Advance(80 * time.Millisecond)
		}
		graphT2DrawFrames(p, sz, 2)
		_ = graphT2FindNode(t, p, "Keep")
		if _, ok := p.Semantics().Find(ggui.RoleButton, "Remove"); ok {
			t.Fatal("expected Remove node to disappear from semantics after BlockUpdate")
		}
		graphT2AssertFiniteNodes(t, p)
	})
}

// --- F1.6 Boundary & Negative Tests ---

func TestTier2_F1_6_LiveMotionBoundaries(t *testing.T) {
	t.Run("unknown_status_renders_safely", func(t *testing.T) {
		MustRejectUI(t, `{
			"kind":"graph","variant":"cards","text":"Unknown Status","maxHeight":400,
			"graphNodes":[{"id":"a","label":"Mystery","shape":"card","status":"unknown"}],
			"graphEdges":[]
		}`)
		raw := &protocol.UiNode{
			Id:        "/g",
			Kind:      "graph",
			Variant:   "cards",
			Text:      "Unknown Status",
			MaxHeight: 400,
			GraphNodes: []*protocol.UiGraphNode{
				{Id: "a", Label: "Mystery", Shape: "card", Status: "unknown"},
			},
		}
		p := graphT2NewProbe(t, raw, nil, ggui.Sz(640, 420))
		p.Advance(0)
		p.Advance(100 * time.Millisecond)
		graphT2DrawFrames(p, ggui.Sz(640, 420), 2)
		_ = graphT2FindNode(t, p, "Mystery")
	})

	t.Run("empty_graph_with_animation_advances_cleanly", func(t *testing.T) {
		tr := MustReadUI(t, `{"kind":"graph","text":"Empty Anim","maxHeight":300,"graphNodes":[],"graphEdges":[]}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(640, 360))
		p.Advance(0)
		p.Advance(16 * time.Millisecond)
		p.Advance(100 * time.Millisecond)
		p.Advance(time.Second)
		graphT2DrawFrames(p, ggui.Sz(640, 360), 2)
	})

	t.Run("toggling_active_true_to_false_stops_edge_flow", func(t *testing.T) {
		js := `export default {
			activate(mako) {
				mako.blocks.register("main", {
					state: { active: true },
					render(state, ui) {
						return ui.column([
							ui.row([
								ui.button("Toggle Flow", { key: "tog", onTap: ["toggle"] }),
								ui.text("Active:" + String(state.active)),
							], { gap: 8 }),
							ui.graph([
								{ id: "a", label: "A" },
								{ id: "b", label: "B" },
							], [{ from: "a", to: "b", active: state.active }], {
								key: "g", caption: "Flow Toggle", grow: 1,
							}),
						], { fill: true });
					},
					on: { toggle(state) { state.active = !state.active; } },
				});
			}
		};`
		sz := ggui.Sz(1080, 680)
		rig, ap, w, blockName := OpenAppProbeBlock(t, "t2-f16-flow-toggle", "Flow Toggle View", js, sz)
		p := ap.Probe()
		WaitProbeSemantics(t, ap, 10*time.Second, "Active:true visible", func(sem *ggui.SemTree) bool {
			return strings.Contains(sem.String(), "Active:true")
		})
		cRect := graphT2FindCanvasRect(t, p, "Flow Toggle")
		img1 := ggtest.Render(p, sz, 1)
		p.Advance(250 * time.Millisecond)
		img2 := ggtest.Render(p, sz, 1)
		if bytes.Equal(CropRGBA(img1, cRect), CropRGBA(img2, cRect)) {
			t.Fatal("expected active=true edge flow to animate graph pixels across 250ms advance")
		}

		togID := FindNodeByKey(w.Latest(t), "tog").GetId()
		if _, err := rig.BlockEvent(t, &protocol.BlockEvent{
			Block: blockName, Node: togID, Kind: protocol.UiTap,
		}); err != nil {
			t.Fatalf("toggle flow event: %v", err)
		}
		WaitProbeSemantics(t, ap, 10*time.Second, "Active:false on same AppProbe", func(sem *ggui.SemTree) bool {
			return strings.Contains(sem.String(), "Active:false")
		})
		p.Advance(16 * time.Millisecond)
		cRect = graphT2FindCanvasRect(t, p, "Flow Toggle")
		img3 := ggtest.Render(p, sz, 1)
		p.Advance(250 * time.Millisecond)
		img4 := ggtest.Render(p, sz, 1)
		if !bytes.Equal(CropRGBA(img3, cRect), CropRGBA(img4, cRect)) {
			t.Fatal("expected active=false edge flow to stop animating graph pixels across 250ms advance")
		}
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("120_simultaneous_working_waiting_nodes_responsive", func(t *testing.T) {
		nodes := make([]map[string]any, 0, 120)
		edges := make([]map[string]any, 0, 150)
		for i := range 120 {
			st := "working"
			if i%2 == 1 {
				st = "waiting"
			}
			nodes = append(nodes, map[string]any{
				"id":     fmt.Sprintf("n%d", i),
				"label":  fmt.Sprintf("S%d", i),
				"shape":  "card",
				"status": st,
			})
			if i > 0 {
				edges = append(edges, map[string]any{
					"from":   fmt.Sprintf("n%d", i-1),
					"to":     fmt.Sprintf("n%d", i),
					"active": i%3 == 0,
				})
			}
		}
		nb, _ := json.Marshal(nodes)
		eb, _ := json.Marshal(edges)
		tr := MustReadUI(t, fmt.Sprintf(`{"kind":"graph","variant":"cards","text":"120 Live","maxHeight":700,"graphNodes":%s,"graphEdges":%s}`, string(nb), string(eb)))
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(1200, 760))
		p.Advance(0)
		for range 8 {
			p.Advance(16 * time.Millisecond)
			graphT2DrawFrames(p, ggui.Sz(1200, 760), 1)
		}
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("max_height_boundaries_120_1200_and_out_of_range_rejected", func(t *testing.T) {
		_ = MustReadUI(t, `{"kind":"graph","maxHeight":120,"graphNodes":[{"id":"a","label":"A"}]}`)
		_ = MustReadUI(t, `{"kind":"graph","maxHeight":1200,"graphNodes":[{"id":"a","label":"A"}]}`)
		MustRejectUI(t, `{"kind":"graph","maxHeight":119,"graphNodes":[{"id":"a","label":"A"}]}`)
		MustRejectUI(t, `{"kind":"graph","maxHeight":1201,"graphNodes":[{"id":"a","label":"A"}]}`)
	})
}

// --- F1.7 Boundary & Negative Tests ---

func TestTier2_F1_7_FillGrowMinimapBoundaries(t *testing.T) {
	t.Run("minimap_false_hides_minimap", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","minimap":false,"maxHeight":420,"text":"No Minimap",
			"graphNodes":[{"id":"a","label":"A"},{"id":"b","label":"B"}],
			"graphEdges":[{"from":"a","to":"b"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(720, 460))
		graphT2DrawFrames(p, ggui.Sz(720, 460), 2)

		toggleBtn, ok := p.Semantics().Find(ggui.RoleButton, "Toggle minimap")
		if !ok {
			t.Fatalf("expected Toggle minimap toolbar button:\n%s", p.Semantics().String())
		}

		r := graphT2FindCanvasRect(t, p, "No Minimap")
		mmTarget := ggui.Pt(r.Origin.X+r.Size.W-30, r.Origin.Y+r.Size.H-25)

		before := graphT2RectCenter(graphT2FindNode(t, p, "A").Rect)
		p.Click(mmTarget)
		p.Frame()
		afterDisabled := graphT2RectCenter(graphT2FindNode(t, p, "A").Rect)
		if math.Hypot(afterDisabled.X-before.X, afterDisabled.Y-before.Y) > 1 {
			t.Fatalf("minimap overlay should be inactive when minimap=false, but node A moved from %v to %v", before, afterDisabled)
		}

		p.Click(graphT2RectCenter(toggleBtn.Rect))
		p.Frame()
		graphT2DrawFrames(p, ggui.Sz(720, 460), 2)

		p.Click(mmTarget)
		p.Frame()
		afterEnabled := graphT2RectCenter(graphT2FindNode(t, p, "A").Rect)
		if math.Hypot(afterEnabled.X-afterDisabled.X, afterEnabled.Y-afterDisabled.Y) <= 1 {
			t.Fatalf("expected Toggle minimap to activate bottom-right minimap panning, node A stayed at %v", afterEnabled)
		}
	})

	t.Run("resize_tiny_120_and_huge_2560x1440", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"column","fill":true,
			"children":[{
				"kind":"graph","grow":1,"minimap":true,"text":"Extreme Resize",
				"graphNodes":[{"id":"a","label":"A"},{"id":"b","label":"B"}],
				"graphEdges":[{"from":"a","to":"b"}]
			}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(120, 120))
		graphT2DrawFrames(p, ggui.Sz(120, 120), 1)
		graphT2AssertFiniteNodes(t, p)

		p.Resize(ggui.Sz(2560, 1440))
		p.Frame()
		graphT2DrawFrames(p, ggui.Sz(2560, 1440), 1)
		rHuge := graphT2FindCanvasRect(t, p, "Extreme Resize")
		if rHuge.Size.W < 2000 || rHuge.Size.H < 1000 {
			t.Fatalf("expected huge fill+grow canvas at 2560x1440, got %v", rHuge)
		}

		p.Resize(ggui.Sz(120, 120))
		p.Frame()
		graphT2DrawFrames(p, ggui.Sz(120, 120), 1)
		graphT2AssertFiniteNodes(t, p)
	})

	t.Run("fill_true_without_max_height_succeeds", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"column","fill":true,
			"children":[{
				"kind":"graph","grow":1,"text":"Fill Without MaxH",
				"graphNodes":[{"id":"a","label":"A"}],
				"graphEdges":[]
			}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(900, 700))
		graphT2DrawFrames(p, ggui.Sz(900, 700), 1)
		r := graphT2FindCanvasRect(t, p, "Fill Without MaxH")
		if r.Size.H < 500 {
			t.Fatalf("expected grow graph inside fill root to fill height >= 500, got %v", r)
		}

		// fill on non-root layout container must still be rejected.
		MustRejectUI(t, `{"kind":"column","children":[{"kind":"column","fill":true,"children":[]}]}`)
	})

	t.Run("max_height_50_rejected_by_uiblock", func(t *testing.T) {
		MustRejectUI(t, `{"kind":"graph","maxHeight":50,"graphNodes":[{"id":"a","label":"A"}]}`)
	})

	t.Run("clicking_outside_minimap_bounds_does_not_jump_wildly", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","minimap":true,"maxHeight":460,"text":"Outside Minimap",
			"graphNodes":[{"id":"a","label":"Anchor","x":-100,"y":0},{"id":"b","label":"Peer","x":100,"y":0}],
			"graphEdges":[{"from":"a","to":"b"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(800, 500))
		before := graphT2RectCenter(graphT2FindNode(t, p, "Anchor").Rect)
		r := graphT2FindCanvasRect(t, p, "Outside Minimap")
		outside := ggui.Pt(r.Origin.X+24, r.Origin.Y+r.Size.H-24)
		p.Click(outside)
		p.Frame()
		after := graphT2RectCenter(graphT2FindNode(t, p, "Anchor").Rect)
		if math.Hypot(after.X-before.X, after.Y-before.Y) > 5 {
			t.Fatalf("click outside minimap unexpectedly jumped node from %v to %v", before, after)
		}
	})
}

// --- F1.8 Boundary & Negative Tests ---

func TestTier2_F1_8_DragDropBoundaries(t *testing.T) {
	t.Run("drag_release_outside_viewport_clamps_safely", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","maxHeight":420,"text":"Clamp Drag",
			"onDrop":["dropped"],
			"graphNodes":[{"id":"a","label":"Drag Out","shape":"card"},{"id":"b","label":"Anchor","shape":"card"}],
			"graphEdges":[{"from":"a","to":"b"}]
		}`)
		sink := &graphT2EventSink{}
		p := graphT2NewProbe(t, tr.Root, sink, ggui.Sz(640, 440))

		start := graphT2RectCenter(graphT2FindNode(t, p, "Drag Out").Rect)
		p.Press(start)
		p.Move(ggui.Pt(-200, -200))
		p.Release(ggui.Pt(-200, -200))
		p.Frame()

		graphT2AssertFiniteNodes(t, p)
		evs := sink.snapshot()
		if len(evs) == 0 {
			t.Fatal("expected UiDrop event when releasing dragged node outside viewport, got []")
		}
		foundDrop := false
		for _, ev := range evs {
			if ev.kind == protocol.UiDrop {
				foundDrop = true
				var pos struct {
					ID string  `json:"id"`
					X  float64 `json:"x"`
					Y  float64 `json:"y"`
				}
				if err := json.Unmarshal([]byte(ev.value), &pos); err != nil {
					t.Fatalf("malformed drop JSON %q: %v", ev.value, err)
				}
				if pos.ID != "a" || math.IsNaN(pos.X) || math.IsNaN(pos.Y) || math.IsInf(pos.X, 0) || math.IsInf(pos.Y, 0) {
					t.Fatalf("invalid or non-finite drop coordinates %+v", pos)
				}
			}
		}
		if !foundDrop {
			t.Fatalf("expected UiDrop in events, got %+v", evs)
		}
	})

	t.Run("micro_jitter_1px_click_not_drag_drop", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","maxHeight":420,"text":"Micro Jitter",
			"onChange":["sel"],"onDrop":["dropped"],
			"graphNodes":[{"id":"a","label":"Jitter Node","shape":"card"}],
			"graphEdges":[]
		}`)
		sink := &graphT2EventSink{}
		p := graphT2NewProbe(t, tr.Root, sink, ggui.Sz(640, 440))

		c := graphT2RectCenter(graphT2FindNode(t, p, "Jitter Node").Rect)
		p.Press(c)
		p.Move(ggui.Pt(c.X+1, c.Y+1))
		p.Release(ggui.Pt(c.X+1, c.Y+1))
		p.Frame()

		evs := sink.snapshot()
		for _, ev := range evs {
			if ev.kind == protocol.UiDrop {
				t.Fatalf("1px micro-jitter should NOT trigger UiDrop, got %+v", evs)
			}
		}
		if len(evs) != 1 || evs[0].kind != protocol.UiChange || evs[0].value != "a" {
			t.Fatalf("expected 1px jitter click to fire UiChange 'a', got %+v", evs)
		}
	})

	t.Run("right_button_drag_does_not_move_node", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","maxHeight":420,"text":"Right Button",
			"onDrop":["dropped"],
			"graphNodes":[{"id":"a","label":"Right Target","shape":"card"}],
			"graphEdges":[]
		}`)
		sink := &graphT2EventSink{}
		p := graphT2NewProbe(t, tr.Root, sink, ggui.Sz(640, 440))

		before := graphT2RectCenter(graphT2FindNode(t, p, "Right Target").Rect)
		p.ClickButton(before, ggui.MouseButtonRight)
		p.Move(ggui.Pt(before.X+60, before.Y+40))
		p.Frame()
		after := graphT2RectCenter(graphT2FindNode(t, p, "Right Target").Rect)
		if math.Hypot(after.X-before.X, after.Y-before.Y) > 1 {
			t.Fatalf("right-button interaction moved node from %v to %v", before, after)
		}
		for _, ev := range sink.snapshot() {
			if ev.kind == protocol.UiDrop {
				t.Fatalf("right-button interaction unexpectedly emitted UiDrop: %+v", ev)
			}
		}
	})

	t.Run("single_node_and_positioned_node_drag", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","maxHeight":420,"text":"Single Positioned",
			"onDrop":["dropped"],
			"graphNodes":[{"id":"solo","label":"Solo Positioned","shape":"card","x":160,"y":180}],
			"graphEdges":[]
		}`)
		sink := &graphT2EventSink{}
		p := graphT2NewProbe(t, tr.Root, sink, ggui.Sz(640, 440))

		start := graphT2RectCenter(graphT2FindNode(t, p, "Solo Positioned").Rect)
		end := ggui.Pt(start.X+50, start.Y+30)
		p.Press(start)
		p.Move(end)
		p.Release(end)
		p.Frame()

		graphT2AssertFiniteNodes(t, p)
		evs := sink.snapshot()
		if len(evs) != 1 || evs[0].kind != protocol.UiDrop {
			t.Fatalf("expected drop on single positioned node, got %+v", evs)
		}
	})

	t.Run("malformed_drop_payload_rejected_by_tree_message", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","key":"g","onDrop":["dropped"],
			"graphNodes":[{"id":"a","label":"A"}],
			"graphEdges":[]
		}`)
		badPayloads := []string{
			"",
			strings.Repeat("x", 300),
			"bad\x00payload",
			"bad\u202epayload",
			`{"x":10,"y":20}`,
			`not-json`,
		}
		for _, val := range badPayloads {
			if _, err := tr.Message(&protocol.BlockEvent{
				Block: "plugin:test/main",
				Node:  "/g",
				Kind:  protocol.UiDrop,
				Value: val,
			}); err == nil {
				t.Fatalf("expected Tree.Message to reject malformed UiDrop payload %q", val)
			}
		}
	})
}

// --- F1.9 Boundary & Negative Tests ---

func TestTier2_F1_9_InteractionBoundaries(t *testing.T) {
	t.Run("two_clicks_over_600ms_apart_do_not_double_tap", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","maxHeight":420,"text":"Slow Clicks",
			"onChange":["sel"],"onDoubleTap":["jump"],
			"graphNodes":[{"id":"a","label":"Slow Node","shape":"card"}],
			"graphEdges":[]
		}`)
		sink := &graphT2EventSink{}
		p := graphT2NewProbe(t, tr.Root, sink, ggui.Sz(640, 440))
		p.Advance(0)

		c := graphT2RectCenter(graphT2FindNode(t, p, "Slow Node").Rect)
		p.Click(c)
		p.Advance(650 * time.Millisecond)
		p.Click(c)
		p.Frame()

		evs := sink.snapshot()
		for _, ev := range evs {
			if ev.kind != protocol.UiChange {
				t.Fatalf("clicks 650ms apart should NOT fire double-tap, got %+v", evs)
			}
		}
		if len(evs) != 2 {
			t.Fatalf("expected 2 single-click UiChange events, got %+v", evs)
		}
	})

	t.Run("two_clicks_over_30px_apart_do_not_double_tap", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","maxHeight":420,"text":"Far Clicks",
			"onChange":["sel"],"onDoubleTap":["jump"],
			"graphNodes":[
				{"id":"a","label":"Wide Node Label For Distance","shape":"card","x":120,"y":160},
				{"id":"b","label":"Second","shape":"card","x":360,"y":240}
			],
			"graphEdges":[{"from":"a","to":"b"}]
		}`)
		sink := &graphT2EventSink{}
		p := graphT2NewProbe(t, tr.Root, sink, ggui.Sz(720, 460))
		p.Advance(0)

		r := graphT2FindNode(t, p, "Wide Node Label For Distance").Rect
		p1 := ggui.Pt(r.Origin.X+4, r.Origin.Y+r.Size.H/2)
		p2 := ggui.Pt(p1.X+38, p1.Y)
		p.Click(p1)
		p.Advance(100 * time.Millisecond)
		p.Click(p2)
		p.Frame()

		evs := sink.snapshot()
		if len(evs) != 2 {
			t.Fatalf("expected 2 UiChange events for two 38px-apart clicks, got %+v", evs)
		}
		for _, ev := range evs {
			if ev.kind != protocol.UiChange {
				t.Fatalf("clicks 38px apart should NOT fire double-tap, got %+v", evs)
			}
		}
	})

	t.Run("right_click_empty_background_no_context_menu", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"column",
			"children":[
				{"kind":"menu","key":"node-menu","variant":"context","children":[
					{"kind":"menu_item","text":"세션 열기","onTap":["open"]}
				]},
				{"kind":"graph","key":"g","contextMenu":"node-menu","maxHeight":420,"text":"Empty Right Click",
					"graphNodes":[{"id":"a","label":"Only Node"}],
					"graphEdges":[]
				}
			]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(720, 460))
		bg := graphT2EmptyCanvasPoint(graphT2FindCanvasRect(t, p, "Empty Right Click"), p)
		p.ClickButton(bg, ggui.MouseButtonRight)
		p.Frame()

		if items := p.FindAll(ggui.RoleMenuItem); len(items) != 0 {
			t.Fatalf("right-click on empty background should not open node context menu, got %+v", items)
		}
	})

	t.Run("hover_empty_background_clears_dimming", func(t *testing.T) {
		tr := MustReadUI(t, `{
			"kind":"graph","variant":"cards","maxHeight":420,"text":"Hover Clear",
			"graphNodes":[
				{"id":"a","label":"Node A","shape":"card"},
				{"id":"b","label":"Node B","shape":"card"}
			],
			"graphEdges":[{"from":"a","to":"b"}]
		}`)
		p := graphT2NewProbe(t, tr.Root, nil, ggui.Sz(720, 460))

		aCenter := graphT2RectCenter(graphT2FindNode(t, p, "Node A").Rect)
		p.Move(aCenter)
		p.Frame()
		if p.Cursor() != ggui.CursorShapePointer {
			t.Fatalf("expected CursorShapePointer while hovering Node A, got %v", p.Cursor())
		}

		bg := graphT2EmptyCanvasPoint(graphT2FindCanvasRect(t, p, "Hover Clear"), p)
		p.Move(bg)
		p.Frame()
		graphT2DrawFrames(p, ggui.Sz(720, 460), 2)
		if p.Cursor() == ggui.CursorShapePointer {
			t.Fatalf("expected hover cursor to clear on empty background, got %v", p.Cursor())
		}
	})

	t.Run("on_double_tap_and_on_drop_on_non_graph_rejected", func(t *testing.T) {
		MustRejectUI(t, `{"kind":"text","text":"hi","onDoubleTap":["open"]}`)
		MustRejectUI(t, `{"kind":"button","text":"ok","onDoubleTap":["open"]}`)
		MustRejectUI(t, `{"kind":"divider","onDrop":["drop"]}`)
		MustRejectUI(t, `{"kind":"input","value":"v","onDrop":["drop"]}`)
	})
}
