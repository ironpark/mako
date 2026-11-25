package gui

import (
	"slices"

	"github.com/ironpark/ggui"
)

// The workspace is a tree of panes. Each pane is a group of tabs showing
// one of them; a split puts two subtrees beside or above each other. A tab
// dragged onto a pane's edge splits the pane there, onto its middle or its
// tab bar moves it into the pane, and a pane left without tabs closes.
//
// m.Tabs lists every tab in the tree, in order, and m.Selected is the tab
// the current pane shows: what the header, the composer and the right
// panel follow.

// tabGroup is one pane: its tabs, in order, and the one it shows.
type tabGroup struct {
	ID     int
	Tabs   []string
	Active string
}

// layoutNode is a pane, when Group is set, or a split of two subtrees.
type layoutNode struct {
	ID            int
	Group         *tabGroup
	Vertical      bool    // Second is below First rather than beside it
	Ratio         float64 // First's share of the split
	First, Second *layoutNode
}

// dropEdge is where on a pane a tab is dropped.
type dropEdge int

const (
	dropCenter dropEdge = iota
	dropLeft
	dropRight
	dropTop
	dropBottom
)

// tabDrag is what a dragged tab, a session dragged from the sidebar, or a
// project chip's run of tabs carries.
type tabDrag struct {
	ID        string
	Tabs      []string
	FromGroup string
	Project   string
}

func (d tabDrag) ids() []string {
	if len(d.Tabs) > 0 {
		return d.Tabs
	}
	if d.ID != "" {
		return []string{d.ID}
	}
	return nil
}

func tabDragEqual(a, b tabDrag) bool {
	return a.ID == b.ID && a.FromGroup == b.FromGroup && a.Project == b.Project && slices.Equal(a.Tabs, b.Tabs)
}

func newLayout() *layoutNode {
	return &layoutNode{ID: 1, Group: &tabGroup{ID: 1}}
}

func (n *layoutNode) clone() *layoutNode {
	if n == nil {
		return nil
	}
	c := *n
	if n.Group != nil {
		g := *n.Group
		g.Tabs = slices.Clone(g.Tabs)
		c.Group = &g
	}
	c.First, c.Second = n.First.clone(), n.Second.clone()
	return &c
}

// groups lists the panes in order, left to right and top to bottom.
func (n *layoutNode) groups() []*tabGroup {
	var out []*tabGroup
	for _, l := range n.leaves() {
		out = append(out, l.Group)
	}
	return out
}

func (n *layoutNode) group(id int) *tabGroup {
	for _, g := range n.groups() {
		if g.ID == id {
			return g
		}
	}
	return nil
}

// groupOf is the pane holding tab.
func (n *layoutNode) groupOf(tab string) *tabGroup {
	for _, g := range n.groups() {
		if slices.Contains(g.Tabs, tab) {
			return g
		}
	}
	return nil
}

func (n *layoutNode) node(id int) *layoutNode {
	if n == nil {
		return nil
	}
	if n.ID == id {
		return n
	}
	if c := n.First.node(id); c != nil {
		return c
	}
	return n.Second.node(id)
}

func (n *layoutNode) maxID() int {
	if n == nil {
		return 0
	}
	id := max(n.ID, n.First.maxID(), n.Second.maxID())
	if n.Group != nil {
		id = max(id, n.Group.ID)
	}
	return id
}

// prune closes panes left without tabs, a split giving way to its other
// side, but keeps the last pane.
func (n *layoutNode) prune() *layoutNode {
	if n.Group != nil {
		return n
	}
	first, second := n.First.prune(), n.Second.prune()
	switch {
	case empty(first):
		return second
	case empty(second):
		return first
	}
	n.First, n.Second = first, second
	return n
}

func empty(n *layoutNode) bool { return n.Group != nil && len(n.Group.Tabs) == 0 }

// remove takes tab out of its pane, which then shows the tab beside it.
func (n *layoutNode) remove(tab string) {
	g := n.groupOf(tab)
	if g == nil {
		return
	}
	i := slices.Index(g.Tabs, tab)
	g.Tabs = slices.Delete(g.Tabs, i, i+1)
	if g.Active == tab {
		g.Active = ""
		if len(g.Tabs) > 0 {
			g.Active = g.Tabs[min(i, len(g.Tabs)-1)]
		}
	}
}

// removeTabs takes out the tabs drop says, leaving panes without tabs for
// prune. If a pane's active tab is dropped, the nearest preceding kept tab
// (or the first kept tab) becomes active so stripping transient tabs does
// not jump to tab 0.
func (n *layoutNode) removeTabs(drop func(id string) bool) {
	for _, g := range n.groups() {
		kept := make([]string, 0, len(g.Tabs))
		activeDropped := false
		prevKept := ""
		for _, id := range g.Tabs {
			if drop(id) {
				if id == g.Active {
					activeDropped = true
				}
				continue
			}
			if !activeDropped {
				prevKept = id
			}
			kept = append(kept, id)
		}
		if len(kept) == 0 {
			g.Tabs, g.Active = nil, ""
			continue
		}
		g.Tabs = kept
		if !slices.Contains(g.Tabs, g.Active) {
			if prevKept != "" {
				g.Active = prevKept
			} else {
				g.Active = g.Tabs[0]
			}
		}
	}
}

func (g *tabGroup) insert(tab string, i int) {
	i = min(max(i, 0), len(g.Tabs))
	g.Tabs = slices.Insert(g.Tabs, i, tab)
	g.Active = tab
}

func (g *tabGroup) insertAll(tabs []string, i int, active string) {
	if len(tabs) == 0 {
		return
	}
	i = min(max(i, 0), len(g.Tabs))
	g.Tabs = slices.Insert(g.Tabs, i, tabs...)
	if active != "" && slices.Contains(tabs, active) {
		g.Active = active
	} else {
		g.Active = tabs[0]
	}
}

// removeSet drops every tab in drop from every pane while keeping a
// neighbouring active tab in panes that retain other tabs.
func (n *layoutNode) removeSet(drop map[string]bool) {
	for _, g := range n.groups() {
		activeIdx := slices.Index(g.Tabs, g.Active)
		activeDropped := g.Active != "" && drop[g.Active]
		kept := make([]string, 0, len(g.Tabs))
		nextActive := ""
		for i, id := range g.Tabs {
			if drop[id] {
				continue
			}
			if activeDropped && i > activeIdx && nextActive == "" {
				nextActive = id
			}
			kept = append(kept, id)
		}
		g.Tabs = kept
		if len(kept) == 0 {
			g.Active = ""
		} else if activeDropped || !slices.Contains(kept, g.Active) {
			if nextActive != "" {
				g.Active = nextActive
			} else {
				g.Active = kept[len(kept)-1]
			}
		}
	}
}

func (n *layoutNode) split(tab string, target int, edge dropEdge) {
	n.splitAll([]string{tab}, tab, target, edge)
}

// splitAll puts tabs in a new pane at edge of the pane with ID target.
func (n *layoutNode) splitAll(tabs []string, active string, target int, edge dropEdge) {
	if len(tabs) == 0 {
		return
	}
	leaves := n.leaves()
	i := slices.IndexFunc(leaves, func(l *layoutNode) bool { return l.Group.ID == target })
	if i < 0 {
		return
	}
	leaf := leaves[i]
	id := n.maxID() + 1
	act := tabs[0]
	if active != "" && slices.Contains(tabs, active) {
		act = active
	}
	fresh := &layoutNode{ID: id, Group: &tabGroup{ID: id + 1, Tabs: slices.Clone(tabs), Active: act}}
	old := &layoutNode{ID: id + 2, Group: leaf.Group}
	*leaf = layoutNode{ID: leaf.ID, Ratio: 0.5, Vertical: edge == dropTop || edge == dropBottom}
	if edge == dropLeft || edge == dropTop {
		leaf.First, leaf.Second = fresh, old
	} else {
		leaf.First, leaf.Second = old, fresh
	}
}

func (n *layoutNode) leaves() []*layoutNode {
	if n == nil {
		return nil
	}
	if n.Group != nil {
		return []*layoutNode{n}
	}
	return append(n.First.leaves(), n.Second.leaves()...)
}

// Model ---------------------------------------------------------------------------

// editLayout changes a copy of the layout and publishes it, with the tabs
// and the current pane's tab that follow from it. fn returns the pane to
// make current, or 0 to keep the current one if it is still there. Each
// pane's tabs of one project are then put side by side.
func (m *model) editLayout(fn func(root *layoutNode) int) {
	root := ggui.Peek(m.Layout).clone()
	focus := fn(root)
	root = root.prune()
	var all []string
	for _, g := range root.groups() {
		all = append(all, g.Tabs...)
	}
	key := m.tabKeys(all)
	for _, g := range root.groups() {
		g.Tabs = tabsByProject(g.Tabs, key)
	}
	if focus == 0 || root.group(focus) == nil {
		focus = ggui.Peek(m.FocusGroup)
	}
	if root.group(focus) == nil {
		focus = root.groups()[0].ID
	}
	var tabs []string
	for _, g := range root.groups() {
		tabs = append(tabs, g.Tabs...)
	}
	active := root.group(focus).Active
	m.Layout.Set(root)
	m.FocusGroup.Set(focus)
	m.Tabs.Set(tabs)
	m.Selected.Set(active)
	m.syncScopeToTab(active, false)
	m.bumpWorkspaceRev()
}

// focusGroup makes a pane current.
func (m *model) focusGroup(id int) {
	if ggui.Peek(m.FocusGroup) != id {
		m.editLayout(func(*layoutNode) int { return id })
	}
}

// showTab shows tab in its pane and makes that pane current, opening it
// in the current pane when it is in none; an agent another spawned, in
// the pane of the tab it shows in, after the last of that tab's agents.
func (m *model) showTab(id string) {
	m.editLayout(func(root *layoutNode) int {
		g := root.groupOf(id)
		if g == nil {
			var at int
			g, at = indexSessions(ggui.Peek(m.Sessions)).familyPlace(root, id)
			if g == nil {
				g = root.group(ggui.Peek(m.FocusGroup))
				if g == nil {
					g = root.groups()[0]
				}
				at = slices.Index(g.Tabs, g.Active) + 1
			}
			g.insert(id, at)
		}
		g.Active = id
		return g.ID
	})
	if isSessionTab(id) {
		m.lastSession = id
	}
	m.syncScopeToTab(id, true)
	m.attach(id)
}

// replaceTab puts tab in old's place, in its pane and at its index, as a
// preview's tab takes the next preview.
func (m *model) replaceTab(old, tab string) {
	m.editLayout(func(root *layoutNode) int {
		g := root.groupOf(old)
		if g == nil {
			return 0
		}
		i := slices.Index(g.Tabs, old)
		g.Tabs[i] = tab
		g.Active = tab
		return g.ID
	})
}

// moveTab puts a tab into pane group before index i of its tabs as they
// are now, opening it if it is in no pane.
func (m *model) moveTab(id string, group, i int) {
	m.editLayout(func(root *layoutNode) int {
		target := root.group(group)
		if target == nil {
			return 0
		}
		if from := slices.Index(target.Tabs, id); from >= 0 && from < i {
			i-- // its own place closes up first
		}
		root.remove(id)
		target.insert(id, i)
		return group
	})
	m.attach(id)
}

func cleanTabIDs(ids []string) ([]string, map[string]bool) {
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, seen
}

func preferredActiveTab(root *layoutNode, focusGroup int, idSet map[string]bool) string {
	if g := root.group(focusGroup); g != nil && idSet[g.Active] {
		return g.Active
	}
	for _, g := range root.groups() {
		if idSet[g.Active] {
			return g.Active
		}
	}
	return ""
}

// moveTabs moves a group of tabs (such as a project chip's run) in order into
// pane group before index i of its current tabs.
func (m *model) moveTabs(ids []string, group, i int) {
	clean, idSet := cleanTabIDs(ids)
	if len(clean) == 0 {
		return
	}
	focus := ggui.Peek(m.FocusGroup)
	m.editLayout(func(root *layoutNode) int {
		target := root.group(group)
		if target == nil {
			return 0
		}
		active := preferredActiveTab(root, focus, idSet)
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
	m.attachVisible()
}

// splitTab moves a tab into a new pane at edge of pane group. A pane's only
// tab has nothing to split from, so it stays.
func (m *model) splitTab(id string, group int, edge dropEdge) {
	if edge == dropCenter {
		g := ggui.Peek(m.Layout).group(group)
		if g != nil {
			m.moveTab(id, group, len(g.Tabs))
		}
		return
	}
	if g := ggui.Peek(m.Layout).group(group); g == nil || (len(g.Tabs) == 1 && g.Tabs[0] == id) {
		return
	}
	m.editLayout(func(root *layoutNode) int {
		root.remove(id)
		root.split(id, group, edge)
		return root.groupOf(id).ID
	})
	m.attach(id)
}

// splitTabs moves a project chip's run of tabs into a new split pane at edge of
// pane group. Splitting all of a pane's own tabs from itself is a no-op.
func (m *model) splitTabs(ids []string, group int, edge dropEdge) {
	clean, idSet := cleanTabIDs(ids)
	if len(clean) == 0 {
		return
	}
	if edge == dropCenter {
		if g := ggui.Peek(m.Layout).group(group); g != nil {
			m.moveTabs(clean, group, len(g.Tabs))
		}
		return
	}
	g := ggui.Peek(m.Layout).group(group)
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
	focus := ggui.Peek(m.FocusGroup)
	m.editLayout(func(root *layoutNode) int {
		active := preferredActiveTab(root, focus, idSet)
		root.removeSet(idSet)
		root.splitAll(clean, active, group, edge)
		return root.groupOf(clean[0]).ID
	})
	m.attachVisible()
}

// setRatio is a split's divider moving.
func (m *model) setRatio(node int, r float64) {
	root := ggui.Peek(m.Layout)
	if n := root.node(node); n == nil || n.Ratio == r {
		return
	}
	next := root.clone()
	next.node(node).Ratio = r
	m.Layout.Set(next)
	m.bumpWorkspaceRev()
}
