package gui

import (
	"math"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

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
