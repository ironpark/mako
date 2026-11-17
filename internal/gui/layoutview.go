package gui

import (
	"cmp"
	"fmt"
	"image/color"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
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
		MinSizes(160, 120).Hairline().Name(T("Pane divider"))
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
	// A press anywhere in it makes the pane current, whatever takes it, as
	// focus coming into it does.
	return paneWatch(widget.Column(
		paneTabBar(m, id, group),
		hline(),
		widget.Expanded(widget.Stack(paneBody(m, id, group), &paneGlow{on: func() bool {
			return m.Prefs.Get().PaneGlow && m.FocusGroup.Get() == id
		}}).Expand()),
	).Stretch(), func() { m.focusGroup(id) })
}

// paneGlow marks the pane that has the keys, where the settings ask: the
// accent round the edge of what it shows, under its tabs, fading inward to
// nothing. It only paints, over the pane's content, and takes no input.
type paneGlow struct {
	on    func() bool
	shown bool        // as the last layout read it
	tint  color.Color // the accent under the theme it was laid out in
}

// glowDepth is how far in from the pane's edge the glow reaches.
const glowDepth = 14

func (g *paneGlow) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	g.shown, g.tint = g.on(), ggui.ResolveColor(skin.AccentBright, env)
	return c.Constrain(ggui.Sz(c.MaxW, c.MaxH))
}

func (g *paneGlow) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if !g.shown {
		return
	}
	cr, cg, cb, _ := g.tint.RGBA()
	// A line a pixel wide at each step in, each fainter than the last, the
	// fall steep at first so the edge reads and the rest is a haze.
	for i := range glowDepth {
		d := float64(i) + .5
		t := 1 - float64(i)/glowDepth
		a := uint8(0x66 * t * t * t)
		if a == 0 {
			break
		}
		at := ggui.Rct(ggui.Pt(r.Origin.X+d, r.Origin.Y+d), ggui.Sz(r.Size.W-2*d, r.Size.H-2*d))
		dst.StrokeRoundRect(at, 0, 1, color.NRGBA{R: uint8(cr >> 8), G: uint8(cg >> 8), B: uint8(cb >> 8), A: a})
	}
}

// paneWatch runs current as a button goes down anywhere over child, before
// whatever under it takes the press, which it leaves to them: a chat's
// text, its feed and its composer each take a press of their own; and as
// the keyboard's focus comes into it. A wheel is not heard: scrolling a
// pane beside the current one leaves it so.
func paneWatch(child ggui.Widget, current func()) ggui.Widget {
	return widget.Within(child).
		OnDown(func(ggui.PointerEvent) { current() }).
		OnFocus(func(in bool) {
			if in {
				current()
			}
		})
}

// paneTabBar is a pane's tabs, which drag to reorder, to another pane or to
// a pane's edge, and after them a + whose quick launchers start in the
// project of the tab shown.
func paneTabBar(m *model, id int, group ggui.Readable[tabGroup]) ggui.Widget {
	projectsOf := paneProjects(m, group)
	tabs := ggui.Reactive(func() ggui.Widget {
		g := group.Get()
		projects := projectsOf.Get()
		grouped := manyProjects(g.Tabs, projects)
		sessions := m.Sessions.Get()
		titles := m.Titles.Get()
		focused := m.FocusGroup.Get() == id
		docs, diffs := m.Docs.Get(), m.Diffs.Get()
		// The launchers start in the project of the tab shown, or without
		// one, in the default place on this node.
		nodeID := m.Conn.Get().Self.GetId()
		project := m.launchProject(nodeID)
		if cur, ok := sessionIn(sessions, g.Active); ok {
			nodeID, project = cur.GetNodeId(), cur.GetProject()
		} else if d, ok := docs[g.Active]; ok && d.Root.Path != "" {
			nodeID, project = d.Root.Node, d.Root.Path
		} else if d, ok := diffs[g.Active]; ok && d.Root.Path != "" {
			nodeID, project = d.Root.Node, d.Root.Path
		}
		targets := nodeTargets(m.Nodes.Get(), nodeID, m.Agents.Get())
		focus := func() { m.focusGroup(id) }
		newSession := func() { focus(); m.openNew(nodeID, project) }
		// The tabs scroll sideways rather than squeeze when the pane is
		// narrow. The bar under them and the + takes a tab dropped past
		// the last one; each tab's own zone, painted over it, takes one
		// dropped on a tab.
		var children, run []ggui.Widget
		chips := 0.0
		counter := &tabCounter{total: len(g.Tabs), hidden: m.hiddenTabsOf(id), post: m.post}
		folded := m.FoldedTabs.Get()
		// Of tabs of several projects, each project's run goes under a
		// label of its name, over a line of its colour; a run folded away
		// keeps only the tab shown.
		var runProject tabProject
		runStarted, runFirst, runFolded := false, -1, 0
		endRun := func() {
			if !runStarted {
				return
			}
			p, first := runProject, runFirst
			var c color.Color = skin.BorderAccent
			if p.Key != "" {
				c = p.Look.color()
			}
			key := foldKey(id, p.Key)
			name := cmp.Or(p.Name, T("Other"))
			// A tab dropped on the label goes first in the run.
			label := tabDrop(m, projectChip(p, runFolded), id, func(widget.Dropped[tabDrag]) int { return first }).
				OnHover(func(bool) { m.leaveHint(id) })
			children = append(children, ui.TabGroup(run...).Label(label).Tint(c).
				OnLabel(func() { m.toggleFold(key) }).
				LabelName(pick(folded[key], Tf("Unfold %s", name), Tf("Fold %s", name))))
			run, runStarted, runFirst, runFolded = nil, false, -1, 0
		}
		// Of runs folded away, the tabs hidden, and how many each hides.
		at := map[string]int{}
		hidden := map[string]bool{}
		hides := map[string]int{}
		for i, tabID := range g.Tabs {
			at[tabID] = i
			if p := projects[tabID]; grouped && folded[foldKey(id, p.Key)] && tabID != g.Active {
				hidden[tabID] = true
				hides[p.Key]++
			}
		}
		// Tabs grow in and shrink out: those leaving keep their place.
		ease := m.tabEaseOf(id)
		for _, slot := range ease.plan(g.Tabs, hidden, projects, grouped) {
			tabID := slot.ID
			p := projects[tabID]
			if slot.Gone {
				p = ease.project(tabID)
			}
			if grouped && (!runStarted || p.Key != runProject.Key) {
				endRun()
				runProject, runStarted, runFolded = p, true, hides[p.Key]
				chips += projectChipWidth(cmp.Or(p.Name, T("Other")))
			}
			i, ok := at[tabID]
			if ok && runFirst < 0 {
				runFirst = i
			}
			if slot.Hidden {
				cell := ease.cell(tabID, nil, nil)
				if grouped {
					run = append(run, cell)
				} else {
					children = append(children, cell)
				}
				continue
			}
			var face ggui.Widget
			if d, ok := docs[tabID]; ok {
				face = docTab(m, tabID, d, tabID == g.Active, focused)
			} else if d, ok := diffs[tabID]; ok {
				face = diffTabFace(m, tabID, d, tabID == g.Active, focused)
			} else {
				s, ok := sessionIn(sessions, tabID)
				if !ok {
					s = &protocol.SessionInfo{Id: tabID, Agent: "shell"}
				}
				face = tab(m, s, tabID == g.Active, focused, titles[tabID])
			}
			// A label stuck at the bar's start would cover the tab shown at
			// its start: scrolled to, it comes clear of it.
			clear := 0.0
			if grouped {
				clear = projectChipWidth(cmp.Or(p.Name, T("Other")))
			}
			var cell ggui.Widget = &paintHook{child: sessionDrag(m, face, tabID), fn: func(dst *ggui.Canvas, r ggui.Rect) {
				counter.saw(r, ease.settled(tabID))
				if tabID == g.Active && m.revealedTab[id] != tabID {
					dst.RequestReveal(ggui.Rct(ggui.Pt(r.Origin.X-clear, r.Origin.Y), ggui.Sz(r.Size.W+clear, r.Size.H)))
					// Revealed again while it grows, the bar long enough
					// for all of it only once it has.
					if ease.settled(tabID) {
						m.revealedTab[id] = tabID
					}
				}
			}}
			// Before the tab or after it, by the half it is over.
			slot := func(d widget.Dropped[tabDrag]) int {
				if d.Pos.X >= d.Size.W/2 {
					return i + 1
				}
				return i
			}
			cells := []ggui.Widget{dropSlot(m, id, i), ease.cell(tabID, face, tabDrop(m, cell, id, slot).
				OnHover(func(bool) { m.leaveHint(id) }))}
			if grouped {
				run = append(run, cells...)
			} else {
				children = append(children, cells...)
			}
		}
		endRun()
		children = append(children, dropSlot(m, id, len(g.Tabs)))
		// Tabs scrolled past an end fade out there; of groups, a label
		// sticking at the start stands in for the fade there.
		bar := widget.Scroll(widget.Box(widget.Row(children...).Align(widget.AlignCenter)).Pad(0, 4).Height(tabBarHeight)).
			Horizontal().Shrink().Bar(color.Transparent).Overscroll(widget.OverscrollBounce).
			// Its own identity, not the order it is built in, which tabs
			// folded, opened or closed before it change: the bar keeps
			// where it was scrolled to.
			Key(tabBarKey{id})
		if grouped {
			bar.FadeEnds(0, 28)
		} else {
			bar.FadeEdges(28)
		}
		counter.child = bar
		// Centred down the bar only: across, it is as wide as itself.
		plus := widget.Box(widget.Column(launchButton(m, nodeID, project, targets, focus, newSession)).Justify(widget.JustifyCenter)).
			Height(tabBarHeight).Pad(0, 4)

		// Beside the tabs: the +.
		extra := float64(34)
		// The + follows the last tab, as far as the bar's end, where it
		// stays in sight as the tabs scroll; and when they do, a menu
		// lists them all, and how many are out of sight.
		need := tabsWidth(g.Tabs, sessions, titles, docs, diffs) + chips + extra + 16
		end := len(g.Tabs)
		tabsAndPlus := tabDrop(m, widget.Row(widget.Flexible(counter), plus).Stretch(), id,
			func(widget.Dropped[tabDrag]) int { return end }).
			OnHover(func(bool) { m.leaveHint(id) })
		return widget.Row(
			widget.Expanded(tabsAndPlus),
			widget.Responsive(need, widget.Box().Size(0, 0), tabList(m, id, g, sessions, titles, docs, diffs)),
		).Stretch()
	})
	return widget.Box(tabs).Height(tabBarHeight)
}

// tabBarKey identifies a pane's scrolling tab bar across its rebuilds.
type tabBarKey struct{ pane int }

// tabWidth estimates how wide a tab is drawn: its label, capped as the tab
// caps it, and its glyph, close button and padding.
func tabWidth(label string) float64 {
	return max(min(float64(len([]rune(label)))*7.2, 168)+83, ui.EditorTabMinWidth+4)
}

// tabsWidth estimates a pane's tabs' width together.
func tabsWidth(tabs []string, sessions []*protocol.SessionInfo, titles map[string]string, docs map[string]docInfo, diffs map[string]diffTab) float64 {
	w := 0.0
	for _, id := range tabs {
		w += tabWidth(tabLabel(id, sessions, titles, docs, diffs))
	}
	return w
}

// tabLabel is what a tab is called.
func tabLabel(id string, sessions []*protocol.SessionInfo, titles map[string]string, docs map[string]docInfo, diffs map[string]diffTab) string {
	if d, ok := docs[id]; ok {
		return d.Name()
	}
	if d, ok := diffs[id]; ok {
		return d.Name()
	}
	s, _ := sessionIn(sessions, id)
	return sessionTitle(s, titles[id])
}

// tabList is a menu of a pane's tabs, for when they do not all fit, with
// how many are out of sight on it.
func tabList(m *model, pane int, g tabGroup, sessions []*protocol.SessionInfo, titles map[string]string, docs map[string]docInfo, diffs map[string]diffTab) ggui.Widget {
	items := make([]ggui.Widget, 0, len(g.Tabs))
	for _, id := range g.Tabs {
		label := tabLabel(id, sessions, titles, docs, diffs)
		if id == g.Active {
			label = "✓ " + label
		}
		items = append(items, ui.MenuItem(label, func() { m.showTab(id) }))
	}
	// Centred down the bar, the size of the + and the eraser beside it.
	menu := iconMenu(ui.MenuOf(icon("chevrons-up-down"), items...).Width(260).Name(T("All tabs")), 6)
	badge := ggui.View(m.hiddenTabsOf(pane), func(n int) ggui.Widget {
		if n <= 0 {
			return widget.Box()
		}
		count := widget.Text(strconv.Itoa(n)).Size(10).Weight(ggui.WeightMedium).NoWrap()
		return widget.Align(widget.Box(count).Pad(0, 4).Height(15).Radius(7.5).Fill(skin.Surface3).Border(1, skin.Border)).At(1, 0)
	})
	return widget.Box(widget.Stack(widget.Column(menu).Justify(widget.JustifyCenter), widget.Box(badge).Pad(4, 0, 0, 0)).Expand()).
		Height(tabBarHeight).Width(36)
}

// tabCounter counts, each whole frame, the tabs of a bar painted where
// they show, its own rect, and keeps how many of total are not in hidden.
// A frame painted in part paints only some tabs, and is not counted.
type tabCounter struct {
	child    ggui.Widget
	total    int
	seen     int
	counting bool
	window   ggui.Rect
	hidden   *ggui.StateValue[int]
	post     func(func())
}

func (t *tabCounter) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	return t.child.Layout(c, env)
}

func (t *tabCounter) Paint(dst *ggui.Canvas, r ggui.Rect) {
	t.seen, t.window, t.counting = 0, r, dst.TakesInput()
	dst.Paint(t.child, r)
	if !t.counting {
		return
	}
	t.counting = false
	if n := t.total - t.seen; n != ggui.Peek(t.hidden) {
		t.post(func() { t.hidden.Set(n) })
	}
}

// saw counts a tab painted at r if it shows whole, near enough, in the
// bar; one still growing in, which the bar is not yet long enough for, if
// it starts there.
func (t *tabCounter) saw(r ggui.Rect, grown bool) {
	end := t.window.Origin.X + t.window.Size.W + 2
	if t.counting && r.Origin.X >= t.window.Origin.X-2 && (r.Origin.X+r.Size.W <= end || !grown && r.Origin.X <= end) {
		t.seen++
	}
}

// paintHook runs fn after painting its child, each frame.
type paintHook struct {
	child ggui.Widget
	fn    func(dst *ggui.Canvas, r ggui.Rect)
}

func (h *paintHook) Layout(c ggui.Constraints, env ggui.Env) ggui.Size { return h.child.Layout(c, env) }

func (h *paintHook) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(h.child, r)
	h.fn(dst, r)
}

// paneBody is the terminal of the tab a pane shows, and while a tab is
// dragged, the places on it the tab can be dropped.
func paneBody(m *model, id int, group ggui.Readable[tabGroup]) ggui.Widget {
	current := ggui.Derived(func() pane {
		tab := group.Get().Active
		return pane{ID: tab, Ready: !isSessionTab(tab) || slices.Contains(m.Attached.Get(), tab)}
	})
	content := ggui.Key(current, func(p pane) ggui.Widget {
		switch {
		case p.ID == "":
			return emptyState(m)
		case isDocTab(p.ID):
			if m.docs[p.ID] == nil {
				return widget.Box()
			}
			return docView(m, p.ID, ggui.Peek(m.FocusGroup) == id)
		case isDiffTab(p.ID):
			return diffTabView(m, p.ID)
		case !p.Ready:
			// Without a daemon there is nothing to attach to; say why.
			return ggui.View(m.Conn, func(c connState) ggui.Widget {
				if !c.OK && c.Err != "" {
					return widget.Center(widget.Column(
						icon("server").Size(28).Color(skin.ExtraMuted),
						muted(widget.Text(c.Err).Size(13)),
						ui.Button(T("Daemon settings"), func() { m.openSettings("daemon") }).Outline(),
					).Gap(12).Align(widget.AlignCenter))
				}
				return widget.Center(widget.Column(ui.Spinner(), muted(widget.Text(T("Attaching…")).Size(13))).Gap(12).Align(widget.AlignCenter))
			})
		default:
			return backdropped(m, sessionBackdrop, sessionView(m, p.ID, id))
		}
	})
	return widget.Stack(
		content,
		ggui.If(m.Dragging, func() ggui.Widget { return newDropZones(m, id) }),
	).Expand()
}

// dropZones covers a pane while a tab is dragged: its four edges split the
// pane there, its middle moves the tab into it, and the part of the pane
// the tab would take is shaded.
type dropZones struct {
	zones [5]*widget.DropZoneWidget[tabDrag] // by dropEdge
	hint  *ggui.StateValue[dropEdge]         // the zone under the drag, or -1
	shade ggui.Widget
	rects [5]ggui.Rect
}

// edgeShare is how much of a pane's width or height its edge zones take.
const edgeShare = 0.25

func newDropZones(m *model, group int) *dropZones {
	z := &dropZones{hint: ggui.State(dropEdge(-1))}
	for e := range z.zones {
		edge := dropEdge(e)
		z.zones[e] = widget.DropZone(widget.Box(), func(d widget.Dropped[tabDrag]) {
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
	accent := ggui.ResolveColor(skin.AccentBright, env)
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
	return widget.DragSource(w, tabDrag{ID: id}).
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
	gap := widget.AnimatedBox().BindWidth(width).Collapsible().Height(ui.EditorTabHeight).Radius(ui.EditorTabRadius).
		Fill(skin.Surface2).Duration(150 * time.Millisecond).Easing(ggui.EaseOut)
	// An open gap is where the pointer is once the tabs moved aside, so it
	// keeps itself open, and takes the tab dropped in it.
	return tabDrop(m, gap, group, func(widget.Dropped[tabDrag]) int { return i })
}

// tabDrop takes a tab dropped on w into a pane's bar at the place index
// reads from the drop, and opens the gap there while one is over w.
func tabDrop(m *model, w ggui.Widget, group int, index func(widget.Dropped[tabDrag]) int) *widget.DropZoneWidget[tabDrag] {
	return widget.DropZone(w, func(d widget.Dropped[tabDrag]) {
		m.endDrag()
		m.moveTab(d.Value.ID, group, index(d))
	}).OnOver(func(d widget.Dropped[tabDrag]) { m.TabHint.Set(tabHint{Group: group, Index: index(d)}) })
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
