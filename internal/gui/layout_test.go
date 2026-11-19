package gui

import (
	"slices"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

func paneTabs(m *model) [][]string {
	var out [][]string
	for _, g := range ggui.Peek(m.Layout).groups() {
		out = append(out, g.Tabs)
	}
	return out
}

func samePanes(got [][]string, want ...[]string) bool {
	return slices.EqualFunc(got, want, func(a, b []string) bool { return slices.Equal(a, b) })
}

func TestTabsSplitMoveAndCloseTheirPanes(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	for _, id := range []string{"a", "b", "c"} {
		m.showTab(id)
	}
	if got := paneTabs(m); !samePanes(got, []string{"a", "b", "c"}) || ggui.Peek(m.Selected) != "c" {
		t.Fatalf("panes %q, selected %q", got, ggui.Peek(m.Selected))
	}

	first := ggui.Peek(m.FocusGroup)
	m.splitTab("c", first, dropRight)
	if got := paneTabs(m); !samePanes(got, []string{"a", "b"}, []string{"c"}) {
		t.Fatalf("after splitting c right: %q", got)
	}
	if ggui.Peek(m.Selected) != "c" || ggui.Peek(m.FocusGroup) == first {
		t.Fatal("the new pane is not the current one")
	}
	if root := ggui.Peek(m.Layout); root.Vertical || root.First.Group.ID != first {
		t.Fatalf("c's pane is not beside the first: %s", layoutShape(root))
	}

	m.splitTab("a", first, dropTop)
	if got := paneTabs(m); !samePanes(got, []string{"a"}, []string{"b"}, []string{"c"}) {
		t.Fatalf("after splitting a above: %q", got)
	}

	// Moving b out empties its pane, which closes.
	right := ggui.Peek(m.Layout).groupOf("c").ID
	m.moveTab("b", right, 0)
	if got := paneTabs(m); !samePanes(got, []string{"a"}, []string{"b", "c"}) {
		t.Fatalf("after moving b before c: %q", got)
	}
	m.moveTab("b", right, 2) // after c, counted before the move
	if got := paneTabs(m); !samePanes(got, []string{"a"}, []string{"c", "b"}) {
		t.Fatalf("after moving b after c: %q", got)
	}

	// A pane's only tab cannot be split from itself.
	only := ggui.Peek(m.Layout).groupOf("a").ID
	m.splitTab("a", only, dropLeft)
	if got := paneTabs(m); !samePanes(got, []string{"a"}, []string{"c", "b"}) {
		t.Fatalf("splitting a pane's only tab changed the panes: %q", got)
	}

	m.closeTab("a")
	if got := paneTabs(m); !samePanes(got, []string{"c", "b"}) || ggui.Peek(m.Layout).Group == nil {
		t.Fatalf("after closing a: %q", got)
	}
	if !slices.Equal(ggui.Peek(m.Tabs), []string{"c", "b"}) {
		t.Fatalf("tabs %q", ggui.Peek(m.Tabs))
	}
	m.closeTab("c")
	m.closeTab("b")
	if got := paneTabs(m); !samePanes(got, nil) && !samePanes(got, []string{}) || ggui.Peek(m.Selected) != "" {
		t.Fatalf("after closing every tab: %q, selected %q", got, ggui.Peek(m.Selected))
	}
}

// A tab dragged to the right edge of its pane splits it there, and one
// dragged onto another pane's tab bar moves there.
func TestDraggingATabArrangesThePanes(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	// No sessions are listed, so nothing dials a daemon; the tabs are
	// named by their titles.
	m.Titles.Set(map[string]string{"s1": "first", "s2": "second"})
	m.SidebarOpen.Set(false)
	m.DetailsOpen.Set(false)
	m.showTab("s1")
	m.showTab("s2")
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	defer p.Close()

	drag := func(label string, to ggui.Point) {
		t.Helper()
		f, ok := p.Find(label)
		if !ok {
			t.Fatalf("no %q", label)
		}
		from := f.Center()
		p.Press(from)
		p.Move(ggui.Pt(from.X+12, from.Y+6))
		p.Move(to)
		p.Move(to)
		p.Release(to)
		p.Frame()
		if ggui.Peek(m.Dragging) {
			t.Fatal("the drag did not end")
		}
	}

	drag("second", ggui.Pt(990, 400))
	if got := paneTabs(m); !samePanes(got, []string{"s1"}, []string{"s2"}) {
		t.Fatalf("after dragging second to the right edge: %q", got)
	}
	if ggui.Peek(m.Selected) != "s2" {
		t.Fatalf("selected %q", ggui.Peek(m.Selected))
	}

	// Dropped on the left pane's tab bar, past its tabs, it goes back.
	first, _ := p.Find("first")
	drag("second", ggui.Pt(first.Rect.Origin.X+first.Rect.Size.W+60, first.Center().Y))
	if got := paneTabs(m); !samePanes(got, []string{"s1", "s2"}) {
		t.Fatalf("after dragging second back: %q", got)
	}
}

// A tab dragged over another opens the gap it would drop into, before or
// after that tab by the half it is over, and the gap closes with the drop.
func TestDraggingATabOpensItsDropGap(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Titles.Set(map[string]string{"s1": "first", "s2": "second"})
	m.SidebarOpen.Set(false)
	m.DetailsOpen.Set(false)
	m.showTab("s1")
	m.showTab("s2")
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	defer p.Close()

	second, _ := p.Find("second")
	first, _ := p.Find("first")
	from := second.Center()
	p.Press(from)
	p.Move(ggui.Pt(from.X-12, from.Y))
	over := ggui.Pt(first.Rect.Origin.X+8, first.Center().Y) // first's left half
	p.Move(over)
	p.Move(over)
	group := ggui.Peek(m.FocusGroup)
	if h := ggui.Peek(m.TabHint); h != (tabHint{Group: group, Index: 0}) {
		t.Fatalf("over first's left half the gap is %+v, want before it", h)
	}
	p.Release(over)
	p.Frame()
	if got := paneTabs(m); !samePanes(got, []string{"s2", "s1"}) {
		t.Fatalf("after the drop: %q", got)
	}
	if h := ggui.Peek(m.TabHint); h != (tabHint{}) {
		t.Fatalf("the gap %+v stayed open after the drop", h)
	}
}

// A side panel slides: part way open between the frames of its motion.
func TestSidePanelsSlide(t *testing.T) {
	open := ggui.State(false)
	width := ggui.State[float64](300)
	var d *drawer
	p := ggui.ProbeBuilder(func() ggui.Widget {
		d = newDrawer(widget.Box().Fill(skin.Surface1), open, width, false)
		return widget.Row(d, widget.Expanded(widget.Box())).Stretch()
	}, ggui.Sz(800, 400))
	defer p.Close()
	p.Frame()
	if d.w != 0 {
		t.Fatalf("closed panel is %v wide", d.w)
	}
	open.Set(true)
	partly := false
	for range 8 {
		p.Advance(drawerDuration / 4)
		partly = partly || (d.w > 0 && d.w < 300)
	}
	if !partly || d.w != 300 {
		t.Fatalf("the panel opened to %v without a frame part way open: %v", d.w, partly)
	}
	// Dragged wider, it follows.
	width.Set(420)
	p.Frame()
	if d.w != 420 {
		t.Fatalf("the panel is %v wide after a resize to 420", d.w)
	}
}

// Tidying keeps agents, shells running a command and files with unsaved
// changes, and takes the rest.
func TestTidyable(t *testing.T) {
	sessions := []*protocol.SessionInfo{
		{Id: "agent", Agent: "claude", State: protocol.StateRunning, Idle: true},
		{Id: "busy", Agent: "shell", State: protocol.StateRunning},
		{Id: "idle", Agent: "shell", State: protocol.StateRunning, Idle: true},
		{Id: "exited", Agent: "shell", State: protocol.StateExited},
		{Id: "dead-agent", Agent: "codex", State: protocol.StateExited},
	}
	clean, dirty := docTabID("n", "/a"), docTabID("n", "/b")
	docs := map[string]docInfo{clean: {}, dirty: {Dirty: true}}
	for id, want := range map[string]bool{
		"agent": false, "busy": false, "idle": true, "exited": true, "dead-agent": false, "unknown": false,
		clean: true, dirty: false, diffPrefix + "x": true,
	} {
		if got := tidyable(id, sessions, docs); got != want {
			t.Errorf("tidyable(%q) = %v, want %v", id, got, want)
		}
	}
}

// A tab's close button is there all the while, in the accessibility tree
// and the Tab order, and shows as the pointer comes over the tab, which a
// click on it then closes.
func TestTabCloseIsThereAndClosesTheTab(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Titles.Set(map[string]string{"s1": "first", "s2": "second"})
	m.SidebarOpen.Set(false)
	m.DetailsOpen.Set(false)
	m.showTab("s1")
	m.showTab("s2")
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	defer p.Close()
	if _, ok := p.Semantics().Find(ggui.RoleButton, Tf("Close %s", "first")); !ok {
		t.Fatal("the hidden close button is not in the accessibility tree")
	}
	first, _ := p.Find("first")
	p.Move(first.Center())
	x, ok := p.Find(Tf("Close %s", "first"))
	if !ok {
		t.Fatal("no close button")
	}
	p.Move(x.Center())
	p.Click(x.Center())
	if ggui.Peek(m.Confirmation).Title == "" && slices.Contains(ggui.Peek(m.Tabs), "s1") {
		t.Fatal("the close button did not close the tab, or ask to")
	}
}

// Holding Ctrl while clicking a tab's close button closes the tab without
// opening a confirmation dialog, while a normal click still asks when needed.
func TestCtrlClickTabCloseSkipsConfirmation(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Prefs.Update(func(p prefs) prefs { p.ConfirmKill = true; p.Language = "en"; return p })
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s1", NodeId: "n1", Agent: "claude", Project: "/tmp", State: protocol.StateRunning},
		{Id: "s2", NodeId: "n1", Agent: "claude", Project: "/tmp", State: protocol.StateRunning},
	})
	m.Titles.Set(map[string]string{"s1": "first", "s2": "second"})
	docID := docTabID("n1", "/tmp/dirty.go")
	m.Docs.Set(map[string]docInfo{
		docID: {Node: "n1", Path: "/tmp/dirty.go", Root: fileRoot{Node: "n1", Path: "/tmp"}, Dirty: true},
	})
	m.docs[docID] = &document{text: ggui.State("edited"), saved: "orig", preview: ggui.State(false)}
	m.SidebarOpen.Set(false)
	m.DetailsOpen.Set(false)
	m.selectSession("s1")
	m.showTab("s2")
	m.showTab(docID)

	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	defer p.Close()
	p.Frame()

	clickClose := func(tabLabel, closeLabel string, mods ggui.Mods) {
		t.Helper()
		f, ok := p.Find(tabLabel)
		if !ok {
			t.Fatalf("no tab %q:\n%s", tabLabel, p.Semantics().String())
		}
		p.Move(f.Center())
		x, ok := p.Find(closeLabel)
		if !ok {
			t.Fatalf("no close button %q", closeLabel)
		}
		p.Move(x.Center())
		p.ClickWith(x.Center(), mods)
		p.Frame()
	}

	// Normal click on a running session tab asks for confirmation.
	clickClose("first", Tf("Close %s", "first"), ggui.Mods{})
	if ggui.Peek(m.Confirmation).Title == "" {
		t.Fatal("normal click on running session tab did not open confirmation")
	}
	if !slices.Contains(ggui.Peek(m.Tabs), "s1") {
		t.Fatal("normal click closed s1 before confirmation")
	}
	m.Confirmation.Set(confirmation{})
	p.Frame()

	// Ctrl+click on the tab body (selecting it) followed by a normal click on
	// its close button must still ask for confirmation.
	first, _ := p.Find("first")
	p.ClickWith(first.Center(), ggui.Mods{Ctrl: true})
	p.Frame()
	clickClose("first", Tf("Close %s", "first"), ggui.Mods{})
	if ggui.Peek(m.Confirmation).Title == "" {
		t.Fatal("normal close click after Ctrl+click on tab body skipped confirmation")
	}
	m.Confirmation.Set(confirmation{})
	p.Frame()

	// Ctrl+click on the session tab's close button closes it immediately
	// without opening a confirmation dialog.
	clickClose("first", Tf("Close %s", "first"), ggui.Mods{Ctrl: true})
	if got := ggui.Peek(m.Confirmation).Title; got != "" {
		t.Fatalf("Ctrl+click on session close button opened confirmation %q", got)
	}
	if slices.Contains(ggui.Peek(m.Tabs), "s1") {
		t.Fatal("Ctrl+click on session close button did not close s1")
	}

	// Normal click on a dirty file tab asks for confirmation.
	clickClose("dirty.go", Tf("Close %s", "dirty.go"), ggui.Mods{})
	if ggui.Peek(m.Confirmation).Title == "" {
		t.Fatal("normal click on dirty doc tab did not open confirmation")
	}
	if !slices.Contains(ggui.Peek(m.Tabs), docID) {
		t.Fatal("normal click closed dirty doc tab before confirmation")
	}
	m.Confirmation.Set(confirmation{})
	p.Frame()

	// Ctrl+click on the dirty file tab's close button closes it immediately
	// without opening a confirmation dialog.
	clickClose("dirty.go", Tf("Close %s", "dirty.go"), ggui.Mods{Ctrl: true})
	if got := ggui.Peek(m.Confirmation).Title; got != "" {
		t.Fatalf("Ctrl+click on dirty doc close button opened confirmation %q", got)
	}
	if slices.Contains(ggui.Peek(m.Tabs), docID) {
		t.Fatal("Ctrl+click on dirty doc close button did not close dirty.go")
	}
}

func TestSplitAndMoveTabsPreserveActiveAndGuardSelfSplit(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "a1", NodeId: "n1", Agent: "claude", Project: "/w/api"},
		{Id: "a2", NodeId: "n1", Agent: "claude", Project: "/w/api"},
		{Id: "w1", NodeId: "n1", Agent: "codex", Project: "/w/web"},
		{Id: "w2", NodeId: "n1", Agent: "codex", Project: "/w/web"},
	})
	for _, id := range []string{"a1", "a2", "w1", "w2"} {
		m.showTab(id)
	}
	// Make a2 active in the focused pane before splitting off api tabs.
	m.showTab("a2")
	pane1 := ggui.Peek(m.FocusGroup)
	m.splitTabs([]string{"a1", "a2"}, pane1, dropRight)
	if got := paneTabs(m); !samePanes(got, []string{"w1", "w2"}, []string{"a1", "a2"}) {
		t.Fatalf("after splitTabs = %q", got)
	}
	if ggui.Peek(m.Selected) != "a2" {
		t.Fatalf("expected active moved tab a2 selected, got %q", ggui.Peek(m.Selected))
	}

	// Self-split guard: splitting web's tabs from a pane that only has web's tabs is a no-op.
	webPane := ggui.Peek(m.Layout).groupOf("w1").ID
	m.splitTabs([]string{"w1", "w2"}, webPane, dropBottom)
	if got := paneTabs(m); !samePanes(got, []string{"w1", "w2"}, []string{"a1", "a2"}) {
		t.Fatalf("self-split guard failed = %q", got)
	}

	// Moving a1, a2 back into webPane closes the second pane.
	m.moveTabs([]string{"a1", "a2"}, webPane, 2)
	if got := paneTabs(m); !samePanes(got, []string{"w1", "w2", "a1", "a2"}) {
		t.Fatalf("after moveTabs = %q", got)
	}
}

func TestDraggingProjectChipMovesSubprojectTabsAndPreservesClickToFold(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Spaces.Set(spaceList{
		Projects: []project{{
			ID: "mono", Name: "mono",
			Roots: []projectRoot{
				{Locations: []fileRoot{{Node: "n1", Path: "/w/mono/api"}}},
				{Locations: []fileRoot{{Node: "n1", Path: "/w/mono/web"}}},
			},
		}},
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "a1", NodeId: "n1", Agent: "claude", Project: "/w/mono/api", State: protocol.StateRunning},
		{Id: "a2", NodeId: "n1", Agent: "claude", Project: "/w/mono/api", State: protocol.StateRunning},
		{Id: "w1", NodeId: "n1", Agent: "codex", Project: "/w/mono/web", State: protocol.StateRunning},
	})
	m.Titles.Set(map[string]string{"a1": "api-1", "a2": "api-2", "w1": "web-1"})
	m.SidebarOpen.Set(false)
	m.DetailsOpen.Set(false)
	for _, id := range []string{"a1", "a2", "w1"} {
		m.showTab(id)
	}
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	defer p.Close()
	p.Frame()

	// Clicking the sub-project chip still folds/unfolds it.
	foldBtn, ok := p.Find(Tf("Fold %s", "api"))
	if !ok {
		t.Fatalf("missing Fold api chip:\n%s", p.Semantics().String())
	}
	p.Click(foldBtn.Center())
	p.Frame()
	if _, ok := p.Find(Tf("Unfold %s", "api")); !ok {
		t.Fatalf("clicking chip did not fold api:\n%s", p.Semantics().String())
	}
	unfoldBtn, _ := p.Find(Tf("Unfold %s", "api"))
	p.Click(unfoldBtn.Center())
	p.Frame()

	// Dragging the api sub-project chip to the right edge moves BOTH a1 and a2 at once.
	chip, ok := p.Find(Tf("Fold %s", "api"))
	if !ok {
		t.Fatalf("missing Fold api chip after unfold")
	}
	from := chip.Center()
	to := ggui.Pt(990, 400)
	p.Press(from)
	p.Move(ggui.Pt(from.X+12, from.Y+8))
	p.Move(to)
	p.Move(to)
	p.Release(to)
	p.Frame()
	if got := paneTabs(m); !samePanes(got, []string{"w1"}, []string{"a1", "a2"}) {
		t.Fatalf("after dragging api chip to right edge: %q", got)
	}
}
