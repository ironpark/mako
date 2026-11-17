package gui

import (
	"slices"
	"time"

	"github.com/ironpark/ggui"
)

// tabEaseDuration is how long a tab takes to grow into its bar or shrink
// out of it.
const tabEaseDuration = 180 * time.Millisecond

// tabEase eases a pane's tabs in and out of its bar: a tab opened, moved
// in or unfolded grows from nothing to its width, and one closed, moved
// out or folded away shrinks to nothing, painted as it last was, before it
// goes. The bar is built anew on each change, so its tabs' widths live
// here, with the pane.
type tabEase struct {
	tabs  map[string]*easedTab
	order []string // the tabs as last built, those leaving included
	built bool     // the bar was built once: its first tabs are there already
}

// easedTab is one tab's share of its width.
type easedTab struct {
	share   ggui.Motion // of its width shown, from 0 to 1
	face    ggui.Widget // as last built, painted while it leaves
	project tabProject  // the run it was in
	leaving bool
}

// tabEaseOf is a pane's tab easing, made the first time it is asked for.
func (m *model) tabEaseOf(pane int) *tabEase {
	e := m.tabEases[pane]
	if e == nil {
		e = &tabEase{tabs: map[string]*easedTab{}}
		m.tabEases[pane] = e
	}
	return e
}

// tabSlot is a place in a bar as plan lays it out: a tab shown, one
// folded away, or one gone from the bar, which is painted as it leaves but
// takes nothing.
type tabSlot struct {
	ID     string
	Hidden bool // folded away, or gone: shrinking to nothing
	Gone   bool // no longer in the pane
}

// plan orders a pane's tabs, those of hidden folded away, with those gone
// from it that have yet to shrink away: each where it was, after the tab
// it followed. Of tabs grouped under their projects, one gone stays only
// beside a tab of its project, so its run, whose label it would otherwise
// keep, goes with it. A tab folded away keeps its place, at no width, for
// its run's label.
func (e *tabEase) plan(tabs []string, hidden map[string]bool, projects map[string]tabProject, grouped bool) []tabSlot {
	now := ggui.FrameTime()
	in := make(map[string]bool, len(tabs))
	slots := make([]tabSlot, 0, len(tabs))
	for _, id := range tabs {
		in[id] = true
		t := e.tabs[id]
		if t == nil {
			t = &easedTab{}
			// A tab of the bar's first build is there already; any other
			// grows in from nothing.
			t.share.Easing(ggui.EaseInOut).MoveTo(pick(!e.built && !hidden[id], 1.0, 0.0), now, 0)
			e.tabs[id] = t
		}
		t.leaving, t.project = hidden[id], projects[id]
		slots = append(slots, tabSlot{ID: id, Hidden: hidden[id]})
	}
	for i, id := range e.order {
		t := e.tabs[id]
		if in[id] || t == nil {
			continue
		}
		if v, moving := t.share.At(now); t.face == nil || v <= 0 && !moving {
			delete(e.tabs, id)
			continue
		}
		t.leaving = true
		// After the nearest tab before it that is still there.
		at := 0
		for k := i - 1; k >= 0; k-- {
			if j := slices.IndexFunc(slots, func(s tabSlot) bool { return s.ID == e.order[k] }); j >= 0 {
				at = j + 1
				break
			}
		}
		slots = slices.Insert(slots, at, tabSlot{ID: id, Hidden: true, Gone: true})
	}
	if grouped {
		var gone []string
		for i, s := range slots {
			if !s.Gone {
				continue
			}
			key := e.tabs[s.ID].project.Key
			beside := func(step int) bool {
				for j := i + step; j >= 0 && j < len(slots); j += step {
					if !slots[j].Gone {
						return projects[slots[j].ID].Key == key
					}
				}
				return false
			}
			if !beside(-1) && !beside(1) {
				gone = append(gone, s.ID)
			}
		}
		slots = slices.DeleteFunc(slots, func(s tabSlot) bool { return slices.Contains(gone, s.ID) })
		for _, id := range gone {
			delete(e.tabs, id)
		}
	}
	e.order = e.order[:0]
	for _, s := range slots {
		e.order = append(e.order, s.ID)
	}
	e.built = true
	return slots
}

// project is the run a tab gone from the bar was in.
func (e *tabEase) project(id string) tabProject { return e.tabs[id].project }

// cell is a tab's place in the bar: w, at the share of its width the tab
// has grown to. face is how it looks, which a tab hidden keeps; w is the
// face with what it takes, a drag and a drop, or nil for a tab hidden,
// which shows the face it had, if it was ever shown.
func (e *tabEase) cell(id string, face, w ggui.Widget) ggui.Widget {
	t := e.tabs[id]
	if w == nil {
		if t.face == nil {
			return ggui.Widget(widgetNone{})
		}
		return &easedCell{t: t, child: t.face}
	}
	t.face = face
	return &easedCell{t: t, child: w}
}

// settled reports whether a tab has grown to its width, so that revealing
// it scrolls to all of it.
func (e *tabEase) settled(id string) bool {
	t := e.tabs[id]
	if t == nil {
		return true
	}
	v, moving := t.share.At(ggui.FrameTime())
	return v >= 1 && !moving
}

// easedCell lays a tab out at the share of its width it has grown to,
// painting it whole, cut at that width and faded by what it lacks. One
// leaving takes no input.
type easedCell struct {
	t     *easedTab
	child ggui.Widget
	size  ggui.Size // the tab's own
	share float64
}

func (c *easedCell) Layout(cs ggui.Constraints, env ggui.Env) ggui.Size {
	c.size = c.child.Layout(cs.Loosen(), env)
	c.share = c.t.share.LayoutTo(env, pick(c.t.leaving, 0.0, 1.0), tabEaseDuration)
	return ggui.Sz(c.size.W*c.share, c.size.H)
}

func (c *easedCell) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if c.t.leaving {
		if c.share <= 0 {
			return
		}
		dst = dst.Inert()
	}
	at := ggui.Rct(r.Origin, c.size)
	if c.share >= 1 {
		dst.Paint(c.child, at)
		return
	}
	dst.Clip(r).Layer(ggui.LayerOptions{Fade: 1 - c.share}, func(dst *ggui.Canvas) { dst.Paint(c.child, at) })
}

// widgetNone takes no room and paints nothing: a tab folded away before it
// was ever shown.
type widgetNone struct{}

func (widgetNone) Layout(ggui.Constraints, ggui.Env) ggui.Size { return ggui.Size{} }
func (widgetNone) Paint(*ggui.Canvas, ggui.Rect)               {}
