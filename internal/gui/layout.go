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

// tabDrag is what a dragged tab, or a session dragged from the sidebar,
// carries.
type tabDrag struct{ ID string }

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

// insert puts tab into pane g at index i, clamped, and shows it.
// removeTabs takes out the tabs drop says, leaving panes without tabs for
// prune.
func (n *layoutNode) removeTabs(drop func(id string) bool) {
	for _, g := range n.groups() {
		g.Tabs = slices.DeleteFunc(g.Tabs, drop)
		if !slices.Contains(g.Tabs, g.Active) {
			g.Active = ""
			if len(g.Tabs) > 0 {
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

// split puts tab in a new pane at edge of the pane with ID target.
func (n *layoutNode) split(tab string, target int, edge dropEdge) {
	leaves := n.leaves()
	i := slices.IndexFunc(leaves, func(l *layoutNode) bool { return l.Group.ID == target })
	if i < 0 {
		return
	}
	leaf := leaves[i]
	id := n.maxID() + 1
	fresh := &layoutNode{ID: id, Group: &tabGroup{ID: id + 1, Tabs: []string{tab}, Active: tab}}
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
// make current, or 0 to keep the current one if it is still there.
func (m *model) editLayout(fn func(root *layoutNode) int) {
	root := ggui.Peek(m.Layout).clone()
	focus := fn(root)
	root = root.prune()
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
	m.Layout.Set(root)
	m.FocusGroup.Set(focus)
	m.Tabs.Set(tabs)
	m.Selected.Set(root.group(focus).Active)
}

// focusGroup makes a pane current.
func (m *model) focusGroup(id int) {
	if ggui.Peek(m.FocusGroup) != id {
		m.editLayout(func(*layoutNode) int { return id })
	}
}

// showTab shows tab in its pane and makes that pane current, opening it
// in the current pane when it is in none.
func (m *model) showTab(id string) {
	m.editLayout(func(root *layoutNode) int {
		g := root.groupOf(id)
		if g == nil {
			g = root.group(ggui.Peek(m.FocusGroup))
			if g == nil {
				g = root.groups()[0]
			}
			g.insert(id, slices.Index(g.Tabs, g.Active)+1)
		}
		g.Active = id
		return g.ID
	})
	if isSessionTab(id) {
		m.lastSession = id
	}
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

// setRatio is a split's divider moving.
func (m *model) setRatio(node int, r float64) {
	root := ggui.Peek(m.Layout)
	if n := root.node(node); n == nil || n.Ratio == r {
		return
	}
	next := root.clone()
	next.node(node).Ratio = r
	m.Layout.Set(next)
}
