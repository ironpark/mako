package gui

import (
	"maps"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/layoutdoc"
)

// What a document sets is the window's defaults; what this device made of
// them is kept apart, by node ID, and is never written into the document:
// whether a collapsible node is open, the size its edge was dragged to,
// and the tab a stack shows. The sidebar's and the right panel's are kept
// as they were before the document, in the model's fields and gui.json's
// sidebar_width and details_width, so a window of an older build reads the
// same widths; every other node's size is kept in gui.json's layout_sizes.
// A size of 0 is none kept: the document's applies.

// nodeState is a node's state on this device.
type nodeState struct {
	open   *ggui.StateValue[bool]
	size   *ggui.StateValue[float64]
	active *ggui.StateValue[string] // a stack's block shown
	lo, hi float64                  // the size an edge drags within
	kept   func(p prefs) float64    // the size kept, 0 for none
	keep   func(p *prefs, size float64)
}

// The bounds an edge drags a node's size within, but for the sidebar's
// and the right panel's, which keep their own.
const minPanelSize, maxPanelSize = 80, 1600

// builtinNodeStates are the states of the sidebar and the right panel,
// the model's own, whatever document places them.
func (m *model) builtinNodeStates() map[string]*nodeState {
	return map[string]*nodeState{
		layoutdoc.SidebarNode: {open: m.SidebarOpen, size: m.SidebarWidth, active: ggui.State(""), lo: minSidebarWidth, hi: maxSidebarWidth,
			kept: func(p prefs) float64 { return p.SidebarWidth },
			keep: func(p *prefs, w float64) { p.SidebarWidth = w }},
		layoutdoc.DetailsNode: {open: m.DetailsOpen, size: m.DetailsWidth, active: m.DetailTab, lo: minDetailsWidth, hi: maxDetailsWidth,
			kept: func(p prefs) float64 { return p.DetailsWidth },
			keep: func(p *prefs, w float64) { p.DetailsWidth = w }},
	}
}

// nodeState is n's state, made the first time it is asked for.
func (m *model) nodeState(n *layoutdoc.Node) *nodeState {
	if st, ok := m.winStates[n.ID]; ok {
		return st
	}
	id := n.ID
	st := &nodeState{open: ggui.State(true), active: ggui.State(n.ActiveBlock()), lo: minPanelSize, hi: maxPanelSize,
		kept: func(p prefs) float64 { return p.LayoutSizes[id] },
		keep: func(p *prefs, size float64) {
			p.LayoutSizes = maps.Clone(p.LayoutSizes)
			if p.LayoutSizes == nil {
				p.LayoutSizes = map[string]float64{}
			}
			p.LayoutSizes[id] = size
		}}
	st.size = ggui.State(st.resting(n, ggui.Peek(m.Prefs)))
	m.winStates[id] = st
	return st
}

// resting is the size n takes until its edge is dragged: the one kept, or
// the document's, within the edge's bounds.
func (st *nodeState) resting(n *layoutdoc.Node, p prefs) float64 {
	size := st.kept(p)
	if size == 0 {
		size = n.Size
	}
	return min(max(size, st.lo), st.hi)
}

// restingSize is the size the node of the ID takes until its edge is
// dragged, or fallback when the document does not size it.
func (m *model) restingSize(id string, fallback float64) float64 {
	n := ggui.Peek(m.WinDoc).Node(id)
	if n == nil || !n.Sized() {
		return fallback
	}
	return m.nodeState(n).resting(n, ggui.Peek(m.Prefs))
}

// setWindowDoc shows d: the sizes not dragged on this device follow it,
// a stack shows d's active tab where d changed the stack or what it
// showed is gone, and every node but the sidebar and the right panel
// starts afresh.
func (m *model) setWindowDoc(d *layoutdoc.Doc) {
	prev := ggui.Peek(m.WinDoc)
	for id := range m.winStates {
		if id != layoutdoc.SidebarNode && id != layoutdoc.DetailsNode {
			delete(m.winStates, id)
		}
	}
	p := ggui.Peek(m.Prefs)
	for _, id := range []string{layoutdoc.SidebarNode, layoutdoc.DetailsNode} {
		st, n := m.winStates[id], d.Node(id)
		if n == nil {
			continue
		}
		if n.Sized() {
			st.size.Set(st.resting(n, p))
		}
		if n.Kind == layoutdoc.KindStack && (changedStack(prev.Node(id), n) || layoutdoc.StackIndex(n, ggui.Peek(st.active)) == n.Active) {
			st.active.Set(n.ActiveBlock())
		}
	}
	m.WinDoc.Set(d)
}

// changedStack says whether stack n differs from what it was, p: its
// blocks or the one it shows first.
func changedStack(p, n *layoutdoc.Node) bool {
	if p == nil || p.Kind != n.Kind || p.Active != n.Active || len(p.Children) != len(n.Children) {
		return true
	}
	for i, c := range n.Children {
		if p.Children[i].Block != c.Block {
			return true
		}
	}
	return false
}

// blockShown says whether the window shows the block: placed by the
// document, every collapsible node it is in open, and every stack showing
// it. Read in an effect, it follows them all.
func (m *model) blockShown(name string) bool {
	path := m.WinDoc.Get().BlockPath(name)
	if path == nil {
		return false
	}
	for i, n := range path {
		if n.Collapsible && !m.nodeState(n).open.Get() {
			return false
		}
		if n.Kind == layoutdoc.KindStack && n.Children[layoutdoc.StackIndex(n, m.nodeState(n).active.Get())] != path[i+1] {
			return false
		}
	}
	return true
}

// revealBlock shows the block wherever the document placed it: the nodes
// it is in opened, and its stack showing it.
func (m *model) revealBlock(name string) {
	path := ggui.Peek(m.WinDoc).BlockPath(name)
	for i, n := range path {
		if n.Collapsible {
			m.nodeState(n).open.Set(true)
		}
		if n.Kind == layoutdoc.KindStack {
			m.nodeState(n).active.Set(path[i+1].Block)
		}
	}
}
