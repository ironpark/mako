package gui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"slices"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/layoutdoc"
)

// stackTabDrag is carried while dragging a tab from a KindStack (such as the
// right sidebar's details stack or one half of a vertically split sidebar).
type stackTabDrag struct {
	StackID string
	BlockID string
	Block   string
	Label   string
}

// stackTabs draws a KindStack's tab strip identically to panelTabs when idle,
// while letting the user drag any tab to reorder it within the stack, move it
// to another stack, or drag it onto a stack's body to split top/bottom.
func stackTabs(m *model, n *layoutdoc.Node, chosen ggui.Readable[int], choose func(int), labels []string) ggui.Widget {
	return ggui.View(chosen, func(current int) ggui.Widget {
		cells := make([]ggui.Widget, len(labels))
		for i, label := range labels {
			idx := i
			child := n.Children[i]
			fg, line := skin.Muted, color.Color(skin.Surface0)
			if i == current {
				fg, line = skin.Fg, skin.AccentBright
			}
			btn := ui.ButtonOf(widget.Box(widget.Row(widget.Text(label).Size(13).Color(fg).NoWrap())).Height(headerHeight),
				func() { choose(idx) }).Ghost().Pad(0, 9).Radius(0).Name(label)
			var cell ggui.Widget = &underlined{child: btn, color: line}

			// Show a crisp vertical accent bar on the left or right side of the
			// hovered tab while another stack tab is dragged over it.
			hoverSide := ggui.State(0) // -1 left, 0 none, +1 right
			marked := &stackTabDropMark{child: cell, side: hoverSide}
			accept := func(d stackTabDrag) bool {
				return d.BlockID != "" && !(d.StackID == n.ID && d.BlockID == child.ID)
			}
			zone := widget.DropZone(marked, func(d widget.Dropped[stackTabDrag]) {
				hoverSide.Set(0)
				m.StackDragging.Set(false)
				target := idx
				if d.Size.W > 0 && d.Pos.X >= d.Size.W/2 {
					target = idx + 1
				}
				m.moveStackBlock(d.Value.StackID, d.Value.BlockID, n.ID, target)
			}).Accept(accept).OnOver(func(d widget.Dropped[stackTabDrag]) {
				if d.Size.W > 0 && d.Pos.X >= d.Size.W/2 {
					hoverSide.Set(1)
				} else {
					hoverSide.Set(-1)
				}
			}).OnHover(func(over bool) {
				if !over {
					hoverSide.Set(0)
				}
			})

			drag := widget.DragSource(zone, stackTabDrag{
				StackID: n.ID,
				BlockID: child.ID,
				Block:   child.Block,
				Label:   label,
			}).OnDragStart(func() {
				m.StackDragging.Set(true)
			}).OnDragEnd(func(bool) {
				m.StackDragging.Set(false)
			})
			cells[i] = drag
		}
		strip := widget.Box(widget.Scroll(widget.Row(cells...)).Horizontal().Bar(color.Transparent)).Height(headerHeight).Pad(0, 4)
		// Dropping onto empty space in the tab strip appends the dragged tab to this stack.
		return widget.DropZone(strip, func(d widget.Dropped[stackTabDrag]) {
			m.StackDragging.Set(false)
			m.moveStackBlock(d.Value.StackID, d.Value.BlockID, n.ID, len(n.Children))
		}).Accept(func(d stackTabDrag) bool {
			return d.BlockID != ""
		})
	})
}

// stackTabDropMark paints a 2px vertical accent bar on the left or right edge
// of its child when a dragged stack tab hovers over it, without changing layout
// dimensions or semantics when idle.
type stackTabDropMark struct {
	child  ggui.Widget
	side   *ggui.StateValue[int]
	accent color.Color
}

func (w *stackTabDropMark) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w.accent = ggui.ResolveColor(skin.AccentBright, env)
	return w.child.Layout(c, env)
}

func (w *stackTabDropMark) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(w.child, r)
	side := w.side.Get()
	if side == 0 {
		return
	}
	barW := 2.0
	x := r.Origin.X
	if side > 0 {
		x = r.Origin.X + r.Size.W - barW
	}
	dst.FillRect(ggui.Rct(ggui.Pt(x, r.Origin.Y+4), ggui.Sz(barW, max(r.Size.H-8, 2))), w.accent)
}

// stackDropZones overlays a KindStack's body while a stack tab is being dragged,
// offering Top/Bottom vertical split zones and a Center merge zone.
type stackDropZones struct {
	zones [3]*widget.DropZoneWidget[stackTabDrag] // 0=top, 1=bottom, 2=center
	edges [3]dropEdge
	hint  *ggui.StateValue[dropEdge]
	shade ggui.Widget
	rects [3]ggui.Rect
}

const stackEdgeShare = 0.32

func newStackDropZones(m *model, n *layoutdoc.Node) *stackDropZones {
	z := &stackDropZones{
		edges: [3]dropEdge{dropTop, dropBottom, dropCenter},
		hint:  ggui.State(dropEdge(-1)),
	}
	for i, edge := range z.edges {
		e := edge
		z.zones[i] = widget.DropZone(widget.Box(), func(d widget.Dropped[stackTabDrag]) {
			m.StackDragging.Set(false)
			switch e {
			case dropTop:
				m.splitStackBlock(d.Value.StackID, d.Value.BlockID, n.ID, true)
			case dropBottom:
				m.splitStackBlock(d.Value.StackID, d.Value.BlockID, n.ID, false)
			case dropCenter:
				m.moveStackBlock(d.Value.StackID, d.Value.BlockID, n.ID, -1)
			}
		}).Accept(func(d stackTabDrag) bool {
			if d.BlockID == "" {
				return false
			}
			if e == dropCenter {
				return d.StackID != n.ID
			}
			// Cannot split a single-tab stack with its own sole tab.
			return !(d.StackID == n.ID && len(n.Children) <= 1)
		}).OnHover(func(over bool) {
			if over {
				z.hint.Set(e)
			} else if ggui.Peek(z.hint) == e {
				z.hint.Set(-1)
			}
		})
	}
	z.shade = ggui.View(z.hint, func(e dropEdge) ggui.Widget { return &dropShade{edge: e} })
	return z
}

func (z *stackDropZones) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	size := c.Max()
	w, h := size.W, size.H
	eh := h * stackEdgeShare
	z.rects[0] = ggui.Rct(ggui.Pt(0, 0), ggui.Sz(w, eh))
	z.rects[1] = ggui.Rct(ggui.Pt(0, h-eh), ggui.Sz(w, eh))
	z.rects[2] = ggui.Rct(ggui.Pt(0, eh), ggui.Sz(w, max(h-2*eh, 0)))
	for i, zone := range z.zones {
		zone.Layout(ggui.Tight(z.rects[i].Size), env)
	}
	z.shade.Layout(ggui.Tight(size), env)
	return size
}

func (z *stackDropZones) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(z.shade, r)
	for i, zone := range z.zones {
		at := z.rects[i]
		at.Origin = at.Origin.Add(r.Origin)
		dst.Paint(zone, at)
	}
}

// firstStackIn returns n if it is a KindStack, or the first KindStack nested
// under n (e.g., when details has been split into a column of stacks).
func firstStackIn(n *layoutdoc.Node) *layoutdoc.Node {
	if n == nil {
		return nil
	}
	if n.Kind == layoutdoc.KindStack {
		return n
	}
	for _, c := range n.Children {
		if st := firstStackIn(c); st != nil {
			return st
		}
	}
	return nil
}

// syncStackActives copies each stack's live selected tab from winStates onto
// the cloned layout document so unaffected stacks keep their visible tab when
// another tab is moved or split.
func (m *model) syncStackActives(doc *layoutdoc.Doc) {
	if doc == nil || doc.Root == nil {
		return
	}
	doc.Root.Walk(nil, func(path []*layoutdoc.Node) bool {
		n := path[len(path)-1]
		if n.Kind != layoutdoc.KindStack {
			return true
		}
		if st, ok := m.winStates[n.ID]; ok {
			if cur := ggui.Peek(st.active); cur != "" {
				n.Active = layoutdoc.StackIndex(n, cur)
			}
		}
		return true
	})
}

func uniqueLayoutNodeID(doc *layoutdoc.Doc, base string) string {
	if doc.Node(base) == nil {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if doc.Node(cand) == nil {
			return cand
		}
	}
}

func (m *model) commitLayoutDoc(docName string, next *layoutdoc.Doc, revealBlock string) {
	if err := next.Check(m.blocks); err != nil {
		m.tellWindowDoc(err)
		return
	}
	body, err := json.Marshal(next)
	if err != nil {
		return
	}
	m.setWindowLayout(docName, body)
	if m.spacesNow().shownLayoutName() == docName {
		if st := ggui.Peek(m.LayoutStage); st == nil || st.GetDocument() != docName {
			m.showWindowLayout(winSource{body: string(body)})
		}
	}
	if revealBlock != "" {
		m.revealBlock(revealBlock)
	}
}

// moveStackBlock reorders blockID within fromStackID, or moves it from
// fromStackID into toStackID at targetIndex (-1 appends). Emptying a split
// stack automatically collapses its parent column back into a single stack.
func (m *model) moveStackBlock(fromStackID, blockID, toStackID string, targetIndex int) {
	docName, doc := m.activeLayoutDoc()
	next := doc.Clone()
	m.syncStackActives(next)

	from := next.Node(fromStackID)
	to := next.Node(toStackID)
	if from == nil || to == nil || from.Kind != layoutdoc.KindStack || to.Kind != layoutdoc.KindStack {
		return
	}
	srcIdx := slices.IndexFunc(from.Children, func(c *layoutdoc.Node) bool { return c.ID == blockID })
	if srcIdx < 0 {
		return
	}

	if from == to {
		if targetIndex < 0 || targetIndex > len(from.Children) {
			targetIndex = len(from.Children)
		}
		if srcIdx < targetIndex {
			targetIndex--
		}
		if srcIdx == targetIndex {
			return
		}
		blk := from.Children[srcIdx]
		from.Children = slices.Delete(from.Children, srcIdx, srcIdx+1)
		from.Children = slices.Insert(from.Children, targetIndex, blk)
		from.Active = targetIndex
		m.commitLayoutDoc(docName, next, blk.Block)
		return
	}

	blk := from.Children[srcIdx]
	from.Children = slices.Delete(from.Children, srcIdx, srcIdx+1)
	if len(from.Children) > 0 && from.Active >= len(from.Children) {
		from.Active = len(from.Children) - 1
	} else if from.Active > srcIdx {
		from.Active--
	}

	if targetIndex < 0 || targetIndex > len(to.Children) {
		targetIndex = len(to.Children)
	}
	to.Children = slices.Insert(to.Children, targetIndex, blk)
	to.Active = targetIndex

	if len(from.Children) == 0 {
		removeEmptyNodeAndCollapse(next, from)
	}
	m.commitLayoutDoc(docName, next, blk.Block)
}

// splitStackBlock moves blockID from fromStackID into a new KindStack placed
// above or below targetStackID inside a vertical column with a draggable
// horizontal resize divider.
func (m *model) splitStackBlock(fromStackID, blockID, targetStackID string, above bool) {
	docName, doc := m.activeLayoutDoc()
	next := doc.Clone()
	m.syncStackActives(next)

	from := next.Node(fromStackID)
	target := next.Node(targetStackID)
	if from == nil || target == nil || from.Kind != layoutdoc.KindStack || target.Kind != layoutdoc.KindStack {
		return
	}
	if from == target && len(from.Children) <= 1 {
		return
	}
	srcIdx := slices.IndexFunc(from.Children, func(c *layoutdoc.Node) bool { return c.ID == blockID })
	if srcIdx < 0 {
		return
	}

	blk := from.Children[srcIdx]
	from.Children = slices.Delete(from.Children, srcIdx, srcIdx+1)
	if len(from.Children) > 0 {
		if from.Active == srcIdx && from.Active >= len(from.Children) {
			from.Active = len(from.Children) - 1
		} else if from.Active > srcIdx {
			from.Active--
		}
	}

	path := next.PathTo(func(x *layoutdoc.Node) bool { return x == target })
	if len(path) < 2 {
		return
	}
	parent := path[len(path)-2]
	targetIdx := slices.Index(parent.Children, target)
	if targetIdx < 0 {
		return
	}

	if parent.Kind == layoutdoc.KindColumn {
		newStack := &layoutdoc.Node{
			Kind:     layoutdoc.KindStack,
			ID:       uniqueLayoutNodeID(next, target.ID+"-split"),
			Size:     260,
			Children: []*layoutdoc.Node{blk},
			Active:   0,
		}
		ins := targetIdx
		if !above {
			ins = targetIdx + 1
		}
		parent.Children = slices.Insert(parent.Children, ins, newStack)
	} else {
		// Wrap target in a KindColumn that inherits target's outer ID, Size,
		// Grow and Collapsible so Cmd+Shift+D, width resizing, and DetailsOpen
		// keep controlling the entire right sidebar seamlessly.
		outerID := target.ID
		col := &layoutdoc.Node{
			Kind:        layoutdoc.KindColumn,
			ID:          outerID,
			Size:        target.Size,
			Grow:        target.Grow,
			Collapsible: target.Collapsible,
		}
		target.Size, target.Grow, target.Collapsible = 0, 0, false
		topID := uniqueLayoutNodeID(next, outerID+"-top")
		bottomID := uniqueLayoutNodeID(next, outerID+"-bottom")

		newStack := &layoutdoc.Node{
			Kind:     layoutdoc.KindStack,
			Children: []*layoutdoc.Node{blk},
			Active:   0,
		}
		if above {
			newStack.ID = topID
			newStack.Grow = 1
			target.ID = bottomID
			target.Size = 320
			col.Children = []*layoutdoc.Node{newStack, target}
		} else {
			target.ID = topID
			target.Grow = 1
			newStack.ID = bottomID
			newStack.Size = 320
			col.Children = []*layoutdoc.Node{target, newStack}
		}
		parent.Children[targetIdx] = col
	}

	if len(from.Children) == 0 {
		removeEmptyNodeAndCollapse(next, from)
	}
	m.commitLayoutDoc(docName, next, blk.Block)
}

// removeNodeAndCollapse removes the node with id from doc, removing any stack
// that becomes empty and unwrapping any single-child split column back into
// its surviving child.
func removeNodeAndCollapse(doc *layoutdoc.Doc, id string) bool {
	path := doc.PathTo(func(n *layoutdoc.Node) bool { return n.ID == id })
	if len(path) < 2 {
		return false
	}
	parent, leaf := path[len(path)-2], path[len(path)-1]
	idx := slices.Index(parent.Children, leaf)
	if idx < 0 {
		return false
	}
	parent.Children = slices.Delete(parent.Children, idx, idx+1)
	if parent.Kind == layoutdoc.KindStack {
		if len(parent.Children) == 0 {
			removeEmptyNodeAndCollapse(doc, parent)
		} else if parent.Active >= len(parent.Children) {
			parent.Active = len(parent.Children) - 1
		} else if parent.Active > idx {
			parent.Active--
		}
	}
	return true
}

// removeEmptyNodeAndCollapse deletes an emptied stack from its parent and
// unwraps any resulting single-child split column while preserving the outer
// column's ID, Size, Grow, and Collapsible settings (e.g. "details").
func removeEmptyNodeAndCollapse(doc *layoutdoc.Doc, empty *layoutdoc.Node) {
	path := doc.PathTo(func(n *layoutdoc.Node) bool { return n == empty })
	if len(path) < 2 {
		return
	}
	parent := path[len(path)-2]
	idx := slices.Index(parent.Children, empty)
	if idx < 0 {
		return
	}
	parent.Children = slices.Delete(parent.Children, idx, idx+1)

	// If parent is a split column with only one remaining child, unwrap it so
	// the surviving stack reclaims the column's ID (such as "details") and size.
	if parent.Kind == layoutdoc.KindColumn && len(parent.Children) == 1 && len(path) >= 3 && parent.ID != "workspace" {
		grand := path[len(path)-3]
		pIdx := slices.Index(grand.Children, parent)
		if pIdx >= 0 {
			only := parent.Children[0]
			only.ID = parent.ID
			only.Size = parent.Size
			only.Grow = parent.Grow
			only.Collapsible = parent.Collapsible
			grand.Children[pIdx] = only
		}
		return
	}

	// Ensure a remaining multi-child column always has a growing child so the
	// sized sibling's horizontal resize divider stays attached to a growing pane.
	if parent.Kind == layoutdoc.KindColumn && len(parent.Children) >= 2 {
		hasGrow := false
		for _, c := range parent.Children {
			if c.Grow > 0 {
				hasGrow = true
				break
			}
		}
		if !hasGrow {
			parent.Children[0].Size = 0
			parent.Children[0].Collapsible = false
			parent.Children[0].Grow = 1
		}
	}
}
