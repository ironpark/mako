package gui

import (
	"fmt"
	"image/color"
	"reflect"
	"slices"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/protocol"
)

// layoutView draws the panes. The tree is rebuilt only when its shape
// changes, a split made or a pane closed; tabs moving and dividers
// dragging update the panes in place, so terminals keep their state.
func layoutView(m *model) ggui.Widget {
	shape := ggui.Map(m.Layout, layoutShape)
	return ggui.Key(shape, func(string) ggui.Widget { return layoutNodeView(m, ggui.Peek(m.Layout)) })
}

// layoutShape writes the tree's splits and panes, without what changes
// inside them.
func layoutShape(n *layoutNode) string {
	if n.Group != nil {
		return fmt.Sprintf("g%d", n.Group.ID)
	}
	dir := "h"
	if n.Vertical {
		dir = "v"
	}
	return fmt.Sprintf("s%d%s(%s,%s)", n.ID, dir, layoutShape(n.First), layoutShape(n.Second))
}

func layoutNodeView(m *model, n *layoutNode) ggui.Widget {
	if n.Group != nil {
		return paneView(m, n.Group.ID)
	}
	id := n.ID
	ratio := ggui.Bind(func() float64 {
		if n := m.Layout.Get().node(id); n != nil {
			return n.Ratio
		}
		return 0.5
	}, func(r float64) { m.setRatio(id, r) })
	split := ui.Resizable(ratio, layoutNodeView(m, n.First), layoutNodeView(m, n.Second)).
		MinSizes(160, 120).Name(T("Pane divider"))
	if n.Vertical {
		split = split.Vertical()
	}
	return split
}

// paneGroup follows one pane of the layout, changing only when the pane
// does.
func paneGroup(m *model, id int) ggui.Readable[tabGroup] {
	return ggui.Derived(func() tabGroup {
		if g := m.Layout.Get().group(id); g != nil {
			return *g
		}
		return tabGroup{ID: id}
	}).WithEqual(func(a, b tabGroup) bool { return reflect.DeepEqual(a, b) })
}

func paneView(m *model, id int) ggui.Widget {
	group := paneGroup(m, id)
	return ggui.Column(
		paneTabBar(m, id, group),
		hline(),
		ggui.Expanded(paneBody(m, id, group)),
	).Stretch()
}

// paneTabBar is a pane's tabs, which drag to reorder, to another pane or to
// a pane's edge, with quick launchers for the project of the tab shown.
func paneTabBar(m *model, id int, group ggui.Readable[tabGroup]) ggui.Widget {
	tabs := ggui.Reactive(func() ggui.Widget {
		g := group.Get()
		sessions := m.Sessions.Get()
		titles := m.Titles.Get()
		focused := m.FocusGroup.Get() == id
		var children []ggui.Widget
		for i, tabID := range g.Tabs {
			s, ok := sessionIn(sessions, tabID)
			if !ok {
				s = protocol.SessionInfo{ID: tabID, Agent: "shell"}
			}
			at := i
			w := sessionDrag(m, tab(m, s, tabID == g.Active, focused, titles[tabID]), tabID)
			var cell ggui.Widget = w
			if tabID == g.Active && m.revealedTab[id] != tabID {
				cell = &revealOnce{child: w, done: func() { m.revealedTab[id] = tabID }}
			}
			// Before the tab or after it, by the half it is over.
			slot := func(d ggui.Dropped[tabDrag]) int {
				if d.Pos.X >= d.Size.W/2 {
					return at + 1
				}
				return at
			}
			children = append(children, dropSlot(m, id, i), tabDrop(m, cell, id, slot).
				OnHover(func(bool) { m.leaveHint(id) }))
		}
		children = append(children, dropSlot(m, id, len(g.Tabs)))
		// The tabs scroll sideways rather than squeeze when the pane is
		// narrow. The bar under them takes a tab dropped past the last one;
		// each tab's own zone, painted over it, takes one dropped on a tab.
		end := len(g.Tabs)
		bar := tabDrop(m, ggui.Scroll(ggui.Row(children...)).Horizontal().Bar(color.Transparent), id,
			func(ggui.Dropped[tabDrag]) int { return end }).
			OnHover(func(bool) { m.leaveHint(id) })

		var launch ggui.Widget = ggui.Box()
		if cur, ok := sessionIn(sessions, g.Active); ok {
			nodeID, project := cur.NodeID, cur.Project
			into := func(fn func()) func() { return func() { m.focusGroup(id); fn() } }
			launch = ggui.Row(
				iconButton("square-terminal", T("New terminal"), into(func() { m.launch("shell", nodeID, project) })),
				iconButton("sparkles", T("New Claude session"), into(func() { m.launch("claude", nodeID, project) })),
				iconButton("plus", T("New session…"), into(func() { m.openNew(nodeID, project) })),
			).Gap(2)
		}
		return ggui.Row(
			ggui.Expanded(bar),
			// Centred in the bar's height, level with the tabs' labels.
			ggui.Box(launch).Height(tabBarHeight).Pad(0, 8),
		).Stretch()
	})
	return ggui.Box(tabs).Height(tabBarHeight)
}

// paneBody is the terminal of the tab a pane shows, and while a tab is
// dragged, the places on it the tab can be dropped.
func paneBody(m *model, id int, group ggui.Readable[tabGroup]) ggui.Widget {
	current := ggui.Derived(func() pane {
		tab := group.Get().Active
		return pane{ID: tab, Ready: slices.Contains(m.Attached.Get(), tab)}
	})
	content := ggui.Key(current, func(p pane) ggui.Widget {
		switch {
		case p.ID == "":
			return emptyState(m)
		case !p.Ready:
			// Without a daemon there is nothing to attach to; say why.
			return ggui.View(m.Conn, func(c connState) ggui.Widget {
				if !c.OK && c.Err != "" {
					return ggui.Center(ggui.Column(
						icon("server").Size(28).Color(tokExtraMuted),
						muted(ggui.Text(c.Err).Size(13)),
						ui.Button(T("Daemon settings"), func() { m.openSettings("daemon") }).Outline(),
					).Gap(12).Align(ggui.AlignCenter))
				}
				return ggui.Center(ggui.Column(ui.Spinner(), muted(ggui.Text(T("Attaching…")).Size(13))).Gap(12).Align(ggui.AlignCenter))
			})
		default:
			// Only the current pane's terminal takes the keyboard as it
			// appears, so a split does not move it.
			focus := ggui.Peek(m.FocusGroup) == id
			term := newTerminalView(m.conns[p.ID], func() prefs { return ggui.Peek(m.Prefs) }, focus).
				OnPress(func() { m.focusGroup(id) })
			// A file dragged from the Files tab is typed as its path.
			return ggui.DropZone(term, func(d ggui.Dropped[pathDrag]) {
				term.setDropOver(false)
				term.typePaths([]string{d.Value.Path})
			}).OnHover(term.setDropOver)
		}
	})
	return ggui.Stack(
		content,
		ggui.If(m.Dragging, func() ggui.Widget { return newDropZones(m, id) }),
	).Expand()
}

// dropZones covers a pane while a tab is dragged: its four edges split the
// pane there, its middle moves the tab into it, and the part of the pane
// the tab would take is shaded.
type dropZones struct {
	zones [5]*ggui.DropZoneWidget[tabDrag] // by dropEdge
	hint  *ggui.StateValue[dropEdge]       // the zone under the drag, or -1
	shade ggui.Widget
	rects [5]ggui.Rect
}

// edgeShare is how much of a pane's width or height its edge zones take.
const edgeShare = 0.25

func newDropZones(m *model, group int) *dropZones {
	z := &dropZones{hint: ggui.State(dropEdge(-1))}
	for e := range z.zones {
		edge := dropEdge(e)
		z.zones[e] = ggui.DropZone(ggui.Box(), func(d ggui.Dropped[tabDrag]) {
			m.endDrag()
			m.splitTab(d.Value.ID, group, edge)
		}).OnHover(func(over bool) {
			if over {
				z.hint.Set(edge)
			} else if ggui.Peek(z.hint) == edge {
				z.hint.Set(-1)
			}
		})
	}
	z.shade = ggui.View(z.hint, func(e dropEdge) ggui.Widget { return &dropShade{edge: e} })
	return z
}

func (z *dropZones) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	size := c.Max()
	w, h := size.W, size.H
	ew, eh := w*edgeShare, h*edgeShare
	z.rects[dropLeft] = ggui.Rct(ggui.Pt(0, 0), ggui.Sz(ew, h))
	z.rects[dropRight] = ggui.Rct(ggui.Pt(w-ew, 0), ggui.Sz(ew, h))
	z.rects[dropTop] = ggui.Rct(ggui.Pt(ew, 0), ggui.Sz(w-2*ew, eh))
	z.rects[dropBottom] = ggui.Rct(ggui.Pt(ew, h-eh), ggui.Sz(w-2*ew, eh))
	z.rects[dropCenter] = ggui.Rct(ggui.Pt(ew, eh), ggui.Sz(w-2*ew, h-2*eh))
	for i, zone := range z.zones {
		zone.Layout(ggui.Tight(z.rects[i].Size), env)
	}
	z.shade.Layout(ggui.Tight(size), env)
	return size
}

func (z *dropZones) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(z.shade, r)
	for i, zone := range z.zones {
		at := z.rects[i]
		at.Origin = at.Origin.Add(r.Origin)
		dst.Paint(zone, at)
	}
}

// dropShade shades the part of a pane a dropped tab would take.
type dropShade struct {
	edge         dropEdge
	fill, border color.Color
}

func (s *dropShade) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	accent := ggui.ResolveColor(tokAccentBright, env)
	if accent == nil {
		accent = darkPalette.AccentBright // no theme, as in a bare probe
	}
	r, g, b, _ := accent.RGBA()
	s.fill = color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0x30}
	s.border = accent
	return c.Max()
}

func (s *dropShade) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if s.edge < 0 {
		return
	}
	x, y, w, h := r.Origin.X, r.Origin.Y, r.Size.W, r.Size.H
	switch s.edge {
	case dropLeft:
		w /= 2
	case dropRight:
		x, w = x+w/2, w/2
	case dropTop:
		h /= 2
	case dropBottom:
		y, h = y+h/2, h/2
	}
	area := ggui.Rct(ggui.Pt(x+4, y+4), ggui.Sz(w-8, h-8))
	dst.FillRoundRect(area, 8, s.fill)
	dst.StrokeRoundRect(area, 8, 1.5, s.border)
}

// sessionDrag lets a sidebar row be dragged into a pane like a tab.
func sessionDrag(m *model, w ggui.Widget, id string) ggui.Widget {
	return ggui.DragSource(w, tabDrag{ID: id}).
		OnDragStart(func() { m.Dragging.Set(true) }).
		OnDragEnd(func(bool) { m.endDrag() })
}

// tabHint is where in a pane's tab bar a dragged tab would drop: before
// the tab at Index, or after the last. Group 0 is nowhere.
type tabHint struct{ Group, Index int }

// slotWidth is how wide the gap a dragged tab would drop into opens.
const slotWidth = 96

// dropSlot is the gap before the tab at index i of a pane's bar, or after
// its last: closed, and opening while a dragged tab is over that place, so
// the tabs ease aside where it would go.
func dropSlot(m *model, group, i int) ggui.Widget {
	width := ggui.Map(m.TabHint, func(h tabHint) float64 {
		if h.Group == group && h.Index == i {
			return slotWidth
		}
		return 0
	})
	gap := ggui.AnimatedBox().BindWidth(width).Collapsible().Height(tabBarHeight).
		Fill(tokSurface2).Duration(150 * time.Millisecond).Easing(ggui.EaseOut)
	// An open gap is where the pointer is once the tabs moved aside, so it
	// keeps itself open, and takes the tab dropped in it.
	return tabDrop(m, gap, group, func(ggui.Dropped[tabDrag]) int { return i })
}

// tabDrop takes a tab dropped on w into a pane's bar at the place index
// reads from the drop, and opens the gap there while one is over w.
func tabDrop(m *model, w ggui.Widget, group int, index func(ggui.Dropped[tabDrag]) int) *ggui.DropZoneWidget[tabDrag] {
	return ggui.DropZone(w, func(d ggui.Dropped[tabDrag]) {
		m.endDrag()
		m.moveTab(d.Value.ID, group, index(d))
	}).OnOver(func(d ggui.Dropped[tabDrag]) { m.TabHint.Set(tabHint{Group: group, Index: index(d)}) })
}

// leaveHint clears the gap a drag leaving a pane's bar had opened there.
func (m *model) leaveHint(group int) {
	if ggui.Peek(m.TabHint).Group == group {
		m.TabHint.Set(tabHint{})
	}
}

// endDrag clears what a tab drag showed.
func (m *model) endDrag() {
	m.Dragging.Set(false)
	m.TabHint.Set(tabHint{})
}

// revealOnce scrolls its child into view the first time it is painted, as
// a pane's tab bar does for the tab it comes to show.
type revealOnce struct {
	child ggui.Widget
	done  func()
}

func (r *revealOnce) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	return r.child.Layout(c, env)
}

func (r *revealOnce) Paint(dst *ggui.Canvas, rect ggui.Rect) {
	dst.Paint(r.child, rect)
	if r.done != nil {
		dst.RequestReveal(rect)
		r.done()
		r.done = nil
	}
}
