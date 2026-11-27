package gui

import (
	"testing"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/protocol"
)

func TestTier5GraphMidDragPreservesPinAndCameraAndPopupCapturesEscape(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Page.Set("workspace")
	m.SidebarPage.Set("plugin:agent-map/map")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s1", NodeId: "n1", Agent: "claude", State: protocol.StateRunning}})

	form := newBlockForm()
	ver := ggui.State(0)
	trees := []*protocol.UiNode{
		{
			Id: "/g", Kind: "graph", GraphLayout: "",
			GraphNodes: []*protocol.UiGraphNode{
				{Id: "sess:s1", Label: "A", X: 50, Y: 50, HasPos: true},
				{Id: "b", Label: "B", X: 250, Y: 50, HasPos: true},
			},
			GraphEdges: []*protocol.UiGraphEdge{{From: "sess:s1", To: "b"}},
		},
		{
			Id: "/g", Kind: "graph", GraphLayout: "force", Value: "b",
			GraphNodes: []*protocol.UiGraphNode{
				{Id: "sess:s1", Label: "A", HasPos: false},
				{Id: "b", Label: "B", X: 250, Y: 50, HasPos: true},
			},
			GraphEdges: []*protocol.UiGraphEdge{{From: "sess:s1", To: "b"}},
		},
	}

	pr := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Reactive(func() ggui.Widget {
			idx := ver.Get()
			form.redraw()
			v := newTreeView(m, "plugin:agent-map/map", "", form, trees[idx])
			return v.node(trees[idx], false)
		})
	}, ggui.Sz(600, 400))
	defer pr.Close()
	img := ggfx.NewImage(600, 400)
	pr.Draw(img)

	st := form.graphs["/g"]
	c := &pluginGraphCanvas{m: m, state: st, nodes: trees[0].GraphNodes}
	if !c.openNodeContextMenu(trees[0].GraphNodes[0], ggui.Pt(100, 100)) {
		t.Fatal("openNodeContextMenu failed")
	}
	pr.Draw(img)
	if !escapeCapturedInSemantics(pr.Semantics()) {
		t.Fatalf("open fallback ctxPopup did not capture Escape in semantics:\n%s", pr.Semantics().String())
	}

	// Register popup form in blockCache and verify m.handleEscape closes popup without clearing SidebarPage.
	m.saveCachedBlock("plugin:agent-map/map", "", blockFeed{}, form)
	m.handleEscape(pr)
	pr.Draw(img)
	if ggui.Peek(m.SidebarPage) != "plugin:agent-map/map" {
		t.Fatalf("handleEscape cleared SidebarPage=%q while ctxPopup was open", ggui.Peek(m.SidebarPage))
	}
	if cs := st.peekCtxState(nil); cs != nil && cs.popup != nil && cs.popup.IsOpen() {
		t.Fatal("handleEscape did not close open ctxPopup")
	}

	// Second Escape (now that popup is closed) returns from sidebar plugin page to workspace.
	m.handleEscape(pr)
	if ggui.Peek(m.SidebarPage) != "" {
		t.Fatalf("second handleEscape did not clear SidebarPage, got %q", ggui.Peek(m.SidebarPage))
	}

	// Mid-drag protection across "" -> "force" + HasPos true->false + Value="b" focusNode.
	st.draggingID = "sess:s1"
	st.pinnedPos["sess:s1"] = ggui.Pt(180, 220)
	origPanX, origPanY, origZoom := st.panX, st.panY, st.zoom
	ver.Set(1)
	pr.Frame()
	pr.Draw(img)

	if _, ok := st.pinnedPos["sess:s1"]; !ok {
		t.Fatal("pinnedPos deleted for actively dragged node when HasPos cleared")
	}
	if st.panX != origPanX || st.panY != origPanY || st.zoom != origZoom {
		t.Fatalf("camera snapped mid-drag from (%.1f,%.1f,z=%.2f) to (%.1f,%.1f,z=%.2f)", origPanX, origPanY, origZoom, st.panX, st.panY, st.zoom)
	}
}

func TestTier5GraphSpaceWindowSessionRouting(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Page.Set("workspace")
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-work", Name: "Work", NodeId: "n1", Agent: "claude", Project: "/work/a", State: protocol.StateRunning},
		{Id: "s-oss", Name: "OSS", NodeId: "n1", Agent: "codex", Project: "/work/b", State: protocol.StateRunning},
	})
	m.selectSession("s-work")

	sw := m.openSpaceWindow(allSpaceID)
	if sw == nil {
		t.Fatal("openSpaceWindow returned nil")
	}
	sw.sidebarPage.Set("plugin:agent-map/map")

	form := newBlockForm()
	tree := &protocol.UiNode{
		Id: "/g", Kind: "graph",
		GraphNodes: []*protocol.UiGraphNode{
			{Id: "sess:s-oss", Label: "OSS", X: 100, Y: 100, HasPos: true},
		},
	}
	v := newTreeView(m, "plugin:agent-map/map", "", form, tree)
	v.spaceWindow = sw

	pr := ggui.ProbeBuilder(func() ggui.Widget {
		return v.node(tree, false)
	}, ggui.Sz(600, 400))
	defer pr.Close()
	img := ggfx.NewImage(600, 400)
	pr.Draw(img)

	c := &pluginGraphCanvas{
		m:           m,
		spaceWindow: sw,
		state:       form.graphs["/g"],
		nodes:       tree.GraphNodes,
	}
	c.triggerDoubleTap("sess:s-oss")

	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("main window Selected mutated to %q, want s-work", got)
	}
	if got := m.spaceWindowActiveTab(sw); got != "s-oss" {
		t.Fatalf("space window active tab = %q, want s-oss", got)
	}
	if got := ggui.Peek(sw.sidebarPage); got != "" {
		t.Fatalf("space window sidebarPage = %q, want cleared after opening session", got)
	}

	// Also verify openNodeContextMenu "Open session" routes to Space window, not main window.
	sw.sidebarPage.Set("plugin:agent-map/map")
	m.selectSession("s-work")
	if !c.openNodeContextMenu(tree.GraphNodes[0], ggui.Pt(120, 120)) {
		t.Fatal("openNodeContextMenu returned false")
	}
	pr.Draw(img)
	openItem, ok := pr.Semantics().Find(ggui.RoleMenuItem, T("Open session"))
	if !ok {
		t.Fatalf("missing Open session item in fallback ctxPopup:\n%s", pr.Semantics().String())
	}
	pr.Click(ggui.Pt(openItem.Rect.Origin.X+openItem.Rect.Size.W/2, openItem.Rect.Origin.Y+openItem.Rect.Size.H/2))
	pr.Frame()
	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("openNodeContextMenu mutated main Selected=%q, want s-work", got)
	}
	if got := m.spaceWindowActiveTab(sw); got != "s-oss" {
		t.Fatalf("openNodeContextMenu space window active tab = %q, want s-oss", got)
	}
	if got := ggui.Peek(sw.sidebarPage); got != "" {
		t.Fatalf("openNodeContextMenu did not clear space window sidebarPage=%q", got)
	}

	// Also verify plugin open intent routing when invoked from a Space window.
	sw.sidebarPage.Set("plugin:agent-map/map")
	m.handlePluginDoneIn(sw, &protocol.PluginDone{
		Open: []*protocol.PluginOpen{{Kind: "session", Key: "s-oss"}},
	})
	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("dispatchPluginOpenIn mutated main Selected=%q, want s-work", got)
	}
	if got := ggui.Peek(sw.sidebarPage); got != "" {
		t.Fatalf("dispatchPluginOpenIn did not clear space window sidebarPage=%q", got)
	}
}

func TestTier5CanvasCJKHitboxMidPanAdoptAndMultiCanvasShortcutIsolation(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	var picked string
	cvNode := &protocol.UiNode{
		Id: "/cv", Kind: "canvas", Change: true, CanvasPan: true, CanvasZoom: true,
		CanvasWidth: 500, CanvasHeight: 200,
		CanvasCommands: []*protocol.UiCanvasCommand{
			{Op: "text", X: 20, Y: 40, Text: "가나다라마바사아자차", Size: 20, HitId: "kr-label", Alpha: proto.Float64(1)},
		},
	}
	form := newBlockForm()
	ver := ggui.State(0)
	pr := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Reactive(func() ggui.Widget {
			_ = ver.Get()
			form.redraw()
			v := newTreeView(nil, "e2e:probe", "", form, cvNode)
			v.onEvent = func(_, _, kind, val string, _ map[string]string) {
				if kind == protocol.UiChange {
					picked = val
				}
			}
			return v.node(cvNode, false)
		})
	}, ggui.Sz(500, 240))
	defer pr.Close()
	img := ggfx.NewImage(500, 240)
	pr.Draw(img)

	// x=300 is far beyond 10 CJK runes (~200px) starting at x=20; byte-length bug reached x=350.
	pr.Click(ggui.Pt(300, 40))
	pr.Draw(img)
	if picked == "kr-label" {
		t.Fatal("CJK text hitbox oversized beyond rendered rune width")
	}

	// Clicking directly over the CJK glyphs still hits kr-label.
	pr.Click(ggui.Pt(60, 48))
	pr.Draw(img)
	if picked != "kr-label" {
		t.Fatalf("expected click on CJK label to hit kr-label, got %q", picked)
	}

	// Mid-pan live tree rebuild must preserve drag state via Adopt.
	st := form.canvases["/cv"]
	pr.Press(ggui.Pt(200, 120))
	pr.Frame()
	ver.Set(1)
	pr.Frame()
	pr.Draw(img)
	pr.Move(ggui.Pt(280, 120))
	pr.Release(ggui.Pt(280, 120))
	pr.Frame()
	if st.panX == 0 {
		t.Fatal("canvas pan state lost across live rebuild")
	}

	// Multi-canvas shortcut isolation: hovering/focusing cv1 must NOT zoom cv2 on cmd+=.
	twoForm := newBlockForm()
	twoTree := &protocol.UiNode{
		Kind: "row",
		Children: []*protocol.UiNode{
			{Id: "/cv1", Kind: "canvas", CanvasWidth: 220, CanvasHeight: 160, CanvasPan: true, CanvasZoom: true},
			{Id: "/cv2", Kind: "canvas", CanvasWidth: 220, CanvasHeight: 160, CanvasPan: true, CanvasZoom: true},
		},
	}
	pr2 := ggui.ProbeBuilder(func() ggui.Widget {
		twoForm.redraw()
		v := newTreeView(nil, "e2e:probe", "", twoForm, twoTree)
		return v.node(twoTree, false)
	}, ggui.Sz(520, 200))
	defer pr2.Close()
	img2 := ggfx.NewImage(520, 200)
	pr2.Draw(img2)

	cv1 := twoForm.canvases["/cv1"]
	cv2 := twoForm.canvases["/cv2"]
	pr2.Click(ggui.Pt(80, 80))
	pr2.Frame()
	pr2.Key("cmd+=")
	pr2.Frame()
	pr2.Draw(img2)
	if cv1.zoom <= 1.0 {
		t.Fatalf("expected hovered/focused cv1 to zoom in, got zoom=%.2f", cv1.zoom)
	}
	if cv2.zoom != 1.0 {
		t.Fatalf("unfocused cv2 unexpectedly zoomed to %.2f on single cmd+=", cv2.zoom)
	}
	pr2.Key("cmd+-")
	pr2.Frame()
	pr2.Draw(img2)
	if cv2.zoom != 1.0 {
		t.Fatalf("unfocused cv2 unexpectedly zoomed to %.2f on cmd+-", cv2.zoom)
	}
	cv2.zoom = 2.0
	pr2.Key("cmd+0")
	pr2.Frame()
	pr2.Draw(img2)
	if cv1.zoom != 1.0 {
		t.Fatalf("expected focused cv1 to reset to 1.0 on cmd+0, got %.2f", cv1.zoom)
	}
	if cv2.zoom != 2.0 {
		t.Fatalf("unfocused cv2 unexpectedly reset from 2.0 to %.2f on cmd+0", cv2.zoom)
	}
}

func TestTier5TimelineEndMarkerClickableAndMidPanAdopt(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	var picked string
	tlNode := &protocol.UiNode{
		Id: "/tl", Kind: "timeline", Change: true,
		TimelineLanes: []*protocol.UiTimelineLane{{
			Id: "l1", Label: "Lane 1",
			Spans: []*protocol.UiTimelineSpan{
				{Id: "sp-start", Label: "Start", StartMs: 0, EndMs: 100},
				{Id: "sp-end-marker", Label: "EndMarker", StartMs: 1000, EndMs: 1000, Kind: "marker"},
			},
		}},
	}
	form := newBlockForm()
	ver := ggui.State(0)
	pr := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Reactive(func() ggui.Widget {
			_ = ver.Get()
			form.redraw()
			v := newTreeView(nil, "e2e:probe", "", form, tlNode)
			v.onEvent = func(_, _, kind, val string, _ map[string]string) {
				if kind == protocol.UiChange {
					picked = val
				}
			}
			return v.node(tlNode, false)
		})
	}, ggui.Sz(600, 200))
	defer pr.Close()
	img := ggfx.NewImage(600, 200)
	pr.Draw(img)

	for y := 30.0; y <= 140.0 && picked == ""; y += 10 {
		for x := 540.0; x <= 599.0 && picked == ""; x += 2 {
			pr.Click(ggui.Pt(x, y))
			pr.Draw(img)
		}
	}
	if picked != "sp-end-marker" {
		t.Fatalf("zero-duration marker at maxMs unclickable (picked=%q)", picked)
	}

	// Zoom in so maxScrollX > 0, pan mid-rebuild, and verify scrollX changes.
	st := form.timelines["/tl"]
	st.zoom = 3.0
	pr.Frame()
	pr.Draw(img)
	st.scrollX = 80
	pr.Press(ggui.Pt(300, 70))
	pr.Frame()
	ver.Set(1)
	pr.Frame()
	pr.Draw(img)
	pr.Move(ggui.Pt(240, 70))
	pr.Release(ggui.Pt(240, 70))
	pr.Frame()
	if st.scrollX == 80 {
		t.Fatalf("timeline pan state lost across live rebuild (scrollX stayed %.1f)", st.scrollX)
	}
}

func TestTier5HeatmapDenseClipBoundsAndSelectionPersist(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	vals := make([]float64, 60)
	for i := range vals {
		vals[i] = float64(i + 1)
	}
	denseNode := &protocol.UiNode{
		Id: "/hm", Kind: "chart", Chart: "heatmap", Change: true,
		Series: []*protocol.UiSeries{{Name: "S", Values: vals}},
	}
	form := newBlockForm()
	ver := ggui.State(0)
	var events []string
	pr := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Reactive(func() ggui.Widget {
			_ = ver.Get()
			form.redraw()
			v := newTreeView(nil, "e2e:probe", "", form, denseNode)
			v.onEvent = func(_, _, kind, val string, _ map[string]string) {
				if kind == protocol.UiChange {
					events = append(events, val)
				}
			}
			return v.node(denseNode, false)
		})
	}, ggui.Sz(260, 160))
	defer pr.Close()
	img := ggfx.NewImage(260, 160)
	pr.Draw(img)

	for _, n := range pr.Semantics().All() {
		if n.Role == ggui.RoleCell && n.Rect.Origin.X+n.Rect.Size.W > 260.5 {
			t.Fatalf("heatmap cell semantic rect spills beyond widget bounds (260): %+v", n.Rect)
		}
	}

	// Out-of-bounds clicks outside heatmap rect must emit 0 events.
	pr.Click(ggui.Pt(290, 80))
	pr.Click(ggui.Pt(100, 180))
	pr.Frame()
	if len(events) != 0 {
		t.Fatalf("expected 0 events for out-of-bounds click, got %v", events)
	}

	// Click an in-bounds cell and verify Selected=true persists across live rebuild.
	var firstCell ggui.SemNode
	foundCell := false
	for _, n := range pr.Semantics().All() {
		if n.Role == ggui.RoleCell {
			firstCell = n
			foundCell = true
			break
		}
	}
	if !foundCell {
		t.Fatal("expected at least one RoleCell in dense heatmap semantics")
	}
	pr.Click(ggui.Pt(firstCell.Rect.Origin.X+firstCell.Rect.Size.W/2, firstCell.Rect.Origin.Y+firstCell.Rect.Size.H/2))
	pr.Frame()
	pr.Draw(img)
	if len(events) != 1 {
		t.Fatalf("expected 1 click event, got %v", events)
	}
	selBefore := 0
	for _, n := range pr.Semantics().All() {
		if n.Role == ggui.RoleCell && n.Selected {
			selBefore++
		}
	}
	if selBefore != 1 {
		t.Fatalf("expected 1 selected cell before rebuild, got %d", selBefore)
	}

	ver.Set(1)
	pr.Frame()
	pr.Draw(img)
	selAfter := 0
	for _, n := range pr.Semantics().All() {
		if n.Role == ggui.RoleCell && n.Selected {
			selAfter++
		}
	}
	if selAfter != 1 {
		t.Fatalf("heatmap lost selected cell across live rebuild: selBefore=%d selAfter=%d", selBefore, selAfter)
	}
}

func TestTier5PluginTabInSpaceWindowRoutesSessionToSpaceWindow(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Page.Set("workspace")
	m.Spaces.Set(spaceList{
		Spaces: []space{
			{ID: "sp-work", Name: "Work", Color: "blue", Owners: []string{"github.com/acme"}},
			{ID: "sp-oss", Name: "OSS", Color: "teal", Owners: []string{"github.com/oss"}},
		},
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-work", Name: "Work", NodeId: "n1", Agent: "claude", Project: "/work/a", Owner: "github.com/acme", State: protocol.StateRunning},
		{Id: "s-oss", Name: "OSS", NodeId: "n1", Agent: "codex", Project: "/work/b", Owner: "github.com/oss", State: protocol.StateRunning},
	})
	m.Prefs.Update(func(p prefs) prefs { p.ShownSpace = "sp-work"; return p })
	m.ws.layoutKey = layoutKey("sp-work", "")
	m.selectSession("s-work")

	sw := m.openSpaceWindow("sp-oss")
	if sw == nil {
		t.Fatal("openSpaceWindow returned nil")
	}
	ptID := pluginTabID("plugin:demo/graph", "default")
	m.PluginTabs.Set(map[string]pluginTab{
		ptID: {Block: "plugin:demo/graph", PluginID: "demo", Key: "default", Title: "Demo Graph"},
	})
	key := sw.peekLayoutKey()
	fg := m.activateTabInKey(key, ggui.Peek(sw.focusGroup), ptID)
	sw.focusGroup.Set(fg)

	tree := &protocol.UiNode{
		Id: "/g", Kind: "graph",
		GraphNodes: []*protocol.UiGraphNode{{Id: "sess:s-oss", Label: "OSS", X: 100, Y: 100, HasPos: true}},
	}
	m.saveCachedBlockInstance("plugin:demo/graph", "", "default", blockFeed{
		Update: &protocol.BlockUpdate{
			Block: "plugin:demo/graph", Plugin: "demo", State: protocol.PluginReady, Tree: tree, Instance: "default",
		},
	}, newBlockForm())

	pr := ggui.ProbeBuilder(func() ggui.Widget {
		return keyedLayoutViewIn(m, sw.layoutKey, sw.focusGroup, sw)
	}, ggui.Sz(800, 500))
	defer pr.Close()
	img := ggfx.NewImage(800, 500)
	pr.Draw(img)

	btn, ok := pr.Semantics().Find(ggui.RoleButton, "OSS")
	if !ok {
		t.Fatalf("missing OSS node button:\n%s", pr.Semantics().String())
	}
	pr.ClickButton(ggui.Pt(btn.Rect.Origin.X+btn.Rect.Size.W/2, btn.Rect.Origin.Y+btn.Rect.Size.H/2), ggui.MouseButtonRight)
	pr.Draw(img)
	openItem, ok := pr.Semantics().Find(ggui.RoleMenuItem, T("Open session"))
	if !ok {
		t.Fatalf("missing Open session item:\n%s", pr.Semantics().String())
	}
	pr.Click(ggui.Pt(openItem.Rect.Origin.X+openItem.Rect.Size.W/2, openItem.Rect.Origin.Y+openItem.Rect.Size.H/2))
	pr.Frame()

	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("pluginTab in Space window mutated main Selected=%q, want s-work", got)
	}
	if got := m.spaceWindowActiveTab(sw); got != "s-oss" {
		t.Fatalf("pluginTab in Space window active tab=%q, want s-oss", got)
	}
}

func TestTier5MultiCanvasHoverOverridesOtherFocusedCanvasShortcut(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	twoForm := newBlockForm()
	twoTree := &protocol.UiNode{
		Kind: "row",
		Children: []*protocol.UiNode{
			{Id: "/cv1", Kind: "canvas", CanvasWidth: 220, CanvasHeight: 160, CanvasPan: true, CanvasZoom: true},
			{Id: "/cv2", Kind: "canvas", CanvasWidth: 220, CanvasHeight: 160, CanvasPan: true, CanvasZoom: true},
		},
	}
	pr := ggui.ProbeBuilder(func() ggui.Widget {
		twoForm.redraw()
		v := newTreeView(nil, "e2e:probe", "", twoForm, twoTree)
		return v.node(twoTree, false)
	}, ggui.Sz(520, 200))
	defer pr.Close()
	img := ggfx.NewImage(520, 200)
	pr.Draw(img)

	cv1 := twoForm.canvases["/cv1"]
	cv2 := twoForm.canvases["/cv2"]

	pr.Click(ggui.Pt(80, 80)) // cv1 Focused
	pr.Frame()
	pr.Move(ggui.Pt(360, 80)) // cv2 Hovered while cv1 remains Focused
	pr.Frame()
	pr.Draw(img)

	pr.Key("cmd+=")
	pr.Frame()
	pr.Draw(img)
	if cv1.zoom != 1.0 {
		t.Fatalf("focused-only cv1 zoomed to %.2f while pointer hovered cv2", cv1.zoom)
	}
	if cv2.zoom <= 1.0 {
		t.Fatalf("hovered cv2 failed to zoom on cmd+= (zoom=%.2f)", cv2.zoom)
	}

	// Moving pointer off both canvases lets focused cv1 zoom via HandleKey/applyZoomChord.
	z2 := cv2.zoom
	pr.Move(ggui.Pt(510, 10))
	pr.Frame()
	pr.Draw(img)
	pr.Key("cmd+=")
	pr.Frame()
	pr.Draw(img)
	if cv1.zoom <= 1.0 {
		t.Fatalf("focused cv1 failed to zoom via HandleKey when unhovered (zoom=%.2f)", cv1.zoom)
	}
	if cv2.zoom != z2 {
		t.Fatalf("unhovered/unfocused cv2 zoomed from %.2f to %.2f", z2, cv2.zoom)
	}
}

func TestTier5SpaceWindowGraphContextActionsAndPluginTabIntents(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Page.Set("workspace")
	m.Spaces.Set(spaceList{
		Spaces: []space{
			{ID: "sp-work", Name: "Work", Color: "blue", Owners: []string{"github.com/acme"}},
			{ID: "sp-oss", Name: "OSS", Color: "teal", Owners: []string{"github.com/oss"}},
		},
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-work", Name: "Work", NodeId: "n1", Agent: "claude", Project: "/work/a", Owner: "github.com/acme", State: protocol.StateRunning},
		{Id: "s-oss", Name: "OSS", NodeId: "n1", Agent: "codex", Project: "/work/b", Owner: "github.com/oss", State: protocol.StateRunning},
	})
	m.Prefs.Update(func(p prefs) prefs { p.ShownSpace = "sp-work"; return p })
	m.ws.layoutKey = layoutKey("sp-work", "")
	m.selectSession("s-work")

	sw := m.openSpaceWindow("sp-oss")
	if sw == nil {
		t.Fatal("openSpaceWindow returned nil")
	}
	sw.sidebarPage.Set("plugin:agent-map/map")

	form := newBlockForm()
	tree := &protocol.UiNode{
		Id: "/g", Kind: "graph",
		GraphNodes: []*protocol.UiGraphNode{{Id: "sess:s-oss", Label: "OSS", X: 100, Y: 100, HasPos: true}},
	}
	v := newTreeView(m, "plugin:agent-map/map", "", form, tree)
	v.spaceWindow = sw

	pr := ggui.ProbeBuilder(func() ggui.Widget {
		return v.node(tree, false)
	}, ggui.Sz(600, 400))
	defer pr.Close()
	img := ggfx.NewImage(600, 400)
	pr.Draw(img)

	c := &pluginGraphCanvas{m: m, spaceWindow: sw, state: form.graphs["/g"], nodes: tree.GraphNodes}
	if !c.openNodeContextMenu(tree.GraphNodes[0], ggui.Pt(120, 120)) {
		t.Fatal("openNodeContextMenu failed")
	}
	pr.Draw(img)
	sendItem, ok := pr.Semantics().Find(ggui.RoleMenuItem, T("Send message"))
	if !ok {
		t.Fatalf("missing Send message item:\n%s", pr.Semantics().String())
	}
	pr.Click(ggui.Pt(sendItem.Rect.Origin.X+sendItem.Rect.Size.W/2, sendItem.Rect.Origin.Y+sendItem.Rect.Size.H/2))
	pr.Frame()
	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("Send message mutated main Selected=%q, want s-work", got)
	}
	if got := m.spaceWindowActiveTab(sw); got != "s-oss" {
		t.Fatalf("Send message sw active tab=%q, want s-oss", got)
	}

	sw.sidebarPage.Set("plugin:agent-map/map")
	if !c.openNodeContextMenu(tree.GraphNodes[0], ggui.Pt(120, 120)) {
		t.Fatal("openNodeContextMenu 2 failed")
	}
	pr.Draw(img)
	subItem, ok := pr.Semantics().Find(ggui.RoleMenuItem, T("Run subagent"))
	if !ok {
		t.Fatalf("missing Run subagent item:\n%s", pr.Semantics().String())
	}
	pr.Click(ggui.Pt(subItem.Rect.Origin.X+subItem.Rect.Size.W/2, subItem.Rect.Origin.Y+subItem.Rect.Size.H/2))
	pr.Frame()
	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("Run subagent mutated main Selected=%q, want s-work", got)
	}
	if got := m.spaceWindowActiveTab(sw); !isLaunchTab(got) {
		t.Fatalf("Run subagent sw active tab=%q, want launch tab", got)
	}

	sw.sidebarPage.Set("plugin:ui-workbench/explorer")
	m.handlePluginDoneIn(sw, &protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind: "tab", Block: "plugin:ui-workbench/viewer", Key: "diff-auth", Title: "Auth Diff",
		}},
	})
	ptID := pluginTabID("plugin:ui-workbench/viewer", "diff-auth")
	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("openTab in sw mutated main Selected=%q, want s-work", got)
	}
	if got := m.spaceWindowActiveTab(sw); got != ptID {
		t.Fatalf("openTab in sw active tab=%q, want %q", got, ptID)
	}

	m.handlePluginDoneIn(sw, &protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind: "close_tab", Block: "plugin:ui-workbench/viewer", Key: "diff-auth",
		}},
	})
	if m.peekLayoutForKey(sw.peekLayoutKey()).groupOf(ptID) != nil {
		t.Fatalf("close_tab in sw left orphaned %q in sw layout", ptID)
	}
	if _, exists := ggui.Peek(m.PluginTabs)[ptID]; exists {
		t.Fatalf("close_tab in sw left %q in m.PluginTabs", ptID)
	}

	// Verify preview replacement in sw stays isolated from main-window preview tabs
	// and closing a tab still open in main preserves its PluginTabs entry.
	m.openPluginTab(pluginTab{Block: "plugin:ui-workbench/viewer", Key: "main-prev", Title: "Main Prev", Preview: true})
	mainPrevID := pluginTabID("plugin:ui-workbench/viewer", "main-prev")
	m.handlePluginDoneIn(sw, &protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind: "tab", Block: "plugin:ui-workbench/viewer", Key: "sw-prev-1", Title: "SW Prev 1", Preview: true,
		}},
	})
	swPrev1ID := pluginTabID("plugin:ui-workbench/viewer", "sw-prev-1")
	m.handlePluginDoneIn(sw, &protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind: "tab", Block: "plugin:ui-workbench/viewer", Key: "sw-prev-2", Title: "SW Prev 2", Preview: true,
		}},
	})
	swPrev2ID := pluginTabID("plugin:ui-workbench/viewer", "sw-prev-2")
	if m.peekLayoutForKey(sw.peekLayoutKey()).groupOf(swPrev1ID) != nil {
		t.Fatalf("sw preview %q was not replaced by %q", swPrev1ID, swPrev2ID)
	}
	if _, exists := ggui.Peek(m.PluginTabs)[swPrev1ID]; exists {
		t.Fatalf("replaced sw preview %q remained in m.PluginTabs", swPrev1ID)
	}
	if got := m.spaceWindowActiveTab(sw); got != swPrev2ID {
		t.Fatalf("sw active tab=%q, want %q", got, swPrev2ID)
	}
	if ggui.Peek(m.Layout).groupOf(mainPrevID) == nil {
		t.Fatalf("sw preview replacement evicted main-window preview %q", mainPrevID)
	}
	if _, exists := ggui.Peek(m.PluginTabs)[mainPrevID]; !exists {
		t.Fatalf("main-window preview %q missing from m.PluginTabs", mainPrevID)
	}

	m.handlePluginDoneIn(sw, &protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind: "tab", Block: "plugin:ui-workbench/viewer", Key: "main-prev", Title: "Main Prev",
		}},
	})
	m.handlePluginDoneIn(sw, &protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind: "close_tab", Block: "plugin:ui-workbench/viewer", Key: "main-prev",
		}},
	})
	if m.peekLayoutForKey(sw.peekLayoutKey()).groupOf(mainPrevID) != nil {
		t.Fatalf("close_tab in sw left %q in sw layout", mainPrevID)
	}
	if _, exists := ggui.Peek(m.PluginTabs)[mainPrevID]; !exists {
		t.Fatalf("close_tab in sw deleted shared %q from m.PluginTabs while still open in main", mainPrevID)
	}
}

func TestTier5MultiCanvasPressedPlusHoverIsolationAndDisabledControls(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	twoForm := newBlockForm()
	twoTree := &protocol.UiNode{
		Kind: "row",
		Children: []*protocol.UiNode{
			{Id: "/cv1", Kind: "canvas", CanvasWidth: 220, CanvasHeight: 160, CanvasPan: true, CanvasZoom: true},
			{Id: "/cv2", Kind: "canvas", CanvasWidth: 220, CanvasHeight: 160, CanvasPan: true, CanvasZoom: true},
		},
	}
	pr := ggui.ProbeBuilder(func() ggui.Widget {
		twoForm.redraw()
		v := newTreeView(nil, "e2e:probe", "", twoForm, twoTree)
		return v.node(twoTree, false)
	}, ggui.Sz(520, 200))
	defer pr.Close()
	img := ggfx.NewImage(520, 200)
	pr.Draw(img)

	cv1 := twoForm.canvases["/cv1"]
	cv2 := twoForm.canvases["/cv2"]

	pr.Press(ggui.Pt(80, 80)) // cv1 Pressed
	pr.Frame()
	pr.Move(ggui.Pt(360, 80)) // pointer moves over cv2 while cv1 remains Pressed
	pr.Frame()
	pr.Draw(img)
	pr.Key("cmd+=")
	pr.Frame()
	pr.Draw(img)
	pr.Release(ggui.Pt(360, 80))
	if cv1.zoom != 1.0 {
		t.Fatalf("pressed cv1 zoomed to %.2f while pointer hovered cv2", cv1.zoom)
	}
	if cv2.zoom <= 1.0 {
		t.Fatalf("hovered cv2 did not zoom on cmd+= (zoom=%.2f)", cv2.zoom)
	}

	disForm := newBlockForm()
	disTree := &protocol.UiNode{
		Id: "/dis", Kind: "canvas", Disabled: true, CanvasWidth: 260, CanvasHeight: 160, CanvasPan: true, CanvasZoom: true,
	}
	prDis := ggui.ProbeBuilder(func() ggui.Widget {
		disForm.redraw()
		v := newTreeView(nil, "e2e:probe", "", disForm, disTree)
		return v.node(disTree, false)
	}, ggui.Sz(300, 200))
	defer prDis.Close()
	imgDis := ggfx.NewImage(300, 200)
	prDis.Draw(imgDis)

	for _, name := range []string{T("Zoom in"), T("Zoom out"), T("Reset")} {
		btn, ok := prDis.Semantics().Find(ggui.RoleButton, name)
		if !ok {
			t.Fatalf("missing %s button:\n%s", name, prDis.Semantics().String())
		}
		if !btn.Disabled {
			t.Fatalf("expected %s overlay button to be Disabled on disabled canvas", name)
		}
	}
	zin, _ := prDis.Semantics().Find(ggui.RoleButton, T("Zoom in"))
	prDis.Click(ggui.Pt(zin.Rect.Origin.X+zin.Rect.Size.W/2, zin.Rect.Origin.Y+zin.Rect.Size.H/2))
	prDis.Frame()
	if disForm.canvases["/dis"].zoom != 1.0 {
		t.Fatalf("disabled canvas zoomed to %.2f via overlay button", disForm.canvases["/dis"].zoom)
	}
}

func TestTier5DetachedPaneSpaceWindowPluginTabAndTabBarCloseCleanup(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Page.Set("workspace")
	m.Spaces.Set(spaceList{
		Spaces: []space{
			{ID: "sp-work", Name: "Work", Color: "blue", Owners: []string{"github.com/acme"}},
			{ID: "sp-oss", Name: "OSS", Color: "teal", Owners: []string{"github.com/oss"}},
		},
		Projects: []project{
			{ID: "p-work", Name: "Proj Work", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/a"}}}}},
			{ID: "p-oss", Name: "Proj OSS", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/b"}}}}},
		},
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-work", Name: "Work", NodeId: "n1", Agent: "claude", Project: "/work/a", Owner: "github.com/acme", State: protocol.StateRunning},
		{Id: "s-oss", Name: "OSS", NodeId: "n1", Agent: "codex", Project: "/work/b", Owner: "github.com/oss", State: protocol.StateRunning},
	})
	m.plugins.List.Set([]*protocol.PluginInfo{{
		Id: "scoped", State: protocol.PluginReady,
		Blocks: []*protocol.PluginBlock{
			{Name: "plugin:scoped/spaceview", Title: "Space View", Scope: protocol.ScopeSpace, Slots: []string{"sidebar", "tab"}},
			{Name: "plugin:scoped/projview", Title: "Project View", Scope: protocol.ScopeProject, Slots: []string{"sidebar", "tab"}},
			{Name: "plugin:scoped/sessview", Title: "Session View", Scope: protocol.ScopeSession, Slots: []string{"sidebar", "tab"}},
			{Name: "plugin:scoped/folderview", Title: "Folder View", Scope: protocol.ScopeFolder, Slots: []string{"sidebar", "tab"}},
		},
	}})
	m.Prefs.Update(func(p prefs) prefs { p.ShownSpace = "sp-work"; return p })
	m.ws.layoutKey = layoutKey("sp-work", "")
	m.selectSession("s-work")

	sw := m.openSpaceWindow("sp-oss")
	if sw == nil {
		t.Fatal("openSpaceWindow returned nil")
	}

	// 1. Detached pane popped out from sw must preserve sw routing for pluginTab.
	m.openPluginTabIn(sw, pluginTab{
		Block: "plugin:demo/graph", PluginID: "demo", Key: "default", Title: "Demo Graph",
	})
	mainFolder := protocol.FolderScope("n1", "/work/a")
	if got := ggui.Untrack(func() string { return m.resolveActiveScopeIn(sw, protocol.ScopeFolder) }); got != "" {
		t.Fatalf("resolveActiveScopeIn(sw, ScopeFolder) on unscoped pluginTab with no sw.scope = %q (main=%q), want empty", got, mainFolder)
	}
	tree := &protocol.UiNode{
		Id: "/g", Kind: "graph",
		GraphNodes: []*protocol.UiGraphNode{{Id: "sess:s-oss", Label: "OSS", X: 100, Y: 100, HasPos: true}},
	}
	m.saveCachedBlockInstance("plugin:demo/graph", "", "default", blockFeed{
		Update: &protocol.BlockUpdate{
			Block: "plugin:demo/graph", Plugin: "demo", State: protocol.PluginReady, Tree: tree, Instance: "default",
		},
	}, newBlockForm())

	ref := paneRef{Key: sw.peekLayoutKey(), GroupID: ggui.Peek(sw.focusGroup)}
	m.popoutPane(ref)
	prDet := ggui.ProbeBuilder(func() ggui.Widget {
		return detachedPaneWindow(m, ref, func() { m.dockPane(ref) })
	}, ggui.Sz(800, 500))
	defer prDet.Close()
	img := ggfx.NewImage(800, 500)
	prDet.Draw(img)

	btn, ok := prDet.Semantics().Find(ggui.RoleButton, "OSS")
	if !ok {
		t.Fatalf("missing OSS node button in detached pane:\n%s", prDet.Semantics().String())
	}
	prDet.ClickButton(ggui.Pt(btn.Rect.Origin.X+btn.Rect.Size.W/2, btn.Rect.Origin.Y+btn.Rect.Size.H/2), ggui.MouseButtonRight)
	prDet.Draw(img)
	openItem, ok := prDet.Semantics().Find(ggui.RoleMenuItem, T("Open session"))
	if !ok {
		t.Fatalf("missing Open session item in detached pane:\n%s", prDet.Semantics().String())
	}
	prDet.Click(ggui.Pt(openItem.Rect.Origin.X+openItem.Rect.Size.W/2, openItem.Rect.Origin.Y+openItem.Rect.Size.H/2))
	prDet.Frame()
	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("detached pane from Space window mutated main Selected=%q, want s-work", got)
	}
	if got := m.spaceWindowActiveTab(sw); got != "s-oss" {
		t.Fatalf("detached pane from Space window sw active tab=%q, want s-oss", got)
	}
	m.dockPane(ref)

	// 2. Scoped resolution, plugin tabs, and sidebar blocks in sw must resolve sw's space/project/session/folder, not main's.
	if got := ggui.Untrack(func() string { return m.resolveActiveScopeIn(sw, protocol.ScopeSpace) }); got != "space:sp-oss" {
		t.Fatalf("resolveActiveScopeIn(sw, ScopeSpace)=%q, want space:sp-oss", got)
	}
	if got := ggui.Untrack(func() string { return m.resolveActiveScopeIn(sw, protocol.ScopeProject) }); got != "project:p-oss" {
		t.Fatalf("resolveActiveScopeIn(sw, ScopeProject)=%q, want project:p-oss", got)
	}
	if got := ggui.Untrack(func() string { return m.resolveActiveScopeIn(sw, protocol.ScopeSession) }); got != "session:s-oss" {
		t.Fatalf("resolveActiveScopeIn(sw, ScopeSession)=%q, want session:s-oss", got)
	}
	wantFolder := protocol.FolderScope("n1", "/work/b")
	if got := ggui.Untrack(func() string { return m.resolveActiveScopeIn(sw, protocol.ScopeFolder) }); got != wantFolder {
		t.Fatalf("resolveActiveScopeIn(sw, ScopeFolder)=%q, want %q", got, wantFolder)
	}

	m.openPluginTabIn(sw, pluginTab{Block: "plugin:scoped/spaceview", Key: "sp"})
	if got := ggui.Peek(m.PluginTabs)[pluginTabID("plugin:scoped/spaceview", "sp")].Scope; got != "space:sp-oss" {
		t.Fatalf("space-scoped pluginTab in sw resolved Scope=%q, want space:sp-oss", got)
	}
	if got := ggui.Untrack(func() string { return m.resolveActiveScopeIn(sw, protocol.ScopeFolder) }); got != wantFolder {
		t.Fatalf("resolveActiveScopeIn(sw, ScopeFolder) on spaceview with sw project scope=%q, want %q (must not leak main %q)", got, wantFolder, mainFolder)
	}
	sw.scope.Set(scopeKey{})
	if got := ggui.Untrack(func() string { return m.resolveActiveScopeIn(sw, protocol.ScopeFolder) }); got != "" {
		t.Fatalf("resolveActiveScopeIn(sw, ScopeFolder) on spaceview with cleared sw.scope=%q, want empty (must not leak main %q)", got, mainFolder)
	}
	m.spaceWindowSelectSession(sw, "s-oss")
	m.openPluginTabIn(sw, pluginTab{Block: "plugin:scoped/projview", Key: "pj"})
	if got := ggui.Peek(m.PluginTabs)[pluginTabID("plugin:scoped/projview", "pj")].Scope; got != "project:p-oss" {
		t.Fatalf("project-scoped pluginTab in sw resolved Scope=%q, want project:p-oss", got)
	}
	m.spaceWindowSelectSession(sw, "s-oss")
	m.openPluginTabIn(sw, pluginTab{Block: "plugin:scoped/sessview", Key: "ss"})
	if got := ggui.Peek(m.PluginTabs)[pluginTabID("plugin:scoped/sessview", "ss")].Scope; got != "session:s-oss" {
		t.Fatalf("session-scoped pluginTab in sw resolved Scope=%q, want session:s-oss", got)
	}
	m.spaceWindowSelectSession(sw, "s-oss")
	m.openPluginTabIn(sw, pluginTab{Block: "plugin:scoped/folderview", Key: "fd"})
	if got := ggui.Peek(m.PluginTabs)[pluginTabID("plugin:scoped/folderview", "fd")].Scope; got != wantFolder {
		t.Fatalf("folder-scoped pluginTab in sw resolved Scope=%q, want %q", got, wantFolder)
	}

	prSide := ggui.ProbeBuilder(func() ggui.Widget {
		return mainPluginPageViewIn(m, sidebarPageDef{
			Block: "plugin:scoped/spaceview", PluginID: "scoped", Title: "Space View", Scope: protocol.ScopeSpace,
		}, sw)
	}, ggui.Sz(600, 400))
	defer prSide.Close()
	prSide.Draw(img)
	if m.cachedBlock("plugin:scoped/spaceview", "space:sp-oss") == nil {
		t.Fatal("sidebar space-scoped block in sw did not resolve/cache under space:sp-oss")
	}
	if m.cachedBlock("plugin:scoped/spaceview", "space:sp-work") != nil {
		t.Fatal("sidebar space-scoped block in sw leaked main scope space:sp-work")
	}

	// 3. keyedPaneTabBar OnClose must clean m.PluginTabs and cachedBlockInstance.
	m.openPluginTabIn(sw, pluginTab{
		Block: "plugin:ui-workbench/viewer", PluginID: "ui-workbench", Key: "diff-auth", Title: "Auth Diff", Params: `{"v":1}`, Preview: false,
	})
	ptID := pluginTabID("plugin:ui-workbench/viewer", "diff-auth")
	m.saveCachedBlockInstance("plugin:ui-workbench/viewer", "", "diff-auth", blockFeed{
		Update: &protocol.BlockUpdate{Block: "plugin:ui-workbench/viewer", Plugin: "ui-workbench", State: protocol.PluginReady, Instance: "diff-auth"},
	}, newBlockForm())

	prSW := ggui.ProbeBuilder(func() ggui.Widget {
		return keyedLayoutViewIn(m, sw.layoutKey, sw.focusGroup, sw)
	}, ggui.Sz(800, 500))
	defer prSW.Close()
	prSW.Draw(img)

	closeBtn, ok := prSW.Semantics().Find(ggui.RoleButton, Tf("Close %s", "Auth Diff"))
	if !ok {
		t.Fatalf("missing Close Auth Diff button:\n%s", prSW.Semantics().String())
	}
	prSW.Click(ggui.Pt(closeBtn.Rect.Origin.X+closeBtn.Rect.Size.W/2, closeBtn.Rect.Origin.Y+closeBtn.Rect.Size.H/2))
	prSW.Frame()
	if _, leaked := ggui.Peek(m.PluginTabs)[ptID]; leaked {
		t.Fatalf("keyedPaneTabBar OnClose leaked %q in m.PluginTabs", ptID)
	}
	if cached := m.cachedBlockInstance("plugin:ui-workbench/viewer", "", "diff-auth"); cached != nil {
		t.Fatalf("keyedPaneTabBar OnClose leaked cachedBlockInstance for %q", ptID)
	}
	m.openPluginTabIn(sw, pluginTab{
		Block: "plugin:ui-workbench/viewer", PluginID: "ui-workbench", Key: "diff-auth", Title: "Auth Diff", Preview: true,
	})
	if pt := ggui.Peek(m.PluginTabs)[ptID]; !pt.Preview || pt.Params != "" {
		t.Fatalf("reopened tab inherited stale state: Preview=%v Params=%q", pt.Preview, pt.Params)
	}
}

func TestTier5DisabledGraphTimelineOverlayButtonsAndCanvasDeepMatrix(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	gForm := newBlockForm()
	gTree := &protocol.UiNode{
		Id: "/g", Kind: "graph", Disabled: true, Minimap: true, GraphLayout: "force",
		GraphNodes: []*protocol.UiGraphNode{{Id: "a", Label: "A", X: 50, Y: 50, HasPos: true}, {Id: "b", Label: "B", X: 200, Y: 50, HasPos: true}},
		GraphEdges: []*protocol.UiGraphEdge{{From: "a", To: "b"}},
	}
	prG := ggui.ProbeBuilder(func() ggui.Widget {
		gForm.redraw()
		v := newTreeView(nil, "e2e:probe", "", gForm, gTree)
		return v.node(gTree, false)
	}, ggui.Sz(640, 360))
	defer prG.Close()
	prG.Draw(ggfx.NewImage(640, 360))
	for _, label := range []string{T("Force layout"), T("Hierarchy layout"), T("Radial layout"), T("Zoom in"), T("Zoom out"), T("Fit view"), T("Reset"), T("Toggle minimap")} {
		btn, ok := prG.Semantics().Find(ggui.RoleButton, label)
		if !ok {
			t.Fatalf("missing graph button %q", label)
		}
		if !btn.Disabled {
			t.Fatalf("disabled graph overlay button %q has Disabled=false", label)
		}
		prG.Click(ggui.Pt(btn.Rect.Origin.X+btn.Rect.Size.W/2, btn.Rect.Origin.Y+btn.Rect.Size.H/2))
		prG.Frame()
	}
	gst := gForm.graphs["/g"]
	if gst.layoutMode != "force" || gst.zoom != 1.0 || !gst.minimapOpen {
		t.Fatalf("disabled graph overlay buttons mutated state: layout=%q zoom=%.2f minimap=%v", gst.layoutMode, gst.zoom, gst.minimapOpen)
	}

	tlForm := newBlockForm()
	tlTree := &protocol.UiNode{
		Id: "/tl", Kind: "timeline", Disabled: true,
		TimelineLanes: []*protocol.UiTimelineLane{{Id: "l1", Label: "L1", Spans: []*protocol.UiTimelineSpan{{Id: "s1", StartMs: 0, EndMs: 1000, Label: "S1"}}}},
	}
	prT := ggui.ProbeBuilder(func() ggui.Widget {
		tlForm.redraw()
		v := newTreeView(nil, "e2e:probe", "", tlForm, tlTree)
		return v.node(tlTree, false)
	}, ggui.Sz(600, 220))
	defer prT.Close()
	prT.Draw(ggfx.NewImage(600, 220))
	for _, label := range []string{T("Zoom in"), T("Zoom out"), T("Reset")} {
		btn, ok := prT.Semantics().Find(ggui.RoleButton, label)
		if !ok {
			t.Fatalf("missing timeline button %q", label)
		}
		if !btn.Disabled {
			t.Fatalf("disabled timeline overlay button %q has Disabled=false", label)
		}
		prT.Click(ggui.Pt(btn.Rect.Origin.X+btn.Rect.Size.W/2, btn.Rect.Origin.Y+btn.Rect.Size.H/2))
		prT.Frame()
	}
	if tlForm.timelines["/tl"].zoom != 1.0 {
		t.Fatalf("disabled timeline zoomed to %.2f via overlay button", tlForm.timelines["/tl"].zoom)
	}
}

func TestTier5DetachedPaneSameSpaceContextCollision(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Page.Set("workspace")
	m.Spaces.Set(spaceList{
		Spaces: []space{
			{ID: "sp-work", Name: "Work", Color: "blue", Owners: []string{"github.com/acme"}},
		},
		Projects: []project{
			{ID: "p-a", Name: "Proj A", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/a"}}}}},
			{ID: "p-b", Name: "Proj B", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/b"}}}}},
			{ID: "p-c", Name: "Proj C", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/c"}}}}},
		},
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-a", Name: "Session A", NodeId: "n1", Agent: "claude", Project: "/work/a", Owner: "github.com/acme", State: protocol.StateRunning},
		{Id: "s-b", Name: "Session B", NodeId: "n1", Agent: "codex", Project: "/work/b", Owner: "github.com/acme", State: protocol.StateRunning},
		{Id: "s-c", Name: "Session C", NodeId: "n1", Agent: "claude", Project: "/work/c", Owner: "github.com/acme", State: protocol.StateRunning},
	})
	m.Prefs.Update(func(p prefs) prefs { p.ShownSpace = "sp-work"; return p })
	m.ws.layoutKey = layoutKey("sp-work", projectContext("p-a"))
	m.selectSession("s-a")

	// Open a Space window for the same spaceID "sp-work", but switch it to project p-b.
	cand := m.openSpaceWindow("sp-work")
	if cand == nil {
		t.Fatal("openSpaceWindow returned nil")
	}
	m.spaceWindowSelectSession(cand, "s-b")
	if cand.peekLayoutKey() == m.ws.layoutKey {
		t.Fatalf("expected different layout keys for main (%q) and space window (%q)", m.ws.layoutKey, cand.peekLayoutKey())
	}

	tree := &protocol.UiNode{
		Id: "/g", Kind: "graph",
		GraphNodes: []*protocol.UiGraphNode{{Id: "sess:s-c", Label: "Target C", X: 100, Y: 100, HasPos: true}},
	}
	m.saveCachedBlockInstance("plugin:demo/graph", "", "default", blockFeed{
		Update: &protocol.BlockUpdate{
			Block: "plugin:demo/graph", Plugin: "demo", State: protocol.PluginReady, Tree: tree, Instance: "default",
		},
	}, newBlockForm())
	m.saveCachedBlockInstance("plugin:demo/graph", "", "sw-graph", blockFeed{
		Update: &protocol.BlockUpdate{
			Block: "plugin:demo/graph", Plugin: "demo", State: protocol.PluginReady, Tree: tree, Instance: "sw-graph",
		},
	}, newBlockForm())

	// 1. Pop out pane from main window (refMain.Key == m.ws.layoutKey != cand.peekLayoutKey()).
	// detachedPaneWindow must resolve sw = nil (main window) and NOT hijack cand.
	m.openPluginTab(pluginTab{
		Block: "plugin:demo/graph", PluginID: "demo", Key: "default", Title: "Demo Graph",
	})
	refMain := paneRef{Key: m.ws.layoutKey, GroupID: ggui.Peek(m.FocusGroup)}
	m.popoutPane(refMain)
	prMain := ggui.ProbeBuilder(func() ggui.Widget {
		return detachedPaneWindow(m, refMain, func() { m.dockPane(refMain) })
	}, ggui.Sz(800, 500))
	defer prMain.Close()
	img := ggfx.NewImage(800, 500)
	prMain.Draw(img)

	btn, ok := prMain.Semantics().Find(ggui.RoleButton, "Target C")
	if !ok {
		t.Fatalf("missing Target C node button in main detached pane:\n%s", prMain.Semantics().String())
	}
	prMain.ClickButton(ggui.Pt(btn.Rect.Origin.X+btn.Rect.Size.W/2, btn.Rect.Origin.Y+btn.Rect.Size.H/2), ggui.MouseButtonRight)
	prMain.Draw(img)
	openItem, ok := prMain.Semantics().Find(ggui.RoleMenuItem, T("Open session"))
	if !ok {
		t.Fatalf("missing Open session item in main detached pane:\n%s", prMain.Semantics().String())
	}
	prMain.Click(ggui.Pt(openItem.Rect.Origin.X+openItem.Rect.Size.W/2, openItem.Rect.Origin.Y+openItem.Rect.Size.H/2))
	prMain.Frame()
	if got := ggui.Peek(m.Selected); got != "s-c" {
		t.Fatalf("main detached pane failed to select s-c in main window (got %q)", got)
	}
	if got := m.spaceWindowActiveTab(cand); got != "s-b" {
		t.Fatalf("main detached pane hijacked Space window cand active tab=%q, want s-b", got)
	}
	m.dockPane(refMain)
	prMain.Close()

	// 2. Pop out pane from Space window cand (refSW.Key == cand.peekLayoutKey()).
	// detachedPaneWindow must route Open session to cand without mutating main window's Selected.
	m.selectSession("s-a")
	m.openPluginTabIn(cand, pluginTab{
		Block: "plugin:demo/graph", PluginID: "demo", Key: "sw-graph", Title: "SW Graph",
	})
	refSW := paneRef{Key: cand.peekLayoutKey(), GroupID: ggui.Peek(cand.focusGroup)}
	m.popoutPane(refSW)
	prSW := ggui.ProbeBuilder(func() ggui.Widget {
		return detachedPaneWindow(m, refSW, func() { m.dockPane(refSW) })
	}, ggui.Sz(800, 500))
	defer prSW.Close()
	prSW.Draw(img)

	btnSW, ok := prSW.Semantics().Find(ggui.RoleButton, "Target C")
	if !ok {
		t.Fatalf("missing Target C node button in sw detached pane:\n%s", prSW.Semantics().String())
	}
	prSW.ClickButton(ggui.Pt(btnSW.Rect.Origin.X+btnSW.Rect.Size.W/2, btnSW.Rect.Origin.Y+btnSW.Rect.Size.H/2), ggui.MouseButtonRight)
	prSW.Draw(img)
	openItemSW, ok := prSW.Semantics().Find(ggui.RoleMenuItem, T("Open session"))
	if !ok {
		t.Fatalf("missing Open session item in sw detached pane:\n%s", prSW.Semantics().String())
	}
	prSW.Click(ggui.Pt(openItemSW.Rect.Origin.X+openItemSW.Rect.Size.W/2, openItemSW.Rect.Origin.Y+openItemSW.Rect.Size.H/2))
	prSW.Frame()
	if got := ggui.Peek(m.Selected); got != "s-a" {
		t.Fatalf("sw detached pane mutated main window Selected=%q, want s-a", got)
	}
	if got := m.spaceWindowActiveTab(cand); got != "s-c" {
		t.Fatalf("sw detached pane active tab=%q, want s-c", got)
	}
	m.dockPane(refSW)
}

func TestTier5DetachedPaneTabBarIsolationMainNavigationAndGraphPopupRebuild(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Page.Set("workspace")
	m.Spaces.Set(spaceList{
		Spaces: []space{
			{ID: "sp-work", Name: "Work", Color: "blue"},
		},
		Projects: []project{
			{ID: "p-a", Name: "Proj A", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/a"}}}}},
			{ID: "p-b", Name: "Proj B", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/b"}}}}},
			{ID: "p-c", Name: "Proj C", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/c"}}}}},
		},
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-a1", Name: "Sess A1", NodeId: "n1", Agent: "claude", Project: "/work/a", State: protocol.StateRunning},
		{Id: "s-a2", Name: "Sess A2", NodeId: "n1", Agent: "codex", Project: "/work/a", State: protocol.StateRunning},
		{Id: "s-b", Name: "Sess B", NodeId: "n1", Agent: "codex", Project: "/work/b", State: protocol.StateRunning},
		{Id: "s-c", Name: "Sess C", NodeId: "n1", Agent: "codex", Project: "/work/c", State: protocol.StateRunning},
	})
	m.Prefs.Update(func(p prefs) prefs { p.ShownSpace = "sp-work"; p.Language = "en"; return p })
	m.showProjectTabs("p-a")
	m.selectSession("s-a1")
	m.activateTabInKey(m.ws.layoutKey, 1, "s-a2")
	m.openPluginTab(pluginTab{Block: "plugin:demo/graph", PluginID: "demo", Key: "main-det", Title: "Main Graph"})
	tree := &protocol.UiNode{
		Id: "/g", Kind: "graph",
		GraphNodes: []*protocol.UiGraphNode{{Id: "sess:s-c", Label: "Target C", X: 100, Y: 100, HasPos: true}},
	}
	m.saveCachedBlockInstance("plugin:demo/graph", "", "main-det", blockFeed{
		Update: &protocol.BlockUpdate{Block: "plugin:demo/graph", Plugin: "demo", State: protocol.PluginReady, Tree: tree, Instance: "main-det"},
	}, newBlockForm())

	// Open Space window on same space "sp-work", project "p-b".
	m.ws.contexts["sp-work"] = projectContext("p-b")
	cand := m.openSpaceWindow("sp-work")
	if cand == nil {
		t.Fatal("openSpaceWindow returned nil")
	}
	m.spaceWindowSelectSession(cand, "s-b")

	// 1. Clicking a tab in a main-window detached pane must NOT mutate cand.scope.
	refMain := paneRef{Key: m.ws.layoutKey, GroupID: ggui.Peek(m.FocusGroup)}
	m.popoutPane(refMain)
	pr := ggui.ProbeBuilder(func() ggui.Widget {
		return detachedPaneRoot(m, refMain, func() { m.dockPane(refMain) })
	}, ggui.Sz(800, 500))
	defer pr.Close()
	img := ggfx.NewImage(800, 500)
	pr.Draw(img)

	tabBtn, ok := pr.Semantics().Find(ggui.RoleTab, "Sess A2")
	if !ok {
		tabBtn, ok = pr.Semantics().Find(ggui.RoleButton, "Sess A2")
	}
	if !ok {
		t.Fatalf("missing Sess A2 tab in detached pane:\n%s", pr.Semantics().String())
	}
	pr.Click(ggui.Pt(tabBtn.Rect.Origin.X+tabBtn.Rect.Size.W/2, tabBtn.Rect.Origin.Y+tabBtn.Rect.Size.H/2))
	pr.Frame()
	if got := ggui.Peek(cand.scope); got.Section != "p-b" {
		t.Fatalf("clicking tab in main detached pane mutated cand.scope=%+v, want Section=p-b", got)
	}

	// Switch back to Main Graph tab in refMain.
	graphTab, ok := pr.Semantics().Find(ggui.RoleTab, "Main Graph")
	if !ok {
		graphTab, ok = pr.Semantics().Find(ggui.RoleButton, "Main Graph")
	}
	if !ok {
		t.Fatalf("missing Main Graph tab:\n%s", pr.Semantics().String())
	}
	pr.Click(ggui.Pt(graphTab.Rect.Origin.X+graphTab.Rect.Size.W/2, graphTab.Rect.Origin.Y+graphTab.Rect.Size.H/2))
	pr.Draw(img)

	// 2. Main window navigates to p-c and language switches (triggering ggui.Key rebuild on detachedPaneRoot).
	// Graph context popup must still open, and Open session must route to main (NOT cand).
	m.showProjectTabs("p-c")
	m.Prefs.Update(func(p prefs) prefs { p.Language = "ko"; return p })
	pr.Draw(img)

	btn, ok := pr.Semantics().Find(ggui.RoleButton, "Target C")
	if !ok {
		t.Fatalf("missing Target C node after rebuild:\n%s", pr.Semantics().String())
	}
	pr.ClickButton(ggui.Pt(btn.Rect.Origin.X+btn.Rect.Size.W/2, btn.Rect.Origin.Y+btn.Rect.Size.H/2), ggui.MouseButtonRight)
	pr.Draw(img)
	openItem, ok := pr.Semantics().Find(ggui.RoleMenuItem, "")
	if !ok {
		t.Fatalf("graph context popup failed to open after ggui.Key rebuild:\n%s", pr.Semantics().String())
	}
	pr.Click(ggui.Pt(openItem.Rect.Origin.X+openItem.Rect.Size.W/2, openItem.Rect.Origin.Y+openItem.Rect.Size.H/2))
	pr.Frame()
	if got := m.spaceWindowActiveTab(cand); got != "s-b" {
		t.Fatalf("main detached pane after main navigated to p-c hijacked cand active tab=%q, want s-b", got)
	}
	if got := ggui.Peek(m.Selected); got != "s-c" {
		t.Fatalf("main detached pane failed to select s-c in main window (got %q)", got)
	}
	m.dockPane(refMain)
}

func TestTier5DualWindowGraphPopupIsolationAndSpaceWindowCloseDocksDetachedPanes(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Prefs.Update(func(p prefs) prefs { p.Language = "en"; return p })
	m.Page.Set("workspace")
	m.Spaces.Set(spaceList{
		Spaces: []space{{ID: "sp-work", Name: "Work", Color: "blue"}},
		Projects: []project{
			{ID: "p-a", Name: "Proj A", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/a"}}}}},
			{ID: "p-b", Name: "Proj B", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/b"}}}}},
			{ID: "p-c", Name: "Proj C", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/work/c"}}}}},
		},
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-a", Name: "Sess A", NodeId: "n1", Agent: "claude", Project: "/work/a", State: protocol.StateRunning},
		{Id: "s-b", Name: "Sess B", NodeId: "n1", Agent: "codex", Project: "/work/b", State: protocol.StateRunning},
		{Id: "s-c", Name: "Sess C", NodeId: "n1", Agent: "codex", Project: "/work/c", State: protocol.StateRunning},
	})
	m.plugins.List.Set([]*protocol.PluginInfo{
		{
			Id:    "demo",
			State: protocol.PluginReady,
			Blocks: []*protocol.PluginBlock{
				{Name: "plugin:demo/topo", Title: "Topo", Slots: []string{"sidebar"}},
			},
		},
	})
	topoTree := &protocol.UiNode{
		Id: "/g", Kind: "graph", Fill: true,
		GraphNodes: []*protocol.UiGraphNode{{Id: "sess:s-a", Label: "Node A", X: 100, Y: 100, HasPos: true}},
	}
	m.saveCachedBlock("plugin:demo/topo", "", blockFeed{
		Update: &protocol.BlockUpdate{Block: "plugin:demo/topo", Plugin: "demo", State: protocol.PluginReady, Tree: topoTree},
	}, newBlockForm())

	m.ws.contexts["sp-work"] = projectContext("p-b")
	sw1 := m.openSpaceWindow("sp-work")
	if sw1 == nil {
		t.Fatal("openSpaceWindow returned nil")
	}

	// 1. Simultaneous main + Space window mount of the same cached graph block must isolate ctxPopup.
	m.SidebarPage.Set("plugin:demo/topo")
	sw1.sidebarPage.Set("plugin:demo/topo")
	prMain := ggui.ProbeBuilder(func() ggui.Widget { return workspaceCenterView(m) }, ggui.Sz(800, 500))
	defer prMain.Close()
	prSpace := ggui.ProbeBuilder(func() ggui.Widget { return spaceWindowRoot(m, sw1) }, ggui.Sz(900, 560))
	defer prSpace.Close()

	imgMain := ggfx.NewImage(800, 500)
	imgSpace := ggfx.NewImage(900, 560)
	prMain.Draw(imgMain)
	prSpace.Draw(imgSpace)

	nodeA, ok := prMain.Semantics().Find(ggui.RoleButton, "Node A")
	if !ok {
		t.Fatalf("missing Node A in main window:\n%s", prMain.Semantics().String())
	}
	prMain.ClickButton(ggui.Pt(nodeA.Rect.Origin.X+nodeA.Rect.Size.W/2, nodeA.Rect.Origin.Y+nodeA.Rect.Size.H/2), ggui.MouseButtonRight)
	prMain.Draw(imgMain)
	prSpace.Draw(imgSpace)

	if _, ok := prMain.Semantics().Find(ggui.RoleMenuItem, ""); !ok {
		t.Fatalf("expected context menu in main window:\n%s", prMain.Semantics().String())
	}
	if leaked, ok := prSpace.Semantics().Find(ggui.RoleMenuItem, ""); ok {
		t.Fatalf("right-clicking graph node in main window leaked context menu item %q to Space window:\n%s", leaked.Name, prSpace.Semantics().String())
	}

	// Also verify opening Space window's own context popup and closing one via Escape does not close the other.
	nodeASpace, ok := prSpace.Semantics().Find(ggui.RoleButton, "Node A")
	if !ok {
		t.Fatalf("missing Node A in Space window:\n%s", prSpace.Semantics().String())
	}
	prSpace.ClickButton(ggui.Pt(nodeASpace.Rect.Origin.X+nodeASpace.Rect.Size.W/2, nodeASpace.Rect.Origin.Y+nodeASpace.Rect.Size.H/2), ggui.MouseButtonRight)
	prSpace.Draw(imgSpace)
	if _, ok := prSpace.Semantics().Find(ggui.RoleMenuItem, ""); !ok {
		t.Fatalf("expected context menu in Space window:\n%s", prSpace.Semantics().String())
	}
	if !m.closeOpenGraphCtxPopupIn(sw1) {
		t.Fatal("closeOpenGraphCtxPopupIn(sw1) returned false")
	}
	prMain.Draw(imgMain)
	prSpace.Draw(imgSpace)
	if _, ok := prSpace.Semantics().Find(ggui.RoleMenuItem, ""); ok {
		t.Fatalf("Space window context menu remained open after closeOpenGraphCtxPopupIn(sw1):\n%s", prSpace.Semantics().String())
	}
	if _, ok := prMain.Semantics().Find(ggui.RoleMenuItem, ""); !ok {
		t.Fatalf("closing Space window context popup also closed main window popup:\n%s", prMain.Semantics().String())
	}
	m.handleEscape(prMain)
	prMain.Draw(imgMain)
	if _, ok := prMain.Semantics().Find(ggui.RoleMenuItem, ""); ok {
		t.Fatalf("main handleEscape did not close main window context popup:\n%s", prMain.Semantics().String())
	}

	// 2. Closing sw1 must dock its detached panes and clear paneOwners so reopened sw2 is not bypassed.
	sw1.sidebarPage.Set("")
	m.spaceWindowSelectSession(sw1, "s-b")
	m.openPluginTabIn(sw1, pluginTab{Block: "plugin:demo/graph", PluginID: "demo", Key: "sw-det", Title: "SW Graph"})
	detTree := &protocol.UiNode{
		Id: "/g", Kind: "graph",
		GraphNodes: []*protocol.UiGraphNode{{Id: "sess:s-c", Label: "Target C", X: 100, Y: 100, HasPos: true}},
	}
	m.saveCachedBlockInstance("plugin:demo/graph", "", "sw-det", blockFeed{
		Update: &protocol.BlockUpdate{Block: "plugin:demo/graph", Plugin: "demo", State: protocol.PluginReady, Tree: detTree, Instance: "sw-det"},
	}, newBlockForm())

	refSw := paneRef{Key: sw1.peekLayoutKey(), GroupID: ggui.Peek(sw1.focusGroup)}
	m.popoutPaneIn(sw1, refSw)
	m.closeSpaceWindow("sp-work")

	if m.isPaneDetached(refSw) {
		t.Fatalf("detached pane %+v remained detached after closeSpaceWindow", refSw)
	}
	if _, stale := m.paneOwners[refSw]; stale {
		t.Fatalf("paneOwners retained stale entry for %+v after closeSpaceWindow", refSw)
	}

	sw2 := m.openSpaceWindow("sp-work")
	if sw2 == nil || sw2 == sw1 {
		t.Fatalf("expected reopened sw2, got %v", sw2)
	}
	if got := m.spaceWindowActiveTab(sw2); got == "" {
		t.Fatal("reopened sw2 has empty active tab")
	}
}
