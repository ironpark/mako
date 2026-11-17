package gui

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
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
		sidebarNode: {open: m.SidebarOpen, size: m.SidebarWidth, active: ggui.State(""), lo: minSidebarWidth, hi: maxSidebarWidth,
			kept: func(p prefs) float64 { return p.SidebarWidth },
			keep: func(p *prefs, w float64) { p.SidebarWidth = w }},
		detailsNode: {open: m.DetailsOpen, size: m.DetailsWidth, active: m.DetailTab, lo: minDetailsWidth, hi: maxDetailsWidth,
			kept: func(p prefs) float64 { return p.DetailsWidth },
			keep: func(p *prefs, w float64) { p.DetailsWidth = w }},
	}
}

// nodeState is n's state, made the first time it is asked for.
func (m *model) nodeState(n *winNode) *nodeState {
	if st, ok := m.winStates[n.ID]; ok {
		return st
	}
	id := n.ID
	st := &nodeState{open: ggui.State(true), active: ggui.State(n.activeBlock()), lo: minPanelSize, hi: maxPanelSize,
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
func (st *nodeState) resting(n *winNode, p prefs) float64 {
	size := st.kept(p)
	if size == 0 {
		size = n.Size
	}
	return min(max(size, st.lo), st.hi)
}

// restingSize is the size the node of the ID takes until its edge is
// dragged, or fallback when the document does not size it.
func (m *model) restingSize(id string, fallback float64) float64 {
	n := ggui.Peek(m.WinDoc).node(id)
	if n == nil || !n.sized() {
		return fallback
	}
	return m.nodeState(n).resting(n, ggui.Peek(m.Prefs))
}

// setWindowDoc shows d: the sizes not dragged on this device follow it,
// the stacks show its active tabs where what they showed is gone, and
// every node but the sidebar and the right panel starts afresh.
func (m *model) setWindowDoc(d *windowDoc) {
	for id := range m.winStates {
		if id != sidebarNode && id != detailsNode {
			delete(m.winStates, id)
		}
	}
	p := ggui.Peek(m.Prefs)
	for _, id := range []string{sidebarNode, detailsNode} {
		st, n := m.winStates[id], d.node(id)
		if n == nil {
			continue
		}
		if n.sized() {
			st.size.Set(st.resting(n, p))
		}
		if n.Kind == nodeStack && stackIndex(n, ggui.Peek(st.active)) == n.Active {
			st.active.Set(n.activeBlock())
		}
	}
	m.WinDoc.Set(d)
}

// blockShown says whether the window shows the block: placed by the
// document, every collapsible node it is in open, and every stack showing
// it. Read in an effect, it follows them all.
func (m *model) blockShown(name string) bool { return m.blockIn(name, true) }

// blockChosen says whether the block is placed and every stack it is in
// shows it, open or not.
func (m *model) blockChosen(name string) bool { return m.blockIn(name, false) }

func (m *model) blockIn(name string, open bool) bool {
	path := m.WinDoc.Get().blockPath(name)
	if path == nil {
		return false
	}
	for i, n := range path {
		if open && n.Collapsible && !m.nodeState(n).open.Get() {
			return false
		}
		if n.Kind == nodeStack && n.Children[stackIndex(n, m.nodeState(n).active.Get())] != path[i+1] {
			return false
		}
	}
	return true
}

// revealBlock shows the block wherever the document placed it: the nodes
// it is in opened, and its stack showing it.
func (m *model) revealBlock(name string) {
	path := ggui.Peek(m.WinDoc).blockPath(name)
	for i, n := range path {
		if n.Collapsible {
			m.nodeState(n).open.Set(true)
		}
		if n.Kind == nodeStack {
			m.nodeState(n).active.Set(path[i+1].Block)
		}
	}
}

// The document is read from layout.json beside gui.json when the window
// starts, and again on the palette's command: the window watches no
// files. It is this window's alone; nothing sends it to a daemon.
const layoutFileName = "layout.json"

// loadWindowDoc reads layout.json: the default document for none, and for
// one that does not parse, with why.
func loadWindowDoc(reg *blockRegistry) (*windowDoc, error) {
	path, err := configFile(layoutFileName)
	if err != nil {
		return defaultWindowDoc(), nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return defaultWindowDoc(), nil
	}
	if err != nil {
		return defaultWindowDoc(), err
	}
	d, err := parseWindowDoc(data, reg)
	if err != nil {
		return defaultWindowDoc(), fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// reloadWindowDoc reads layout.json again and shows it.
func (m *model) reloadWindowDoc() {
	d, err := loadWindowDoc(m.blocks)
	m.setWindowDoc(d)
	m.tellWindowDoc(err)
}

// resetWindowDoc shows the default document, moving layout.json aside to
// layout.json.bak so that nothing written in it is lost.
func (m *model) resetWindowDoc() {
	if path, err := configFile(layoutFileName); err == nil {
		if err := os.Rename(path, path+".bak"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			m.toaster.Push(ui.Toast(T("Could not reset the window layout"), err.Error()).Destructive())
			return
		}
	}
	m.setWindowDoc(defaultWindowDoc())
}

// tellWindowDoc says why the window's layout is the default rather than
// the file's.
func (m *model) tellWindowDoc(err error) {
	if err == nil {
		return
	}
	log.Printf("mako: window layout: %v", err)
	m.toaster.Push(ui.Toast(T("The window layout could not be read, so the default is shown"), err.Error()).Destructive())
}
