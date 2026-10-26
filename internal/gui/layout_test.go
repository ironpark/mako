package gui

import (
	"slices"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/config"
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
		d = newDrawer(ggui.Box().Fill(tokSurface1), open, width, false)
		return ggui.Row(d, ggui.Expanded(ggui.Box())).Stretch()
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
