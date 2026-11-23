package gui

import (
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/layoutdoc"
)

// windowView draws the window's layout document, again from the top when
// it changes. The default document draws the window as it was before
// there was one: see internal/layoutdoc.
func windowView(m *model) ggui.Widget {
	return ggui.Key(m.WinDoc, func(d *layoutdoc.Doc) ggui.Widget {
		v := &layoutDraw{m: m}
		return v.node(d.Root, true)
	})
}

// layoutDraw draws one document.
type layoutDraw struct{ m *model }

func (v *layoutDraw) node(n *layoutdoc.Node, root bool) ggui.Widget {
	switch n.Kind {
	case layoutdoc.KindRow, layoutdoc.KindColumn:
		return v.line(n, root)
	case layoutdoc.KindStack:
		return v.stack(n)
	case layoutdoc.KindPanes:
		return layoutView(v.m)
	}
	return v.renderBlockNode(nil, n, blockPlace{Solo: true}, nil)
}

// isDynamicBlock reports whether a block name comes from a plugin or an agent,
// whose registration may appear or disappear at runtime.
func isDynamicBlock(name string) bool {
	return strings.HasPrefix(name, "plugin:") || strings.HasPrefix(name, "agent:")
}

// renderBlockNode draws a block node inside its placement chrome (solo dock
// header when at.Solo and chromePanel, tabbed page when !at.Solo and
// chromePanel, bare Body for chromeBare). For plugin/agent blocks it reacts to
// v.m.blocks.rev so missing blocks seamlessly appear once registered.
// If onMenu is non-nil, it receives the built blockView's Menu callback.
func (v *layoutDraw) renderBlockNode(stackNode, child *layoutdoc.Node, at blockPlace, onMenu func(func() []ggui.Widget)) ggui.Widget {
	build := func() ggui.Widget {
		def, ok := v.m.blocks.lookup(child.Block)
		if !ok {
			if onMenu != nil {
				onMenu(nil)
			}
			if isDynamicBlock(child.Block) {
				return missingPluginBlock(v.m, child.Block)
			}
			return missingBlock(child.Block)
		}
		bv := def.Build(v.m, child.Props, at)
		if onMenu != nil {
			onMenu(bv.Menu)
		}
		if def.Chrome == chromePanel {
			if at.Solo {
				return soloPanel(v.m, stackNode, child, def, bv)
			}
			return tabbedPage(bv)
		}
		return bv.Body
	}
	if isDynamicBlock(child.Block) {
		return ggui.View(v.m.blocks.rev, func(int) ggui.Widget { return build() })
	}
	return build()
}

// line draws a row or a column: a sized child as a panel that slides and
// resizes, a growing one sharing the room left, the rest as tall or wide
// as they are. A line divides two children neither of which is a panel,
// whose edge is one. A line other than the root is drawn on the
// workspace's surface; the root is not, so that the window's backdrop
// shows through the sidebar.
func (v *layoutDraw) line(n *layoutdoc.Node, root bool) ggui.Widget {
	vertical := n.Kind == layoutdoc.KindColumn
	kids := make([]ggui.Widget, 0, 2*len(n.Children))
	grown := false
	for i, c := range n.Children {
		if i > 0 && !c.Sized() && !n.Children[i-1].Sized() {
			kids = append(kids, pick(vertical, hline(), vline()))
		}
		switch {
		case c.Sized():
			kids = append(kids, v.panel(c, vertical, grown))
		case c.Grow > 0:
			grown = true
			if c.Grow == 1 {
				kids = append(kids, widget.Expanded(v.node(c, false)))
			} else {
				kids = append(kids, widget.Flex(v.node(c, false), c.Grow))
			}
		default:
			kids = append(kids, v.node(c, false))
		}
	}
	var w ggui.Widget
	if vertical {
		col := widget.Column(kids...).Stretch()
		if n.Gap > 0 {
			col = col.Gap(n.Gap)
		}
		w = col
	} else {
		row := widget.Row(kids...).Stretch()
		if n.Gap > 0 {
			row = row.Gap(n.Gap)
		}
		w = row
	}
	if root {
		return w
	}
	return widget.Box(w).Fill(skin.Surface0)
}

// panel draws a sized child of a line: as wide (or tall) as its state
// says, with an edge on the side toward the line's growing child that
// drags to resize it, sliding shut and open when it is collapsible.
// trailing says it comes after the growing child: on the right, or at
// the bottom. A block is built in a component of its own, which owns
// what it makes.
func (v *layoutDraw) panel(n *layoutdoc.Node, vertical, trailing bool) ggui.Widget {
	st := v.m.nodeState(n)
	var open ggui.Readable[bool] = ggui.Const(true)
	if n.Collapsible {
		open = st.open
	}
	arrange := func(b blockView) ggui.Widget {
		edge := v.edge(st, b.Edge, vertical, trailing)
		body := widget.Expanded(b.Body)
		if vertical {
			return widget.Column(pick(trailing, []ggui.Widget{edge, body}, []ggui.Widget{body, edge})...).Stretch()
		}
		return widget.Row(pick(trailing, []ggui.Widget{edge, body}, []ggui.Widget{body, edge})...).Stretch()
	}
	var child ggui.Widget
	if n.Kind == layoutdoc.KindBlock {
		child = ggui.Component(func() ggui.Widget {
			if !isDynamicBlock(n.Block) {
				if def, ok := v.m.blocks.lookup(n.Block); ok {
					bv := def.Build(v.m, n.Props, blockPlace{Solo: true})
					body := bv.Body
					if def.Chrome == chromePanel {
						body = soloPanel(v.m, nil, n, def, bv)
					}
					return arrange(blockView{Body: body, Edge: bv.Edge})
				}
			}
			return arrange(blockView{Body: v.renderBlockNode(nil, n, blockPlace{Solo: true}, nil)})
		})
	} else {
		child = arrange(blockView{Body: v.node(n, false)})
	}
	d := newDrawer(child, open, ggui.Map(st.size, func(s float64) float64 { return s + panelEdgeWidth }), trailing)
	d.vertical = vertical
	return d
}

// edge is a panel's edge: a line along the side toward the growing child
// on face, the block's or the workspace's surface, which drags to resize
// the panel, keeping the size when the drag ends.
func (v *layoutDraw) edge(st *nodeState, face func(ggui.Widget, bool) ggui.Widget, vertical, trailing bool) ggui.Widget {
	var line ggui.Widget
	switch {
	case vertical && trailing:
		line = widget.Column(hline(), widget.Spacer()).Stretch()
	case vertical:
		line = widget.Column(widget.Spacer(), hline()).Stretch()
	case trailing:
		line = widget.Row(vline(), widget.Spacer()).Stretch()
	default:
		line = widget.Row(widget.Spacer(), vline()).Stretch()
	}
	if face == nil {
		face = func(line ggui.Widget, vertical bool) ggui.Widget { return edgeBox(line, vertical).Fill(skin.Surface0) }
	}
	return resizeEdge(v.m, face(line, vertical), st, pick(trailing, -1.0, 1.0), vertical)
}

// resizeEdge has edge drag st's size within its bounds: larger as it
// moves the way dir says, 1 for right or down, and kept in the settings
// when the drag ends.
func resizeEdge(m *model, edge ggui.Widget, st *nodeState, dir float64, vertical bool) ggui.Widget {
	var from, start float64
	along := func(p ggui.Point) float64 { return pick(vertical, p.Y, p.X) }
	return widget.Pointer(edge).Cursor(pick(vertical, ggui.CursorShapeNSResize, ggui.CursorShapeEWResize)).
		OnDown(func(ev ggui.PointerEvent) { from, start = along(ev.Pos), ggui.Peek(st.size) }).
		OnDrag(func(ev ggui.PointerEvent) { st.size.Set(min(max(start+dir*(along(ev.Pos)-from), st.lo), st.hi)) }).
		OnUp(func(ggui.PointerEvent) {
			s := ggui.Peek(st.size)
			m.Prefs.Update(func(p prefs) prefs { st.keep(&p, s); return p })
		})
}

// stack draws a stack: when it holds a single panel block it renders the
// unified solo dock header; with two or more children it draws the tab strip
// over the active page. Each block is built once, so it keeps its scroll and
// open folders across tabs.
func (v *layoutDraw) stack(n *layoutdoc.Node) ggui.Widget {
	if len(n.Children) == 1 {
		return v.renderBlockNode(n, n.Children[0], blockPlace{Solo: true}, nil)
	}
	st := v.m.nodeState(n)
	pages := make([]ggui.Widget, len(n.Children))
	pageMenus := make([]func() []ggui.Widget, len(n.Children))
	menuRefs := make([]func() []ggui.Widget, len(n.Children))
	for i, c := range n.Children {
		idx := i
		pages[i] = v.renderBlockNode(n, c, blockPlace{Solo: false}, func(fn func() []ggui.Widget) {
			pageMenus[idx] = fn
		})
		menuRefs[i] = func() []ggui.Widget {
			if fn := pageMenus[idx]; fn != nil {
				return fn()
			}
			return nil
		}
	}
	shown := ggui.Map(st.active, func(name string) int { return layoutdoc.StackIndex(n, name) })
	labels := ggui.Derived(func() []string {
		v.m.blocks.rev.Get() // a plugin's block's title comes with it
		out := make([]string, len(n.Children))
		for i, c := range n.Children {
			def, _ := v.m.blocks.lookup(c.Block)
			out[i] = blockLabel(v.m, def, c.Block)
		}
		return out
	}).WithEqual(slices.Equal)
	choose := func(i int) { st.active.Set(n.Children[i].Block) }
	body := widget.Stack(
		ggui.Key(shown, func(i int) ggui.Widget { return pages[i] }),
		ggui.If(v.m.StackDragging, func() ggui.Widget { return newStackDropZones(v.m, n) }),
	).Expand()
	return widget.Box(widget.Column(
		ggui.View(labels, func(l []string) ggui.Widget { return stackTabs(v.m, n, shown, choose, l, menuRefs) }),
		hline(),
		widget.Expanded(body),
	).Stretch()).Fill(skin.Surface0)
}
