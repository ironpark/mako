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
	"github.com/ironpark/mako/internal/uiblock"
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

func TestGraphBaseScaleWideAndDegenerateLayouts(t *testing.T) {
	rect := ggui.Rct(ggui.Pt(0, 0), ggui.Sz(720, 440))

	// Normal FR, hierarchy, and radial layouts remain comfortably scaled in [10, 95].
	normalNodes := []*protocol.UiGraphNode{
		{Id: "a", Label: "A", Group: "g1"},
		{Id: "b", Label: "B", Group: "g1"},
		{Id: "c", Label: "C", Group: "g2"},
		{Id: "d", Label: "D", Group: "g2"},
	}
	normalEdges := []*protocol.UiGraphEdge{
		{From: "a", To: "b"},
		{From: "a", To: "c"},
		{From: "c", To: "d"},
	}
	for _, mode := range []string{graphLayoutForce, graphLayoutHierarchy, graphLayoutRadial} {
		lay := computeGraphLayoutWithMode(normalNodes, normalEdges, mode, nil, false)
		s := graphBaseScale(rect, lay)
		if s < 10 || s > 95 {
			t.Fatalf("mode %s normal layout scale = %v, want in [10, 95]", mode, s)
		}
	}

	// Empty, single-node, and zero-span coincident layouts clamp cleanly at upper bound 95.
	empty := computeGraphLayout(nil, nil)
	if s := graphBaseScale(rect, empty); s != 95 {
		t.Fatalf("empty-layout scale = %v, want 95", s)
	}
	single := computeGraphLayout([]*protocol.UiGraphNode{{Id: "solo", X: 500, Y: -300, HasPos: true}}, nil)
	if s := graphBaseScale(rect, single); s != 95 {
		t.Fatalf("single-node scale = %v, want 95", s)
	}
	coincident := computeGraphLayout([]*protocol.UiGraphNode{
		{Id: "a", X: 250, Y: 250, HasPos: true},
		{Id: "b", X: 250, Y: 250, HasPos: true},
	}, nil)
	if s := graphBaseScale(rect, coincident); s != 95 {
		t.Fatalf("coincident-nodes scale = %v, want 95", s)
	}

	// Wide hasPos spans (±240, ±1000, ±100000) scale below 10 so nodes stay inside rect.
	for _, half := range []float64{240, 1000, 100000} {
		lay := computeGraphLayout([]*protocol.UiGraphNode{
			{Id: "left", X: -half, Y: -half / 2, HasPos: true},
			{Id: "right", X: half, Y: half / 2, HasPos: true},
		}, nil)
		s := graphBaseScale(rect, lay)
		if s >= 10 || s <= 0 {
			t.Fatalf("half=%v: scale = %v, want in (0, 10)", half, s)
		}
		canvas := &pluginGraphCanvas{state: &graphViewState{zoom: 1, layout: lay}}
		spL := canvas.worldToScreen(rect, lay.pos["left"])
		spR := canvas.worldToScreen(rect, lay.pos["right"])
		if !rect.Contains(spL) || !rect.Contains(spR) {
			t.Fatalf("half=%v: screen points %v / %v escaped rect %+v (scale=%v)", half, spL, spR, rect, s)
		}
	}

	// Zero viewport and non-finite span guards return safe positive finite scales.
	if s := graphBaseScale(ggui.Rect{}, single); s <= 0 || math.IsNaN(s) || math.IsInf(s, 0) {
		t.Fatalf("zero-rect scale = %v, want finite > 0", s)
	}
	if s := graphBaseScale(rect, graphLayout{spanW: math.NaN(), spanH: 10}); s <= 0 || math.IsNaN(s) || math.IsInf(s, 0) {
		t.Fatalf("NaN-span scale = %v, want finite > 0", s)
	}
	if s := graphBaseScale(rect, graphLayout{spanW: math.Inf(1), spanH: 10}); s <= 0 || math.IsNaN(s) || math.IsInf(s, 0) {
		t.Fatalf("Inf-span scale = %v, want finite > 0", s)
	}
}

func TestPluginGraphWideHasPosScaleAndRoundTrip(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	graph := &protocol.UiNode{
		Id:        "/wide",
		Kind:      "graph",
		Variant:   "cards",
		MaxHeight: 420,
		Minimap:   true,
		Change:    true,
		DoubleTap: true,
		Drop:      true,
		GraphNodes: []*protocol.UiGraphNode{
			{Id: "west", Label: "West Card", Shape: "card", X: -240, Y: 0, HasPos: true},
			{Id: "east", Label: "East Card", Shape: "card", X: 240, Y: 0, HasPos: true},
			{Id: "south", Label: "South Circle", Shape: "circle", X: 0, Y: 140, HasPos: true},
		},
		GraphEdges: []*protocol.UiGraphEdge{
			{From: "west", To: "east", Label: "span"},
		},
	}

	type ev struct{ kind, value string }
	var events []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:test/wide",
			form:  form,
			onEvent: func(_, _, kind, value string, _ map[string]string) {
				events = append(events, ev{kind, value})
			},
		}
		return ggui.Provide(ggui.ReducedMotionKey, true, v.node(graph, false))
	}, ggui.Sz(760, 480))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	st := form.graphState("/wide")
	if scale := graphBaseScale(st.lastRect, st.layout); scale >= 10 || scale <= 0 {
		t.Fatalf("wide graphBaseScale = %v, want in (0, 10)", scale)
	}

	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}
	for _, label := range []string{"West Card", "East Card", "South Circle"} {
		sem, ok := p.Semantics().Find(ggui.RoleButton, label)
		if !ok {
			t.Fatalf("missing node %q", label)
		}
		if !st.lastRect.Contains(center(sem.Rect)) {
			t.Fatalf("node %q center %v outside canvas %+v", label, center(sem.Rect), st.lastRect)
		}
	}

	// Hover + click East Card succeeds inside canvas.
	eastSem, _ := p.Semantics().Find(ggui.RoleButton, "East Card")
	p.Move(center(eastSem.Rect))
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleStatus, "Detail: East Card"); !ok {
		t.Fatalf("hover detail card missing for wide-coord node:\n%s", p.Semantics().String())
	}
	p.Click(center(eastSem.Rect))
	p.Frame()
	if len(events) != 1 || events[0].kind != protocol.UiChange || events[0].value != "east" {
		t.Fatalf("click East Card events = %+v, want [change:east]", events)
	}

	// Keyboard navigation: ArrowLeft from east -> west, then Enter -> double_tap:west.
	events = nil
	p.Type(ggui.Mods{}, ggui.KeyArrowLeft)
	p.Frame()
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	p.Frame()
	if len(events) != 2 ||
		events[0].kind != protocol.UiChange || events[0].value != "west" ||
		events[1].kind != protocol.UiDoubleTap || events[1].value != "west" {
		t.Fatalf("keyboard nav events = %+v, want [change:west double_tap:west]", events)
	}

	// Fit view keeps nodes on-screen and zoom in [0.55, 1.8].
	fitBtn, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
	if !ok {
		t.Fatal("Fit view button missing")
	}
	p.Click(center(fitBtn.Rect))
	p.Frame()
	if st.zoom < 0.55 || st.zoom > 1.8 {
		t.Fatalf("fitView zoom = %v, want in [0.55, 1.8]", st.zoom)
	}
	for _, label := range []string{"West Card", "East Card", "South Circle"} {
		sem, ok := p.Semantics().Find(ggui.RoleButton, label)
		if !ok || !st.lastRect.Contains(center(sem.Rect)) {
			t.Fatalf("after Fit view node %q missing or outside canvas %+v: %+v", label, st.lastRect, sem.Rect)
		}
	}

	// Drag South Circle and verify UiDrop emits world coordinates (not screen pixels).
	events = nil
	southSem, _ := p.Semantics().Find(ggui.RoleButton, "South Circle")
	start := center(southSem.Rect)
	end := ggui.Pt(start.X+40, start.Y-30)
	p.Press(start)
	p.Frame()
	p.Move(ggui.Pt(start.X+20, start.Y-15))
	p.Frame()
	p.Move(end)
	p.Frame()
	p.Release(end)
	p.Frame()
	if len(events) != 1 || events[0].kind != protocol.UiDrop {
		t.Fatalf("drag South Circle events = %+v, want single UiDrop", events)
	}
	var drop struct {
		ID string  `json:"id"`
		X  float64 `json:"x"`
		Y  float64 `json:"y"`
	}
	if err := json.Unmarshal([]byte(events[0].value), &drop); err != nil {
		t.Fatalf("unmarshal UiDrop %q: %v", events[0].value, err)
	}
	if drop.ID != "south" || drop.X <= 0 || drop.X >= 240 || drop.Y >= 140 || drop.Y <= -140 {
		t.Fatalf("unexpected wide-coord world drop payload: %+v", drop)
	}

	// Reload emitted UiDrop (x, y) with HasPos: true, Pinned: true in a fresh graph tree;
	// verify South Circle remains on-screen and clickable.
	reloaded := &protocol.UiNode{
		Id:        "/wide",
		Kind:      "graph",
		Variant:   "cards",
		MaxHeight: 420,
		Minimap:   true,
		Change:    true,
		DoubleTap: true,
		Drop:      true,
		GraphNodes: []*protocol.UiGraphNode{
			{Id: "west", Label: "West Card", Shape: "card", X: -240, Y: 0, HasPos: true},
			{Id: "east", Label: "East Card", Shape: "card", X: 240, Y: 0, HasPos: true},
			{Id: "south", Label: "South Circle", Shape: "circle", X: drop.X, Y: drop.Y, HasPos: true, Pinned: true},
		},
		GraphEdges: []*protocol.UiGraphEdge{
			{From: "west", To: "east", Label: "span"},
		},
	}
	var reloadedEvents []ev
	form2 := newBlockForm()
	p2 := ggui.ProbeBuilder(func() ggui.Widget {
		form2.redraw()
		v := &treeView{
			block: "plugin:test/wide-reload",
			form:  form2,
			onEvent: func(_, _, kind, value string, _ map[string]string) {
				reloadedEvents = append(reloadedEvents, ev{kind, value})
			},
		}
		return ggui.Provide(ggui.ReducedMotionKey, true, v.node(reloaded, false))
	}, ggui.Sz(760, 480))
	defer p2.Close()
	for range 3 {
		p2.Frame()
	}

	st2 := form2.graphState("/wide")
	gotPos := st2.layout.pos["south"]
	if math.Abs(gotPos.X-drop.X) > 1e-6 || math.Abs(gotPos.Y-drop.Y) > 1e-6 {
		t.Fatalf("reloaded south pos = %v, want (%v,%v)", gotPos, drop.X, drop.Y)
	}
	southSem2, ok := p2.Semantics().Find(ggui.RoleButton, "South Circle")
	if !ok {
		t.Fatal("reloaded South Circle missing")
	}
	if !st2.lastRect.Contains(center(southSem2.Rect)) {
		t.Fatalf("reloaded South Circle center %v outside canvas %+v", center(southSem2.Rect), st2.lastRect)
	}
	p2.Click(center(southSem2.Rect))
	p2.Frame()
	if len(reloadedEvents) != 1 || reloadedEvents[0].kind != protocol.UiChange || reloadedEvents[0].value != "south" {
		t.Fatalf("reloaded click South Circle events = %+v, want [change:south]", reloadedEvents)
	}
}

func TestPluginGraphDragClampedAtMaxCoordAndUiblockRoundTrip(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	if got := clampGraphCoord(math.NaN()); got != 0 {
		t.Fatalf("clampGraphCoord(NaN) = %v, want 0", got)
	}
	if got := clampGraphCoord(2e6); got != graphCoordLimit {
		t.Fatalf("clampGraphCoord(2e6) = %v, want %v", got, graphCoordLimit)
	}
	if got := clampGraphCoord(-2e6); got != -graphCoordLimit {
		t.Fatalf("clampGraphCoord(-2e6) = %v, want %v", got, -graphCoordLimit)
	}

	raw := `{
		"kind":"graph","key":"maxcoord","maxHeight":420,"onDrop":["savePos"],
		"graphNodes":[
			{"id":"min","label":"Min Node","x":-1000000,"y":-1000000,"hasPos":true},
			{"id":"max","label":"Max Node","x":1000000,"y":1000000,"hasPos":true}
		],
		"graphEdges":[{"from":"min","to":"max"}]
	}`
	parsed, err := uiblock.Read([]byte(raw))
	if err != nil {
		t.Fatalf("uiblock.Read: %v", err)
	}

	type ev struct{ node, kind, value string }
	var events []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:test/maxcoord",
			form:  form,
			onEvent: func(_, node, kind, value string, _ map[string]string) {
				events = append(events, ev{node, kind, value})
			},
		}
		return ggui.Provide(ggui.ReducedMotionKey, true, v.node(parsed.Root, false))
	}, ggui.Sz(760, 480))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}
	st := form.graphState(parsed.Root.GetId())

	dragAndVerify := func(label, wantID string, delta ggui.Point, wantX, wantY float64) {
		t.Helper()
		events = nil
		sem, ok := p.Semantics().Find(ggui.RoleButton, label)
		if !ok {
			t.Fatalf("missing node %q", label)
		}
		start := center(sem.Rect)
		end := ggui.Pt(start.X+delta.X, start.Y+delta.Y)
		p.Press(start)
		p.Frame()
		p.Move(ggui.Pt(start.X+delta.X/2, start.Y+delta.Y/2))
		p.Frame()
		p.Move(end)
		p.Frame()
		p.Release(end)
		p.Frame()

		posInLayout := st.layout.pos[wantID]
		if math.Abs(posInLayout.X) > graphCoordLimit || math.Abs(posInLayout.Y) > graphCoordLimit {
			t.Fatalf("%s st.layout.pos = %v exceeds ±%v", label, posInLayout, graphCoordLimit)
		}
		if len(events) != 1 || events[0].kind != protocol.UiDrop {
			t.Fatalf("%s drag events = %+v, want single UiDrop", label, events)
		}
		var pos struct {
			ID string  `json:"id"`
			X  float64 `json:"x"`
			Y  float64 `json:"y"`
		}
		if err := json.Unmarshal([]byte(events[0].value), &pos); err != nil {
			t.Fatalf("unmarshal drop payload %q: %v", events[0].value, err)
		}
		if pos.ID != wantID || pos.X != wantX || pos.Y != wantY {
			t.Fatalf("%s drop payload = %+v, want {ID:%s X:%v Y:%v}", label, pos, wantID, wantX, wantY)
		}
		msg, err := parsed.Message(&protocol.BlockEvent{
			Block: "plugin:test/maxcoord",
			Node:  events[0].node,
			Kind:  protocol.UiDrop,
			Value: events[0].value,
		})
		if err != nil {
			t.Fatalf("uiblock Tree.Message rejected clamped drop %q: %v", events[0].value, err)
		}
		if msg.Name != "savePos" {
			t.Fatalf("Message.Name = %q, want savePos", msg.Name)
		}
	}

	dragAndVerify("Max Node", "max", ggui.Pt(40, 40), 1e6, 1e6)
	dragAndVerify("Min Node", "min", ggui.Pt(-40, -40), -1e6, -1e6)
}

func TestPluginGraphOutwardDragUpdatesBoundsFitViewMinimapAndAvoidsJump(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	nodeA := &protocol.UiGraphNode{Id: "a", Label: "Node A", X: -5, Y: 0, HasPos: true}
	nodeB := &protocol.UiGraphNode{Id: "b", Label: "Node B", X: 5, Y: 0, HasPos: true}
	graph := &protocol.UiNode{
		Id:         "/bounds",
		Kind:       "graph",
		Variant:    "cards",
		MaxHeight:  420,
		Minimap:    true,
		Drop:       true,
		GraphNodes: []*protocol.UiGraphNode{nodeA, nodeB},
		GraphEdges: []*protocol.UiGraphEdge{{From: "a", To: "b"}},
	}

	var drops []string
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:test/bounds",
			form:  form,
			onEvent: func(_, _, kind, value string, _ map[string]string) {
				if kind == protocol.UiDrop {
					drops = append(drops, value)
					var payload struct {
						ID string  `json:"id"`
						X  float64 `json:"x"`
						Y  float64 `json:"y"`
					}
					if err := json.Unmarshal([]byte(value), &payload); err == nil && payload.ID == "b" {
						nodeB.X = payload.X
						nodeB.Y = payload.Y
						nodeB.HasPos = true
						nodeB.Pinned = true
					}
				}
			},
		}
		return ggui.Provide(ggui.ReducedMotionKey, true, v.node(graph, false))
	}, ggui.Sz(760, 480))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}
	semRect := func(label string) ggui.Rect {
		s, ok := p.Semantics().Find(ggui.RoleButton, label)
		if !ok {
			t.Fatalf("missing %q", label)
		}
		return s.Rect
	}

	start := center(semRect("Node B"))
	end := ggui.Pt(start.X+300, start.Y)

	p.Press(start)
	p.Frame()
	p.Move(ggui.Pt(start.X+150, start.Y))
	p.Frame()
	p.Move(end)
	p.Frame()

	rectABeforeUp := semRect("Node A")
	rectBBeforeUp := semRect("Node B")

	p.Release(end)
	p.Frame()
	if len(drops) != 1 {
		t.Fatalf("expected 1 UiDrop event, got %v", drops)
	}

	rectAAfterUp := semRect("Node A")
	rectBAfterUp := semRect("Node B")
	assertNoRectJump := func(phase, label string, before, after ggui.Rect) {
		t.Helper()
		cb, ca := center(before), center(after)
		if d := math.Hypot(ca.X-cb.X, ca.Y-cb.Y); d > 0.01 {
			t.Fatalf("%s %s jumped on %s: before=%v after=%v (delta=%.4f)", label, "center", phase, cb, ca, d)
		}
		if math.Abs(after.Size.W-before.Size.W) > 0.01 || math.Abs(after.Size.H-before.Size.H) > 0.01 {
			t.Fatalf("%s size popped on %s: before=%v after=%v", label, phase, before.Size, after.Size)
		}
	}
	assertNoRectJump("PointerUp", "Node A", rectABeforeUp, rectAAfterUp)
	assertNoRectJump("PointerUp", "Node B", rectBBeforeUp, rectBAfterUp)

	// Trigger a live re-render frame with HasPos:true, Pinned:true applied on nodeB.
	p.Frame()
	rectAAfterRerender := semRect("Node A")
	rectBAfterRerender := semRect("Node B")
	assertNoRectJump("HasPos/Pinned re-render", "Node A", rectAAfterUp, rectAAfterRerender)
	assertNoRectJump("HasPos/Pinned re-render", "Node B", rectBAfterUp, rectBAfterRerender)

	st := form.graphState("/bounds")
	if st.layout.maxX <= 9.0 || st.layout.spanW <= 14.0 {
		t.Fatalf("expected refreshed layout bounds after outward drag, got maxX=%v spanW=%v", st.layout.maxX, st.layout.spanW)
	}

	// Clicking Node B's projected point on the minimap centers Node B within 2px.
	canvas := &pluginGraphCanvas{state: st}
	mmRect, ok := canvas.minimapRect(st.lastRect)
	if !ok {
		t.Fatal("minimapRect not active")
	}
	pad := 10.0
	innerW := max(mmRect.Size.W-pad*2, 1)
	innerH := max(mmRect.Size.H-pad*2, 1)
	mmScale := min(innerW/max(st.layout.spanW, 1), innerH/max(st.layout.spanH, 1))
	mmCX := mmRect.Origin.X + mmRect.Size.W/2
	mmCY := mmRect.Origin.Y + mmRect.Size.H/2
	bPos := st.layout.pos["b"]
	mmB := ggui.Pt(mmCX+(bPos.X-st.layout.centerX)*mmScale, mmCY+(bPos.Y-st.layout.centerY)*mmScale)
	p.Click(mmB)
	p.Frame()
	bMinimapPan := center(semRect("Node B"))
	canvasCenter := center(st.lastRect)
	if d := math.Hypot(bMinimapPan.X-canvasCenter.X, bMinimapPan.Y-canvasCenter.Y); d > 2.0 {
		t.Fatalf("minimap click did not center dragged Node B: nodeB=%v canvasCenter=%v dist=%.2f", bMinimapPan, canvasCenter, d)
	}

	// Fit view starts from the current rendered camera without a frame-1 jump on
	// Node B's screen center, then settles at pan=(0,0) with BOTH nodes inside st.lastRect.
	fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
	if !ok {
		t.Fatal("Fit view button missing")
	}
	p.Click(center(fitSem.Rect))
	p.Advance(0)
	bFitFrame1 := center(semRect("Node B"))
	if d := math.Hypot(bFitFrame1.X-bMinimapPan.X, bFitFrame1.Y-bMinimapPan.Y); d > 0.01 {
		t.Fatalf("Node B jumped on frame 1 of Fit view: before=%v after=%v (dist=%.4f)", bMinimapPan, bFitFrame1, d)
	}
	p.Advance(260 * time.Millisecond)
	p.Frame()
	if st.panX != 0 || st.panY != 0 {
		t.Fatalf("after Fit view pan=(%v,%v), want (0,0)", st.panX, st.panY)
	}
	for _, lbl := range []string{"Node A", "Node B"} {
		c := center(semRect(lbl))
		if !st.lastRect.Contains(c) {
			t.Fatalf("after Fit view node %q center %v outside lastRect %+v", lbl, c, st.lastRect)
		}
	}
}

func TestGraphSignaturePrecisionLargeCoords(t *testing.T) {
	mk := func(x2 float64) []*protocol.UiGraphNode {
		return []*protocol.UiGraphNode{
			{Id: "n1", Label: "A", X: 999990.0, Y: 0, HasPos: true},
			{Id: "n2", Label: "B", X: x2, Y: 0, HasPos: true},
		}
	}
	s1 := graphSignatureWithMode(mk(999990.4), nil, graphLayoutForce, false)
	s2 := graphSignatureWithMode(mk(999990.3), nil, graphLayoutForce, false)
	if s1 == s2 {
		t.Fatalf("graphSignatureWithMode collided for distinct high-precision coords: %d == %d", s1, s2)
	}

	w1 := graphSignatureWithMode([]*protocol.UiGraphNode{{Id: "w", Weight: 100000.1}}, nil, graphLayoutForce, false)
	w2 := graphSignatureWithMode([]*protocol.UiGraphNode{{Id: "w", Weight: 100000.2}}, nil, graphLayoutForce, false)
	if w1 == w2 {
		t.Fatalf("graphSignatureWithMode collided for distinct high-precision weights: %d == %d", w1, w2)
	}

	negZero := []*protocol.UiGraphNode{{Id: "z", Weight: math.Copysign(0, -1), X: math.Copysign(0, -1), Y: math.Copysign(0, -1), HasPos: true}}
	posZero := []*protocol.UiGraphNode{{Id: "z", Weight: 0, X: 0, Y: 0, HasPos: true}}
	if sz1, sz2 := graphSignatureWithMode(negZero, nil, graphLayoutForce, false), graphSignatureWithMode(posZero, nil, graphLayoutForce, false); sz1 != sz2 {
		t.Fatalf("-0 and +0 produced different signatures: %d != %d", sz1, sz2)
	}
}

func TestPluginGraphMidTweenDragAndDropLiveRerender(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	n1 := &protocol.UiGraphNode{Id: "n1", Label: "Node One", Group: "g1"}
	n2 := &protocol.UiGraphNode{Id: "n2", Label: "Node Two", Group: "g1"}
	n3 := &protocol.UiGraphNode{Id: "n3", Label: "Node Three", Group: "g2"}
	graph := &protocol.UiNode{
		Id:         "/tween-drag",
		Kind:       "graph",
		MaxHeight:  380,
		Change:     true,
		Drop:       true,
		GraphNodes: []*protocol.UiGraphNode{n1, n2, n3},
		GraphEdges: []*protocol.UiGraphEdge{{From: "n1", To: "n2"}, {From: "n2", To: "n3"}},
	}

	var drops []string
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:test/tween-drag",
			form:  form,
			onEvent: func(_, _, kind, value string, _ map[string]string) {
				if kind == protocol.UiDrop {
					drops = append(drops, value)
					var payload struct {
						ID string  `json:"id"`
						X  float64 `json:"x"`
						Y  float64 `json:"y"`
					}
					if err := json.Unmarshal([]byte(value), &payload); err == nil && payload.ID == "n1" {
						n1.X = payload.X
						n1.Y = payload.Y
						n1.HasPos = true
						n1.Pinned = true
					}
				}
			},
		}
		return v.node(graph, false)
	}, ggui.Sz(720, 460))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}
	hierBtn, ok := p.Semantics().Find(ggui.RoleButton, "Hierarchy layout")
	if !ok {
		t.Fatal("Hierarchy layout button missing")
	}
	p.Click(center(hierBtn.Rect))
	p.Frame()
	p.Advance(80 * time.Millisecond) // mid-flight of 260ms layoutTween
	p.Frame()

	st := form.graphState("/tween-drag")
	sem1, ok := p.Semantics().Find(ggui.RoleButton, "Node One")
	if !ok {
		t.Fatal("Node One missing mid-tween")
	}
	// Verify layoutTween is genuinely mid-flight before starting the drag.
	rp2, ok := st.renderedPos("n2")
	if !ok {
		t.Fatal("missing renderedPos(n2)")
	}
	if target2 := st.layout.pos["n2"]; math.Hypot(rp2.X-target2.X, rp2.Y-target2.Y) < 1e-3 {
		t.Fatalf("expected n2 mid-tween before drag, got rendered=%v target=%v", rp2, target2)
	}

	start := center(sem1.Rect)
	mid := ggui.Pt(start.X+30, start.Y+20)
	end := ggui.Pt(start.X+60, start.Y+40)

	p.Press(start)
	p.Frame()
	p.Move(mid)
	p.Frame()
	semMid, _ := p.Semantics().Find(ggui.RoleButton, "Node One")
	if d := math.Hypot(center(semMid.Rect).X-mid.X, center(semMid.Rect).Y-mid.Y); d > 4.0 {
		t.Fatalf("mid-tween drag snapped away from pointer: node=%v mid=%v dist=%.2f", center(semMid.Rect), mid, d)
	}
	// Other mid-tween nodes should not have been snapped to their final targets during drag.
	if rp2Drag, ok := st.renderedPos("n2"); ok {
		if target2 := st.layout.pos["n2"]; math.Hypot(rp2Drag.X-target2.X, rp2Drag.Y-target2.Y) < 1e-3 {
			t.Fatalf("dragging Node One snapped unrelated mid-tween Node Two to target %v", target2)
		}
	}

	p.Move(end)
	p.Frame()
	p.Release(end)
	p.Frame()
	p.Advance(300 * time.Millisecond)
	p.Frame()

	if len(drops) != 1 {
		t.Fatalf("expected 1 UiDrop after mid-tween drag, got %v", drops)
	}
	semEnd, _ := p.Semantics().Find(ggui.RoleButton, "Node One")
	if d := math.Hypot(center(semEnd.Rect).X-end.X, center(semEnd.Rect).Y-end.Y); d > 4.0 {
		t.Fatalf("Node One moved away from drop point after HasPos/Pinned re-render: node=%v end=%v dist=%.2f", center(semEnd.Rect), end, d)
	}
	if !st.lastRect.Contains(center(semEnd.Rect)) {
		t.Fatalf("dropped Node One %v outside lastRect %+v", center(semEnd.Rect), st.lastRect)
	}
	for _, id := range []string{"n2", "n3"} {
		rp, ok := st.renderedPos(id)
		if !ok {
			t.Fatalf("missing renderedPos(%s)", id)
		}
		target := st.layout.pos[id]
		if d := math.Hypot(rp.X-target.X, rp.Y-target.Y); d > 1e-3 {
			t.Fatalf("node %s did not settle at hierarchy target %v after tween: got %v", id, target, rp)
		}
	}
}

func TestPluginGraphResetPinsClearsPinnedPosAndUpdatesCamBounds(t *testing.T) {
	t.Run("wide_haspos_reset_pins", func(t *testing.T) {
		t.Setenv("MAKO_HOME", t.TempDir())
		i18n.Set("en")

		nodeA := &protocol.UiGraphNode{Id: "a", Label: "Node A", Shape: "card", X: -500, Y: 0, HasPos: true, Pinned: true}
		nodeB := &protocol.UiGraphNode{Id: "b", Label: "Node B", Shape: "card", X: 500, Y: 0, HasPos: true, Pinned: true}
		graph := &protocol.UiNode{
			Id:         "/reset-wide",
			Kind:       "graph",
			Variant:    "cards",
			MaxHeight:  420,
			GraphNodes: []*protocol.UiGraphNode{nodeA, nodeB},
			GraphEdges: []*protocol.UiGraphEdge{{From: "a", To: "b"}},
		}

		var rev *ggui.StateValue[int]
		builds := 0
		form := newBlockForm()
		p := ggui.ProbeBuilder(func() ggui.Widget {
			if rev == nil {
				rev = ggui.State(0)
			}
			return ggui.Reactive(func() ggui.Widget {
				_ = rev.Get()
				builds++
				form.redraw()
				v := &treeView{block: "plugin:test/reset-wide", form: form}
				return v.node(graph, false)
			})
		}, ggui.Sz(760, 480))
		defer p.Close()
		for range 3 {
			p.Frame()
		}

		center := func(r ggui.Rect) ggui.Point {
			return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
		}
		semCenter := func(label string) ggui.Point {
			s, ok := p.Semantics().Find(ggui.RoleButton, label)
			if !ok {
				t.Fatalf("missing %q", label)
			}
			return center(s.Rect)
		}

		st := form.graphState("/reset-wide")
		if st.camBounds.spanW != 1000 || st.layout.spanW != 1000 {
			t.Fatalf("initial spans = (cam=%v, layout=%v), want 1000", st.camBounds.spanW, st.layout.spanW)
		}
		cA0, cB0 := semCenter("Node A"), semCenter("Node B")
		if dist0 := math.Hypot(cB0.X-cA0.X, cB0.Y-cA0.Y); dist0 < 500 {
			t.Fatalf("initial wide separation = %.2fpx, want >= 500px", dist0)
		}

		// Simulate Reset pins (HasPos: true -> false, Pinned: true -> false).
		nodeA.HasPos, nodeA.Pinned, nodeA.X, nodeA.Y = false, false, 0, 0
		nodeB.HasPos, nodeB.Pinned, nodeB.X, nodeB.Y = false, false, 0, 0
		rev.Update(func(x int) int { return x + 1 })
		p.Frame()
		p.Advance(0)
		p.Advance(300 * time.Millisecond)
		p.Frame()

		if builds < 2 {
			t.Fatalf("expected reactive re-render on Reset pins, got builds=%d", builds)
		}
		if st.layout.spanW >= 10 {
			t.Fatalf("expected algorithmic layout spanW < 10 after Reset pins, got %v", st.layout.spanW)
		}
		if st.camBounds.spanW != st.layout.spanW {
			t.Fatalf("camBounds.spanW=%v != layout.spanW=%v after Reset pins", st.camBounds.spanW, st.layout.spanW)
		}
		if st.cameraLayout().spanW != st.layout.spanW {
			t.Fatalf("cameraLayout().spanW=%v != layout.spanW=%v after Reset pins", st.cameraLayout().spanW, st.layout.spanW)
		}
		cA1, cB1 := semCenter("Node A"), semCenter("Node B")
		if dist1 := math.Hypot(cB1.X-cA1.X, cB1.Y-cA1.Y); dist1 <= 100 {
			t.Fatalf("nodes collapsed after Reset pins: dist=%.2fpx (A=%v B=%v)", dist1, cA1, cB1)
		}
	})

	t.Run("post_drag_reset_pins_clears_pinnedPos", func(t *testing.T) {
		t.Setenv("MAKO_HOME", t.TempDir())
		i18n.Set("en")

		n1 := &protocol.UiGraphNode{Id: "n1", Label: "Lead", Group: "g1"}
		n2 := &protocol.UiGraphNode{Id: "n2", Label: "Worker", Group: "g1"}
		n3 := &protocol.UiGraphNode{Id: "n3", Label: "Review", Group: "g2"}
		graph := &protocol.UiNode{
			Id:         "/reset-drag",
			Kind:       "graph",
			MaxHeight:  420,
			Drop:       true,
			GraphNodes: []*protocol.UiGraphNode{n1, n2, n3},
			GraphEdges: []*protocol.UiGraphEdge{{From: "n1", To: "n2"}, {From: "n2", To: "n3"}},
		}

		var rev *ggui.StateValue[int]
		builds := 0
		form := newBlockForm()
		p := ggui.ProbeBuilder(func() ggui.Widget {
			if rev == nil {
				rev = ggui.State(0)
			}
			return ggui.Reactive(func() ggui.Widget {
				_ = rev.Get()
				builds++
				form.redraw()
				v := &treeView{
					block: "plugin:test/reset-drag",
					form:  form,
					onEvent: func(_, _, kind, value string, _ map[string]string) {
						if kind == protocol.UiDrop {
							var payload struct {
								ID string  `json:"id"`
								X  float64 `json:"x"`
								Y  float64 `json:"y"`
							}
							if err := json.Unmarshal([]byte(value), &payload); err == nil && payload.ID == "n1" {
								n1.X = payload.X
								n1.Y = payload.Y
								n1.HasPos = true
								n1.Pinned = true
								rev.Update(func(x int) int { return x + 1 })
							}
						}
					},
				}
				return v.node(graph, false)
			})
		}, ggui.Sz(760, 480))
		defer p.Close()
		for range 3 {
			p.Frame()
		}

		st := form.graphState("/reset-drag")
		algoPos := st.layout.pos["n1"]

		sem1, ok := p.Semantics().Find(ggui.RoleButton, "Lead")
		if !ok {
			t.Fatal("missing Lead node")
		}
		start := ggui.Pt(sem1.Rect.Origin.X+sem1.Rect.Size.W/2, sem1.Rect.Origin.Y+sem1.Rect.Size.H/2)
		end := ggui.Pt(start.X+140, start.Y-80)
		p.Press(start)
		p.Frame()
		p.Move(ggui.Pt(start.X+70, start.Y-40))
		p.Frame()
		p.Move(end)
		p.Frame()
		p.Release(end)
		p.Frame()

		if builds < 2 {
			t.Fatalf("expected reactive re-render after drop, got builds=%d", builds)
		}
		if _, ok := st.pinnedPos["n1"]; !ok {
			t.Fatal("expected st.pinnedPos[n1] to be populated after drag")
		}
		if d := math.Hypot(st.layout.pos["n1"].X-algoPos.X, st.layout.pos["n1"].Y-algoPos.Y); d < 0.5 {
			t.Fatalf("drag did not move n1 far enough from algoPos: algo=%v dragged=%v", algoPos, st.layout.pos["n1"])
		}

		// Simulate Reset pins (HasPos: true -> false, Pinned: true -> false).
		n1.HasPos, n1.Pinned, n1.X, n1.Y = false, false, 0, 0
		rev.Update(func(x int) int { return x + 1 })
		p.Frame()
		p.Advance(0)
		p.Advance(300 * time.Millisecond)
		p.Frame()

		if builds < 3 {
			t.Fatalf("expected reactive re-render after Reset pins, got builds=%d", builds)
		}

		if _, ok := st.pinnedPos["n1"]; ok {
			t.Fatalf("Reset pins left stale entry in st.pinnedPos: %v", st.pinnedPos)
		}
		if d := math.Hypot(st.layout.pos["n1"].X-algoPos.X, st.layout.pos["n1"].Y-algoPos.Y); d > 1e-6 {
			t.Fatalf("Reset pins did not restore algorithmic layout: got=%v want=%v", st.layout.pos["n1"], algoPos)
		}
		rp, ok := st.renderedPos("n1")
		if !ok || math.Hypot(rp.X-algoPos.X, rp.Y-algoPos.Y) > 1e-3 {
			t.Fatalf("renderedPos(n1) did not settle at algorithmic position %v: got=%v", algoPos, rp)
		}
		if st.camBounds.spanW != st.layout.spanW {
			t.Fatalf("camBounds.spanW=%v != layout.spanW=%v after Reset pins", st.camBounds.spanW, st.layout.spanW)
		}
	})
}

func TestPluginGraphNormalMotionFitViewNoCardSizePopOrInwardJump(t *testing.T) {
	t.Run("outward_drag_normal_motion_no_card_size_pop", func(t *testing.T) {
		t.Setenv("MAKO_HOME", t.TempDir())
		i18n.Set("en")

		nodeA := &protocol.UiGraphNode{Id: "a", Label: "Node A", Shape: "card", X: -5, Y: 0, HasPos: true}
		nodeB := &protocol.UiGraphNode{Id: "b", Label: "Node B", Shape: "card", X: 5, Y: 0, HasPos: true}
		graph := &protocol.UiNode{
			Id:         "/fit-normal-out",
			Kind:       "graph",
			Variant:    "cards",
			MaxHeight:  420,
			Minimap:    true,
			Drop:       true,
			GraphNodes: []*protocol.UiGraphNode{nodeA, nodeB},
			GraphEdges: []*protocol.UiGraphEdge{{From: "a", To: "b"}},
		}

		var rev *ggui.StateValue[int]
		builds := 0
		form := newBlockForm()
		p := ggui.ProbeBuilder(func() ggui.Widget {
			if rev == nil {
				rev = ggui.State(0)
			}
			return ggui.Reactive(func() ggui.Widget {
				_ = rev.Get()
				builds++
				form.redraw()
				v := &treeView{
					block: "plugin:test/fit-normal-out",
					form:  form,
					onEvent: func(_, _, kind, value string, _ map[string]string) {
						if kind == protocol.UiDrop {
							var payload struct {
								ID string  `json:"id"`
								X  float64 `json:"x"`
								Y  float64 `json:"y"`
							}
							if err := json.Unmarshal([]byte(value), &payload); err == nil && payload.ID == "b" {
								nodeB.X = payload.X
								nodeB.Y = payload.Y
								nodeB.HasPos = true
								nodeB.Pinned = true
								rev.Update(func(x int) int { return x + 1 })
							}
						}
					},
				}
				// Normal motion (ReducedMotion=false).
				return v.node(graph, false)
			})
		}, ggui.Sz(760, 480))
		defer p.Close()
		for range 3 {
			p.Frame()
		}

		center := func(r ggui.Rect) ggui.Point {
			return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
		}
		semRect := func(label string) ggui.Rect {
			s, ok := p.Semantics().Find(ggui.RoleButton, label)
			if !ok {
				t.Fatalf("missing %q", label)
			}
			return s.Rect
		}

		start := center(semRect("Node B"))
		end := ggui.Pt(start.X+300, start.Y)
		p.Press(start)
		p.Frame()
		p.Move(ggui.Pt(start.X+150, start.Y))
		p.Frame()
		p.Move(end)
		p.Frame()
		p.Release(end)
		p.Frame()
		if builds < 2 {
			t.Fatalf("expected reactive re-render after outward drop, got builds=%d", builds)
		}

		st := form.graphState("/fit-normal-out")
		canvas := &pluginGraphCanvas{state: st}
		mmRect, ok := canvas.minimapRect(st.lastRect)
		if !ok {
			t.Fatal("minimapRect not active")
		}
		pad := 10.0
		mmScale := min(max(mmRect.Size.W-pad*2, 1)/max(st.layout.spanW, 1), max(mmRect.Size.H-pad*2, 1)/max(st.layout.spanH, 1))
		mmCX, mmCY := mmRect.Origin.X+mmRect.Size.W/2, mmRect.Origin.Y+mmRect.Size.H/2
		bPos := st.layout.pos["b"]
		p.Click(ggui.Pt(mmCX+(bPos.X-st.layout.centerX)*mmScale, mmCY+(bPos.Y-st.layout.centerY)*mmScale))
		p.Frame()

		beforeFit := semRect("Node B")
		if math.Abs(beforeFit.Size.W-196) > 0.5 || math.Abs(beforeFit.Size.H-48) > 0.5 {
			t.Fatalf("unexpected initial card size before Fit view: %v, want ~196x48", beforeFit.Size)
		}

		fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
		if !ok {
			t.Fatal("Fit view button missing")
		}
		p.Click(center(fitSem.Rect))
		p.Advance(0)

		afterFitF1 := semRect("Node B")
		if dw, dh := math.Abs(afterFitF1.Size.W-beforeFit.Size.W), math.Abs(afterFitF1.Size.H-beforeFit.Size.H); dw > 0.5 || dh > 0.5 {
			t.Fatalf("card size popped on Frame 1 of Fit view: before=%v after=%v", beforeFit.Size, afterFitF1.Size)
		}
		cb, ca := center(beforeFit), center(afterFitF1)
		if d := math.Hypot(ca.X-cb.X, ca.Y-cb.Y); d > 0.5 {
			t.Fatalf("Node B jumped on Frame 1 of Fit view: before=%v after=%v (delta=%.4f)", cb, ca, d)
		}

		p.Advance(300 * time.Millisecond)
		p.Frame()
		if st.panX != 0 || st.panY != 0 {
			t.Fatalf("after Fit view pan=(%v,%v), want (0,0)", st.panX, st.panY)
		}
		if st.cameraLayout().spanW != st.layout.spanW {
			t.Fatalf("cameraLayout().spanW=%v != layout.spanW=%v after Fit view tween", st.cameraLayout().spanW, st.layout.spanW)
		}
		for _, lbl := range []string{"Node A", "Node B"} {
			c := center(semRect(lbl))
			if !st.lastRect.Contains(c) {
				t.Fatalf("after Fit view node %q center %v outside lastRect %+v", lbl, c, st.lastRect)
			}
		}
	})

	t.Run("inward_outlier_drag_normal_motion_no_frame1_jump", func(t *testing.T) {
		t.Setenv("MAKO_HOME", t.TempDir())
		i18n.Set("en")

		nodeA := &protocol.UiGraphNode{Id: "a", Label: "Node A", Shape: "card", X: -500, Y: 0, HasPos: true}
		nodeB := &protocol.UiGraphNode{Id: "b", Label: "Node B", Shape: "card", X: 500, Y: 0, HasPos: true}
		graph := &protocol.UiNode{
			Id:         "/fit-normal-in",
			Kind:       "graph",
			Variant:    "cards",
			MaxHeight:  420,
			Drop:       true,
			GraphNodes: []*protocol.UiGraphNode{nodeA, nodeB},
			GraphEdges: []*protocol.UiGraphEdge{{From: "a", To: "b"}},
		}

		var rev *ggui.StateValue[int]
		builds := 0
		form := newBlockForm()
		p := ggui.ProbeBuilder(func() ggui.Widget {
			if rev == nil {
				rev = ggui.State(0)
			}
			return ggui.Reactive(func() ggui.Widget {
				_ = rev.Get()
				builds++
				form.redraw()
				v := &treeView{
					block: "plugin:test/fit-normal-in",
					form:  form,
					onEvent: func(_, _, kind, value string, _ map[string]string) {
						if kind == protocol.UiDrop {
							var payload struct {
								ID string  `json:"id"`
								X  float64 `json:"x"`
								Y  float64 `json:"y"`
							}
							if err := json.Unmarshal([]byte(value), &payload); err == nil && payload.ID == "b" {
								nodeB.X = payload.X
								nodeB.Y = payload.Y
								nodeB.HasPos = true
								nodeB.Pinned = true
								rev.Update(func(x int) int { return x + 1 })
							}
						}
					},
				}
				return v.node(graph, false)
			})
		}, ggui.Sz(760, 480))
		defer p.Close()
		for range 3 {
			p.Frame()
		}

		center := func(r ggui.Rect) ggui.Point {
			return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
		}
		semRect := func(label string) ggui.Rect {
			s, ok := p.Semantics().Find(ggui.RoleButton, label)
			if !ok {
				t.Fatalf("missing %q", label)
			}
			return s.Rect
		}

		cA := center(semRect("Node A"))
		cB := center(semRect("Node B"))
		destB := ggui.Pt(cA.X+20, cA.Y)
		p.Press(cB)
		p.Frame()
		p.Move(ggui.Pt((cB.X+destB.X)/2, destB.Y))
		p.Frame()
		p.Move(destB)
		p.Frame()
		p.Release(destB)
		p.Frame()
		if builds < 2 {
			t.Fatalf("expected reactive re-render after inward drop, got builds=%d", builds)
		}

		rectABeforeFit := semRect("Node A")
		rectBBeforeFit := semRect("Node B")

		fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
		if !ok {
			t.Fatal("Fit view button missing")
		}
		p.Click(center(fitSem.Rect))
		p.Advance(0)

		rectAF1 := semRect("Node A")
		rectBF1 := semRect("Node B")
		for _, tc := range []struct {
			lbl    string
			before ggui.Rect
			after  ggui.Rect
		}{
			{"Node A", rectABeforeFit, rectAF1},
			{"Node B", rectBBeforeFit, rectBF1},
		} {
			cb, ca := center(tc.before), center(tc.after)
			if d := math.Hypot(ca.X-cb.X, ca.Y-cb.Y); d > 0.5 {
				t.Fatalf("%s jumped on Frame 1 of Fit view after inward drag: before=%v after=%v (delta=%.2fpx)", tc.lbl, cb, ca, d)
			}
			if dw, dh := math.Abs(tc.after.Size.W-tc.before.Size.W), math.Abs(tc.after.Size.H-tc.before.Size.H); dw > 0.5 || dh > 0.5 {
				t.Fatalf("%s card size popped on Frame 1 of Fit view: before=%v after=%v", tc.lbl, tc.before.Size, tc.after.Size)
			}
		}

		p.Advance(300 * time.Millisecond)
		p.Frame()
		st := form.graphState("/fit-normal-in")
		if st.panX != 0 || st.panY != 0 {
			t.Fatalf("after Fit view pan=(%v,%v), want (0,0)", st.panX, st.panY)
		}
		if st.cameraLayout().spanW != st.layout.spanW {
			t.Fatalf("cameraLayout().spanW=%v != layout.spanW=%v after Fit view tween", st.cameraLayout().spanW, st.layout.spanW)
		}
		for _, lbl := range []string{"Node A", "Node B"} {
			c := center(semRect(lbl))
			if !st.lastRect.Contains(c) {
				t.Fatalf("after Fit view node %q center %v outside lastRect %+v", lbl, c, st.lastRect)
			}
		}
	})
}

func TestPluginGraphDeleteDraggedNodeMidDrag(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	n1 := &protocol.UiGraphNode{Id: "n1", Label: "Node One", X: -2, Y: 0, HasPos: true}
	n2 := &protocol.UiGraphNode{Id: "n2", Label: "Node Two", X: 2, Y: 0, HasPos: true}
	graph := &protocol.UiNode{
		Id:         "/del-drag",
		Kind:       "graph",
		MaxHeight:  380,
		Change:     true,
		Drop:       true,
		GraphNodes: []*protocol.UiGraphNode{n1, n2},
		GraphEdges: []*protocol.UiGraphEdge{{From: "n1", To: "n2"}},
	}

	type ev struct{ kind, value string }
	var events []ev
	var rev *ggui.StateValue[int]
	builds := 0
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		if rev == nil {
			rev = ggui.State(0)
		}
		return ggui.Reactive(func() ggui.Widget {
			_ = rev.Get()
			builds++
			form.redraw()
			v := &treeView{
				block: "plugin:test/del-drag",
				form:  form,
				onEvent: func(_, _, kind, value string, _ map[string]string) {
					events = append(events, ev{kind, value})
				},
			}
			return v.node(graph, false)
		})
	}, ggui.Sz(640, 420))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	sem1, ok := p.Semantics().Find(ggui.RoleButton, "Node One")
	if !ok {
		t.Fatal("missing Node One")
	}
	start := ggui.Pt(sem1.Rect.Origin.X+sem1.Rect.Size.W/2, sem1.Rect.Origin.Y+sem1.Rect.Size.H/2)
	mid := ggui.Pt(start.X+30, start.Y+20)
	end := ggui.Pt(start.X+70, start.Y+45)

	p.Press(start)
	p.Frame()
	p.Move(mid)
	p.Frame()

	st := form.graphState("/del-drag")
	if st.draggingID != "n1" {
		t.Fatalf("expected st.draggingID=n1 mid-drag, got %q", st.draggingID)
	}

	// Delete n1 via live re-render mid-drag, then continue moving and release.
	graph.GraphNodes = []*protocol.UiGraphNode{n2}
	graph.GraphEdges = nil
	rev.Update(func(x int) int { return x + 1 })
	p.Frame()
	if builds < 2 {
		t.Fatalf("expected reactive re-render on mid-drag node deletion, got builds=%d", builds)
	}

	p.Move(end)
	p.Frame()
	p.Release(end)
	p.Frame()

	if _, ok := st.layout.pos["n1"]; ok {
		t.Fatalf("deleted node n1 was resurrected in st.layout.pos: %v", st.layout.pos)
	}
	if _, ok := st.pinnedPos["n1"]; ok {
		t.Fatalf("deleted node n1 remained in st.pinnedPos: %v", st.pinnedPos)
	}
	if st.draggingID != "" {
		t.Fatalf("expected st.draggingID cleared after mid-drag deletion, got %q", st.draggingID)
	}
	if len(events) != 0 {
		t.Fatalf("expected zero events after mid-drag node deletion, got %+v", events)
	}
}

func TestPluginGraphMidFitViewInterruptions(t *testing.T) {
	setupWideInwardFit := func(t *testing.T, id string, minimap bool) (*ggui.Probe, *blockForm, func(ggui.Rect) ggui.Point, func(string) ggui.Rect) {
		t.Helper()
		t.Setenv("MAKO_HOME", t.TempDir())
		i18n.Set("en")

		nodeA := &protocol.UiGraphNode{Id: "a", Label: "Node A", Shape: "card", X: -500, Y: 0, HasPos: true}
		nodeB := &protocol.UiGraphNode{Id: "b", Label: "Node B", Shape: "card", X: 500, Y: 0, HasPos: true}
		graph := &protocol.UiNode{
			Id:         id,
			Kind:       "graph",
			Variant:    "cards",
			MaxHeight:  420,
			Minimap:    minimap,
			Change:     true,
			Drop:       true,
			GraphNodes: []*protocol.UiGraphNode{nodeA, nodeB},
			GraphEdges: []*protocol.UiGraphEdge{{From: "a", To: "b"}},
		}

		var rev *ggui.StateValue[int]
		builds := 0
		form := newBlockForm()
		p := ggui.ProbeBuilder(func() ggui.Widget {
			if rev == nil {
				rev = ggui.State(0)
			}
			return ggui.Reactive(func() ggui.Widget {
				_ = rev.Get()
				builds++
				form.redraw()
				v := &treeView{
					block: "plugin:test" + id,
					form:  form,
					onEvent: func(_, _, kind, value string, _ map[string]string) {
						if kind == protocol.UiDrop {
							var payload struct {
								ID string  `json:"id"`
								X  float64 `json:"x"`
								Y  float64 `json:"y"`
							}
							if err := json.Unmarshal([]byte(value), &payload); err == nil && payload.ID == "b" {
								nodeB.X = payload.X
								nodeB.Y = payload.Y
								nodeB.HasPos = true
								nodeB.Pinned = true
								rev.Update(func(x int) int { return x + 1 })
							}
						}
					},
				}
				return v.node(graph, false)
			})
		}, ggui.Sz(760, 480))
		for range 3 {
			p.Frame()
		}

		center := func(r ggui.Rect) ggui.Point {
			return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
		}
		semRect := func(label string) ggui.Rect {
			s, ok := p.Semantics().Find(ggui.RoleButton, label)
			if !ok {
				t.Fatalf("missing %q", label)
			}
			return s.Rect
		}

		// Drag Node B inward next to Node A and persist via onDrop.
		cA := center(semRect("Node A"))
		cB := center(semRect("Node B"))
		destB := ggui.Pt(cA.X+20, cA.Y)
		p.Press(cB)
		p.Frame()
		p.Move(ggui.Pt((cB.X+destB.X)/2, destB.Y))
		p.Frame()
		p.Move(destB)
		p.Frame()
		p.Release(destB)
		p.Frame()
		if builds < 2 {
			t.Fatalf("expected reactive re-render after inward drop, got builds=%d", builds)
		}

		fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
		if !ok {
			t.Fatal("Fit view button missing")
		}
		p.Click(center(fitSem.Rect))
		p.Frame()
		p.Advance(60 * time.Millisecond)
		p.Frame()

		return p, form, center, semRect
	}

	t.Run("focusNode_mid_fitView", func(t *testing.T) {
		p, form, center, semRect := setupWideInwardFit(t, "/mid-fit-focus", false)
		defer p.Close()

		p.Click(center(semRect("Node B")))
		p.Frame()
		p.Advance(300 * time.Millisecond)
		p.Frame()

		st := form.graphState("/mid-fit-focus")
		bAfter := center(semRect("Node B"))
		canvasCenter := center(st.lastRect)
		if !st.lastRect.Contains(bAfter) {
			t.Fatalf("Node B after mid-fitView focusNode is outside lastRect: pos=%v rect=%+v", bAfter, st.lastRect)
		}
		if d := math.Hypot(bAfter.X-canvasCenter.X, bAfter.Y-canvasCenter.Y); d > 5.0 {
			t.Fatalf("Node B after mid-fitView focusNode not centered: pos=%v canvasCenter=%v err=%.2fpx", bAfter, canvasCenter, d)
		}
	})

	t.Run("minimap_click_mid_fitView", func(t *testing.T) {
		p, form, center, semRect := setupWideInwardFit(t, "/mid-fit-minimap", true)
		defer p.Close()

		st := form.graphState("/mid-fit-minimap")
		canvas := &pluginGraphCanvas{state: st}
		mmRect, ok := canvas.minimapRect(st.lastRect)
		if !ok {
			t.Fatal("minimapRect not active")
		}
		pad := 10.0
		innerW := max(mmRect.Size.W-pad*2, 1)
		innerH := max(mmRect.Size.H-pad*2, 1)
		mmScale := min(innerW/max(st.layout.spanW, 1), innerH/max(st.layout.spanH, 1))
		mmCX := mmRect.Origin.X + mmRect.Size.W/2
		mmCY := mmRect.Origin.Y + mmRect.Size.H/2
		bPos := st.layout.pos["b"]
		mmB := ggui.Pt(mmCX+(bPos.X-st.layout.centerX)*mmScale, mmCY+(bPos.Y-st.layout.centerY)*mmScale)

		p.Click(mmB)
		p.Frame()
		bImmediate := center(semRect("Node B"))
		canvasCenter := center(st.lastRect)
		if d := math.Hypot(bImmediate.X-canvasCenter.X, bImmediate.Y-canvasCenter.Y); d > 5.0 {
			t.Fatalf("minimap click mid-fitView did not center Node B immediately: pos=%v center=%v err=%.2fpx", bImmediate, canvasCenter, d)
		}

		p.Advance(300 * time.Millisecond)
		p.Frame()
		bAfter := center(semRect("Node B"))
		if !st.lastRect.Contains(bAfter) {
			t.Fatalf("Node B after mid-fitView minimap click is outside lastRect: pos=%v rect=%+v", bAfter, st.lastRect)
		}
		if d := math.Hypot(bAfter.X-canvasCenter.X, bAfter.Y-canvasCenter.Y); d > 5.0 {
			t.Fatalf("minimap click mid-fitView drifted Node B: pos=%v center=%v err=%.2fpx", bAfter, canvasCenter, d)
		}
	})

	t.Run("pan_and_scroll_mid_fitView", func(t *testing.T) {
		t.Setenv("MAKO_HOME", t.TempDir())
		i18n.Set("en")

		nodeA := &protocol.UiGraphNode{Id: "a", Label: "Node A", Shape: "card", X: -500, Y: 0, HasPos: true}
		nodeB := &protocol.UiGraphNode{Id: "b", Label: "Node B", Shape: "card", X: 500, Y: 0, HasPos: true}
		graph := &protocol.UiNode{
			Id:         "/mid-fit-pan-scroll",
			Kind:       "graph",
			Variant:    "cards",
			MaxHeight:  420,
			GraphNodes: []*protocol.UiGraphNode{nodeA, nodeB},
			GraphEdges: []*protocol.UiGraphEdge{{From: "a", To: "b"}},
		}

		form := newBlockForm()
		p := ggui.ProbeBuilder(func() ggui.Widget {
			form.redraw()
			v := &treeView{
				block: "plugin:test/mid-fit-pan-scroll",
				form:  form,
			}
			return v.node(graph, false)
		}, ggui.Sz(760, 480))
		defer p.Close()
		for range 3 {
			p.Frame()
		}

		center := func(r ggui.Rect) ggui.Point {
			return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
		}
		semRect := func(label string) ggui.Rect {
			s, ok := p.Semantics().Find(ggui.RoleButton, label)
			if !ok {
				t.Fatalf("missing %q", label)
			}
			return s.Rect
		}

		st := form.graphState("/mid-fit-pan-scroll")
		bgStart := ggui.Pt(st.lastRect.Origin.X+st.lastRect.Size.W/2, st.lastRect.Origin.Y+40)
		// Pan (+250, +100) via background drag.
		p.Press(bgStart)
		p.Frame()
		p.Move(ggui.Pt(bgStart.X+250, bgStart.Y+100))
		p.Frame()
		p.Release(ggui.Pt(bgStart.X+250, bgStart.Y+100))
		p.Frame()

		fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
		if !ok {
			t.Fatal("Fit view button missing")
		}
		p.Click(center(fitSem.Rect))
		p.Frame()
		p.Advance(60 * time.Millisecond)
		p.Frame()

		// Background pan +10px right mid-fitView should move Node A by ~10px without snap jump.
		aBeforePan := center(semRect("Node A"))
		p.Press(bgStart)
		p.Frame()
		p.Move(ggui.Pt(bgStart.X+10, bgStart.Y))
		p.Frame()
		aAfterMove := center(semRect("Node A"))
		wantA := ggui.Pt(aBeforePan.X+10, aBeforePan.Y)
		if d := math.Hypot(aAfterMove.X-wantA.X, aAfterMove.Y-wantA.Y); d > 2.0 {
			t.Fatalf("background pan mid-fitView snapped/jumped: before=%v after=%v want=%v err=%.2fpx", aBeforePan, aAfterMove, wantA, d)
		}
		p.Release(ggui.Pt(bgStart.X+10, bgStart.Y))
		p.Frame()
		p.Advance(300 * time.Millisecond)
		p.Frame()
		aPanSettled := center(semRect("Node A"))
		if d := math.Hypot(aPanSettled.X-aAfterMove.X, aPanSettled.Y-aAfterMove.Y); d > 0.5 {
			t.Fatalf("background pan mid-fitView drifted after release: move=%v settled=%v err=%.2fpx", aAfterMove, aPanSettled, d)
		}

		// Re-trigger Fit view and interrupt at 60ms with Ctrl+wheel PointerScroll over Node A.
		p.Click(center(fitSem.Rect))
		p.Frame()
		p.Advance(60 * time.Millisecond)
		p.Frame()

		scrollCanvas := &pluginGraphCanvas{state: st, rect: st.lastRect}
		aBeforeScroll := center(semRect("Node A"))
		bBeforeScroll := scrollCanvas.worldToScreen(st.lastRect, st.layout.pos["b"])
		_, _, zoomBefore := st.renderedCamera()
		if zoomBefore == st.zoom {
			t.Fatalf("expected zoomBefore (%.4f) to differ from target zoom (%.4f) mid-fitView", zoomBefore, st.zoom)
		}

		if !scrollCanvas.HandlePointer(ggui.PointerEvent{
			Kind:   ggui.PointerScroll,
			Pos:    aBeforeScroll,
			Scroll: ggui.Pt(0, -2),
			Mods:   ggui.Mods{Ctrl: true},
		}) {
			t.Fatal("Ctrl+wheel PointerScroll mid-fitView was not consumed")
		}
		p.Frame()

		_, _, zoomAfter := st.renderedCamera()
		if zoomAfter <= zoomBefore {
			t.Fatalf("expected Ctrl+wheel scroll-up to zoom in from rendered zoom: before=%.4f after=%.4f", zoomBefore, zoomAfter)
		}
		aAfterScroll := center(semRect("Node A"))
		if d := math.Hypot(aAfterScroll.X-aBeforeScroll.X, aAfterScroll.Y-aBeforeScroll.Y); d > 2.0 {
			t.Fatalf("PointerScroll mid-fitView snapped Node A under cursor: before=%v after=%v err=%.2fpx", aBeforeScroll, aAfterScroll, d)
		}
		bAfterScroll := scrollCanvas.worldToScreen(st.lastRect, st.layout.pos["b"])
		scaleRatio := zoomAfter / zoomBefore
		wantB := ggui.Pt(
			aBeforeScroll.X+(bBeforeScroll.X-aBeforeScroll.X)*scaleRatio,
			aBeforeScroll.Y+(bBeforeScroll.Y-aBeforeScroll.Y)*scaleRatio,
		)
		if d := math.Hypot(bAfterScroll.X-wantB.X, bAfterScroll.Y-wantB.Y); d > 2.0 {
			t.Fatalf("PointerScroll mid-fitView snapped Node B: aBefore=%v bBefore=%v aAfter=%v zoomBefore=%.6f zoomAfter=%.6f after=%v want=%v err=%.2fpx",
				aBeforeScroll, bBeforeScroll, aAfterScroll, zoomBefore, zoomAfter, bAfterScroll, wantB, d)
		}

		p.Advance(300 * time.Millisecond)
		p.Frame()
		aScrollSettled := center(semRect("Node A"))
		if d := math.Hypot(aScrollSettled.X-aAfterScroll.X, aScrollSettled.Y-aAfterScroll.Y); d > 0.5 {
			t.Fatalf("PointerScroll mid-fitView drifted after settle: after=%v settled=%v err=%.2fpx", aAfterScroll, aScrollSettled, d)
		}
	})

	setupWideInwardNoFit := func(t *testing.T, id string) (*ggui.Probe, *blockForm, func(ggui.Rect) ggui.Point, func(string) ggui.Rect) {
		t.Helper()
		t.Setenv("MAKO_HOME", t.TempDir())
		i18n.Set("en")

		nodeA := &protocol.UiGraphNode{Id: "a", Label: "Node A", Shape: "card", X: -500, Y: 0, HasPos: true}
		nodeB := &protocol.UiGraphNode{Id: "b", Label: "Node B", Shape: "card", X: 500, Y: 0, HasPos: true}
		graph := &protocol.UiNode{
			Id:         id,
			Kind:       "graph",
			Variant:    "cards",
			MaxHeight:  420,
			Change:     true,
			Drop:       true,
			GraphNodes: []*protocol.UiGraphNode{nodeA, nodeB},
			GraphEdges: []*protocol.UiGraphEdge{{From: "a", To: "b"}},
		}

		var rev *ggui.StateValue[int]
		builds := 0
		form := newBlockForm()
		p := ggui.ProbeBuilder(func() ggui.Widget {
			if rev == nil {
				rev = ggui.State(0)
			}
			return ggui.Reactive(func() ggui.Widget {
				_ = rev.Get()
				builds++
				form.redraw()
				v := &treeView{
					block: "plugin:test" + id,
					form:  form,
					onEvent: func(_, _, kind, value string, _ map[string]string) {
						if kind == protocol.UiDrop {
							var payload struct {
								ID string  `json:"id"`
								X  float64 `json:"x"`
								Y  float64 `json:"y"`
							}
							if err := json.Unmarshal([]byte(value), &payload); err == nil && payload.ID == "b" {
								nodeB.X = payload.X
								nodeB.Y = payload.Y
								nodeB.HasPos = true
								nodeB.Pinned = true
								rev.Update(func(x int) int { return x + 1 })
							}
						}
					},
				}
				return v.node(graph, false)
			})
		}, ggui.Sz(760, 480))
		for range 3 {
			p.Frame()
		}

		center := func(r ggui.Rect) ggui.Point {
			return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
		}
		semRect := func(label string) ggui.Rect {
			s, ok := p.Semantics().Find(ggui.RoleButton, label)
			if !ok {
				t.Fatalf("missing %q", label)
			}
			return s.Rect
		}

		cA := center(semRect("Node A"))
		cB := center(semRect("Node B"))
		destB := ggui.Pt(cA.X+20, cA.Y)
		p.Press(cB)
		p.Frame()
		p.Move(ggui.Pt((cB.X+destB.X)/2, destB.Y))
		p.Frame()
		p.Move(destB)
		p.Frame()
		p.Release(destB)
		p.Frame()
		if builds < 2 {
			t.Fatalf("expected reactive re-render after inward drop, got builds=%d", builds)
		}
		return p, form, center, semRect
	}

	t.Run("drag_node_mid_fitView_tracks_pointer_single_and_multi_frame", func(t *testing.T) {
		t.Run("zoom_mismatch_and_pan", func(t *testing.T) {
			p, form, center, semRect := setupWideInwardNoFit(t, "/mid-fit-drag-zoom")
			defer p.Close()

			zoomInSem, ok := p.Semantics().Find(ggui.RoleButton, "Zoom in")
			if !ok {
				t.Fatal("Zoom in button missing")
			}
			for range 3 {
				p.Click(center(zoomInSem.Rect))
				p.Frame()
			}
			p.Advance(300 * time.Millisecond)
			p.Frame()

			st := form.graphState("/mid-fit-drag-zoom")
			bgStart := ggui.Pt(st.lastRect.Origin.X+st.lastRect.Size.W/2, st.lastRect.Origin.Y+40)
			p.Press(bgStart)
			p.Frame()
			p.Move(ggui.Pt(bgStart.X+180, bgStart.Y+90))
			p.Frame()
			p.Release(ggui.Pt(bgStart.X+180, bgStart.Y+90))
			p.Frame()

			fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
			if !ok {
				t.Fatal("Fit view button missing")
			}
			p.Click(center(fitSem.Rect))
			p.Frame()
			p.Advance(40 * time.Millisecond)
			p.Frame()

			_, _, zoomMid := st.renderedCamera()
			if math.Abs(zoomMid-st.zoom) < 0.05 {
				t.Fatalf("expected active zoom tween mid-fitView: rendered=%.4f target=%.4f", zoomMid, st.zoom)
			}

			b0 := center(semRect("Node B"))
			grabOff := ggui.Pt(12, -6)
			grab0 := ggui.Pt(b0.X+grabOff.X, b0.Y+grabOff.Y)
			p.Press(grab0)
			p.Frame()

			// Frame-1 move +80px with zero Advance: must track cursor immediately.
			dest1 := ggui.Pt(grab0.X+80, grab0.Y)
			p.Move(dest1)
			p.Advance(0)
			p.Frame()
			bF1 := center(semRect("Node B"))
			want1 := ggui.Pt(dest1.X-grabOff.X, dest1.Y-grabOff.Y)
			if err := math.Hypot(bF1.X-want1.X, bF1.Y-want1.Y); err > 2.0 {
				t.Fatalf("single-frame node drag mid-fitView drifted: got=%v want=%v err=%.2fpx", bF1, want1, err)
			}

			// Multi-frame 4 x 16ms drag steps: camera must remain frozen with <= 2.0px error.
			curDest := dest1
			for step := 1; step <= 4; step++ {
				p.Advance(16 * time.Millisecond)
				curDest = ggui.Pt(dest1.X+float64(step)*5, dest1.Y+float64(step)*7.5)
				p.Move(curDest)
				p.Frame()
				bStep := center(semRect("Node B"))
				wantStep := ggui.Pt(curDest.X-grabOff.X, curDest.Y-grabOff.Y)
				if err := math.Hypot(bStep.X-wantStep.X, bStep.Y-wantStep.Y); err > 2.0 {
					t.Fatalf("multi-frame step %d node drag mid-fitView drifted: got=%v want=%v err=%.2fpx", step, bStep, wantStep, err)
				}
			}

			p.Release(curDest)
			p.Frame()
			p.Advance(300 * time.Millisecond)
			p.Frame()
			bSettled := center(semRect("Node B"))
			wantEnd := ggui.Pt(curDest.X-grabOff.X, curDest.Y-grabOff.Y)
			if err := math.Hypot(bSettled.X-wantEnd.X, bSettled.Y-wantEnd.Y); err > 2.0 {
				t.Fatalf("node drag mid-fitView drifted after release: got=%v want=%v err=%.2fpx", bSettled, wantEnd, err)
			}
		})

		t.Run("camBounds_and_zoom_animating", func(t *testing.T) {
			p, form, center, semRect := setupWideInwardNoFit(t, "/mid-fit-drag-cambounds")
			defer p.Close()

			zoomInSem, ok := p.Semantics().Find(ggui.RoleButton, "Zoom in")
			if !ok {
				t.Fatal("Zoom in button missing")
			}
			for range 2 {
				p.Click(center(zoomInSem.Rect))
				p.Frame()
			}
			p.Advance(300 * time.Millisecond)
			p.Frame()

			fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
			if !ok {
				t.Fatal("Fit view button missing")
			}
			p.Click(center(fitSem.Rect))
			p.Frame()
			p.Advance(40 * time.Millisecond)
			p.Frame()

			st := form.graphState("/mid-fit-drag-cambounds")
			if math.Abs(st.cameraLayout().spanW-st.layout.spanW) < 10 {
				t.Fatalf("expected active camBoundsTween mid-fitView: camSpanW=%.2f layoutSpanW=%.2f", st.cameraLayout().spanW, st.layout.spanW)
			}

			canvas := &pluginGraphCanvas{state: st, rect: st.lastRect}
			b0 := canvas.worldToScreen(st.lastRect, st.layout.pos["b"])
			p.Press(b0)
			p.Frame()

			var curDest ggui.Point
			for step := 1; step <= 4; step++ {
				p.Advance(16 * time.Millisecond)
				curDest = ggui.Pt(b0.X+20*float64(step), b0.Y+5*float64(step))
				p.Move(curDest)
				p.Frame()
				bWorld := canvas.worldToScreen(st.lastRect, st.layout.pos["b"])
				if err := math.Hypot(bWorld.X-curDest.X, bWorld.Y-curDest.Y); err > 2.0 {
					t.Fatalf("step %d drag node mid-fitView (camBounds+zoom) drifted: got=%v want=%v err=%.2fpx", step, bWorld, curDest, err)
				}
			}

			p.Release(curDest)
			p.Frame()
			p.Advance(300 * time.Millisecond)
			p.Frame()
			bSettled := center(semRect("Node B"))
			if err := math.Hypot(bSettled.X-curDest.X, bSettled.Y-curDest.Y); err > 2.0 {
				t.Fatalf("drag node mid-fitView (camBounds+zoom) drifted after release: got=%v want=%v err=%.2fpx", bSettled, curDest, err)
			}
		})
	})

	t.Run("zoom_buttons_mid_fitView_never_invert_direction", func(t *testing.T) {
		t.Run("zoom_in_mid_fitView_from_high_zoom", func(t *testing.T) {
			p, form, center, _ := setupWideInwardNoFit(t, "/mid-fit-zoom-in")
			defer p.Close()

			zoomInSem, ok := p.Semantics().Find(ggui.RoleButton, "Zoom in")
			if !ok {
				t.Fatal("Zoom in button missing")
			}
			for range 3 {
				p.Click(center(zoomInSem.Rect))
				p.Frame()
			}
			p.Advance(300 * time.Millisecond)
			p.Frame()

			fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
			if !ok {
				t.Fatal("Fit view button missing")
			}
			p.Click(center(fitSem.Rect))
			p.Frame()
			p.Advance(40 * time.Millisecond)
			p.Frame()

			st := form.graphState("/mid-fit-zoom-in")
			_, _, renderedMid := st.renderedCamera()
			if renderedMid <= st.zoom {
				t.Fatalf("expected fitView to be zooming out from high zoom: renderedMid=%.4f target=%.4f", renderedMid, st.zoom)
			}

			p.Click(center(zoomInSem.Rect))
			p.Frame()
			p.Advance(300 * time.Millisecond)
			p.Frame()

			_, _, zFinal := st.renderedCamera()
			if zFinal <= renderedMid {
				t.Fatalf("Zoom in mid-fitView inverted direction: renderedMid=%.4f zFinal=%.4f", renderedMid, zFinal)
			}
		})

		t.Run("zoom_out_mid_fitView_from_low_zoom", func(t *testing.T) {
			p, form, center, _ := setupWideInwardNoFit(t, "/mid-fit-zoom-out")
			defer p.Close()

			zoomOutSem, ok := p.Semantics().Find(ggui.RoleButton, "Zoom out")
			if !ok {
				t.Fatal("Zoom out button missing")
			}
			for range 3 {
				p.Click(center(zoomOutSem.Rect))
				p.Frame()
			}
			p.Advance(300 * time.Millisecond)
			p.Frame()

			fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
			if !ok {
				t.Fatal("Fit view button missing")
			}
			p.Click(center(fitSem.Rect))
			p.Frame()
			p.Advance(40 * time.Millisecond)
			p.Frame()

			st := form.graphState("/mid-fit-zoom-out")
			_, _, renderedMid := st.renderedCamera()
			if renderedMid >= st.zoom {
				t.Fatalf("expected fitView to be zooming in from low zoom: renderedMid=%.4f target=%.4f", renderedMid, st.zoom)
			}

			p.Click(center(zoomOutSem.Rect))
			p.Frame()
			p.Advance(300 * time.Millisecond)
			p.Frame()

			_, _, zFinal := st.renderedCamera()
			if zFinal >= renderedMid {
				t.Fatalf("Zoom out mid-fitView inverted direction: renderedMid=%.4f zFinal=%.4f", renderedMid, zFinal)
			}
		})

		t.Run("rapid_double_click_accumulates", func(t *testing.T) {
			p, form, center, _ := setupWideInwardNoFit(t, "/mid-fit-zoom-rapid")
			defer p.Close()

			zoomInSem, _ := p.Semantics().Find(ggui.RoleButton, "Zoom in")
			zoomOutSem, _ := p.Semantics().Find(ggui.RoleButton, "Zoom out")
			resetSem, _ := p.Semantics().Find(ggui.RoleButton, "Reset")

			p.Click(center(zoomInSem.Rect))
			p.Frame()
			p.Advance(16 * time.Millisecond)
			p.Click(center(zoomInSem.Rect))
			p.Frame()
			p.Advance(300 * time.Millisecond)
			p.Frame()

			st := form.graphState("/mid-fit-zoom-rapid")
			if st.zoom < 1.50 {
				t.Fatalf("expected rapid double Zoom in to accumulate >= 1.50, got %.4f", st.zoom)
			}

			p.Click(center(resetSem.Rect))
			p.Frame()
			p.Click(center(zoomOutSem.Rect))
			p.Frame()
			p.Advance(16 * time.Millisecond)
			p.Click(center(zoomOutSem.Rect))
			p.Frame()
			p.Advance(300 * time.Millisecond)
			p.Frame()

			if st.zoom > 0.66 {
				t.Fatalf("expected rapid double Zoom out to accumulate <= 0.66, got %.4f", st.zoom)
			}
		})
	})
}

func TestPluginGraphMidDragLiveRerenderPreservesCursorTracking(t *testing.T) {
	setupForceCardGraph := func(t *testing.T, id string) (
		p *ggui.Probe,
		form *blockForm,
		graph *protocol.UiNode,
		nodeA, nodeB, nodeC *protocol.UiGraphNode,
		rev *ggui.StateValue[int],
		drops *[]string,
		center func(ggui.Rect) ggui.Point,
		semRect func(string) ggui.Rect,
	) {
		t.Helper()
		t.Setenv("MAKO_HOME", t.TempDir())
		i18n.Set("en")

		nodeA = &protocol.UiGraphNode{Id: "a", Label: "Node A", Shape: "card"}
		nodeB = &protocol.UiGraphNode{Id: "b", Label: "Node B", Shape: "card"}
		nodeC = &protocol.UiGraphNode{Id: "c", Label: "Node C", Shape: "card"}
		graph = &protocol.UiNode{
			Id:         id,
			Kind:       "graph",
			Variant:    "cards",
			MaxHeight:  420,
			Change:     true,
			Drop:       true,
			GraphNodes: []*protocol.UiGraphNode{nodeA, nodeB, nodeC},
			GraphEdges: []*protocol.UiGraphEdge{{From: "a", To: "b"}, {From: "b", To: "c"}},
		}

		var dropList []string
		drops = &dropList
		form = newBlockForm()
		p = ggui.ProbeBuilder(func() ggui.Widget {
			if rev == nil {
				rev = ggui.State(0)
			}
			return ggui.Reactive(func() ggui.Widget {
				_ = rev.Get()
				form.redraw()
				v := &treeView{
					block: "plugin:test" + id,
					form:  form,
					onEvent: func(_, _, kind, value string, _ map[string]string) {
						if kind == protocol.UiDrop {
							dropList = append(dropList, value)
							var payload struct {
								ID string  `json:"id"`
								X  float64 `json:"x"`
								Y  float64 `json:"y"`
							}
							if err := json.Unmarshal([]byte(value), &payload); err == nil && payload.ID == "c" {
								nodeC.X = payload.X
								nodeC.Y = payload.Y
								nodeC.HasPos = true
								nodeC.Pinned = true
								rev.Update(func(x int) int { return x + 1 })
							}
						}
					},
				}
				return v.node(graph, false)
			})
		}, ggui.Sz(760, 480))
		for range 3 {
			p.Frame()
		}

		center = func(r ggui.Rect) ggui.Point {
			return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
		}
		semRect = func(label string) ggui.Rect {
			s, ok := p.Semantics().Find(ggui.RoleButton, label)
			if !ok {
				t.Fatalf("missing %q", label)
			}
			return s.Rect
		}
		return p, form, graph, nodeA, nodeB, nodeC, rev, drops, center, semRect
	}

	t.Run("live_rerender_changes_bounds_mid_drag", func(t *testing.T) {
		p, form, _, nodeA, _, _, rev, drops, center, semRect := setupForceCardGraph(t, "/mid-drag-rerender-bounds")
		defer p.Close()

		st := form.graphState("/mid-drag-rerender-bounds")
		canvas := &pluginGraphCanvas{state: st}

		c0 := canvas.worldToScreen(st.lastRect, st.layout.pos["c"])
		grabOff := ggui.Pt(8, -4)
		grab0 := ggui.Pt(c0.X+grabOff.X, c0.Y+grabOff.Y)
		p.Press(grab0)
		p.Frame()

		pt1 := ggui.Pt(grab0.X-40, grab0.Y-10)
		p.Move(pt1)
		p.Advance(16 * time.Millisecond)
		p.Frame()

		want1 := ggui.Pt(pt1.X-grabOff.X, pt1.Y-grabOff.Y)
		got1 := canvas.worldToScreen(st.lastRect, st.layout.pos["c"])
		if err := math.Hypot(got1.X-want1.X, got1.Y-want1.Y); err > 2.0 {
			t.Fatalf("pre-rerender drag drifted: got=%v want=%v err=%.2fpx", got1, want1, err)
		}

		// Live re-render updates node A coordinates & expands graph bounds mid-drag.
		nodeA.X = -300
		nodeA.Y = 0
		nodeA.HasPos = true
		rev.Update(func(x int) int { return x + 1 })
		p.Frame()

		pt2 := ggui.Pt(pt1.X-10, pt1.Y-5)
		p.Move(pt2)
		p.Advance(16 * time.Millisecond)
		p.Frame()

		want2 := ggui.Pt(pt2.X-grabOff.X, pt2.Y-grabOff.Y)
		got2 := canvas.worldToScreen(st.lastRect, st.layout.pos["c"])
		if err := math.Hypot(got2.X-want2.X, got2.Y-want2.Y); err > 2.0 {
			t.Fatalf("live re-render changing bounds mid-drag broke cursor tracking: got=%v want=%v err=%.2fpx (camBounds.spanW=%.2f)", got2, want2, err, st.camBounds.spanW)
		}

		p.Release(pt2)
		p.Frame()
		if len(*drops) != 1 {
			t.Fatalf("expected 1 UiDrop event, got %v", *drops)
		}

		fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
		if !ok {
			t.Fatal("Fit view button missing")
		}
		p.Click(center(fitSem.Rect))
		p.Frame()
		p.Advance(300 * time.Millisecond)
		p.Frame()

		for _, lbl := range []string{"Node A", "Node B", "Node C"} {
			c := center(semRect(lbl))
			if !st.lastRect.Contains(c) {
				t.Fatalf("after Fit view node %q center %v outside lastRect %+v", lbl, c, st.lastRect)
			}
		}
	})

	t.Run("live_rerender_adds_node_mid_drag", func(t *testing.T) {
		p, form, graph, _, _, _, rev, drops, center, semRect := setupForceCardGraph(t, "/mid-drag-rerender-add")
		defer p.Close()

		st := form.graphState("/mid-drag-rerender-add")
		canvas := &pluginGraphCanvas{state: st}

		c0 := canvas.worldToScreen(st.lastRect, st.layout.pos["c"])
		grabOff := ggui.Pt(8, -4)
		grab0 := ggui.Pt(c0.X+grabOff.X, c0.Y+grabOff.Y)
		p.Press(grab0)
		p.Frame()

		pt1 := ggui.Pt(grab0.X-40, grab0.Y-10)
		p.Move(pt1)
		p.Advance(16 * time.Millisecond)
		p.Frame()

		want1 := ggui.Pt(pt1.X-grabOff.X, pt1.Y-grabOff.Y)
		got1 := canvas.worldToScreen(st.lastRect, st.layout.pos["c"])
		if err := math.Hypot(got1.X-want1.X, got1.Y-want1.Y); err > 2.0 {
			t.Fatalf("pre-rerender drag drifted: got=%v want=%v err=%.2fpx", got1, want1, err)
		}

		// Live re-render appends node d and edge a -> d mid-drag.
		nodeD := &protocol.UiGraphNode{Id: "d", Label: "Node D", Shape: "card", X: 300, Y: 0, HasPos: true}
		graph.GraphNodes = append(graph.GraphNodes, nodeD)
		graph.GraphEdges = append(graph.GraphEdges, &protocol.UiGraphEdge{From: "a", To: "d"})
		rev.Update(func(x int) int { return x + 1 })
		p.Frame()

		pt2 := ggui.Pt(pt1.X-10, pt1.Y-5)
		p.Move(pt2)
		p.Advance(16 * time.Millisecond)
		p.Frame()

		want2 := ggui.Pt(pt2.X-grabOff.X, pt2.Y-grabOff.Y)
		got2 := canvas.worldToScreen(st.lastRect, st.layout.pos["c"])
		if err := math.Hypot(got2.X-want2.X, got2.Y-want2.Y); err > 2.0 {
			t.Fatalf("live re-render adding node mid-drag broke cursor tracking: got=%v want=%v err=%.2fpx (camBounds.spanW=%.2f)", got2, want2, err, st.camBounds.spanW)
		}

		p.Release(pt2)
		p.Frame()
		if len(*drops) != 1 {
			t.Fatalf("expected 1 UiDrop event, got %v", *drops)
		}

		fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
		if !ok {
			t.Fatal("Fit view button missing")
		}
		p.Click(center(fitSem.Rect))
		p.Frame()
		p.Advance(300 * time.Millisecond)
		p.Frame()

		for _, lbl := range []string{"Node A", "Node B", "Node C", "Node D"} {
			c := center(semRect(lbl))
			if !st.lastRect.Contains(c) {
				t.Fatalf("after Fit view node %q center %v outside lastRect %+v", lbl, c, st.lastRect)
			}
		}
	})

	t.Run("live_rerender_subthreshold_before_4px", func(t *testing.T) {
		p, form, graph, _, _, _, rev, drops, _, _ := setupForceCardGraph(t, "/mid-drag-subthreshold-rerender")
		defer p.Close()

		st := form.graphState("/mid-drag-subthreshold-rerender")
		canvas := &pluginGraphCanvas{state: st}

		c0 := canvas.worldToScreen(st.lastRect, st.layout.pos["c"])
		grabOff := ggui.Pt(8, -4)
		grab0 := ggui.Pt(c0.X+grabOff.X, c0.Y+grabOff.Y)
		p.Press(grab0)
		p.Frame()

		// +2px sub-threshold move: drag has not crossed 4px yet.
		p.Move(ggui.Pt(grab0.X+2, grab0.Y))
		p.Frame()
		if st.draggingID != "" {
			t.Fatalf("expected empty st.draggingID before 4px threshold, got %q", st.draggingID)
		}

		// Live re-render appends node d (X=300, Y=0, HasPos=true) before 4px threshold is crossed.
		nodeD := &protocol.UiGraphNode{Id: "d", Label: "Node D", Shape: "card", X: 300, Y: 0, HasPos: true}
		graph.GraphNodes = append(graph.GraphNodes, nodeD)
		graph.GraphEdges = append(graph.GraphEdges, &protocol.UiGraphEdge{From: "a", To: "d"})
		rev.Update(func(x int) int { return x + 1 })
		p.Frame()

		// Cross 4px threshold: cursor tracking must stay anchored within <= 2.0px.
		dragPt := ggui.Pt(grab0.X+10, grab0.Y+5)
		p.Move(dragPt)
		p.Advance(16 * time.Millisecond)
		p.Frame()

		want := ggui.Pt(dragPt.X-grabOff.X, dragPt.Y-grabOff.Y)
		got := canvas.worldToScreen(st.lastRect, st.layout.pos["c"])
		if err := math.Hypot(got.X-want.X, got.Y-want.Y); err > 2.0 {
			t.Fatalf("sub-threshold live re-render before 4px broke cursor tracking: got=%v want=%v err=%.2fpx", got, want, err)
		}

		p.Release(dragPt)
		p.Frame()
		if len(*drops) != 1 {
			t.Fatalf("expected 1 UiDrop event, got %v", *drops)
		}
	})

	t.Run("subthreshold_pause_while_layoutTween_moves_node_then_drag", func(t *testing.T) {
		p, form, _, _, _, _, _, drops, center, semRect := setupForceCardGraph(t, "/mid-drag-subthreshold-tween")
		defer p.Close()

		st := form.graphState("/mid-drag-subthreshold-tween")
		canvas := &pluginGraphCanvas{state: st}

		hierSem, ok := p.Semantics().Find(ggui.RoleButton, "Hierarchy layout")
		if !ok {
			t.Fatal("Hierarchy layout button missing")
		}
		fitSem, ok := p.Semantics().Find(ggui.RoleButton, "Fit view")
		if !ok {
			t.Fatal("Fit view button missing")
		}
		p.Click(center(hierSem.Rect))
		p.Click(center(fitSem.Rect))
		p.Frame()

		rpBefore, ok := st.renderedPos("c")
		if !ok {
			t.Fatal("missing renderedPos(c)")
		}
		c0 := canvas.worldToScreen(st.lastRect, rpBefore)
		grabOff := ggui.Pt(8, -4)
		grab0 := ggui.Pt(c0.X+grabOff.X, c0.Y+grabOff.Y)
		p.Press(grab0)
		p.Frame()

		// Sub-threshold +2px move and 64ms pause while layoutTween moves Node C.
		p.Move(ggui.Pt(grab0.X+2, grab0.Y))
		p.Advance(64 * time.Millisecond)
		p.Frame()

		rpMid, _ := st.renderedPos("c")
		if math.Hypot(rpMid.X-rpBefore.X, rpMid.Y-rpBefore.Y) < 1e-2 {
			t.Fatalf("expected layoutTween to move Node C during 64ms subthreshold pause: before=%v mid=%v", rpBefore, rpMid)
		}

		// Cross 4px threshold: Node C must track the active pointer within <= 2.0px.
		dragPt := ggui.Pt(grab0.X+10, grab0.Y+5)
		p.Move(dragPt)
		p.Frame()

		want := ggui.Pt(dragPt.X-grabOff.X, dragPt.Y-grabOff.Y)
		got := canvas.worldToScreen(st.lastRect, st.layout.pos["c"])
		if err := math.Hypot(got.X-want.X, got.Y-want.Y); err > 2.0 {
			t.Fatalf("sub-threshold pause during layoutTween broke cursor tracking: got=%v want=%v err=%.2fpx", got, want, err)
		}

		p.Release(dragPt)
		p.Frame()
		if len(*drops) != 1 {
			t.Fatalf("expected 1 UiDrop event, got %v", *drops)
		}
		_ = semRect("Node C")
	})
}
