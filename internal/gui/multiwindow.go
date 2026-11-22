package gui

import (
	"cmp"
	"fmt"
	"image/color"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// Multi-window support:
//
//  1. Detached split panes: any pane in a split layout can be popped out into
//     its own OS window without mutating m.Layout, m.ws.layouts, workspace.json,
//     or CRDT state. While detached, visibleLayoutNode hides that pane from the
//     parent window's rendered tree (collapsing splits in view-only rendering).
//     Closing the OS window — or clicking Return — restores the pane to its
//     exact original position.
//
//  2. Detached space windows: any space (including "All sessions") can be
//     opened in its own independent OS window with its own sidebar, scope,
//     project context, and split pane view, without changing the main window's
//     active space.

// paneRef identifies a split pane by its workspace layout key and group ID.
type paneRef struct {
	Key     string
	GroupID int
}

// spaceWindowState holds the local UI state of an independent Space OS window.
type spaceWindowState struct {
	spaceID      string
	context      *ggui.StateValue[string]
	scope        *ggui.StateValue[scopeKey]
	focusGroup   *ggui.StateValue[int]
	sidebarOpen  *ggui.StateValue[bool]
	sidebarWidth *ggui.StateValue[float64]
	sidebarSplit *ggui.StateValue[float64]
	win          *ggui.Window
}

func (sw *spaceWindowState) layoutKey() string {
	return layoutKey(sw.spaceID, sw.context.Get())
}

func (sw *spaceWindowState) peekLayoutKey() string {
	return layoutKey(sw.spaceID, ggui.Peek(sw.context))
}

// Secondary window tracking & repaint ----------------------------------------

func (m *model) registerSecondaryWindow(w *ggui.Window) {
	if w == nil {
		return
	}
	m.secWinMu.Lock()
	m.secWins[w] = struct{}{}
	m.secWinMu.Unlock()
}

func (m *model) unregisterSecondaryWindow(w *ggui.Window) {
	if w == nil {
		return
	}
	m.secWinMu.Lock()
	delete(m.secWins, w)
	m.secWinMu.Unlock()
}

func (m *model) repaintSecondaryWindows() {
	m.secWinMu.RLock()
	if len(m.secWins) == 0 {
		m.secWinMu.RUnlock()
		return
	}
	wins := make([]*ggui.Window, 0, len(m.secWins))
	for w := range m.secWins {
		wins = append(wins, w)
	}
	m.secWinMu.RUnlock()
	for _, w := range wins {
		w.Repaint()
	}
}

// repaintTerminalRect repaints a terminal's rect in the main window and, when
// any secondary windows are open, asks them to repaint too so terminals moved
// to detached panes or space windows update immediately.
func (m *model) repaintTerminalRect(r ggui.Rect) {
	if w := m.window; w != nil {
		w.RepaintRect(r)
	}
	m.repaintSecondaryWindows()
}

// closeAllSecondaryWindows closes every open detached pane and space window.
func (m *model) closeAllSecondaryWindows() {
	for ref, w := range maps.Clone(m.paneWindows) {
		delete(m.paneWindows, ref)
		if w != nil {
			m.unregisterSecondaryWindow(w)
			w.Close()
		}
	}
	for id, sw := range maps.Clone(m.spaceWindows) {
		delete(m.spaceWindows, id)
		if sw != nil && sw.win != nil {
			m.unregisterSecondaryWindow(sw.win)
			sw.win.Close()
		}
	}
	if len(ggui.Peek(m.DetachedPanes)) > 0 {
		m.DetachedPanes.Set(map[paneRef]bool{})
	}
	if len(ggui.Peek(m.SpaceWindows)) > 0 {
		m.SpaceWindows.Set(map[string]bool{})
	}
}

// Keyed layout accessors -----------------------------------------------------

func (m *model) bumpWorkspaceRev() {
	if m.wsRev != nil {
		m.wsRev.Update(func(v uint64) uint64 { return v + 1 })
	}
}

// peekLayoutForKey returns the layout tree for key without subscribing.
func (m *model) peekLayoutForKey(key string) *layoutNode {
	if key == m.ws.layoutKey {
		return ggui.Peek(m.Layout)
	}
	if root := m.ws.layouts[key]; root != nil {
		return root
	}
	return newLayout()
}

// layoutForKey returns the layout tree for key reactively.
func (m *model) layoutForKey(key string) *layoutNode {
	if m.wsRev != nil {
		m.wsRev.Get()
	}
	if key == m.ws.layoutKey {
		return m.Layout.Get()
	}
	if root := m.ws.layouts[key]; root != nil {
		return root
	}
	return newLayout()
}

// editLayoutForKey edits the layout tree for key. When key is the main
// window's active layout, it delegates to editLayout; otherwise it updates
// m.ws.layouts[key] directly, persists via workspaceChanged, and notifies
// secondary windows through wsRev.
func (m *model) editLayoutForKey(key string, fn func(root *layoutNode) int) int {
	if key == m.ws.layoutKey {
		m.editLayout(fn)
		return ggui.Peek(m.FocusGroup)
	}
	base := m.ws.layouts[key]
	if base == nil {
		base = newLayout()
	}
	root := base.clone()
	focus := fn(root)
	root = root.prune()
	var all []string
	for _, g := range root.groups() {
		all = append(all, g.Tabs...)
	}
	keys := m.tabKeys(all)
	for _, g := range root.groups() {
		g.Tabs = tabsByProject(g.Tabs, keys)
	}
	if focus == 0 || root.group(focus) == nil {
		focus = root.groups()[0].ID
	}
	m.ws.layouts[key] = root
	m.bumpWorkspaceRev()
	m.workspaceChanged()
	m.attachVisible()
	return focus
}

// setRatioForKey updates a split divider ratio in key's layout.
func (m *model) setRatioForKey(key string, node int, r float64) {
	if key == m.ws.layoutKey {
		m.setRatio(node, r)
		return
	}
	root := m.ws.layouts[key]
	if root == nil {
		return
	}
	n := root.node(node)
	if n == nil || n.Ratio == r {
		return
	}
	next := root.clone()
	next.node(node).Ratio = r
	m.ws.layouts[key] = next
	m.bumpWorkspaceRev()
	m.workspaceChanged()
}

// activateTabInKey shows tabID in key's layout (in groupID when not already
// open there) and attaches its terminal. Unlike selectSession, this does not
// switch the main window's space or project when key != m.ws.layoutKey.
func (m *model) activateTabInKey(key string, groupID int, tabID string) int {
	if tabID == "" {
		return groupID
	}
	focus := m.editLayoutForKey(key, func(root *layoutNode) int {
		g := root.groupOf(tabID)
		if g == nil {
			var at int
			g, at = indexSessions(ggui.Peek(m.Sessions)).familyPlace(root, tabID)
			if g == nil {
				g = root.group(groupID)
				if g == nil {
					g = root.groups()[0]
				}
				at = slices.Index(g.Tabs, g.Active) + 1
			}
			g.insert(tabID, at)
		}
		g.Active = tabID
		return g.ID
	})
	if isSessionTab(tabID) {
		m.lastSession = tabID
	}
	m.attach(tabID)
	return focus
}

// closeTabInKey removes tabID from key's layout and forgets its resources if
// no other layout still holds it.
func (m *model) closeTabInKey(key, tabID string) {
	if key == m.ws.layoutKey {
		m.closeTab(tabID)
		return
	}
	root := m.ws.layouts[key]
	if root == nil || root.groupOf(tabID) == nil {
		return
	}
	m.editLayoutForKey(key, func(r *layoutNode) int {
		r.remove(tabID)
		return 0
	})
	delete(m.launches, tabID)
	m.forgetClosedTabs()
}

// moveTabInKey moves tabID into group at index i inside key's layout.
func (m *model) moveTabInKey(key, tabID string, group, i int) {
	if key == m.ws.layoutKey {
		m.moveTab(tabID, group, i)
		return
	}
	m.editLayoutForKey(key, func(root *layoutNode) int {
		target := root.group(group)
		if target == nil {
			return 0
		}
		if from := slices.Index(target.Tabs, tabID); from >= 0 && from < i {
			i--
		}
		root.remove(tabID)
		target.insert(tabID, i)
		return group
	})
	m.attach(tabID)
}

// moveTabsInKey moves a run of tabs into group at index i inside key's layout.
func (m *model) moveTabsInKey(key string, ids []string, group, i int) {
	if key == m.ws.layoutKey {
		m.moveTabs(ids, group, i)
		return
	}
	clean, idSet := cleanTabIDs(ids)
	if len(clean) == 0 {
		return
	}
	m.editLayoutForKey(key, func(root *layoutNode) int {
		target := root.group(group)
		if target == nil {
			return 0
		}
		active := preferredActiveTab(root, group, idSet)
		limit := min(max(i, 0), len(target.Tabs))
		shift := 0
		for _, id := range target.Tabs[:limit] {
			if idSet[id] {
				shift++
			}
		}
		i = max(0, i-shift)
		root.removeSet(idSet)
		target.insertAll(clean, i, active)
		return group
	})
}

// splitTabInKey splits tabID to edge of group inside key's layout.
func (m *model) splitTabInKey(key, tabID string, group int, edge dropEdge) {
	if key == m.ws.layoutKey {
		m.splitTab(tabID, group, edge)
		return
	}
	if edge == dropCenter {
		if g := m.peekLayoutForKey(key).group(group); g != nil {
			m.moveTabInKey(key, tabID, group, len(g.Tabs))
		}
		return
	}
	if g := m.peekLayoutForKey(key).group(group); g == nil || (len(g.Tabs) == 1 && g.Tabs[0] == tabID) {
		return
	}
	m.editLayoutForKey(key, func(root *layoutNode) int {
		root.remove(tabID)
		root.split(tabID, group, edge)
		return root.groupOf(tabID).ID
	})
	m.attach(tabID)
}

// splitTabsInKey splits a run of tabs to edge of group inside key's layout.
func (m *model) splitTabsInKey(key string, ids []string, group int, edge dropEdge) {
	if key == m.ws.layoutKey {
		m.splitTabs(ids, group, edge)
		return
	}
	clean, idSet := cleanTabIDs(ids)
	if len(clean) == 0 {
		return
	}
	if edge == dropCenter {
		if g := m.peekLayoutForKey(key).group(group); g != nil {
			m.moveTabsInKey(key, clean, group, len(g.Tabs))
		}
		return
	}
	g := m.peekLayoutForKey(key).group(group)
	if g == nil {
		return
	}
	hasOther := false
	for _, t := range g.Tabs {
		if !idSet[t] {
			hasOther = true
			break
		}
	}
	if !hasOther {
		return
	}
	m.editLayoutForKey(key, func(root *layoutNode) int {
		active := preferredActiveTab(root, group, idSet)
		root.removeSet(idSet)
		root.splitAll(clean, active, group, edge)
		return root.groupOf(clean[0]).ID
	})
}

// openNewInKey opens a new-session launch tab directly inside key / groupID.
func (m *model) openNewInKey(key string, groupID int, nodeID, project string) *launchState {
	if key == m.ws.layoutKey {
		m.focusGroup(groupID)
		return m.openNew(nodeID, project)
	}
	f := newSession{Agent: cmp.Or(m.lastAgent, ggui.Peek(m.Prefs).DefaultAgent), NodeID: nodeID, Project: project}
	if f.NodeID == "" {
		f.NodeID = ggui.Peek(m.Conn).Self.GetId()
	}
	if f.Project == "" {
		f.Project = m.launchProject(f.NodeID)
	}
	m.launchSeq++
	id := launchPrefix + strconv.Itoa(m.launchSeq)
	l := m.launchOf(id)
	l.Form.Set(f)
	m.activateTabInKey(key, groupID, id)
	return l
}

// findLayoutOfTab returns the layout key holding tabID, or "" if none does.
func (m *model) findLayoutOfTab(tabID string) string {
	if tabID == "" {
		return ""
	}
	if ggui.Peek(m.Layout).groupOf(tabID) != nil {
		return m.ws.layoutKey
	}
	for key, root := range m.ws.layouts {
		if root != nil && root.groupOf(tabID) != nil {
			return key
		}
	}
	return ""
}

// View-only layout filtering (never mutates layout data) ---------------------

// visibleLayoutNode returns a view-only layout tree for key with all panes in
// detached omitted. The original root tree is NEVER modified: splits whose one
// side is detached collapse cleanly to the other side, and if every leaf pane
// is detached, nil is returned.
func visibleLayoutNode(root *layoutNode, detached map[paneRef]bool, key string) *layoutNode {
	if root == nil {
		return nil
	}
	if root.Group != nil {
		if detached[paneRef{Key: key, GroupID: root.Group.ID}] {
			return nil
		}
		return root
	}
	first := visibleLayoutNode(root.First, detached, key)
	second := visibleLayoutNode(root.Second, detached, key)
	switch {
	case first == nil:
		return second
	case second == nil:
		return first
	case first == root.First && second == root.Second:
		return root
	default:
		return &layoutNode{
			ID:       root.ID,
			Vertical: root.Vertical,
			Ratio:    root.Ratio,
			First:    first,
			Second:   second,
		}
	}
}

// detachedRefsInLayout returns the detached pane references belonging to key
// that still exist in root, in layout order.
func detachedRefsInLayout(root *layoutNode, detached map[paneRef]bool, key string) []paneRef {
	if root == nil || len(detached) == 0 {
		return nil
	}
	var out []paneRef
	for _, g := range root.groups() {
		ref := paneRef{Key: key, GroupID: g.ID}
		if detached[ref] {
			out = append(out, ref)
		}
	}
	return out
}

// Feature A: Detached Split Pane Windows -------------------------------------

// isPaneDetached reports whether ref is currently popped out into its own window.
func (m *model) isPaneDetached(ref paneRef) bool {
	return ggui.Peek(m.DetachedPanes)[ref]
}

// popoutCurrentPane pops out the main window's currently focused split pane.
func (m *model) popoutCurrentPane() {
	id := ggui.Peek(m.FocusGroup)
	ref := paneRef{Key: m.ws.layoutKey, GroupID: id}
	if g := ggui.Peek(m.Layout).group(id); g == nil || len(g.Tabs) == 0 {
		return
	}
	m.popoutPane(ref)
}

// popoutPane opens split pane ref in a dedicated OS window without mutating
// persisted layout data. Closing that window automatically returns the pane to
// its original position.
func (m *model) popoutPane(ref paneRef) {
	if m.peekLayoutForKey(ref.Key).group(ref.GroupID) == nil {
		return
	}
	if w := m.paneWindows[ref]; w != nil {
		w.Focus()
		return
	}
	next := maps.Clone(ggui.Peek(m.DetachedPanes))
	if next == nil {
		next = map[paneRef]bool{}
	}
	next[ref] = true
	m.DetachedPanes.Set(next)
	m.attachVisible()
	if m.window == nil {
		return // headless probe/test has no OS window manager
	}
	var gone bool
	var win *ggui.Window
	closed := func() {
		if gone {
			return
		}
		gone = true
		delete(m.paneWindows, ref)
		m.unregisterSecondaryWindow(win)
		m.returnPane(ref)
	}
	w, err := m.window.App().OpenWindow(ggui.Config{
		Title:     m.paneWindowTitle(ref),
		Width:     960,
		Height:    680,
		MinWidth:  420,
		MinHeight: 300,
		Resizable: true,
	}, func() ggui.Widget {
		w := ggui.UseWindow()
		win = w
		m.registerSecondaryWindow(w)
		title := ggui.Derived(func() string { return m.paneWindowTitleReactive(ref) })
		ggui.Watch(title, func(t string) { w.SetTitle(t) })
		exists := ggui.Derived(func() bool { return m.layoutForKey(ref.Key).group(ref.GroupID) != nil })
		ggui.Watch(exists, func(ok bool) {
			if !ok {
				w.Close()
			}
		})
		ggui.OnCleanup(closed)
		return detachedPaneRoot(m, ref, func() { w.Close() })
	})
	if err != nil {
		closed()
		if m.toaster != nil {
			m.toaster.Push(ui.Toast(T("Could not open window"), err.Error()).Destructive())
		}
		return
	}
	m.paneWindows[ref] = w
}

// returnPane restores a detached pane back into its parent layout view without
// touching persisted layout data.
func (m *model) returnPane(ref paneRef) {
	cur := ggui.Peek(m.DetachedPanes)
	if !cur[ref] {
		return
	}
	next := maps.Clone(cur)
	delete(next, ref)
	m.DetachedPanes.Set(next)
	m.attachVisible()
}

// dockPane closes the detached pane's OS window (if open) and returns the pane
// to its parent layout.
func (m *model) dockPane(ref paneRef) {
	if w := m.paneWindows[ref]; w != nil {
		w.Close()
		return
	}
	m.returnPane(ref)
}

// focusPaneWindow brings the OS window for detached pane ref to the front.
func (m *model) focusPaneWindow(ref paneRef) {
	if w := m.paneWindows[ref]; w != nil {
		w.Focus()
	}
}

// returnAllPanes docks every detached pane belonging to layout key.
func (m *model) returnAllPanes(key string) {
	for ref := range maps.Clone(ggui.Peek(m.DetachedPanes)) {
		if ref.Key == key {
			m.dockPane(ref)
		}
	}
}

// returnAllDetachedPanes docks every detached pane across all layouts.
func (m *model) returnAllDetachedPanes() {
	for ref := range maps.Clone(ggui.Peek(m.DetachedPanes)) {
		m.dockPane(ref)
	}
}

func (m *model) paneWindowTitle(ref paneRef) string {
	g := m.peekLayoutForKey(ref.Key).group(ref.GroupID)
	if g != nil && g.Active != "" {
		label := tabLabel(g.Active, ggui.Peek(m.Sessions), ggui.Peek(m.Titles), ggui.Peek(m.Docs), ggui.Peek(m.Diffs))
		if label != "" {
			return label + " — mako"
		}
	}
	return m.spaceTitleByID(keySpace(ref.Key)) + " — mako"
}

func (m *model) paneWindowTitleReactive(ref paneRef) string {
	g := m.layoutForKey(ref.Key).group(ref.GroupID)
	if g != nil && g.Active != "" {
		label := tabLabel(g.Active, m.Sessions.Get(), m.Titles.Get(), m.Docs.Get(), m.Diffs.Get())
		if label != "" {
			return label + " — mako"
		}
	}
	return m.spaceTitleByIDReactive(keySpace(ref.Key)) + " — mako"
}

// detachedPaneRoot builds the root widget of a detached pane OS window.
func detachedPaneRoot(m *model, ref paneRef, dock func()) ggui.Widget {
	lang := ggui.Map(m.Prefs, func(p prefs) string { return i18n.Resolve(p.Language) })
	return ggui.Key(lang, func(code string) ggui.Widget {
		i18n.Set(code)
		return backdropped(m, windowBackdrop, detachedPaneWindow(m, ref, dock))
	})
}

// detachedPaneWindow renders a single detached split pane in its own window.
func detachedPaneWindow(m *model, ref paneRef, dock func()) ggui.Widget {
	group := paneGroupForKey(m, ref.Key, ref.GroupID)
	if dock == nil {
		dock = func() { m.dockPane(ref) }
	}
	return widget.Column(
		keyedPaneTabBar(m, ref.Key, ref.GroupID, group, true, dock, nil),
		hline(),
		keyedAgentLane(m, ref.Key, ref.GroupID, group, nil),
		widget.Expanded(keyedPaneBody(m, ref.Key, ref.GroupID, group, nil)),
	).Stretch()
}

// detachedPanesPlaceholder is shown when every split pane in key is currently
// open in separate windows, offering one-click Focus or Return actions.
func detachedPanesPlaceholder(m *model, key string) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		root := m.layoutForKey(key)
		detached := m.DetachedPanes.Get()
		refs := detachedRefsInLayout(root, detached, key)
		sessions, titles := m.Sessions.Get(), m.Titles.Get()
		docs, diffs := m.Docs.Get(), m.Diffs.Get()

		rows := []ggui.Widget{
			icon("external-link").Size(32).Color(skin.ExtraMuted),
			widget.Text(pick(len(refs) > 1, T("Panes opened in separate windows"), T("Pane opened in a separate window"))).
				Size(15).Weight(ggui.WeightMedium),
			muted(widget.Text(T("Closing a pane window returns it to its original position here.")).Size(12.5)),
		}
		if len(refs) > 0 {
			var cards []ggui.Widget
			for _, ref := range refs {
				r := ref
				g := root.group(r.GroupID)
				name := Tf("Pane %d", r.GroupID)
				if g != nil && g.Active != "" {
					name = tabLabel(g.Active, sessions, titles, docs, diffs)
				}
				tabCount := 0
				if g != nil {
					tabCount = len(g.Tabs)
				}
				meta := pick(tabCount == 1, T("1 tab"), Tf("%d tabs", tabCount))
				actions := []ggui.Widget{}
				if m.paneWindows[r] != nil {
					actions = append(actions, ui.Button(T("Focus window"), func() { m.focusPaneWindow(r) }).Outline())
				}
				actions = append(actions, ui.Button(T("Return here"), func() { m.dockPane(r) }).Outline())
				cards = append(cards, widget.Box(widget.Row(
					widget.Expanded(widget.Column(
						widget.Text(name).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis(),
						muted(widget.Text(meta).Size(11.5).NoWrap()),
					).Gap(2).Stretch()),
					widget.Row(actions...).Gap(6),
				).Gap(12).Align(widget.AlignCenter)).Pad(10, 14).Radius(8).Fill(skin.Surface1).Border(1, skin.Border))
			}
			rows = append(rows, widget.Box(widget.Column(cards...).Gap(8).Stretch()).MaxWidth(440))
		}
		if len(refs) > 1 {
			rows = append(rows, ui.Button(T("Return all panes"), func() { m.returnAllPanes(key) }))
		}
		return widget.Center(widget.Column(rows...).Gap(12).Align(widget.AlignCenter))
	})
}

// Feature B: Independent Space Windows ---------------------------------------

// spaceByID finds the index (0 for allSpaceID, 1..N for user spaces) and space
// metadata in set.
func spaceIndexInSet(set spaceSet, spaceID string) (int, space, bool) {
	if spaceID == "" || spaceID == allSpaceID {
		return allSpace, space{ID: allSpaceID}, true
	}
	if i := spaceIndex(set.Spaces, spaceID); i >= 0 {
		return i + 1, set.Spaces[i], true
	}
	return -1, space{}, false
}

func (m *model) spaceTitleByID(spaceID string) string {
	set := m.spacesNow()
	if idx, _, ok := spaceIndexInSet(set, spaceID); ok {
		return set.spaceName(idx)
	}
	return T("All sessions")
}

func (m *model) spaceTitleByIDReactive(spaceID string) string {
	set := m.spaces()
	if idx, _, ok := spaceIndexInSet(set, spaceID); ok {
		return set.spaceName(idx)
	}
	return T("All sessions")
}

// openCurrentSpaceWindow opens the main window's active Space in a new OS window.
func (m *model) openCurrentSpaceWindow() {
	set := m.spacesNow()
	m.openSpaceWindow(set.spaceID(set.Space))
}

// openSpaceWindow opens spaceID (or focuses its existing window) in its own
// independent OS window without modifying the main window's active space.
func (m *model) openSpaceWindow(spaceID string) *spaceWindowState {
	if spaceID == "" {
		spaceID = allSpaceID
	}
	if _, _, ok := spaceIndexInSet(m.spacesNow(), spaceID); !ok {
		return nil
	}
	if existing := m.spaceWindows[spaceID]; existing != nil {
		if existing.win != nil {
			existing.win.Focus()
		}
		return existing
	}
	ctx := m.ws.contexts[spaceID]
	if !fits(ctx) {
		ctx = ""
	}
	initKey := layoutKey(spaceID, ctx)
	initFocus := 1
	if groups := m.peekLayoutForKey(initKey).groups(); len(groups) > 0 {
		initFocus = groups[0].ID
	}
	var initScope scopeKey
	if sec, ok := strings.CutPrefix(ctx, "project:"); ok && sec != "" {
		initScope = scopeKey{Section: sec}
	}
	sw := &spaceWindowState{
		spaceID:      spaceID,
		context:      ggui.State(ctx),
		scope:        ggui.State(initScope),
		focusGroup:   ggui.State(initFocus),
		sidebarOpen:  ggui.State(true),
		sidebarWidth: ggui.State(float64(sidebarWidth)),
		sidebarSplit: ggui.State(ggui.Peek(m.Prefs).SidebarSplit),
	}
	m.spaceWindows[spaceID] = sw
	next := maps.Clone(ggui.Peek(m.SpaceWindows))
	if next == nil {
		next = map[string]bool{}
	}
	next[spaceID] = true
	m.SpaceWindows.Set(next)
	m.attachVisible()
	if m.window == nil {
		return sw // headless probe/test
	}
	var gone bool
	closed := func() {
		if gone {
			return
		}
		gone = true
		delete(m.spaceWindows, spaceID)
		m.unregisterSecondaryWindow(sw.win)
		sw.win = nil
		cur := ggui.Peek(m.SpaceWindows)
		if cur[spaceID] {
			n := maps.Clone(cur)
			delete(n, spaceID)
			m.SpaceWindows.Set(n)
		}
	}
	w, err := m.window.App().OpenWindow(ggui.Config{
		Title:     m.spaceTitleByID(spaceID) + " — mako",
		Width:     1180,
		Height:    760,
		MinWidth:  640,
		MinHeight: 440,
		Resizable: true,
	}, func() ggui.Widget {
		w := ggui.UseWindow()
		sw.win = w
		m.registerSecondaryWindow(w)
		title := ggui.Derived(func() string { return m.spaceTitleByIDReactive(spaceID) + " — mako" })
		ggui.Watch(title, func(t string) { w.SetTitle(t) })
		exists := ggui.Derived(func() bool {
			_, _, ok := spaceIndexInSet(m.spaces(), spaceID)
			return ok
		})
		ggui.Watch(exists, func(ok bool) {
			if !ok {
				w.Close()
			}
		})
		ggui.OnCleanup(closed)
		return spaceWindowRoot(m, sw)
	})
	if err != nil {
		closed()
		if m.toaster != nil {
			m.toaster.Push(ui.Toast(T("Could not open window"), err.Error()).Destructive())
		}
		return nil
	}
	sw.win = w
	return sw
}

// closeSpaceWindow closes the OS window for spaceID if open.
func (m *model) closeSpaceWindow(spaceID string) {
	sw := m.spaceWindows[spaceID]
	if sw == nil {
		return
	}
	if sw.win != nil {
		sw.win.Close()
		return
	}
	delete(m.spaceWindows, spaceID)
	cur := ggui.Peek(m.SpaceWindows)
	if cur[spaceID] {
		n := maps.Clone(cur)
		delete(n, spaceID)
		m.SpaceWindows.Set(n)
	}
}

// Space window actions (scoped strictly to sw, never mutating main window) ---

func (m *model) spaceWindowPickScope(sw *spaceWindowState, k scopeKey) {
	if ggui.Peek(sw.scope) == k {
		k = scopeKey{}
	}
	sw.scope.Set(k)
	if k.Section != "" {
		ctx := projectContext(k.Section)
		sw.context.Set(ctx)
		m.ws.contexts[sw.spaceID] = ctx
		key := sw.peekLayoutKey()
		if groups := m.peekLayoutForKey(key).groups(); len(groups) > 0 {
			sw.focusGroup.Set(groups[0].ID)
		}
		m.spaceWindowActivateScopedSession(sw, k)
	}
}

func (m *model) spaceWindowActivateScopedSession(sw *spaceWindowState, k scopeKey) {
	if k.Section == "" {
		return
	}
	defs := m.spacesNow().Projects
	key := sw.peekLayoutKey()
	root := m.peekLayoutForKey(key)
	fg := ggui.Peek(sw.focusGroup)
	if g := root.group(fg); g != nil {
		if s, ok := m.session(g.Active); ok && k.holds(sessionScope(defs, s)) {
			return
		}
	}
	for _, g := range root.groups() {
		for _, id := range g.Tabs {
			if s, ok := m.session(id); ok && k.holds(sessionScope(defs, s)) {
				sw.focusGroup.Set(m.activateTabInKey(key, g.ID, id))
				return
			}
		}
	}
	for _, s := range ggui.Peek(m.Sessions) {
		if k.holds(sessionScope(defs, s)) {
			sw.focusGroup.Set(m.activateTabInKey(key, fg, s.GetId()))
			return
		}
	}
}

func (m *model) spaceWindowSelectSession(sw *spaceWindowState, id string) {
	if id == "" {
		return
	}
	if s, ok := m.session(id); ok {
		sec := sectionID(m.projectsNow(), s)
		ctx := projectContext(sec)
		sw.context.Set(ctx)
		m.ws.contexts[sw.spaceID] = ctx
		cur := ggui.Peek(sw.scope)
		if cur.Section != "" {
			if in := sessionScope(m.projectsNow(), s); !cur.holds(in) {
				sw.scope.Set(scopeKey{Section: in.Section})
			}
		}
	}
	key := sw.peekLayoutKey()
	focus := m.activateTabInKey(key, ggui.Peek(sw.focusGroup), id)
	sw.focusGroup.Set(focus)
}

func (m *model) spaceWindowActiveTab(sw *spaceWindowState) string {
	root := m.layoutForKey(sw.layoutKey())
	fg := sw.focusGroup.Get()
	if g := root.group(fg); g != nil {
		return g.Active
	}
	if groups := root.groups(); len(groups) > 0 {
		return groups[0].Active
	}
	return ""
}

// Space window views ---------------------------------------------------------

func spaceWindowRoot(m *model, sw *spaceWindowState) ggui.Widget {
	lang := ggui.Map(m.Prefs, func(p prefs) string { return i18n.Resolve(p.Language) })
	return ggui.Key(lang, func(code string) ggui.Widget {
		i18n.Set(code)
		return backdropped(m, windowBackdrop, spaceWindowView(m, sw))
	})
}

func spaceWindowView(m *model, sw *spaceWindowState) ggui.Widget {
	left := ggui.If(sw.sidebarOpen, func() ggui.Widget {
		return widget.Row(
			widget.Box(spaceWindowSidebar(m, sw)).BindWidth(sw.sidebarWidth),
			spaceWindowSidebarEdge(sw),
		).Stretch()
	})
	right := widget.Column(
		spaceWindowHeader(m, sw),
		hline(),
		widget.Expanded(keyedLayoutView(m, sw.layoutKey, sw.focusGroup)),
	).Stretch()
	return widget.Row(left, widget.Expanded(right)).Stretch()
}

func spaceWindowSidebarEdge(sw *spaceWindowState) ggui.Widget {
	var from, start float64
	return widget.Pointer(widget.Box(vline()).Width(panelEdgeWidth)).
		Cursor(ggui.CursorShapeEWResize).
		OnDown(func(ev ggui.PointerEvent) { from, start = ev.Pos.X, ggui.Peek(sw.sidebarWidth) }).
		OnDrag(func(ev ggui.PointerEvent) {
			sw.sidebarWidth.Set(min(max(start+ev.Pos.X-from, minSidebarWidth), maxSidebarWidth))
		})
}

func spaceWindowSidebar(m *model, sw *spaceWindowState) ggui.Widget {
	return ggui.Component(func() ggui.Widget {
		st := newSidebarState(m)
		header := ggui.Reactive(func() ggui.Widget {
			set := m.spaces()
			idx, sp, ok := spaceIndexInSet(set, sw.spaceID)
			if !ok {
				return widget.Box().Height(headerHeight)
			}
			parts := []ggui.Widget{}
			if sp.color() != nil {
				parts = append(parts, widget.Box(spaceMark(sp)).Pad(0, 2, 0, 0))
			}
			parts = append(parts, widget.Text(set.spaceName(idx)).Size(14).Weight(ggui.WeightSemibold).NoWrap().Ellipsis())
			return widget.Box(widget.Row(
				icon("messages-square").Size(17),
				widget.Expanded(widget.Row(parts...).Gap(6).Align(widget.AlignCenter)),
				iconButton("plus", T("Add a project"), m.addProjectFolder),
			).Gap(6).Align(widget.AlignCenter)).Pad(0, 8, 0, 12).Height(headerHeight)
		})
		grip := spaceWindowSidebarGrip(sw)
		body := &sidebarBody{
			top:    widget.Scroll(widget.Padding(spaceWindowProjectTree(m, st, sw), 10, 8, 10, 8)).Bar(skin.Surface3),
			grip:   grip,
			bottom: spaceWindowAgentsPanel(m, st, sw),
			split:  sw.sidebarSplit,
		}
		column := widget.Column(
			header,
			hline(),
			widget.Expanded(body),
		).Stretch()
		washed := ggui.Reactive(func() ggui.Widget {
			set := m.spaces()
			_, sp, _ := spaceIndexInSet(set, sw.spaceID)
			var tint color.NRGBA
			if c := sp.color(); c != nil {
				tint = color.NRGBAModel.Convert(c).(color.NRGBA)
			}
			return widget.Box(column).Fill(skin.Sidebar).Gradient(spaceWash(tint))
		})
		return uitheme.Override(func(t *uitheme.Theme) { t.Bg = t.Sidebar }, washed)
	})
}

func spaceWindowSidebarGrip(sw *spaceWindowState) ggui.Widget {
	hover := ggui.State(false)
	handle := ggui.View(hover, func(h bool) ggui.Widget {
		c := pick[color.Color](h, skin.Surface3, color.Transparent)
		return widget.Box(widget.Center(widget.Box().Size(32, 3).Radius(1.5).Fill(c))).Height(8)
	})
	var from, start float64
	return widget.Pointer(widget.Column(hline(), handle).Stretch()).Cursor(ggui.CursorShapeNSResize).OnHover(hover.Set).
		OnDown(func(ev ggui.PointerEvent) { from, start = ev.Pos.Y, ggui.Peek(sw.sidebarSplit) }).
		OnDrag(func(ev ggui.PointerEvent) {
			sw.sidebarSplit.Set(min(max(start+ev.Pos.Y-from, minSidebarSplit), maxSidebarSplit))
		})
}

func spaceWindowProjectTree(m *model, st sidebarState, sw *spaceWindowState) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		set := m.spaces()
		idx, sp, ok := spaceIndexInSet(set, sw.spaceID)
		if !ok {
			return widget.Box()
		}
		groups := m.spaceSections(st, idx)
		nodes := m.Nodes.Get()
		scope := sw.scope.Get()
		looks := set.Looks
		if len(groups) == 0 {
			empty := T("No projects yet. Add a folder with +.")
			if sw.spaceID != allSpaceID {
				empty = T("No projects in this space. Add a folder with +, or move a project here from its menu.")
			}
			return widget.Box(muted(widget.Text(empty).Size(12))).Pad(8)
		}
		labels := projectLabels(groups, nodes)
		spColor := sp.color()
		var rows []ggui.Widget
		sections := byOwner(groups)
		if sections == nil {
			sections = []ownerSection{{Groups: groups}}
		}
		selectedRow := func(w ggui.Widget, on bool) ggui.Widget {
			if !on {
				return w
			}
			fill := color.Color(skin.SidebarSel)
			if spColor != nil {
				fill = withAlpha(spColor, 0x26)
			}
			return widget.Box(w).Fill(fill).Radius(7)
		}
		for _, o := range sections {
			if o.Owner != "" && len(o.Groups) > 0 {
				name, host := ownerName(o.Owner)
				var end ggui.Widget
				if host != "" {
					end = widget.Text(host).Size(11.5).Color(skin.ExtraMuted).NoWrap()
				}
				rows = append(rows, treeHeading(icon(ownerGlyph(host)).Size(14), name, end))
			}
			for _, g := range o.Groups {
				grp := g
				name := cmp.Or(grp.Name, labels[grp.Key], groupName(grp))
				key := scopeKey{Section: grp.Key}
				look := lookOf(looks, grp.Key)
				title := []ggui.Widget{
					look.glyph(14),
					widget.Expanded(widget.Align(widget.Text(name).Size(13.5).
						Weight(pick(scope.Section == grp.Key, ggui.WeightSemibold, ggui.WeightMedium)).
						Color(pick[color.Color](scope.Section == grp.Key, skin.Fg, skin.Muted)).NoWrap().Ellipsis()).Left()),
				}
				if working(grp.Sessions) {
					title = append(title, workingDot())
				}
				if len(grp.Sessions) > 0 {
					title = append(title, kindBadges(grp.Sessions))
				}
				btn := ui.ButtonOf(widget.Row(title...).Gap(6).Align(widget.AlignCenter), func() {
					m.spaceWindowPickScope(sw, key)
				}).Ghost().Pad(6, 8, 6, 6).Radius(7).Name(name)
				rows = append(rows, selectedRow(btn, scope == key))
				if grp.multiRoot() {
					for j, r := range grp.Def.Roots {
						root := r
						rKey := scopeKey{Section: grp.Key, Root: rootKey(root)}
						rSel := scope == rKey
						rLabel := root.label()
						rBtn := ui.ButtonOf(widget.Row(
							hashMark(13),
							widget.Expanded(widget.Align(widget.Text(rLabel).Size(13).Color(pick[color.Color](rSel, skin.Fg, skin.Muted)).NoWrap().Ellipsis()).Left()),
						).Gap(6).Align(widget.AlignCenter), func() {
							m.spaceWindowPickScope(sw, rKey)
						}).Ghost().Pad(5, 8, 5, 6).Radius(7).Name("#" + rLabel)
						rows = append(rows, &treeBranch{child: selectedRow(rBtn, rSel), last: j == len(grp.Def.Roots)-1})
					}
				}
			}
		}
		return widget.Column(rows...).Gap(1).Stretch()
	})
}

func spaceWindowAgentsPanel(m *model, st sidebarState, sw *spaceWindowState) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		m.Unread.Get()
		set := m.spaces()
		idx, _, ok := spaceIndexInSet(set, sw.spaceID)
		if !ok {
			return widget.Box()
		}
		groups := m.spaceSections(st, idx)
		scope := sw.scope.Get()
		defs := set.Projects
		var sessions []*protocol.SessionInfo
		where := map[string]string{}
		for _, g := range groups {
			for _, s := range g.Sessions {
				if !scope.holds(sessionScope(defs, s)) {
					continue
				}
				sessions = append(sessions, s)
				where[s.GetId()] = groupName(g)
			}
		}
		v := st.view.Get()
		m.sortAgents(sessions, v.Sort)
		nodes, titles := m.Nodes.Get(), m.Titles.Get()
		activeTab := m.spaceWindowActiveTab(sw)

		head := widget.Box(widget.Row(
			widget.Text(T("Agents")).Size(13).Weight(ggui.WeightSemibold).NoWrap(),
			widget.Box(widget.Text(fmt.Sprint(len(sessions))).Size(11).Color(skin.Muted).NoWrap()).Pad(1, 6).Radius(9).Fill(skin.Surface2),
			widget.Spacer(),
			iconButton("plus", T("New session"), func() {
				node, dir := "", ""
				if scope.Section != "" {
					if at := slices.IndexFunc(groups, func(g sessionGroup) bool { return g.Key == scope.Section }); at >= 0 {
						node, dir = groups[at].NodeID, groups[at].Project
					}
				}
				m.openNewInKey(sw.peekLayoutKey(), ggui.Peek(sw.focusGroup), node, dir)
			}),
		).Gap(4).Align(widget.AlignCenter)).Pad(8, 6, 6, 14)

		var rows []ggui.Widget
		if len(sessions) == 0 {
			rows = append(rows, widget.Box(widget.Center(muted(widget.Text(T("No agents here.")).Size(12.5)))).Pad(24, 12))
		} else {
			for _, s := range sessions {
				sess := s
				sid := sess.GetId()
				label := sessionTitle(sess, titles[sid])
				sel := sid == activeTab
				line := []ggui.Widget{
					sessionAvatar(m, sess, 20),
					widget.Expanded(widget.Text(label).Size(13).NoWrap().Ellipsis()),
					widget.Box(widget.Center(&activityDot{state: m.dotFor(sid)})).Size(18, 18),
				}
				sub := where[sid]
				if n := nodeName(nodes, sess.GetNodeId()); n != "" && !m.onThisMachine(sess.GetNodeId()) {
					sub = joinNonEmpty(sub, n)
				}
				col := []ggui.Widget{widget.Row(line...).Gap(8).Align(widget.AlignCenter)}
				if !v.Compact && sub != "" {
					col = append(col, widget.Text(sub).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis())
				}
				btn := ui.ButtonOf(widget.Column(col...).Gap(2).Stretch(), func() {
					m.spaceWindowSelectSession(sw, sid)
				}).Ghost().Pad(6, 10).Radius(8).Name(label)
				var rowW ggui.Widget = btn
				if sel {
					rowW = widget.Box(btn).Fill(skin.SidebarSel).Radius(8)
				}
				rows = append(rows, sessionDrag(m, rowW, sid))
			}
		}
		list := widget.Scroll(widget.Padding(widget.Column(rows...).Gap(1).Stretch(), 0, 8, 12, 8)).Bar(skin.Surface3)
		return widget.Column(head, widget.Expanded(list)).Stretch()
	})
}

func spaceWindowHeader(m *model, sw *spaceWindowState) ggui.Widget {
	title := ggui.Reactive(func() ggui.Widget {
		id := m.spaceWindowActiveTab(sw)
		if isDocTab(id) {
			if d, ok := m.Docs.Get()[id]; ok {
				return widget.Text(d.Name()).Size(13.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()
			}
		}
		if isDiffTab(id) {
			if d, ok := m.Diffs.Get()[id]; ok {
				return widget.Text(d.Name()).Size(13.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()
			}
		}
		if s, ok := sessionIn(m.Sessions.Get(), id); ok {
			host, path := nodeName(m.Nodes.Get(), s.GetNodeId()), prettyPath(s.GetProject())
			return widget.Row(
				widget.Text(projectName(s.GetProject())).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
				whereCrumb(host, path),
			).Gap(10).Align(widget.AlignCenter)
		}
		return widget.Text(m.spaceTitleByIDReactive(sw.spaceID)).Size(13.5).Weight(ggui.WeightMedium).NoWrap()
	})
	return widget.Box(widget.Row(
		iconButton("panel-left", T("Toggle sidebar"), func() { ggui.Toggle(sw.sidebarOpen) }),
		widget.Expanded(title),
	).Gap(8).Align(widget.AlignCenter)).Pad(0, 10, 0, 8).Height(headerHeight)
}

// Keyed layout & pane views (used by Space windows and Detached pane windows) -

func paneGroupForKey(m *model, key string, id int) ggui.Readable[tabGroup] {
	return ggui.Derived(func() tabGroup {
		if g := m.layoutForKey(key).group(id); g != nil {
			return *g
		}
		return tabGroup{ID: id}
	}).WithEqual(func(a, b tabGroup) bool { return reflect.DeepEqual(a, b) })
}

func keyedLayoutView(m *model, keyFn func() string, focusGroup *ggui.StateValue[int]) ggui.Widget {
	type layoutKeyShape struct {
		Key   string
		Shape string
	}
	ks := ggui.Derived(func() layoutKeyShape {
		key := keyFn()
		vis := visibleLayoutNode(m.layoutForKey(key), m.DetachedPanes.Get(), key)
		if vis == nil {
			return layoutKeyShape{Key: key, Shape: ""}
		}
		return layoutKeyShape{Key: key, Shape: layoutShape(vis)}
	})
	return widget.Stack(
		ggui.Key(ks, func(s layoutKeyShape) ggui.Widget {
			if s.Shape == "" {
				return detachedPanesPlaceholder(m, s.Key)
			}
			vis := visibleLayoutNode(m.peekLayoutForKey(s.Key), ggui.Peek(m.DetachedPanes), s.Key)
			if vis == nil {
				return detachedPanesPlaceholder(m, s.Key)
			}
			return keyedLayoutNodeView(m, s.Key, vis, focusGroup)
		}),
		&dragGhostOverlay{m: m},
	).Expand()
}

func keyedLayoutNodeView(m *model, key string, n *layoutNode, focusGroup *ggui.StateValue[int]) ggui.Widget {
	if n.Group != nil {
		return keyedPaneView(m, key, n.Group.ID, focusGroup)
	}
	id := n.ID
	ratio := ggui.Bind(func() float64 {
		if nd := m.layoutForKey(key).node(id); nd != nil {
			return nd.Ratio
		}
		return 0.5
	}, func(r float64) { m.setRatioForKey(key, id, r) })
	split := ui.Resizable(ratio, keyedLayoutNodeView(m, key, n.First, focusGroup), keyedLayoutNodeView(m, key, n.Second, focusGroup)).
		MinSizes(160, 120).Hairline().Name(T("Pane divider"))
	if n.Vertical {
		split = split.Vertical()
	}
	return split
}

func keyedPaneView(m *model, key string, id int, focusGroup *ggui.StateValue[int]) ggui.Widget {
	group := paneGroupForKey(m, key, id)
	return paneWatch(widget.Column(
		keyedPaneTabBar(m, key, id, group, false, nil, focusGroup),
		hline(),
		keyedAgentLane(m, key, id, group, focusGroup),
		widget.Expanded(widget.Stack(keyedPaneBody(m, key, id, group, focusGroup), &paneGlow{on: func() bool {
			return m.Prefs.Get().PaneGlow && focusGroup != nil && focusGroup.Get() == id
		}}).Expand()),
	).Stretch(), func() {
		if focusGroup != nil {
			focusGroup.Set(id)
		}
	})
}

func keyedPaneTabBar(m *model, key string, id int, group ggui.Readable[tabGroup], detached bool, onDock func(), focusGroup *ggui.StateValue[int]) ggui.Widget {
	tabs := ggui.Reactive(func() ggui.Widget {
		g := group.Get()
		sessions := m.Sessions.Get()
		titles := m.Titles.Get()
		docs, diffs := m.Docs.Get(), m.Diffs.Get()
		focused := detached || focusGroup == nil || focusGroup.Get() == id
		index := indexSessions(sessions)
		hosts := index.tabHosts(g.Tabs)
		active := cmp.Or(hosts[g.Active], g.Active)

		nodeID := m.Conn.Get().Self.GetId()
		project := m.launchProject(nodeID)
		if cur, ok := sessionIn(sessions, g.Active); ok {
			nodeID, project = cur.GetNodeId(), cur.GetProject()
		} else if d, ok := docs[g.Active]; ok && d.Root.Path != "" {
			nodeID, project = d.Root.Node, d.Root.Path
		} else if d, ok := diffs[g.Active]; ok && d.Root.Path != "" {
			nodeID, project = d.Root.Node, d.Root.Path
		}

		var children []ggui.Widget
		for idx, tabID := range g.Tabs {
			if hosts[tabID] != "" {
				continue
			}
			tid := tabID
			tabIdx := idx
			label := tabLabel(tid, sessions, titles, docs, diffs)
			selected := tid == active
			fg := pick[color.Color](selected, skin.Fg, skin.Muted)
			var glyph ggui.Widget
			switch {
			case isLaunchTab(tid):
				glyph = icon("plus").Size(14).Color(fg)
			case docs[tid].Path != "":
				glyph = icon(fileIconName(docs[tid].Name())).Size(14).Color(fg)
			case diffs[tid].File != nil:
				glyph = statusIcon(diffs[tid].File.GetStatus())
			default:
				s, _ := sessionIn(sessions, tid)
				glyph = agentGlyph(s.GetAgent(), fg).Size(14)
				if kids := index.spawned(tid); len(kids) > 0 {
					glyph = widget.Row(glyph, familyMarks(m, kids)).Gap(6).Align(widget.AlignCenter)
				}
			}
			t := ui.EditorTab(label, func() {
				f := m.activateTabInKey(key, id, tid)
				if focusGroup != nil {
					focusGroup.Set(f)
				}
			}).Icon(glyph).MaxLabelWidth(168).Selected(selected).Current(focused).
				OnClose(func() { m.closeTabInKey(key, tid) }).CloseName(Tf("Close %s", label))
			if isSessionTab(tid) {
				dot := m.dotFor(tid)
				t = t.Status(&activityDot{state: dot, quiet: true}).StatusWhile(func() bool { return dot() != dotRest })
			}
			var cell ggui.Widget = t
			if !detached {
				cell = keyedTabDrop(m, key, tabBarDrag(m, t, tid), id, func(d widget.Dropped[tabDrag]) int {
					if d.Pos.X >= d.Size.W/2 {
						return tabIdx + 1
					}
					return tabIdx
				})
			}
			children = append(children, cell)
		}

		bar := widget.Scroll(widget.Box(widget.Row(children...).Gap(2).Align(widget.AlignCenter)).Pad(0, 4).Height(tabBarHeight)).
			Horizontal().Shrink().Bar(color.Transparent).Overscroll(widget.OverscrollBounce).FadeEdges(24)

		plus := widget.Box(widget.Column(
			iconButton("plus", T("New session…"), func() {
				if focusGroup != nil {
					focusGroup.Set(id)
				}
				m.openNewInKey(key, id, nodeID, project)
			}),
		).Justify(widget.JustifyCenter)).Height(tabBarHeight).Pad(0, 4)

		var endBtn ggui.Widget
		if detached {
			endBtn = widget.Box(widget.Column(
				ui.Tooltip(ui.ButtonOf(widget.Row(
					icon("arrow-up-to-line").Size(14),
					widget.Text(T("Return")).Size(12).NoWrap(),
				).Gap(5).Align(widget.AlignCenter), onDock).Ghost().Pad(4, 8).Radius(6).Name(T("Return to main window")),
					T("Return to main window")),
			).Justify(widget.JustifyCenter)).Height(tabBarHeight).Pad(0, 6, 0, 2)
		} else if len(g.Tabs) > 0 {
			ref := paneRef{Key: key, GroupID: id}
			endBtn = widget.Box(widget.Column(
				iconButton("external-link", T("Open pane in new window"), func() { m.popoutPane(ref) }),
			).Justify(widget.JustifyCenter)).Height(tabBarHeight).Pad(0, 4, 0, 0)
		}

		tabsAndPlus := ggui.Widget(widget.Row(widget.Flexible(bar), plus).Stretch())
		if !detached {
			end := len(g.Tabs)
			tabsAndPlus = keyedTabDrop(m, key, tabsAndPlus, id, func(widget.Dropped[tabDrag]) int { return end })
		}
		row := []ggui.Widget{
			widget.Expanded(tabsAndPlus),
		}
		if endBtn != nil {
			row = append(row, endBtn)
		}
		return widget.Row(row...).Stretch()
	})
	return widget.Box(tabs).Height(tabBarHeight)
}

func keyedAgentLane(m *model, key string, id int, group ggui.Readable[tabGroup], focusGroup *ggui.StateValue[int]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		g := group.Get()
		titles := m.Titles.Get()
		if !isSessionTab(g.Active) {
			return widget.Box()
		}
		x := indexSessions(m.Sessions.Get())
		host := cmp.Or(x.hostIn(g.Tabs, g.Active), g.Active)
		agents := x.family(host)
		if len(agents) < 2 {
			return widget.Box()
		}
		var chips []ggui.Widget
		for i, s := range agents {
			sid := s.GetId()
			on := sid == g.Active
			fg := pick[color.Color](on, skin.Fg, skin.Muted)
			title := sessionTitle(s, titles[sid])
			label := widget.Text(title).Size(12.5).Color(fg).NoWrap()
			if on {
				label.Weight(ggui.WeightMedium)
			}
			face := widget.Row(
				agentGlyph(s.GetAgent(), fg).Size(13),
				label,
				&activityDot{state: m.dotFor(sid), quiet: true},
			).Gap(6).Align(widget.AlignCenter)
			chip := ui.ButtonOf(face, func() {
				f := m.activateTabInKey(key, id, sid)
				if focusGroup != nil {
					focusGroup.Set(f)
				}
			}).Ghost().Pad(4, 9).Radius(7).Name(title)
			var w ggui.Widget = chip
			if on {
				w = widget.Box(chip).Fill(skin.Surface2).Radius(7)
			}
			chips = append(chips, w)
			if i == 0 {
				chips = append(chips, widget.Box(icon("git-fork").Size(14).Color(skin.ExtraMuted)).Pad(0, 2))
			}
		}
		row := widget.Scroll(widget.Box(widget.Row(chips...).Gap(2).Align(widget.AlignCenter)).Pad(0, 8)).
			Horizontal().Shrink().Bar(color.Transparent).FadeEdges(20)
		return widget.Column(widget.Box(row).Height(laneHeight), hline()).Stretch()
	})
}

func keyedPaneBody(m *model, key string, id int, group ggui.Readable[tabGroup], focusGroup *ggui.StateValue[int]) ggui.Widget {
	current := ggui.Derived(func() pane {
		tab := group.Get().Active
		return pane{ID: tab, Ready: !isSessionTab(tab) || slices.Contains(m.Attached.Get(), tab)}
	})
	content := ggui.Key(current, func(p pane) ggui.Widget {
		switch {
		case p.ID == "":
			return widget.Center(widget.Column(
				icon("square-terminal").Size(36).Color(skin.ExtraMuted),
				widget.Text(T("No session open")).Size(16).Weight(ggui.WeightMedium),
				muted(widget.Text(T("Pick a session in the sidebar, or start an agent on any node in the mesh.")).Size(13)),
				widget.Box().Height(4),
				ui.Button(T("New session"), func() { m.openNewInKey(key, id, "", "") }),
			).Gap(10).Align(widget.AlignCenter))
		case isDocTab(p.ID):
			if m.docs[p.ID] == nil {
				return widget.Box()
			}
			return docView(m, p.ID, focusGroup == nil || ggui.Peek(focusGroup) == id)
		case isDiffTab(p.ID):
			return diffTabView(m, p.ID)
		case isLaunchTab(p.ID):
			return launchView(m, p.ID)
		case !p.Ready:
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
	if focusGroup == nil {
		return content
	}
	return widget.Stack(
		content,
		ggui.If(m.Dragging, func() ggui.Widget { return newKeyedDropZones(m, key, id) }),
	).Expand()
}

func keyedTabDrop(m *model, key string, w ggui.Widget, group int, index func(widget.Dropped[tabDrag]) int) *widget.DropZoneWidget[tabDrag] {
	return widget.DropZone(w, func(d widget.Dropped[tabDrag]) {
		m.endDrag()
		idx := index(d)
		if len(d.Value.Tabs) > 0 {
			m.moveTabsInKey(key, d.Value.Tabs, group, idx)
			if d.Value.FromGroup != "" {
				m.clearFold(d.Value.FromGroup)
			}
			return
		}
		m.moveTabInKey(key, d.Value.ID, group, idx)
	}).Accept(func(d tabDrag) bool { return len(d.ids()) > 0 })
}

func newKeyedDropZones(m *model, key string, group int) *dropZones {
	z := &dropZones{hint: ggui.State(dropEdge(-1))}
	for e := range z.zones {
		edge := dropEdge(e)
		z.zones[e] = widget.DropZone(widget.Box(), func(d widget.Dropped[tabDrag]) {
			m.endDrag()
			if len(d.Value.Tabs) > 0 {
				m.splitTabsInKey(key, d.Value.Tabs, group, edge)
				if d.Value.FromGroup != "" {
					m.clearFold(d.Value.FromGroup)
				}
				return
			}
			m.splitTabInKey(key, d.Value.ID, group, edge)
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

// attachVisibleMultiWindow attaches active session tabs across all detached
// pane windows and open Space windows.
func (m *model) attachVisibleMultiWindow() {
	attachActive := func(id string) {
		if id == "" {
			return
		}
		conn := m.conns[id]
		switch {
		case conn != nil && !conn.failed():
			m.heldUp(id)
		case conn != nil && !m.sessionLive(id):
		default:
			if _, ok := m.session(id); ok && m.redialDue(id) {
				m.attach(id)
			}
		}
	}
	for ref := range ggui.Peek(m.DetachedPanes) {
		if g := m.peekLayoutForKey(ref.Key).group(ref.GroupID); g != nil {
			attachActive(g.Active)
		}
	}
	for _, sw := range m.spaceWindows {
		if sw == nil {
			continue
		}
		for _, g := range m.peekLayoutForKey(sw.peekLayoutKey()).groups() {
			attachActive(g.Active)
		}
	}
}

// openSpaceWindowMenu builds the "Open in new window" submenu items for spaces.
func openSpaceWindowItems(m *model, p spaceSet) []ggui.Widget {
	open := m.SpaceWindows.Get()
	items := make([]ggui.Widget, 0, len(p.Spaces)+1)
	for i := p.firstSpace(); i <= len(p.Spaces); i++ {
		id := p.spaceID(i)
		item := ui.MenuItem(p.spaceName(i), func() { m.openSpaceWindow(id) }).Checked(open[id])
		if sp, ok := p.spaceOf(i); ok {
			item = item.Icon(spaceMark(sp))
		}
		items = append(items, item)
	}
	return items
}
