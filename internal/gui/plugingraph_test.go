package gui

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/protocol"
)

func TestComputeGraphLayoutDeterministic(t *testing.T) {
	nodes := []*protocol.UiGraphNode{
		{Id: "a", Label: "Architecture", Group: "core", Tone: "accent", Weight: 3},
		{Id: "b", Label: "Plugins", Group: "core", Tone: "success", Weight: 2},
		{Id: "c", Label: "Schema", Group: "proto", Weight: 1.5},
		{Id: "d", Label: "Orphan", Group: "misc", Weight: 1},
	}
	edges := []*protocol.UiGraphEdge{
		{From: "a", To: "b", Label: "loads"},
		{From: "b", To: "c", Dashed: true, Tone: "warning"},
	}

	l1 := computeGraphLayout(nodes, edges)
	l2 := computeGraphLayout(nodes, edges)

	if len(l1.pos) != len(nodes) {
		t.Fatalf("layout pos len = %d, want %d", len(l1.pos), len(nodes))
	}
	for _, nd := range nodes {
		p1, ok1 := l1.pos[nd.GetId()]
		p2, ok2 := l2.pos[nd.GetId()]
		if !ok1 || !ok2 {
			t.Fatalf("missing node %q in layout", nd.GetId())
		}
		if p1 != p2 {
			t.Fatalf("non-deterministic layout for %q: %v != %v", nd.GetId(), p1, p2)
		}
		if math.IsNaN(p1.X) || math.IsNaN(p1.Y) || math.IsInf(p1.X, 0) || math.IsInf(p1.Y, 0) {
			t.Fatalf("non-finite position for %q: %v", nd.GetId(), p1)
		}
	}

	// Verify 0-node and 1-node edge cases.
	empty := computeGraphLayout(nil, nil)
	if len(empty.pos) != 0 {
		t.Fatalf("empty graph produced %d positions", len(empty.pos))
	}
	single := computeGraphLayout([]*protocol.UiGraphNode{{Id: "solo", Label: "Solo"}}, nil)
	if single.pos["solo"] != ggui.Pt(0, 0) {
		t.Fatalf("single node pos = %v, want (0,0)", single.pos["solo"])
	}
}

func TestPluginGraphRenderAndNodeClickHitTest(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	graph := &protocol.UiNode{
		Id:        "/graph",
		Kind:      "graph",
		Text:      "Knowledge Graph",
		Value:     "alpha",
		MaxHeight: 340,
		Change:    true,
		GraphNodes: []*protocol.UiGraphNode{
			{Id: "alpha", Label: "Alpha Node", Group: "core", Tone: "accent", Subtitle: "Primary hub", Weight: 3},
			{Id: "beta", Label: "Beta Node", Group: "core", Tone: "success", Weight: 2},
			{Id: "gamma", Label: "Gamma Node", Group: "ext", Tone: "danger", Weight: 1},
		},
		GraphEdges: []*protocol.UiGraphEdge{
			{From: "alpha", To: "beta", Label: "refs"},
			{From: "beta", To: "gamma", Dashed: true, Tone: "danger"},
		},
	}

	type ev struct {
		node, kind, value string
		values            map[string]string
	}
	var got []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:okf/vault",
			form:  form,
			onEvent: func(_, node, kind, value string, values map[string]string) {
				got = append(got, ev{node, kind, value, values})
			},
		}
		return v.node(graph, false)
	}, ggui.Sz(560, 420))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	sem := p.Semantics().String()
	for _, want := range []string{"Knowledge Graph", "Alpha Node", "Beta Node", "Gamma Node", "Zoom in", "Zoom out", "Reset"} {
		if !strings.Contains(sem, want) {
			t.Fatalf("graph semantics missing %q:\n%s", want, sem)
		}
	}

	// Click Beta Node using its reported semantic bounds and verify UiChange fires with "beta".
	betaSem, ok := p.Semantics().Find(ggui.RoleButton, "Beta Node")
	if !ok {
		t.Fatalf("Beta Node semantic leaf not found:\n%s", sem)
	}
	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}
	p.Click(center(betaSem.Rect))
	p.Frame()
	if len(got) != 1 || got[0].node != "/graph" || got[0].kind != protocol.UiChange || got[0].value != "beta" {
		t.Fatalf("click Beta Node events = %+v, want UiChange beta", got)
	}
	if len(got[0].values) != 0 {
		t.Fatalf("graph click unexpectedly included form values: %v", got[0].values)
	}

	// Dragging on the canvas pans without firing a node selection event.
	got = nil
	st := form.graphState("/graph")
	start := ggui.Pt(80, 120)
	end := ggui.Pt(150, 170)
	p.Press(start)
	p.Frame()
	p.Move(ggui.Pt(115, 145))
	p.Frame()
	p.Move(end)
	p.Frame()
	p.Release(end)
	p.Frame()
	if len(got) != 0 {
		t.Fatalf("pan drag unexpectedly fired selection event: %+v", got)
	}
	if st.panX == 0 && st.panY == 0 {
		t.Fatalf("expected non-zero pan after drag, got (%v, %v)", st.panX, st.panY)
	}

	// Zoom in and Reset buttons adjust view state cleanly.
	zoomIn, ok := p.Semantics().Find(ggui.RoleButton, "Zoom in")
	if !ok {
		t.Fatalf("Zoom in button not found")
	}
	p.Click(center(zoomIn.Rect))
	p.Frame()
	if st.zoom <= 1.0 {
		t.Fatalf("expected zoom > 1.0 after Zoom in, got %v", st.zoom)
	}
	resetBtn, ok := p.Semantics().Find(ggui.RoleButton, "Reset")
	if !ok {
		t.Fatalf("Reset button not found")
	}
	p.Click(center(resetBtn.Rect))
	p.Frame()
	if st.zoom != 1.0 || st.panX != 0 || st.panY != 0 {
		t.Fatalf("expected reset view (1,0,0), got zoom=%v pan=(%v,%v)", st.zoom, st.panX, st.panY)
	}
}

// Regression test [QA-5]: plain mouse-wheel scrolling over a ui.graph must not
// hijack scrolling from an enclosing ui.scroll container; only Ctrl/Meta/Shift +
// wheel (or the explicit +/- buttons) zooms the graph canvas.
func TestPluginGraphWheelPassesThroughToParentScroll(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	tree := &protocol.UiNode{
		Id:   "/sc",
		Kind: "scroll",
		Children: []*protocol.UiNode{
			{
				Id:        "/sc/graph",
				Kind:      "graph",
				Text:      "Knowledge Graph",
				MaxHeight: 280,
				GraphNodes: []*protocol.UiGraphNode{
					{Id: "a", Label: "Alpha Node", Tone: "accent"},
					{Id: "b", Label: "Beta Node", Tone: "success"},
				},
				GraphEdges: []*protocol.UiGraphEdge{
					{From: "a", To: "b"},
				},
			},
			{Id: "/sc/spacer", Kind: "card", Children: []*protocol.UiNode{
				{Id: "/sc/spacer/1", Kind: "text", Text: "Section One"},
				{Id: "/sc/spacer/2", Kind: "text", Text: "Section Two"},
				{Id: "/sc/spacer/3", Kind: "text", Text: "Section Three"},
				{Id: "/sc/spacer/4", Kind: "text", Text: "Section Four"},
				{Id: "/sc/spacer/5", Kind: "text", Text: "Insights Footer"},
			}},
		},
	}

	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{block: "plugin:okf/vault", form: form}
		return v.node(tree, false)
	}, ggui.Sz(420, 320))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	st := form.graphState("/sc/graph")
	footerBefore, ok := p.Semantics().Find(ggui.RoleText, "Insights Footer")
	if !ok {
		t.Fatalf("missing Insights Footer:\n%s", p.Semantics().String())
	}

	// Plain wheel scroll over the graph canvas must pass through to the parent scroll
	// and must not zoom the graph.
	p.Scroll(ggui.Pt(210, 140), ggui.Pt(0, -120))
	for range 3 {
		p.Frame()
	}
	if st.zoom != 1.0 {
		t.Fatalf("plain wheel unexpectedly zoomed graph to %v", st.zoom)
	}
	footerAfter, ok := p.Semantics().Find(ggui.RoleText, "Insights Footer")
	if !ok {
		t.Fatalf("missing Insights Footer after scroll")
	}
	if footerAfter.Rect.Origin.Y >= footerBefore.Rect.Origin.Y {
		t.Fatalf("parent scroll did not move on plain wheel over graph: beforeY=%.1f afterY=%.1f",
			footerBefore.Rect.Origin.Y, footerAfter.Rect.Origin.Y)
	}

	// Verify canvas HandlePointer directly: plain wheel returns false; Ctrl/Meta/Shift + wheel zooms.
	canvas := &pluginGraphCanvas{state: newGraphViewState()}
	if canvas.HandlePointer(ggui.PointerEvent{Kind: ggui.PointerScroll, Scroll: ggui.Pt(0, -2)}) {
		t.Fatal("plain PointerScroll should return false")
	}
	if canvas.state.zoom != 1.0 {
		t.Fatalf("plain PointerScroll changed zoom to %v", canvas.state.zoom)
	}
	for _, tc := range []struct {
		name string
		mods ggui.Mods
	}{
		{"ctrl", ggui.Mods{Ctrl: true}},
		{"meta", ggui.Mods{Meta: true}},
		{"shift", ggui.Mods{Shift: true}},
	} {
		canvas.state.reset()
		if !canvas.HandlePointer(ggui.PointerEvent{Kind: ggui.PointerScroll, Scroll: ggui.Pt(0, -2), Mods: tc.mods}) {
			t.Fatalf("%s+wheel should be consumed by graph canvas", tc.name)
		}
		if canvas.state.zoom <= 1.0 {
			t.Fatalf("%s+wheel did not zoom in: zoom=%v", tc.name, canvas.state.zoom)
		}
	}
}

func TestPluginRichUIv2Components(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")
	m := newModel(config.Default(), "")
	m.post = func(fn func()) { fn() }

	tree := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Children: []*protocol.UiNode{
			{
				Id:      "/bc",
				Kind:    "breadcrumb",
				Options: []string{"Vault", "architecture", "plugin-system.md"},
				Change:  true,
			},
			{
				Id:   "/wrap",
				Kind: "wrap",
				Gap:  6,
				Children: []*protocol.UiNode{
					{Id: "/wrap/1", Kind: "pill", Text: "architecture", Tone: "accent"},
					{Id: "/wrap/2", Kind: "chip", Text: "v2"},
					{Id: "/wrap/btn-sm", Kind: "button", Text: "Small Ghost", Size: "small", Variant: "ghost", Tap: true},
					{Id: "/wrap/btn-lg", Kind: "button", Text: "Large Primary", Size: "large", Variant: "primary", Tap: true},
					{Id: "/wrap/btn-icon", Kind: "button", Icon: "plus", Size: "small", Variant: "secondary", Tap: true},
				},
			},
			{
				Id:      "/grid",
				Kind:    "grid",
				Columns: 3,
				Gap:     8,
				Children: []*protocol.UiNode{
					{Id: "/grid/c1", Kind: "card", Variant: "outline", Children: []*protocol.UiNode{{Id: "/grid/c1/t", Kind: "text", Text: "KPI 1"}}},
					{Id: "/grid/c2", Kind: "card", Variant: "secondary", Children: []*protocol.UiNode{{Id: "/grid/c2/t", Kind: "text", Text: "KPI 2"}}},
					{Id: "/grid/c3", Kind: "card", Variant: "ghost", Children: []*protocol.UiNode{{Id: "/grid/c3/t", Kind: "text", Text: "KPI 3"}}},
				},
			},
			{
				Id:        "/split",
				Kind:      "split",
				Direction: "horizontal",
				Ratio:     0.4,
				MaxHeight: 120,
				Children: []*protocol.UiNode{
					{Id: "/split/l", Kind: "text", Text: "Left Pane"},
					{Id: "/split/r", Kind: "text", Text: "Right Pane"},
				},
			},
			{
				Id:      "/col",
				Kind:    "collapsible",
				Text:    "Metadata Details",
				Icon:    "info",
				Checked: false,
				Change:  true,
				Children: []*protocol.UiNode{
					{Id: "/col/body", Kind: "text", Text: "Hidden Provenance"},
				},
			},
			{
				Id:   "/tree",
				Kind: "tree",
				Children: []*protocol.UiNode{
					{
						Id:         "/tree/dir",
						Kind:       "tree_item",
						Text:       "architecture",
						Value:      "2 docs",
						Icon:       "folder",
						Checked:    true,
						Change:     true,
						Drop:       true,
						DropAccept: []string{"okf-doc"},
						Children: []*protocol.UiNode{
							{
								Id:        "/tree/dir/doc1",
								Kind:      "tree_item",
								Text:      "plugin-system.md",
								Value:     "Concept",
								Icon:      "file-text",
								Tone:      "accent",
								Tap:       true,
								Drag:      "architecture/plugin-system.md",
								DragGroup: "okf-doc",
							},
						},
					},
					{
						Id:         "/tree/archive",
						Kind:       "tree_item",
						Text:       "archive",
						Icon:       "folder",
						Checked:    false,
						Change:     true,
						Drop:       true,
						DropAccept: []string{"okf-doc"},
					},
				},
			},
			{
				Id:     "/donut",
				Kind:   "chart",
				Chart:  "donut",
				Text:   "Docs by Type",
				Labels: []string{"Concept", "Decision", "Guide"},
				Series: []*protocol.UiSeries{{Name: "Docs", Values: []float64{5, 3, 2}}},
			},
			{
				Id:     "/pie",
				Kind:   "chart",
				Chart:  "pie",
				Text:   "Status Share",
				Labels: []string{"Active", "Review"},
				Series: []*protocol.UiSeries{{Name: "Status", Values: []float64{8, 2}}},
			},
			{
				Id:     "/md",
				Kind:   "markdown",
				Text:   "See [Plugin Spec](okf:plugin-system.md) for details.",
				Change: true,
			},
		},
	}

	type ev struct {
		node, kind, value string
		values            map[string]string
	}
	var got []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			m:     m,
			block: "plugin:okf/vault",
			form:  form,
			onEvent: func(_, node, kind, value string, values map[string]string) {
				got = append(got, ev{node, kind, value, values})
			},
		}
		return v.node(tree, false)
	}, ggui.Sz(640, 1400))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	sem := p.Semantics().String()
	for _, want := range []string{
		"Vault", "architecture", "plugin-system.md",
		"Small Ghost", "Large Primary", "plus",
		"KPI 1", "KPI 2", "KPI 3",
		"Left Pane", "Right Pane",
		"Metadata Details",
		"Docs by Type", "Status Share",
		"Plugin Spec",
	} {
		if !strings.Contains(sem, want) {
			t.Fatalf("missing %q in semantics:\n%s", want, sem)
		}
	}

	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}

	// 1. Breadcrumb click fires UiChange with 0-based index string.
	bcLink, ok := p.Semantics().Find(ggui.RoleLink, "Vault")
	if !ok {
		t.Fatalf("breadcrumb link Vault not found:\n%s", sem)
	}
	p.Click(center(bcLink.Rect))
	p.Frame()
	if len(got) != 1 || got[0].node != "/bc" || got[0].kind != protocol.UiChange || got[0].value != "0" {
		t.Fatalf("breadcrumb click events = %+v, want /bc change 0", got)
	}
	// Local UI states (split/collapsible/tree_item) must NOT leak into form values.
	if len(got[0].values) != 0 {
		t.Fatalf("non-form UI state leaked into BlockEvent.Values: %v", got[0].values)
	}

	// 2. Icon-only button click fires UiTap.
	got = nil
	iconBtn, ok := p.Semantics().Find(ggui.RoleButton, "plus")
	if !ok {
		t.Fatalf("icon-only button 'plus' not found:\n%s", p.Semantics().String())
	}
	p.Click(center(iconBtn.Rect))
	p.Frame()
	if len(got) != 1 || got[0].node != "/wrap/btn-icon" || got[0].kind != protocol.UiTap {
		t.Fatalf("icon-only button click events = %+v", got)
	}

	// 3. Collapsible click reveals children and fires UiChange "true".
	got = nil
	colBtn, ok := p.Semantics().Find(ggui.RoleDisclosure, "Metadata Details")
	if !ok {
		t.Fatalf("collapsible disclosure not found:\n%s", p.Semantics().String())
	}
	p.Click(center(colBtn.Rect))
	for range 3 {
		p.Frame()
	}
	if len(got) != 1 || got[0].node != "/col" || got[0].kind != protocol.UiChange || got[0].value != "true" {
		t.Fatalf("collapsible toggle events = %+v, want /col change true", got)
	}

	// 4. Tree item tap fires UiTap; chevron toggle fires UiChange "false"; drag onto archive fires UiDrop.
	got = nil
	docBtn, ok := p.Semantics().Find(ggui.RoleButton, "plugin-system.md")
	if !ok {
		t.Fatalf("tree item plugin-system.md not found:\n%s", p.Semantics().String())
	}
	p.Click(center(docBtn.Rect))
	p.Frame()
	if len(got) != 1 || got[0].node != "/tree/dir/doc1" || got[0].kind != protocol.UiTap {
		t.Fatalf("tree item tap events = %+v", got)
	}

	got = nil
	archiveBtn, ok := p.Semantics().Find(ggui.RoleButton, "archive")
	if !ok {
		t.Fatalf("tree item archive not found:\n%s", p.Semantics().String())
	}
	from := center(docBtn.Rect)
	to := center(archiveBtn.Rect)
	p.Press(from)
	p.Frame()
	p.Move(ggui.Pt((from.X+to.X)/2, (from.Y+to.Y)/2))
	p.Frame()
	p.Move(to)
	p.Frame()
	p.Release(to)
	p.Frame()
	if len(got) != 1 || got[0].node != "/tree/archive" || got[0].kind != protocol.UiDrop || got[0].value != "architecture/plugin-system.md" {
		t.Fatalf("tree item drag/drop events = %+v, want drop onto /tree/archive", got)
	}

	got = nil
	chevBtn, ok := p.Semantics().Find(ggui.RoleButton, "Collapse architecture")
	if !ok {
		t.Fatalf("tree chevron Collapse architecture not found:\n%s", p.Semantics().String())
	}
	p.Click(center(chevBtn.Rect))
	p.Frame()
	if len(got) != 1 || got[0].node != "/tree/dir" || got[0].kind != protocol.UiChange || got[0].value != "false" {
		t.Fatalf("tree chevron collapse events = %+v", got)
	}

	// 5. Markdown onLink fires UiChange with the link target.
	got = nil
	mdLink, ok := p.Semantics().Find(ggui.RoleLink, "okf:plugin-system.md")
	if !ok {
		t.Fatalf("markdown link okf:plugin-system.md not found:\n%s", p.Semantics().String())
	}
	p.Click(center(mdLink.Rect))
	p.Frame()
	if len(got) != 1 || got[0].node != "/md" || got[0].kind != protocol.UiChange || got[0].value != "okf:plugin-system.md" {
		t.Fatalf("markdown onLink events = %+v", got)
	}
}

func TestPluginBlockFillRootLayout(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")
	m := newModel(config.Default(), "")

	tree := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Fill: true,
		Children: []*protocol.UiNode{
			{Id: "/toolbar", Kind: "row", Children: []*protocol.UiNode{
				{Id: "/toolbar/t", Kind: "text", Text: "Sticky Header", Grow: 1},
				{Id: "/toolbar/b", Kind: "button", Text: "New", Size: "small", Variant: "ghost", Tap: true},
			}},
			{Id: "/body", Kind: "scroll", Grow: 1, Children: []*protocol.UiNode{
				{Id: "/body/t", Kind: "text", Text: "Scrollable Body Content"},
			}},
		},
	}

	p := ggui.ProbeBuilder(func() ggui.Widget {
		return pluginFrame(m, "plugin:okf/vault", "okf", "Vault", &protocol.BlockUpdate{
			Title: "Vault",
			State: protocol.PluginRunning,
			Tree:  tree,
		})
	}, ggui.Sz(440, 360))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	sem := p.Semantics().String()
	for _, want := range []string{"Vault", "Sticky Header", "New", "Scrollable Body Content"} {
		if !strings.Contains(sem, want) {
			t.Fatalf("fill root layout missing %q:\n%s", want, sem)
		}
	}
}

func TestPluginLocaleResolution(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	old := i18n.Current()
	t.Cleanup(func() { i18n.Set(old) })

	m := newModel(config.Default(), "")
	i18n.Set("en")
	m.Prefs.Update(func(p prefs) prefs { p.Language = "system"; return p })
	if got := m.currentLocale(); got != "en" {
		t.Fatalf("system/en currentLocale = %q, want en", got)
	}
	m.Prefs.Update(func(p prefs) prefs { p.Language = "ko"; return p })
	if got := m.currentLocale(); got != "ko" {
		t.Fatalf("ko currentLocale = %q, want ko", got)
	}
	if got := m.activeLocale(); got != "ko" {
		t.Fatalf("ko activeLocale = %q, want ko", got)
	}
}

func TestPluginGraphCardNodesBadgesAndHulls(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	graph := &protocol.UiNode{
		Id:        "/cards",
		Kind:      "graph",
		Variant:   "cards",
		MaxHeight: 460,
		Change:    true,
		GraphNodes: []*protocol.UiGraphNode{
			{
				Id: "lead", Label: "Lead Agent", Group: "cluster-a", Tone: "accent",
				Subtitle: "claude · main", Shape: "card", Icon: "bot", Status: "working",
				Badges: []*protocol.UiBadge{{Text: "main", Tone: "accent"}, {Text: "live", Tone: "success"}},
			},
			{
				Id: "sub", Label: "Sub Agent", Group: "cluster-a", Tone: "success",
				Subtitle: "codex · fix", Shape: "circle", Icon: "sparkles", Status: "waiting",
			},
			{
				Id: "ext", Label: "Docs Hub", Group: "cluster-b", Shape: "card", Icon: "layers", Status: "done",
			},
		},
		GraphEdges: []*protocol.UiGraphEdge{
			{From: "lead", To: "sub", Label: "spawn", Tone: "accent", Active: true, Kind: "parent"},
			{From: "sub", To: "lead", Label: "reply", Tone: "warning", Dashed: true, Active: true, Kind: "agentMessage"},
			{From: "lead", To: "ext", Label: "reads"},
		},
	}

	var got []string
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:agent-map/map",
			form:  form,
			onEvent: func(_, _, kind, value string, _ map[string]string) {
				got = append(got, kind+":"+value)
			},
		}
		return ggui.Provide(ggui.ReducedMotionKey, true, v.node(graph, false))
	}, ggui.Sz(720, 520))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	leadSem, ok := p.Semantics().Find(ggui.RoleButton, "Lead Agent")
	if !ok {
		t.Fatalf("Lead Agent card semantic leaf missing:\n%s", p.Semantics().String())
	}
	if leadSem.Rect.Size.W < 100 || leadSem.Rect.Size.H < 40 {
		t.Fatalf("expected card-sized semantic rect, got %+v", leadSem.Rect)
	}
	subSem, ok := p.Semantics().Find(ggui.RoleButton, "Sub Agent")
	if !ok {
		t.Fatalf("Sub Agent circle semantic leaf missing:\n%s", p.Semantics().String())
	}
	if subSem.Rect.Size.W > 80 {
		t.Fatalf("expected circle-sized semantic rect for explicit shape:circle, got %+v", subSem.Rect)
	}
	p.Click(ggui.Pt(leadSem.Rect.Origin.X+leadSem.Rect.Size.W/2, leadSem.Rect.Origin.Y+leadSem.Rect.Size.H/2))
	p.Frame()
	if len(got) != 1 || got[0] != protocol.UiChange+":lead" {
		t.Fatalf("card click events = %v, want [change:lead]", got)
	}
}

func TestPluginGraphLayoutModesAndTween(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	nodes := []*protocol.UiGraphNode{
		{Id: "a", Label: "Root", Group: "g1", Weight: 3},
		{Id: "b", Label: "Child 1", Group: "g1", Weight: 2},
		{Id: "c", Label: "Child 2", Group: "g2", Weight: 1, X: 5.5, Y: -2.5, HasPos: true},
	}
	edges := []*protocol.UiGraphEdge{
		{From: "a", To: "b"},
		{From: "a", To: "c"},
	}

	for _, mode := range []string{"force", "hierarchy", "radial"} {
		l1 := computeGraphLayoutWithMode(nodes, edges, mode, nil, false)
		l2 := computeGraphLayoutWithMode(nodes, edges, mode, nil, false)
		if l1.pos["a"] != l2.pos["a"] || l1.pos["b"] != l2.pos["b"] {
			t.Fatalf("mode %s not deterministic", mode)
		}
		if l1.pos["c"] != ggui.Pt(5.5, -2.5) {
			t.Fatalf("mode %s did not preserve explicit hasPos on node c: got %v", mode, l1.pos["c"])
		}
	}

	graph := &protocol.UiNode{
		Id:          "/g",
		Kind:        "graph",
		MaxHeight:   360,
		GraphLayout: "force",
		Change:      true,
		GraphNodes: []*protocol.UiGraphNode{
			{Id: "a", Label: "Root", Group: "g1"},
			{Id: "b", Label: "Child 1", Group: "g1"},
			{Id: "c", Label: "Child 2", Group: "g2"},
		},
		GraphEdges: edges,
	}

	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{block: "plugin:test/g", form: form}
		return v.node(graph, false)
	}, ggui.Sz(640, 420))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	st := form.graphState("/g")
	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}

	hierBtn, ok := p.Semantics().Find(ggui.RoleButton, "Hierarchy layout")
	if !ok {
		t.Fatalf("Hierarchy layout button missing:\n%s", p.Semantics().String())
	}
	p.Click(center(hierBtn.Rect))
	p.Frame()
	if st.layoutMode != graphLayoutHierarchy {
		t.Fatalf("layoutMode = %q, want hierarchy", st.layoutMode)
	}
	p.Advance(120 * time.Millisecond)
	p.Advance(200 * time.Millisecond)

	radBtn, ok := p.Semantics().Find(ggui.RoleButton, "Radial layout")
	if !ok {
		t.Fatalf("Radial layout button missing")
	}
	p.Click(center(radBtn.Rect))
	p.Frame()
	if st.layoutMode != graphLayoutRadial {
		t.Fatalf("layoutMode = %q, want radial", st.layoutMode)
	}

	fitBtn, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
	if !ok {
		t.Fatalf("Fit view button missing")
	}
	p.Click(center(fitBtn.Rect))
	p.Advance(260 * time.Millisecond)
	if st.panX != 0 || st.panY != 0 {
		t.Fatalf("Fit view pan = (%v,%v), want (0,0)", st.panX, st.panY)
	}
}

func TestPluginGraphLiveMotionAndReducedMotion(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	graph := &protocol.UiNode{
		Id:        "/live",
		Kind:      "graph",
		Variant:   "cards",
		MaxHeight: 320,
		GraphNodes: []*protocol.UiGraphNode{
			{Id: "w", Label: "Working", Status: "working", Shape: "card"},
			{Id: "wt", Label: "Waiting", Status: "waiting", Shape: "circle"},
		},
		GraphEdges: []*protocol.UiGraphEdge{
			{From: "w", To: "wt", Active: true, Dashed: true, Kind: "agentMessage"},
		},
	}

	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{block: "plugin:test/live", form: form}
		return v.node(graph, false)
	}, ggui.Sz(560, 380))
	defer p.Close()
	p.Frame()
	p.Advance(160 * time.Millisecond)
	p.Advance(160 * time.Millisecond)

	formRM := newBlockForm()
	pRM := ggui.ProbeBuilder(func() ggui.Widget {
		formRM.redraw()
		v := &treeView{block: "plugin:test/live-rm", form: formRM}
		return ggui.Provide(ggui.ReducedMotionKey, true, v.node(graph, false))
	}, ggui.Sz(560, 380))
	defer pRM.Close()
	pRM.Frame()
	pRM.Advance(160 * time.Millisecond)
}

func TestPluginGraphFillGrowAndMinimap(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	tree := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Fill: true,
		Children: []*protocol.UiNode{
			{Id: "/root/hdr", Kind: "text", Text: "Map Header"},
			{
				Id:      "/root/g",
				Kind:    "graph",
				Fill:    true,
				Minimap: true,
				GraphNodes: []*protocol.UiGraphNode{
					{Id: "a", Label: "Alpha"},
					{Id: "b", Label: "Beta"},
				},
				GraphEdges: []*protocol.UiGraphEdge{{From: "a", To: "b"}},
			},
		},
	}

	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{block: "plugin:test/fill", form: form}
		return widget.Expanded(v.node(tree, false))
	}, ggui.Sz(700, 500))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	st := form.graphState("/root/g")
	if !st.minimapOpen {
		t.Fatal("expected minimapOpen=true from node Minimap field")
	}
	if st.lastRect.Size.H < 420 {
		t.Fatalf("fill graph height = %.1f, expected > 420", st.lastRect.Size.H)
	}

	p.Resize(ggui.Sz(700, 720))
	for range 3 {
		p.Frame()
	}
	if st.lastRect.Size.H < 640 {
		t.Fatalf("fill graph height after resize = %.1f, expected > 640", st.lastRect.Size.H)
	}

	// Click inside bottom-right minimap to pan camera.
	mmPt := ggui.Pt(st.lastRect.Origin.X+st.lastRect.Size.W-40, st.lastRect.Origin.Y+st.lastRect.Size.H-30)
	p.Click(mmPt)
	p.Frame()
	if st.panX == 0 && st.panY == 0 {
		t.Fatalf("expected minimap click to pan camera, got (%v,%v)", st.panX, st.panY)
	}

	// Toggle minimap off.
	mmBtn, ok := p.Semantics().Find(ggui.RoleButton, "Toggle minimap")
	if !ok {
		t.Fatal("Toggle minimap button missing")
	}
	p.Click(ggui.Pt(mmBtn.Rect.Origin.X+mmBtn.Rect.Size.W/2, mmBtn.Rect.Origin.Y+mmBtn.Rect.Size.H/2))
	p.Frame()
	if st.minimapOpen {
		t.Fatal("Toggle minimap did not close minimap")
	}
}

func TestPluginGraphNodeDragDropAndDoubleTap(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	graph := &protocol.UiNode{
		Id:        "/g",
		Kind:      "graph",
		MaxHeight: 360,
		Change:    true,
		DoubleTap: true,
		Drop:      true,
		GraphNodes: []*protocol.UiGraphNode{
			{Id: "alpha", Label: "Alpha Node", X: -2, Y: 0, HasPos: true},
			{Id: "beta", Label: "Beta Node", X: 2, Y: 0, HasPos: true},
		},
		GraphEdges: []*protocol.UiGraphEdge{{From: "alpha", To: "beta"}},
	}

	type ev struct{ kind, value string }
	var events []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:test/dnd",
			form:  form,
			onEvent: func(_, _, kind, value string, _ map[string]string) {
				events = append(events, ev{kind, value})
			},
		}
		return v.node(graph, false)
	}, ggui.Sz(600, 400))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	alphaSem, ok := p.Semantics().Find(ggui.RoleButton, "Alpha Node")
	if !ok {
		t.Fatal("Alpha Node missing")
	}
	start := ggui.Pt(alphaSem.Rect.Origin.X+alphaSem.Rect.Size.W/2, alphaSem.Rect.Origin.Y+alphaSem.Rect.Size.H/2)
	end := ggui.Pt(start.X+36, start.Y+28)
	p.Press(start)
	p.Frame()
	p.Move(ggui.Pt(start.X+18, start.Y+14))
	p.Frame()
	p.Move(end)
	p.Frame()
	p.Release(end)
	p.Frame()

	if len(events) != 1 || events[0].kind != protocol.UiDrop {
		t.Fatalf("node drag/drop events = %+v, want single UiDrop", events)
	}
	var dropPos struct {
		ID string  `json:"id"`
		X  float64 `json:"x"`
		Y  float64 `json:"y"`
	}
	if err := json.Unmarshal([]byte(events[0].value), &dropPos); err != nil || dropPos.ID != "alpha" {
		t.Fatalf("invalid drop payload %q: %v", events[0].value, err)
	}

	// Double-tap Beta Node within 400ms fires UiDoubleTap.
	events = nil
	betaSem, ok := p.Semantics().Find(ggui.RoleButton, "Beta Node")
	if !ok {
		t.Fatal("Beta Node missing")
	}
	betaPt := ggui.Pt(betaSem.Rect.Origin.X+betaSem.Rect.Size.W/2, betaSem.Rect.Origin.Y+betaSem.Rect.Size.H/2)
	p.Click(betaPt)
	p.Advance(120 * time.Millisecond)
	p.Click(betaPt)
	p.Frame()

	foundDoubleTap := false
	for _, e := range events {
		if e.kind == protocol.UiDoubleTap && e.value == "beta" {
			foundDoubleTap = true
		}
	}
	if !foundDoubleTap {
		t.Fatalf("expected UiDoubleTap for beta, got %+v", events)
	}
}

func TestPluginGraphContextMenuAndHover1Hop(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s1", NodeId: "n1", Agent: "claude", Project: "/repo/app", State: protocol.StateRunning},
	})
	m.openSidebarPage("plugin:agent-map/map")

	tree := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Children: []*protocol.UiNode{
			{
				Id:        "/root/g",
				Kind:      "graph",
				MaxHeight: 360,
				Change:    true,
				GraphNodes: []*protocol.UiGraphNode{
					{Id: "sess:s1", Label: "Session S1", Subtitle: "claude", Status: "working", X: -2, Y: 0, HasPos: true},
					{Id: "doc:1", Label: "Doc Only", ContextMenu: "doc-ctx", ContextArg: "doc-1", X: 2, Y: 0, HasPos: true},
					{Id: "plain", Label: "Plain Node", X: 0, Y: 2, HasPos: true},
				},
				GraphEdges: []*protocol.UiGraphEdge{
					{From: "sess:s1", To: "doc:1"},
				},
			},
			{
				Id:      "/doc-ctx",
				Kind:    "menu",
				Variant: "context",
				Children: []*protocol.UiNode{
					{Id: "/doc-ctx/open", Kind: "menu_item", Text: "Inspect Doc", Tap: true},
				},
			},
		},
	}

	type ev struct{ node, kind, value string }
	var events []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := newTreeView(m, "plugin:agent-map/map", "", form, tree)
		v.onEvent = func(_, node, kind, value string, _ map[string]string) {
			events = append(events, ev{node, kind, value})
		}
		return ggui.Provide(ggui.ReducedMotionKey, true, v.node(tree, false))
	}, ggui.Sz(680, 440))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}

	// 1. Right-click empty background -> NO menu opens.
	p.ClickButton(ggui.Pt(20, 20), ggui.MouseButtonRight)
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleMenuItem, "Open session"); ok {
		t.Fatal("right-clicking empty background unexpectedly opened context menu")
	}

	// 2. Right-click plain node without contextMenu and not a live session -> NO menu opens.
	plainSem, ok := p.Semantics().Find(ggui.RoleButton, "Plain Node")
	if !ok {
		t.Fatal("Plain Node missing")
	}
	p.ClickButton(center(plainSem.Rect), ggui.MouseButtonRight)
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleMenuItem, "Open session"); ok {
		t.Fatal("right-clicking non-session plain node unexpectedly opened session context menu")
	}

	// 3. Right-click plugin contextMenu node -> opens Inspect Doc & sends UiTap with contextArg.
	docSem, ok := p.Semantics().Find(ggui.RoleButton, "Doc Only")
	if !ok {
		t.Fatal("Doc Only node missing")
	}
	p.ClickButton(center(docSem.Rect), ggui.MouseButtonRight)
	p.Frame()
	inspItem, ok := p.Semantics().Find(ggui.RoleMenuItem, "Inspect Doc")
	if !ok {
		t.Fatalf("plugin context menu item Inspect Doc not found:\n%s", p.Semantics().String())
	}
	p.Click(center(inspItem.Rect))
	p.Frame()
	if len(events) != 1 || events[0].node != "/doc-ctx/open" || events[0].kind != protocol.UiTap || events[0].value != "doc-1" {
		t.Fatalf("plugin context menu tap events = %+v, want UiTap doc-1", events)
	}

	// 4. Right-click live session node sess:s1 -> opens native session actions & Run subagent sets ParentSessionID.
	s1Sem, ok := p.Semantics().Find(ggui.RoleButton, "Session S1")
	if !ok {
		t.Fatal("Session S1 node missing")
	}
	p.ClickButton(center(s1Sem.Rect), ggui.MouseButtonRight)
	p.Frame()
	for _, want := range []string{"Open session", "Send message", "Run subagent", "Kill session"} {
		if _, ok := p.Semantics().Find(ggui.RoleMenuItem, want); !ok {
			t.Fatalf("missing session context menu item %q:\n%s", want, p.Semantics().String())
		}
	}
	subItem, _ := p.Semantics().Find(ggui.RoleMenuItem, "Run subagent")
	p.Click(center(subItem.Rect))
	p.Frame()
	if len(m.launches) != 1 {
		t.Fatalf("expected 1 subagent launch tab, got %d", len(m.launches))
	}
	for _, ls := range m.launches {
		if ggui.Peek(ls.Form).ParentSessionID != "s1" {
			t.Fatalf("launch ParentSessionID = %q, want s1", ggui.Peek(ls.Form).ParentSessionID)
		}
	}

	// 5. Hover over Session S1 -> sets hoveredID and renders floating Detail card.
	p.Move(center(s1Sem.Rect))
	p.Frame()
	st := form.graphState("/root/g")
	if st.hoveredID != "sess:s1" {
		t.Fatalf("hoveredID = %q, want sess:s1", st.hoveredID)
	}
	if _, ok := p.Semantics().Find(ggui.RoleStatus, "Detail: Session S1"); !ok {
		t.Fatalf("floating hover detail card missing:\n%s", p.Semantics().String())
	}
}

func TestPluginGraphKeyboardNavigation(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	graph := &protocol.UiNode{
		Id:        "/kg",
		Kind:      "graph",
		MaxHeight: 320,
		Value:     "left",
		Change:    true,
		DoubleTap: true,
		GraphNodes: []*protocol.UiGraphNode{
			{Id: "left", Label: "Left Node", X: -2, Y: 0, HasPos: true},
			{Id: "right", Label: "Right Node", X: 2, Y: 0, HasPos: true},
		},
		GraphEdges: []*protocol.UiGraphEdge{{From: "left", To: "right"}},
	}

	type ev struct{ kind, value string }
	var events []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:test/keys",
			form:  form,
			onEvent: func(_, _, kind, value string, _ map[string]string) {
				events = append(events, ev{kind, value})
			},
		}
		return ggui.Provide(ggui.ReducedMotionKey, true, v.node(graph, false))
	}, ggui.Sz(560, 360))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	// Click canvas to focus it, then press ArrowRight and Enter.
	p.Click(ggui.Pt(40, 40))
	p.Frame()
	events = nil
	p.Type(ggui.Mods{}, ggui.KeyArrowRight)
	p.Frame()
	if len(events) != 1 || events[0].kind != protocol.UiChange || events[0].value != "right" {
		t.Fatalf("ArrowRight events = %+v, want change:right", events)
	}

	p.Type(ggui.Mods{}, ggui.KeyEnter)
	p.Frame()
	if len(events) != 2 || events[1].kind != protocol.UiDoubleTap || events[1].value != "right" {
		t.Fatalf("Enter events = %+v, want double_tap:right", events)
	}
}

func TestPluginGraphHoverExitClearsState(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	tree := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Children: []*protocol.UiNode{
			{Id: "/root/top", Kind: "button", Text: "Top Button", Tap: true},
			{
				Id:        "/root/g",
				Kind:      "graph",
				Variant:   "cards",
				MaxHeight: 320,
				GraphNodes: []*protocol.UiGraphNode{
					{Id: "w", Label: "Worker", Status: "working", Shape: "card", X: 0, Y: 0, HasPos: true},
				},
			},
		},
	}

	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block:   "plugin:test/hover-exit",
			form:    form,
			onEvent: func(_, _, _, _ string, _ map[string]string) {},
		}
		return ggui.Provide(ggui.ReducedMotionKey, true, v.node(tree, false))
	}, ggui.Sz(640, 440))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}

	workerSem, ok := p.Semantics().Find(ggui.RoleButton, "Worker")
	if !ok {
		t.Fatalf("Worker node missing:\n%s", p.Semantics().String())
	}
	topSem, ok := p.Semantics().Find(ggui.RoleButton, "Top Button")
	if !ok {
		t.Fatalf("Top Button missing:\n%s", p.Semantics().String())
	}

	p.Move(center(workerSem.Rect))
	p.Frame()
	st := form.graphState("/root/g")
	if st.hoveredID != "w" {
		t.Fatalf("hoveredID after moving onto Worker = %q, want w", st.hoveredID)
	}
	if _, ok := p.Semantics().Find(ggui.RoleStatus, "Detail: Worker"); !ok {
		t.Fatalf("expected floating hover detail card while hovering Worker:\n%s", p.Semantics().String())
	}

	p.Advance(16 * time.Millisecond)
	p.Move(center(topSem.Rect))
	p.Frame()
	if st.hoveredID != "" {
		t.Fatalf("hoveredID did not clear on PointerExit onto Top Button: got %q", st.hoveredID)
	}
	if _, ok := p.Semantics().Find(ggui.RoleStatus, "Detail: Worker"); ok {
		t.Fatalf("floating hover detail card remained after PointerExit:\n%s", p.Semantics().String())
	}
}

func TestPluginGraphDoubleTapNeighborUnderCursor(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	graph := &protocol.UiNode{
		Id:        "/dt",
		Kind:      "graph",
		MaxHeight: 360,
		Change:    true,
		DoubleTap: true,
		GraphNodes: []*protocol.UiGraphNode{
			{Id: "a", Label: "Node A", X: -5, Y: 0, HasPos: true},
			{Id: "b", Label: "Node B", X: -10, Y: 0, HasPos: true},
			{Id: "c", Label: "Node C", X: 10, Y: 0, HasPos: true},
		},
	}

	type ev struct{ kind, value string }
	var events []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:test/dbltap-neighbor",
			form:  form,
			onEvent: func(_, _, kind, value string, _ map[string]string) {
				events = append(events, ev{kind, value})
			},
		}
		return v.node(graph, false)
	}, ggui.Sz(600, 400))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	aSem, ok := p.Semantics().Find(ggui.RoleButton, "Node A")
	if !ok {
		t.Fatal("Node A missing")
	}
	cA := ggui.Pt(aSem.Rect.Origin.X+aSem.Rect.Size.W/2, aSem.Rect.Origin.Y+aSem.Rect.Size.H/2)

	// 1st click on Node A triggers focusNode("a") camera pan.
	p.Click(cA)
	for range 18 {
		p.Advance(16 * time.Millisecond)
	}

	bSem, ok := p.Semantics().Find(ggui.RoleButton, "Node B")
	if !ok {
		t.Fatal("Node B missing after pan")
	}
	if !bSem.Rect.Contains(cA) {
		t.Fatalf("expected Node B rect %+v to pan under stationary cursor %+v", bSem.Rect, cA)
	}

	// 2nd click at the exact stationary cursor within 400ms must fire UiDoubleTap("a"), not UiChange("b").
	p.Click(cA)
	p.Frame()

	want := []ev{
		{protocol.UiChange, "a"},
		{protocol.UiDoubleTap, "a"},
	}
	if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] {
		t.Fatalf("events = %+v, want %+v", events, want)
	}
}
