package gui

// The window is drawn from a layout document: a tree of rows, columns and
// stacks whose leaves are blocks of the registry (blocks.go) and the panes,
// the tab groups' split tree (layout.go), which the document places but
// does not hold. Its JSON form and checks are in winparse.go, its drawing
// in winview.go, the device's own state of its nodes and the file it is
// read from in winstate.go; docs/protocol.md describes the format.

// windowDocVersion is the version of the format this window reads.
const windowDocVersion = 1

// windowDoc is a layout document.
type windowDoc struct {
	Version int
	Root    *winNode
}

// The kinds of node.
const (
	nodeRow    = "row"    // children side by side
	nodeColumn = "column" // children one over another
	nodeStack  = "stack"  // children over each other, one shown, as tabs
	nodeBlock  = "block"  // a registered block
	nodePanes  = "panes"  // the tab groups' split tree
)

// winNode is a node of the document. Size, Grow and Collapsible place a
// child of a row or column; Size is the default, which a device's own
// size, kept by node ID, overrides (winstate.go).
type winNode struct {
	Kind        string
	ID          string
	Children    []*winNode     // a row's, column's or stack's
	Block       string         // a block's registered name
	Panes       string         // which panes: "main"
	Size        float64        // px along the parent's axis, with an edge to drag
	Grow        float64        // share of the room the other children leave
	Gap         float64        // px between a row's or column's children
	Collapsible bool           // slides shut and open again, a sized child
	Props       map[string]any // a block's
	Active      int            // the child a stack shows until another is picked
	// short says the node was written as a bare block name in a list of
	// children, and is written so again.
	short bool
}

// The IDs of the nodes the window's own state follows: the sidebar's and
// the right panel's open, width and tab are kept as they were before the
// document (winstate.go), and keyboard shortcuts and menus find them by
// these IDs.
const (
	sidebarNode = "sidebar"
	detailsNode = "details"
	mainPanes   = "main"
)

// defaultWindowDoc is the window as it has always been laid out: the
// sidebar, the workspace of the session header over the panes, and the
// right panel's tabs, the files first.
func defaultWindowDoc() *windowDoc {
	return &windowDoc{Version: windowDocVersion, Root: &winNode{Kind: nodeRow, ID: "window", Children: []*winNode{
		{Kind: nodeBlock, ID: sidebarNode, Block: blockSidebar, Size: sidebarWidth, Collapsible: true},
		{Kind: nodeColumn, ID: "workspace", Grow: 1, Children: []*winNode{
			{Kind: nodeBlock, ID: "header", Block: blockHeader},
			{Kind: nodePanes, ID: mainPanes, Panes: mainPanes, Grow: 1},
		}},
		{Kind: nodeStack, ID: detailsNode, Size: detailsWidth, Collapsible: true, Children: []*winNode{
			shortBlock(filesTab), shortBlock(gitTab), shortBlock(agentsTab), shortBlock(forgeTab),
		}},
	}}}
}

// shortBlock is a block written by its name alone, its ID that name.
func shortBlock(name string) *winNode {
	return &winNode{Kind: nodeBlock, ID: name, Block: name, short: true}
}

// walk calls fn on n and every node under it, parents first, with the
// chain of nodes from the root to it; it stops where fn says.
func (n *winNode) walk(path []*winNode, fn func(path []*winNode) bool) bool {
	path = append(path, n)
	if !fn(path) {
		return false
	}
	for _, c := range n.Children {
		if !c.walk(path, fn) {
			return false
		}
	}
	return true
}

// pathTo is the chain from the root to the first node that has, nil when
// none does.
func (d *windowDoc) pathTo(has func(*winNode) bool) []*winNode {
	var found []*winNode
	if d == nil || d.Root == nil {
		return nil
	}
	d.Root.walk(nil, func(path []*winNode) bool {
		if has(path[len(path)-1]) {
			found = append([]*winNode(nil), path...)
			return false
		}
		return true
	})
	return found
}

// node is the node of the ID, nil when none is.
func (d *windowDoc) node(id string) *winNode {
	path := d.pathTo(func(n *winNode) bool { return n.ID == id })
	if path == nil {
		return nil
	}
	return path[len(path)-1]
}

// blockPath is the chain from the root to where the document places the
// block, nil when it does not.
func (d *windowDoc) blockPath(name string) []*winNode {
	return d.pathTo(func(n *winNode) bool { return n.Kind == nodeBlock && n.Block == name })
}

// stackIndex is the child of stack n that shows for the block chosen,
// the document's Active when the stack holds no such block.
func stackIndex(n *winNode, chosen string) int {
	for i, c := range n.Children {
		if c.Block == chosen {
			return i
		}
	}
	return n.Active
}

// activeBlock is the block a stack shows until another is picked.
func (n *winNode) activeBlock() string {
	if n.Active >= 0 && n.Active < len(n.Children) {
		return n.Children[n.Active].Block
	}
	return ""
}

// sized says n has a size along its parent's axis, and so an edge to
// resize it, and may collapse.
func (n *winNode) sized() bool { return n.Size > 0 }
