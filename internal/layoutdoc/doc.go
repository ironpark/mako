// Package layoutdoc is the window layout document: a tree of rows, columns
// and stacks whose leaves are registered blocks and the panes, the tab
// groups' split tree, which the document places but does not hold. The
// desktop window draws it (internal/gui), the daemon keeps it in the
// workspace and lets agents change it (internal/daemon); both read and
// check it here. docs/protocol.md, "Layout documents", describes the
// format.
package layoutdoc

import "slices"

// Version is the latest version of the format this build reads strictly
// and writes. A document of a later version is read leniently (Parse).
// A document is written at the earliest version that holds it, so that a
// window of an older build reads one that holds nothing newer as it
// always has: version 1, but for a document with variants.
const Version = 2

// VersionVariants is the version that adds variants: a document holding
// one is written at it. An older window reads such a document leniently,
// leaving the variants out, and draws its root.
const VersionVariants = 2

// DefaultName is the name of the document a space that names none uses.
const DefaultName = "default"

// Doc is a layout document.
type Doc struct {
	// Version is the version the document was written at, which it is
	// written at again when later than this build's; otherwise it is
	// written at the earliest that holds it (Written).
	Version int
	Root    *Node
	// Compact is variants.compact: the root a narrow screen, a phone,
	// shows in place of Root, nil for none (Pages). Every node has its
	// own place in it: an ID unique within it, as Root's are within Root.
	Compact *Node
}

// Written is the version d is written at: its own, of a later version;
// else the earliest that holds what it holds.
func (d *Doc) Written() int {
	switch {
	case d.Version > Version:
		return d.Version
	case d.Compact != nil:
		return VersionVariants
	}
	return 1
}

// The kinds of node this version knows. A document of a later version
// may hold others, kept as leaves of their kind's name (Known).
const (
	KindRow    = "row"    // children side by side
	KindColumn = "column" // children one over another
	KindStack  = "stack"  // children over each other, one shown, as tabs
	KindBlock  = "block"  // a registered block
	KindPanes  = "panes"  // the tab groups' split tree
)

var kinds = []string{KindRow, KindColumn, KindStack, KindBlock, KindPanes}

// Known says whether this version knows the kind of node.
func Known(kind string) bool {
	return slices.Contains(kinds, kind)
}

// Node is a node of the document. Size, Grow and Collapsible place a
// child of a row or column; Size is the default, which a device's own
// size, kept by node ID, overrides.
type Node struct {
	Kind        string
	ID          string
	Children    []*Node        // a row's, column's or stack's
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

// The IDs of the nodes a window's own state follows: the sidebar's and
// the right panel's open, width and tab, which keyboard shortcuts and
// menus find by these IDs; and the one panes node.
const (
	SidebarNode = "sidebar"
	DetailsNode = "details"
	MainPanes   = "main"
)

// The default document's sizes, as the window has always been laid out.
const (
	DefaultSidebarSize = 272
	DefaultDetailsSize = 320
)

// Default is the window as it has always been laid out: the sidebar, the
// workspace of the session header over the panes, and the right panel's
// tabs, the files first. A space that names no document, where the
// workspace holds none of DefaultName, shows it.
func Default() *Doc {
	return &Doc{Version: 1, Root: &Node{Kind: KindRow, ID: "window", Children: []*Node{
		{Kind: KindBlock, ID: SidebarNode, Block: BlockSidebar, Size: DefaultSidebarSize, Collapsible: true},
		{Kind: KindColumn, ID: "workspace", Grow: 1, Children: []*Node{
			{Kind: KindBlock, ID: "header", Block: BlockHeader},
			{Kind: KindPanes, ID: MainPanes, Panes: MainPanes, Grow: 1},
		}},
		{Kind: KindStack, ID: DetailsNode, Size: DefaultDetailsSize, Collapsible: true, Children: []*Node{
			Short(BlockFiles), Short(BlockGit), Short(BlockAgents), Short(BlockForge),
		}},
	}}}
}

// Short is a block written by its name alone, its ID that name.
func Short(name string) *Node {
	return &Node{Kind: KindBlock, ID: name, Block: name, short: true}
}

// Walk calls fn on n and every node under it, parents first, with the
// chain of nodes from the root to it; it stops where fn says.
func (n *Node) Walk(path []*Node, fn func(path []*Node) bool) bool {
	path = append(path, n)
	if !fn(path) {
		return false
	}
	for _, c := range n.Children {
		if !c.Walk(path, fn) {
			return false
		}
	}
	return true
}

// PathTo is the chain from the root to the first node that has, nil when
// none does.
func (d *Doc) PathTo(has func(*Node) bool) []*Node {
	var found []*Node
	if d == nil || d.Root == nil {
		return nil
	}
	d.Root.Walk(nil, func(path []*Node) bool {
		if has(path[len(path)-1]) {
			found = append([]*Node(nil), path...)
			return false
		}
		return true
	})
	return found
}

// Node is the node of the ID, nil when none is.
func (d *Doc) Node(id string) *Node {
	path := d.PathTo(func(n *Node) bool { return n.ID == id })
	if path == nil {
		return nil
	}
	return path[len(path)-1]
}

// BlockPath is the chain from the root to where the document places the
// block, nil when it does not.
func (d *Doc) BlockPath(name string) []*Node {
	return d.PathTo(func(n *Node) bool { return n.Kind == KindBlock && n.Block == name })
}

// StackIndex is the child of stack n that shows for the block chosen,
// the document's Active when the stack holds no such block.
func StackIndex(n *Node, chosen string) int {
	for i, c := range n.Children {
		if c.Block == chosen {
			return i
		}
	}
	return n.Active
}

// ActiveBlock is the block a stack shows until another is picked.
func (n *Node) ActiveBlock() string {
	if n.Active >= 0 && n.Active < len(n.Children) {
		return n.Children[n.Active].Block
	}
	return ""
}

// Sized says n has a size along its parent's axis, and so an edge to
// resize it, and may collapse.
func (n *Node) Sized() bool { return n.Size > 0 }

// Clone is a copy of d that shares nothing with it.
func (d *Doc) Clone() *Doc {
	if d == nil {
		return nil
	}
	return &Doc{Version: d.Version, Root: d.Root.clone(), Compact: d.Compact.clone()}
}

func (n *Node) clone() *Node {
	if n == nil {
		return nil
	}
	c := *n
	if n.Props != nil {
		c.Props = cloneAny(n.Props).(map[string]any)
	}
	c.Children = nil
	for _, k := range n.Children {
		c.Children = append(c.Children, k.clone())
	}
	return &c
}

// cloneAny copies a value as encoding/json decodes one.
func cloneAny(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = cloneAny(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = cloneAny(e)
		}
		return out
	}
	return v
}
