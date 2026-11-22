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
// changes, a split made or a pane closed (or detached/returned to a separate
// window); tabs moving and dividers dragging update the panes in place, so
// terminals keep their state.
func layoutView(m *model) ggui.Widget {
	type visibleShape struct {
		Key   string
		Shape string
	}
	shape := ggui.Derived(func() visibleShape {
		m.wsRev.Get()
		key := m.ws.layoutKey
		vis := visibleLayoutNode(m.Layout.Get(), m.DetachedPanes.Get(), key)
		if vis == nil {
			return visibleShape{Key: key, Shape: ""}
		}
		return visibleShape{Key: key, Shape: layoutShape(vis)}
	})
	return widget.Stack(
		ggui.Key(shape, func(s visibleShape) ggui.Widget {
			if s.Shape == "" {
				return detachedPanesPlaceholder(m, s.Key)
			}
			vis := visibleLayoutNode(ggui.Peek(m.Layout), ggui.Peek(m.DetachedPanes), s.Key)
			if vis == nil {
				return detachedPanesPlaceholder(m, s.Key)
			}
			return layoutNodeView(m, vis)
		}),
		&dragGhostOverlay{m: m},
	).Expand()
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
		agentLane(m, id, group),
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
		grouped := showProjectChips(g.Tabs, projects)
		dragItem := m.DragItem.Get()
		chipHeld := m.ChipHeld.Get()
		lifted := make(map[string]bool, len(dragItem.ids()))
		for _, did := range dragItem.ids() {
			lifted[did] = true
		}
		liftedCount := 0
		for _, tabID := range g.Tabs {
			if lifted[tabID] {
				liftedCount++
			}
		}
		sessions := m.Sessions.Get()
		titles := m.Titles.Get()
		focused := m.FocusGroup.Get() == id
		docs, diffs := m.Docs.Get(), m.Diffs.Get()
		// The launchers start in the project of the tab shown, or without
		// one (or when scoped to a sub-project whose tab is not shown), in
		// the sidebar's selected scope, else the default place on this node.
		scope := m.Scope.Get()
		spaces := m.spaces()
		scopedHere := scope.Section != "" && scope.Space == spaces.spaceID(spaces.Space)
		nodeID := m.Conn.Get().Self.GetId()
		project := m.launchProject(nodeID)
		tabLocated := false
		if cur, ok := sessionIn(sessions, g.Active); ok {
			nodeID, project, tabLocated = cur.GetNodeId(), cur.GetProject(), true
			if scopedHere && !scope.scopeKey.holds(sessionScope(spaces.Projects, cur)) {
				tabLocated = false
			}
		} else if d, ok := docs[g.Active]; ok && d.Root.Path != "" {
			nodeID, project, tabLocated = d.Root.Node, d.Root.Path, true
			if scopedHere && !scope.scopeKey.holds(rootScope(spaces.Projects, d.Root)) {
				tabLocated = false
			}
		} else if d, ok := diffs[g.Active]; ok && d.Root.Path != "" {
			nodeID, project, tabLocated = d.Root.Node, d.Root.Path, true
			if scopedHere && !scope.scopeKey.holds(rootScope(spaces.Projects, d.Root)) {
				tabLocated = false
			}
		}
		if !tabLocated {
			if n, d, ok := m.scopeLocation(); ok {
				nodeID, project = n, d
			} else {
				nodeID = m.Conn.Get().Self.GetId()
				project = m.launchProject(nodeID)
			}
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
		counter := &tabCounter{total: len(g.Tabs) - liftedCount, hidden: m.hiddenTabsOf(id), post: m.post}
		folded := m.FoldedTabs.Get()
		// Of tabs of several projects or sub-projects, each project's run goes
		// under a label of its name, over a line of its colour; a run folded
		// away keeps only the tab shown, while holding the chip hides all of
		// its tabs until released or dragged.
		var runProject tabProject
		var runIDs []string
		runStarted, runFirst, runFirstVisible, runFolded := false, -1, -1, 0
		endRun := func() {
			if !runStarted {
				return
			}
			p := runProject
			first := runFirstVisible
			if first < 0 {
				first = runFirst
			}
			ids := slices.Clone(runIDs)
			var c color.Color = skin.BorderAccent
			if p.Key != "" {
				c = p.Look.color()
			}
			key := foldKey(id, p.Key)
			name := cmp.Or(p.Name, T("Other"))
			var chip ggui.Widget = &chipPress{
				m:     m,
				key:   key,
				child: projectChip(p, runFolded),
				onTap: func() { m.toggleFold(key) },
			}
			if len(ids) > 0 {
				chip = chipDrag(m, chip, ids, key, name, c)
			}
			// A tab or chip dropped on the label goes first in the run.
			label := tabDrop(m, chip, id, func(widget.Dropped[tabDrag]) int { return first }).
				OnHover(func(bool) { m.leaveHint(id) })
			children = append(children, ui.TabGroup(run...).Label(label).Tint(c).
				OnLabel(func() { m.toggleFold(key) }).
				LabelName(pick(folded[key], Tf("Unfold %s", name), Tf("Fold %s", name))))
			run, runIDs, runStarted, runFirst, runFirstVisible, runFolded = nil, nil, false, -1, -1, 0
		}
		type runSpan struct{ first, end int }
		spanByTab := make(map[string]runSpan, len(g.Tabs))
		if grouped {
			for idx := 0; idx < len(g.Tabs); {
				k := projects[g.Tabs[idx]].Key
				j := idx + 1
				for j < len(g.Tabs) && projects[g.Tabs[j]].Key == k {
					j++
				}
				for tIdx := idx; tIdx < j; tIdx++ {
					spanByTab[g.Tabs[tIdx]] = runSpan{first: idx, end: j}
				}
				idx = j
			}
		}
		// The tabs of agents another spawned show in its tab, which is
		// the one shown while any of them is.
		index := indexSessions(sessions)
		hosts := index.tabHosts(g.Tabs)
		active := cmp.Or(hosts[g.Active], g.Active)
		// What the bar draws, and the agents each session's tab shows.
		var shown []string
		kids := map[string][]*protocol.SessionInfo{}
		for _, tabID := range g.Tabs {
			if hosts[tabID] == "" && !lifted[tabID] {
				shown = append(shown, tabID)
				kids[tabID] = index.spawned(tabID)
			}
		}
		// Of runs folded away or held, the tabs hidden, and how many each hides.
		at := map[string]int{}
		hidden := map[string]bool{}
		hides := map[string]int{}
		var visibleIndices []int
		for i, tabID := range g.Tabs {
			at[tabID] = i
			if hosts[tabID] != "" {
				hidden[tabID] = true
				continue
			}
			if lifted[tabID] {
				continue
			}
			if p := projects[tabID]; grouped {
				key := foldKey(id, p.Key)
				if chipHeld == key || (folded[key] && tabID != active) {
					hidden[tabID] = true
					hides[p.Key]++
					continue
				}
			}
			visibleIndices = append(visibleIndices, i)
		}
		nextVisibleFrom := func(from int) int {
			for _, vi := range visibleIndices {
				if vi >= from {
					return vi
				}
			}
			return len(g.Tabs)
		}
		// Tabs grow in and shrink out: those leaving keep their place, while
		// tabs being dragged are omitted immediately.
		ease := m.tabEaseOf(id)
		for _, slot := range ease.plan(g.Tabs, hidden, lifted, projects, grouped) {
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
			if ok {
				if runFirst < 0 {
					runFirst = i
				}
				if !slot.Hidden && runFirstVisible < 0 {
					runFirstVisible = i
				}
				runIDs = append(runIDs, tabID)
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
			if isLaunchTab(tabID) {
				face = launchTabFace(m, tabID, tabID == active, focused)
			} else if d, ok := docs[tabID]; ok {
				face = docTab(m, tabID, d, tabID == g.Active, focused)
			} else if d, ok := diffs[tabID]; ok {
				face = diffTabFace(m, tabID, d, tabID == g.Active, focused)
			} else {
				s, ok := sessionIn(sessions, tabID)
				if !ok {
					s = &protocol.SessionInfo{Id: tabID, Agent: "shell"}
				}
				face = tab(m, s, tabID == active, focused, titles[tabID], kids[tabID])
			}
			// A label stuck at the bar's start would cover the tab shown at
			// its start: scrolled to, it comes clear of it.
			clear := 0.0
			if grouped {
				clear = projectChipWidth(cmp.Or(p.Name, T("Other")))
			}
			var cell ggui.Widget = &paintHook{child: tabBarDrag(m, face, tabID), fn: func(dst *ggui.Canvas, r ggui.Rect) {
				counter.saw(r, ease.settled(tabID))
				if tabID == active && m.revealedTab[id] != tabID {
					dst.RequestReveal(ggui.Rct(ggui.Pt(r.Origin.X-clear, r.Origin.Y), ggui.Sz(r.Size.W+clear, r.Size.H)))
					// Revealed again while it grows, the bar long enough
					// for all of it only once it has.
					if ease.settled(tabID) {
						m.revealedTab[id] = tabID
					}
				}
			}}
			// Before the tab or after it, by the half it is over; when
			// dragging a whole project chip over a grouped bar, snap to
			// that project run's boundary so the gap matches tabsByProject.
			span := spanByTab[tabID]
			nextSlot := nextVisibleFrom(i + 1)
			spanFirstSlot := nextVisibleFrom(span.first)
			spanEndSlot := nextVisibleFrom(span.end)
			slotFn := func(d widget.Dropped[tabDrag]) int {
				if grouped && len(d.Value.Tabs) > 0 {
					if d.Pos.X >= d.Size.W/2 {
						return spanEndSlot
					}
					return spanFirstSlot
				}
				if d.Pos.X >= d.Size.W/2 {
					return nextSlot
				}
				return i
			}
			cells := []ggui.Widget{dropSlot(m, id, i), ease.cell(tabID, face, tabDrop(m, cell, id, slotFn).
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

		// Beside the tabs: the +, and when the pane has tabs, a pop-out button.
		var popout ggui.Widget
		if len(g.Tabs) > 0 {
			ref := paneRef{Key: m.ws.layoutKey, GroupID: id}
			popout = widget.Box(widget.Column(
				iconButton("external-link", T("Open pane in new window"), func() { m.popoutPane(ref) }),
			).Justify(widget.JustifyCenter)).Height(tabBarHeight).Pad(0, 4, 0, 0)
		}
		extra := float64(34)
		if popout != nil {
			extra += 32
		}
		// The + follows the last tab, as far as the bar's end, where it
		// stays in sight as the tabs scroll; and when they do, a menu
		// lists them all, and how many are out of sight.
		need := tabsWidth(shown, sessions, titles, docs, diffs) + chips + extra + 16
		for _, k := range kids {
			need += familyMarksWidth(len(k))
		}
		end := len(g.Tabs)
		tabsAndPlus := tabDrop(m, widget.Row(widget.Flexible(counter), plus).Stretch(), id,
			func(widget.Dropped[tabDrag]) int { return end }).
			OnHover(func(bool) { m.leaveHint(id) })
		row := []ggui.Widget{
			widget.Expanded(tabsAndPlus),
			widget.Responsive(need, widget.Box().Size(0, 0), tabList(m, id, g, sessions, titles, docs, diffs)),
		}
		if popout != nil {
			row = append(row, popout)
		}
		return widget.Row(row...).Stretch()
	})
	return widget.Box(tabs).Height(tabBarHeight)
}

// tabBarKey identifies a pane's scrolling tab bar across its rebuilds.
type tabBarKey struct{ pane int }

// tabWidth estimates how wide a tab is drawn: its label, capped as the tab
// caps it, and its glyph, a status's room and padding; its close button
// goes over its end.
func tabWidth(label string) float64 {
	return max(min(float64(len([]rune(label)))*7.2, 168)+53, ui.EditorTabMinWidth+4)
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
	if isLaunchTab(id) {
		return T("New session")
	}
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
	if n := t.total - t.seen; t.post != nil && n != ggui.Peek(t.hidden) {
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
		case isLaunchTab(p.ID):
			return launchView(m, p.ID)
		case !p.Ready:
			// Without a daemon there is nothing to attach to; say why.
			return ggui.View(m.Conn, func(c connState) ggui.Widget {
				if !c.OK && c.Err != "" && !ggui.Peek(m.DaemonState).Starting {
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
			if len(d.Value.Tabs) > 0 {
				m.splitTabs(d.Value.Tabs, group, edge)
				if d.Value.FromGroup != "" {
					m.clearFold(d.Value.FromGroup)
				}
				return
			}
			m.splitTab(d.Value.ID, group, edge)
		}).Accept(func(d tabDrag) bool { return len(d.ids()) > 0 }).OnHover(func(over bool) {
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

// tabBarDrag lets a tab in a pane's tab bar be dragged while lifting it out
// of the bar so only its floating preview follows the pointer.
func tabBarDrag(m *model, w ggui.Widget, id string) ggui.Widget {
	return &tabDragSource{
		m:     m,
		child: w,
		value: tabDrag{ID: id},
	}
}

// chipDrag lets a project or sub-project chip drag all of its tabs at once,
// lifting the chip and its tabs out of the bar just like a regular tab.
func chipDrag(m *model, w ggui.Widget, tabs []string, foldKey, name string, c color.Color) ggui.Widget {
	preview := widget.Box(widget.Row(
		widget.Text(name).Size(12.5).Weight(ggui.WeightSemibold).Color(c).NoWrap(),
		widget.Box(widget.Text(strconv.Itoa(len(tabs))).Size(11).Weight(ggui.WeightMedium).Color(skin.Muted).NoWrap()).
			Pad(1, 5).Radius(6).Fill(skin.Surface2),
	).Gap(6).Align(widget.AlignCenter)).Pad(5, 10).Radius(8).Fill(skin.Surface1).Border(1, c)
	return &tabDragSource{
		m:       m,
		child:   w,
		preview: preview,
		value:   tabDrag{Tabs: tabs, FromGroup: foldKey, Project: name},
	}
}

// dragGhost holds the floating preview for a tab or project chip lifted out
// of a tab bar during a drag.
type dragGhost struct {
	widget ggui.Widget
	size   ggui.Size
	grab   ggui.Point
}

// dragGhostOverlay paints the lifted tab or chip ghost under the pointer so
// the preview remains visible even while its source is absent from the tab bar.
type dragGhostOverlay struct {
	m    *model
	last *dragGhost
	size ggui.Size
}

func (o *dragGhostOverlay) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	g := o.m.DragGhost.Get()
	o.last = g
	if g != nil && g.widget != nil {
		if g.size.W > 0 && g.size.H > 0 {
			o.size = g.widget.Layout(ggui.Tight(g.size), env)
		} else {
			o.size = g.widget.Layout(ggui.Constraints{MaxW: 320, MaxH: tabBarHeight}, env)
		}
	} else {
		o.size = ggui.Size{}
	}
	return c.Constrain(ggui.Sz(c.MaxW, c.MaxH))
}

func (o *dragGhostOverlay) Paint(dst *ggui.Canvas, _ ggui.Rect) {
	g := o.last
	if g == nil || g.widget == nil || o.size.W <= 0 || o.size.H <= 0 {
		return
	}
	ptr, ok := dst.Pointer()
	if !ok {
		return
	}
	grabX := min(max(g.grab.X, 0), o.size.W)
	grabY := min(max(g.grab.Y, 0), o.size.H)
	at := ggui.Rct(ggui.Pt(ptr.X-grabX, ptr.Y-grabY), o.size)
	dst.Overlay(func(dst *ggui.Canvas) {
		dst.Inert().Layer(ggui.LayerOptions{Fade: 0.3}, func(layer *ggui.Canvas) {
			layer.Paint(g.widget, at)
		})
	})
}

// tabDragSource is a Draggable for a tab or project chip that lifts the source
// out of the tab bar on drag start and renders its preview via dragGhostOverlay.
type tabDragSource struct {
	m        *model
	child    ggui.Widget
	preview  ggui.Widget
	value    tabDrag
	size     ggui.Size
	dragging bool
}

func (d *tabDragSource) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	d.size = d.child.Layout(c, env)
	return d.size
}

func (d *tabDragSource) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.HitDrag(r, d)
	dst.Paint(d.child, r)
}

func (d *tabDragSource) DragValue() any { return d.value }

func (d *tabDragSource) DragMoved(_, grab ggui.Point) {
	if !d.dragging {
		d.dragging = true
		ghost := &dragGhost{widget: d.child, size: d.size, grab: grab}
		if d.preview != nil {
			ghost = &dragGhost{widget: d.preview, grab: grab}
		}
		d.m.startTabDrag(d.value, ghost)
	}
}

func (d *tabDragSource) DragEnded(bool) {
	d.dragging = false
	d.m.endDrag()
}

// chipPress tracks pressing and holding a project chip so all of its tabs
// collapse under the chip while held, while preserving tap-to-fold and drag.
type chipPressKey string

type chipPress struct {
	m     *model
	key   string
	child ggui.Widget
	onTap func()
}

func (c *chipPress) HitID() any { return chipPressKey(c.key) }

func (c *chipPress) Layout(cs ggui.Constraints, env ggui.Env) ggui.Size {
	return c.child.Layout(cs, env)
}

func (c *chipPress) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.HitPointer(r, c)
	dst.Paint(c.child, r)
}

func (c *chipPress) HandlePointer(ev ggui.PointerEvent) bool {
	switch ev.Kind {
	case ggui.PointerDown:
		if ev.Button == ggui.MouseButtonLeft {
			c.m.ChipHeld.Set(c.key)
			return true
		}
		return false
	case ggui.PointerUp:
		if ev.Button == ggui.MouseButtonLeft {
			if ggui.Peek(c.m.ChipHeld) == c.key {
				c.m.ChipHeld.Set("")
			}
			return true
		}
		return false
	case ggui.PointerTap:
		if ev.Button == ggui.MouseButtonLeft && c.onTap != nil {
			c.onTap()
			return true
		}
		return false
	case ggui.PointerDrag:
		return true
	}
	return false
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

// tabDrop takes a tab or project chip dropped on w into a pane's bar at the
// place index reads from the drop, and opens the gap there while one is over w.
func tabDrop(m *model, w ggui.Widget, group int, index func(widget.Dropped[tabDrag]) int) *widget.DropZoneWidget[tabDrag] {
	return widget.DropZone(w, func(d widget.Dropped[tabDrag]) {
		m.endDrag()
		idx := index(d)
		if len(d.Value.Tabs) > 0 {
			m.moveTabs(d.Value.Tabs, group, idx)
			if d.Value.FromGroup != "" {
				m.clearFold(d.Value.FromGroup)
			}
			return
		}
		m.moveTab(d.Value.ID, group, idx)
	}).Accept(func(d tabDrag) bool { return len(d.ids()) > 0 }).
		OnOver(func(d widget.Dropped[tabDrag]) { m.TabHint.Set(tabHint{Group: group, Index: index(d)}) })
}

// leaveHint clears the gap a drag leaving a pane's bar had opened there.
func (m *model) leaveHint(group int) {
	if ggui.Peek(m.TabHint).Group == group {
		m.TabHint.Set(tabHint{})
	}
}

// startTabDrag marks val as actively dragged from a tab bar and sets its ghost.
func (m *model) startTabDrag(val tabDrag, ghost *dragGhost) {
	m.ChipHeld.Set("")
	m.DragItem.Set(val)
	m.DragGhost.Set(ghost)
	m.Dragging.Set(true)
}

// endDrag clears what a tab drag showed.
func (m *model) endDrag() {
	m.Dragging.Set(false)
	m.DragItem.Set(tabDrag{})
	m.DragGhost.Set(nil)
	m.ChipHeld.Set("")
	m.TabHint.Set(tabHint{})
}
