package gui

import (
	"slices"

	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

// The blocks agents show (show_ui) are their daemon's, which lists them
// with where each goes; the window lays them over the document it draws,
// never into it: by default as the last of the right panel's tabs, which
// the panel is for, and shown as the block comes; elsewhere where the
// user allowed it. Every window of the daemon lays them out alike, as
// the list is the daemon's, and a window of another node has none of
// them: agents' blocks are their node's alone.

// setAgentBlocks takes the daemon's listing in: the blocks registered,
// the document drawn again with them, and a block new to the window
// shown.
func (m *model) setAgentBlocks(list []*protocol.AgentBlock) {
	prev := ggui.Peek(m.AgentBlocks)
	if slices.EqualFunc(prev, list, func(a, b *protocol.AgentBlock) bool { return proto.Equal(a, b) }) {
		return
	}
	m.AgentBlocks.Set(list)
	defs := make([]blockDef, 0, len(list))
	for _, b := range list {
		name, origin, title := b.GetName(), blockOrigin{session: b.GetSessionId(), agent: b.GetAgent()}, b.GetTitle()
		label := cmpTone(title, cmpTone(b.GetAgent(), T("Agent")))
		defs = append(defs, blockDef{Name: name, Title: label,
			Build: func(m *model, _ map[string]any) blockView { return blockView{Body: treeBlock(m, name, origin, title)} }})
	}
	m.blocks.replaceOf("agent:", defs)
	// The window is laid out again only as a block comes, goes or moves:
	// a new title is the registry's.
	if !slices.EqualFunc(prev, list, samePlace) {
		m.setWindowDoc(m.overlaid(m.winBase))
	}
	for _, b := range list {
		if !slices.ContainsFunc(prev, func(p *protocol.AgentBlock) bool { return p.GetName() == b.GetName() }) {
			m.revealBlock(b.GetName())
		}
	}
}

// drawWindowDoc draws d, with the agents' blocks over it.
func (m *model) drawWindowDoc(d *layoutdoc.Doc) {
	m.winBase = d
	m.setWindowDoc(m.overlaid(d))
}

// overlaid is d with the agents' blocks placed over it; d itself, with
// none. A block d places already, as a change staged to place it does,
// stays where d has it.
func (m *model) overlaid(d *layoutdoc.Doc) *layoutdoc.Doc {
	list := ggui.Peek(m.AgentBlocks)
	if len(list) == 0 || d == nil {
		return d
	}
	shown := m.spacesNow().shownLayoutName()
	for _, b := range list {
		if d.BlockPath(b.GetName()) != nil {
			continue
		}
		if next, err := layoutdoc.Apply(d, []layoutdoc.Op{placeOp(d, b, shown)}, m.blocks); err == nil {
			d = next
		}
	}
	return d
}

// placeOp is where block b goes in d, the document named shown: where
// the user allowed it, when that is in d; else the right panel's last
// tab; else, with no right panel, the root's last child.
func placeOp(d *layoutdoc.Doc, b *protocol.AgentBlock, shown string) layoutdoc.Op {
	name := b.GetName()
	if p := b.GetParent(); p != "" && b.GetDocument() == shown && d.Node(p) != nil {
		node := map[string]any{"id": name, "block": name}
		if b.GetSize() > 0 {
			node["size"] = b.GetSize()
		}
		op := layoutdoc.Op{Op: "insert", Parent: p, Node: node}
		if i := int(b.GetIndex()); i >= 0 {
			op.Index = &i
		}
		return op
	}
	if n := d.Node(layoutdoc.DetailsNode); n != nil && n.Kind == layoutdoc.KindStack {
		return layoutdoc.Op{Op: "insert", Parent: layoutdoc.DetailsNode, Node: name}
	}
	return layoutdoc.Op{Op: "insert", Parent: d.Root.ID, Node: map[string]any{"id": name, "block": name, "size": layoutdoc.DefaultDetailsSize}}
}

// samePlace says a and b are one block, placed alike.
func samePlace(a, b *protocol.AgentBlock) bool {
	return a.GetName() == b.GetName() && a.GetDocument() == b.GetDocument() && a.GetParent() == b.GetParent() &&
		a.GetIndex() == b.GetIndex() && a.GetSize() == b.GetSize()
}
