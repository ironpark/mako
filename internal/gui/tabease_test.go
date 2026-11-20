package gui

import (
	"slices"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
)

// A tab closed shrinks out of its bar, the tabs after it easing over, and
// one opened grows in, pushing them along.
func TestTabsEaseInAndOutOfTheirBar(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Titles.Set(map[string]string{"s1": "first", "s2": "second", "s3": "third", "s4": "fourth"})
	m.SidebarOpen.Set(false)
	m.DetailsOpen.Set(false)
	for _, id := range []string{"s1", "s2", "s3"} {
		m.showTab(id)
	}
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	defer p.Close()
	x := func(name string) float64 {
		t.Helper()
		r, ok := p.Find(name)
		if !ok {
			t.Fatalf("no %q", name)
		}
		return r.Rect.Origin.X
	}
	second, third := x("second"), x("third")

	m.closeTab("s2")
	p.Frame()
	p.Advance(tabEaseDuration / 2)
	if mid := x("third"); mid >= third || mid <= second {
		t.Fatalf("halfway through closing the tab before it, third is at %v; want between %v and %v", mid, second, third)
	}
	p.Advance(tabEaseDuration)
	if end := x("third"); end != second {
		t.Fatalf("once closed, third is at %v; want %v, where second was", end, second)
	}

	// The + follows the last tab.
	plus := x(T("New session…"))
	m.showTab("s4")
	p.Frame()
	if at := x(T("New session…")); at > plus+20 {
		t.Fatalf("a tab opened pushed the + from %v to %v at once", plus, at)
	}
	p.Advance(tabEaseDuration * 2)
	if at := x(T("New session…")); at < plus+60 {
		t.Fatalf("once the opened tab has grown, the + is at %v from %v", at, plus)
	}
}

func TestTabEasePlansWhereTabsLeaveFrom(t *testing.T) {
	ids := func(slots []tabSlot) (out []string) {
		for _, s := range slots {
			id := s.ID
			if s.Gone {
				id += "-"
			} else if s.Hidden {
				id += "_"
			}
			out = append(out, id)
		}
		return out
	}
	projects := map[string]tabProject{"a": {Key: "p"}, "b": {Key: "p"}, "c": {Key: "q"}, "d": {Key: "q"}, "e": {Key: "r"}}
	e := &tabEase{tabs: map[string]*easedTab{}}
	built := func(slots []tabSlot) {
		for _, s := range slots {
			if !s.Hidden {
				e.cell(s.ID, widgetNone{}, widgetNone{})
				e.tabs[s.ID].share.MoveTo(1, ggui.FrameTime(), time.Second) // under way
			}
		}
	}
	built(e.plan([]string{"a", "b", "c", "d", "e"}, nil, nil, projects, true))

	// b closed stays where it was; e, alone in its run, goes with it.
	got := ids(e.plan([]string{"a", "c", "d"}, nil, nil, projects, true))
	if want := []string{"a", "b-", "c", "d"}; !slices.Equal(got, want) {
		t.Fatalf("after closing b and e: %q, want %q", got, want)
	}
	// q folded away, the tab shown elsewhere, keeps its run at no width.
	got = ids(e.plan([]string{"a", "c", "d"}, map[string]bool{"c": true, "d": true}, nil, projects, true))
	if want := []string{"a", "b-", "c_", "d_"}; !slices.Equal(got, want) {
		t.Fatalf("with q folded: %q, want %q", got, want)
	}
	// Lifted tabs (being dragged) are omitted immediately with no leaving slot.
	got = ids(e.plan([]string{"a", "c", "d"}, nil, map[string]bool{"c": true, "d": true}, projects, true))
	if want := []string{"a", "b-"}; !slices.Equal(got, want) {
		t.Fatalf("with q lifted: %q, want %q", got, want)
	}
}

// A bar scrolled along stays there as its tabs change: a tab closed
// rebuilds it with fewer widgets before the scroll.
func TestTabBarKeepsItsScrollAsItsTabsChange(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	titles := map[string]string{}
	var ids []string
	for i := range 12 {
		id := string(rune('a' + i))
		ids = append(ids, id)
		titles[id] = "tab " + id
	}
	m.Titles.Set(titles)
	var posted []func()
	m.post = func(f func()) { posted = append(posted, f) } // the hidden tabs' count
	m.SidebarOpen.Set(false)
	m.DetailsOpen.Set(false)
	for _, id := range ids {
		m.showTab(id)
	}
	m.showTab("a") // the tab shown, at the start
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(700, 500))
	defer p.Close()
	x := func(name string) float64 {
		t.Helper()
		r, ok := p.Find(name)
		if !ok {
			t.Fatalf("no %q", name)
		}
		return r.Rect.Origin.X
	}
	before := x("tab c")
	c, _ := p.Find("tab c")
	p.Scroll(c.Center(), ggui.Pt(-3, 0))
	p.Advance(time.Second)
	scrolled := x("tab c")
	if scrolled >= before-50 {
		t.Fatalf("scrolling the bar moved tab c from %v to %v", before, scrolled)
	}
	m.closeTab("l")
	p.Advance(time.Second)
	if got := x("tab c"); got != scrolled {
		t.Fatalf("closing a tab out of sight moved tab c from %v to %v", scrolled, got)
	}
}
