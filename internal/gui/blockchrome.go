package gui

import (
	"image/color"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/layoutdoc"
)

// Unified panel chrome: solo panels (one-child KindStack or bare chromePanel
// KindBlock) get a single dock header at headerHeight carrying drag grip, icon,
// title (or lifted toolbar when TitleInTools), Tools, Accessory, and ⋯ menu.
// Tabbed stacks (len > 1) keep the tab strip (with right-click context menus
// and third-party package glyphs) and draw a slim Tools/Accessory strip above
// Body when present, with no outer card border.

// gripDots paints a compact 2×3 vertical drag grip, visible only while hovered.
type gripDots struct {
	visible ggui.Readable[bool]
	fg      color.Color
}

func (g *gripDots) Layout(_ ggui.Constraints, env ggui.Env) ggui.Size {
	g.fg = ggui.ResolveColor(skin.ExtraMuted, env)
	return ggui.Sz(8, 14)
}

func (g *gripDots) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if !g.visible.Get() {
		return
	}
	const dot = 2.0
	xs := [2]float64{r.Origin.X + 1, r.Origin.X + 5}
	ys := [3]float64{r.Origin.Y + 2, r.Origin.Y + 6, r.Origin.Y + 10}
	for _, y := range ys {
		for _, x := range xs {
			dst.FillRoundRect(ggui.Rct(ggui.Pt(x, y), ggui.Sz(dot, dot)), dot/2, g.fg)
		}
	}
}

// semLabeled wraps a child widget in a semantic group carrying label, so
// compact visual badges (such as third-party plugin provenance) expose full
// accessible names to screen readers and probes.
type semLabeled struct {
	child ggui.Widget
	label string
}

func (s *semLabeled) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	return s.child.Layout(c, env)
}

func (s *semLabeled) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Node(r, ggui.Node{Role: ggui.RoleGroup, Name: s.label}, func(dst *ggui.Canvas) {
		dst.Paint(s.child, r)
	})
}

// isThirdPartyPluginBlock reports whether blockName belongs to a third-party
// (non-bundled) plugin.
func isThirdPartyPluginBlock(m *model, blockName string) bool {
	rest, ok := strings.CutPrefix(blockName, "plugin:")
	if !ok {
		return false
	}
	pid, _, ok := strings.Cut(rest, "/")
	if !ok || pid == "" {
		return false
	}
	return !m.isBundledPlugin(pid)
}

// blockLabel returns the display title for a block node. Reactive when
// def.Label reads model state.
func blockLabel(m *model, def *blockDef, blockName string) string {
	switch {
	case def == nil:
		return blockName
	case def.Label != nil:
		return def.Label(m)
	case def.Title != "":
		return T(def.Title)
	default:
		return blockName
	}
}

// otherStacksIn returns all KindStack nodes in doc whose ID differs from
// excludeStackID, in document walk order.
func otherStacksIn(doc *layoutdoc.Doc, excludeStackID string) []*layoutdoc.Node {
	if doc == nil || doc.Root == nil {
		return nil
	}
	var out []*layoutdoc.Node
	doc.Root.Walk(nil, func(path []*layoutdoc.Node) bool {
		n := path[len(path)-1]
		if n.Kind == layoutdoc.KindStack && n.ID != excludeStackID {
			out = append(out, n)
		}
		return true
	})
	return out
}

// stackMenuLabel builds a readable name for a destination stack in Move to ▸.
func stackMenuLabel(m *model, st *layoutdoc.Node) string {
	names := make([]string, 0, len(st.Children))
	for _, c := range st.Children {
		def, _ := m.blocks.lookup(c.Block)
		names = append(names, blockLabel(m, def, c.Block))
	}
	if len(names) == 0 {
		return st.ID
	}
	return strings.Join(names, ", ")
}

// closeBlockAction closes child from the layout document: removePluginBlock
// for plugin/agent blocks, closeLayoutBlock for built-in blocks.
func closeBlockAction(m *model, child *layoutdoc.Node) {
	if child == nil {
		return
	}
	if strings.HasPrefix(child.Block, "plugin:") || strings.HasPrefix(child.Block, "agent:") {
		m.removePluginBlock(child.Block)
		return
	}
	m.closeLayoutBlock(child.ID)
}

// collapseNearestCollapsible toggles the nearest collapsible ancestor (or self)
// of blockID in the active layout document.
func (m *model) collapseNearestCollapsible(blockID string) {
	doc := ggui.Peek(m.WinDoc)
	if doc == nil {
		return
	}
	path := doc.PathTo(func(n *layoutdoc.Node) bool { return n.ID == blockID })
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Collapsible {
			m.nodeState(path[i]).open.Update(func(open bool) bool { return !open })
			return
		}
	}
}

// tabContextMenuItems returns the right-click context menu entries for a tab
// inside a multi-tab stack.
func tabContextMenuItems(m *model, stackNode, child *layoutdoc.Node, blockMenu func() []ggui.Widget) []ggui.Widget {
	items := []ggui.Widget{
		ui.MenuItem(T("Move to split above"), func() {
			m.splitStackBlock(stackNode.ID, child.ID, stackNode.ID, true)
		}),
		ui.MenuItem(T("Move to split below"), func() {
			m.splitStackBlock(stackNode.ID, child.ID, stackNode.ID, false)
		}),
	}
	if blockMenu != nil {
		if extra := blockMenu(); len(extra) > 0 {
			items = append(items, ui.MenuDivider())
			items = append(items, extra...)
		}
	}
	items = append(items,
		ui.MenuDivider(),
		ui.MenuItem(T("Close panel"), func() { closeBlockAction(m, child) }),
	)
	return items
}

// soloHeader builds the unified dock header for a standalone panel block
// (one-child KindStack or bare chromePanel KindBlock) at headerHeight:
//
//	[⠿grip + icon + title (omitted when TitleInTools) | Tools | Accessory | ⋯]
//
// The grip/title handle is a DragSource[stackTabDrag] (StackID == "" for bare
// blocks, lazily wrapped via wrapInStack on move/split). Double-clicking the
// handle collapses a collapsible ancestor drawer. The whole header is a DropZone
// so dropping another tab turns the solo panel back into a tabbed stack.
func soloHeader(m *model, stackNode, child *layoutdoc.Node, def *blockDef, bv blockView) ggui.Widget {
	stackID := ""
	dropTargetID := child.ID
	if stackNode != nil {
		stackID = stackNode.ID
		dropTargetID = stackNode.ID
	}

	hovered := ggui.State(false)
	dropOver := ggui.State(false)
	var lastTap time.Time

	titleInTools := bv.titleInToolsOf
	if titleInTools == nil {
		titleInTools = ggui.Const(bv.TitleInTools)
	}

	handle := ggui.Reactive(func() ggui.Widget {
		label := blockLabel(m, def, child.Block)
		hideTitle := titleInTools.Get()
		parts := []ggui.Widget{&gripDots{visible: hovered}}
		if !hideTitle {
			if def != nil && def.Icon != "" {
				parts = append(parts, pluginIcon(def.Icon).Size(14).Color(skin.Muted))
			}
			parts = append(parts, widget.Text(label).Size(13).Weight(ggui.WeightMedium).Color(skin.Fg).NoWrap().Ellipsis())
		}
		face := widget.Box(widget.Row(parts...).Gap(5).Align(widget.AlignCenter)).Pad(4, 4, 4, 2)
		interactive := widget.Pointer(face).
			Cursor(ggui.CursorShapeMove).
			OnHover(func(over bool) { hovered.Set(over) }).
			OnTap(func() {
				now := time.Now()
				if !lastTap.IsZero() && now.Sub(lastTap) < 400*time.Millisecond {
					lastTap = time.Time{}
					m.collapseNearestCollapsible(child.ID)
					return
				}
				lastTap = now
			})
		return widget.DragSource(interactive, stackTabDrag{
			StackID: stackID,
			BlockID: child.ID,
			Block:   child.Block,
			Label:   label,
		}).OnDragStart(func() {
			m.StackDragging.Set(true)
		}).OnDragEnd(func(bool) {
			m.StackDragging.Set(false)
		})
	})

	overflow := ggui.Reactive(func() ggui.Widget {
		doc := m.WinDoc.Get()
		m.blocks.rev.Get()
		others := otherStacksIn(doc, stackID)
		hasSibling := len(others) > 0
		var siblingID string
		if hasSibling {
			siblingID = others[0].ID
		}
		items := []ggui.Widget{
			ui.MenuItem(T("Split above"), func() {
				if siblingID != "" {
					m.splitStackBlock(stackID, child.ID, siblingID, true)
				}
			}).Disabled(!hasSibling),
			ui.MenuItem(T("Split below"), func() {
				if siblingID != "" {
					m.splitStackBlock(stackID, child.ID, siblingID, false)
				}
			}).Disabled(!hasSibling),
		}
		if hasSibling {
			moveItems := make([]ggui.Widget, 0, len(others))
			for _, st := range others {
				targetID := st.ID
				moveItems = append(moveItems, ui.MenuItem(stackMenuLabel(m, st), func() {
					m.moveStackBlock(stackID, child.ID, targetID, -1)
				}))
			}
			items = append(items, ui.MenuSub(T("Move to"), moveItems...))
		}
		if bv.Menu != nil {
			if extra := bv.Menu(); len(extra) > 0 {
				items = append(items, ui.MenuDivider())
				items = append(items, extra...)
			}
		}
		items = append(items,
			ui.MenuDivider(),
			ui.MenuItem(T("Close panel"), func() { closeBlockAction(m, child) }),
		)
		return iconMenu(ui.MenuOf(icon("sliders-horizontal").Size(14).Color(skin.Muted), items...).Name(T("Menu")), 6)
	})

	var middle ggui.Widget = widget.Spacer()
	if bv.Tools != nil {
		if bv.hasTools != nil {
			middle = widget.Expanded(ggui.If(bv.hasTools, func() ggui.Widget {
				return bv.Tools
			}).Else(func() ggui.Widget {
				return widget.Box()
			}))
		} else {
			middle = widget.Expanded(bv.Tools)
		}
	}

	rowItems := []ggui.Widget{handle, middle}
	if bv.Accessory != nil {
		rowItems = append(rowItems, bv.Accessory)
	}
	rowItems = append(rowItems, overflow)

	bar := ggui.View(dropOver, func(over bool) ggui.Widget {
		box := widget.Box(widget.Row(rowItems...).Gap(6).Align(widget.AlignCenter)).
			Height(headerHeight).Pad(0, 6, 0, 6)
		if over {
			box = box.Border(1, skin.AccentBright)
		}
		return box
	})

	return widget.DropZone(bar, func(d widget.Dropped[stackTabDrag]) {
		dropOver.Set(false)
		m.StackDragging.Set(false)
		m.moveStackBlock(d.Value.StackID, d.Value.BlockID, dropTargetID, -1)
	}).Accept(func(d stackTabDrag) bool {
		return d.BlockID != "" && !(d.StackID == stackID && d.BlockID == child.ID)
	}).OnHover(func(over bool) {
		dropOver.Set(over)
	})
}

// soloPanel draws a standalone panel block: dockHeader, hairline divider, and
// Body overlaid with stack drop zones while a tab drag is active.
func soloPanel(m *model, stackNode, child *layoutdoc.Node, def *blockDef, bv blockView) ggui.Widget {
	dropTarget := child
	if stackNode != nil {
		dropTarget = stackNode
	}
	body := widget.Stack(
		bv.Body,
		ggui.If(m.StackDragging, func() ggui.Widget { return newStackDropZones(m, dropTarget) }),
	).Expand()
	return widget.Box(widget.Column(
		soloHeader(m, stackNode, child, def, bv),
		hline(),
		widget.Expanded(body),
	).Stretch()).Fill(skin.Surface0)
}

// tabbedPage draws one page inside a multi-tab stack: an optional slim bar for
// Tools and Accessory above Body, with no outer card frame or double header.
func tabbedPage(bv blockView) ggui.Widget {
	if bv.Tools == nil && bv.Accessory == nil {
		return bv.Body
	}
	var items []ggui.Widget
	if bv.Tools != nil {
		items = append(items, widget.Expanded(bv.Tools))
	} else {
		items = append(items, widget.Spacer())
	}
	if bv.Accessory != nil {
		items = append(items, bv.Accessory)
	}
	bar := widget.Box(widget.Row(items...).Gap(6).Align(widget.AlignCenter)).
		Height(tabBarHeight).Pad(0, 6, 0, 12)
	if bv.hasTools != nil {
		header := ggui.If(bv.hasTools, func() ggui.Widget {
			return widget.Column(bar, hline()).Stretch()
		})
		return widget.Column(header, widget.Expanded(bv.Body)).Stretch()
	}
	return widget.Column(bar, hline(), widget.Expanded(bv.Body)).Stretch()
}
