package gui

import (
	"image/color"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/ui/icons"
	"github.com/ironpark/ggui/widget"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/gui/iconset"
	"github.com/ironpark/mako/internal/gui/mdview"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A block's tree, a plugin's or an agent's, as the daemon checked it, drawn with the window's own
// components: the right panel's pills, chips and rows (panelkit.go), its
// buttons, fields and switches. A colour is a token of the theme in force,
// so a tree follows the theme and draws nothing the theme does not have;
// an icon not bundled is a plain square.

// toneColor is a tone's colour; "" is the text's own.
func toneColor(tone string) color.Color {
	switch tone {
	case "muted":
		return skin.Muted
	case "faint":
		return skin.ExtraMuted
	case "accent":
		return skin.AccentBright
	case "success":
		return skin.DiffAdded
	case "warning":
		return skin.DotWaiting
	case "danger":
		return skin.DiffRemoved
	}
	return skin.Fg
}

func textSize(size string) float64 {
	switch size {
	case "small":
		return 11.5
	case "large":
		return 15
	}
	return 13
}

func textWeight(w string) ggui.FontWeight {
	switch w {
	case "medium":
		return ggui.WeightMedium
	case "semibold":
		return ggui.WeightSemibold
	}
	return ggui.WeightRegular
}

// pluginIcon is a bundled icon, or a plain square for a name not bundled.
func pluginIcon(name string) *icons.Widget {
	if !iconset.Has(name) {
		name = "square"
	}
	return icon(name)
}

// blockDrag is the payload carried while a draggable node of a block's tree
// is dragged inside the window. Only drop targets of the same block accept it.
type blockDrag struct {
	Block string
	Node  string
	Value string
	Group string
}

// treeView is a block's tree as it is drawn: the block, and what the user
// set in its fields.
type treeView struct {
	m       *model
	block   string
	scope   string
	form    *blockForm
	onEvent func(block, node, kind, value string, values map[string]string)
}

// send tells the block's source of the user acting on node id, with the
// block's fields as they stand.
func (v *treeView) send(id, kind, value string) {
	if v.onEvent != nil {
		v.onEvent(v.block, id, kind, value, v.form.values())
		return
	}
	v.m.blockEvent(v.block, v.scope, id, kind, value, v.form.values())
}

// node draws a node of the tree; inLine says it is a row's child, where
// text keeps to a line.
func (v *treeView) node(n *protocol.UiNode, inLine bool) ggui.Widget {
	tap := func() {}
	if n.GetTap() {
		id := n.GetId()
		tap = func() { v.send(id, protocol.UiTap, "") }
	}
	switch n.GetKind() {
	case "column", "row", "stack", "card", "scroll", "list", "table", "wrap", "grid", "split":
		return v.container(n)
	case "collapsible":
		return v.collapsibleNode(n)
	case "breadcrumb":
		return v.breadcrumbNode(n)
	case "tree":
		return v.treeNode(n)
	case "tree_item":
		return v.treeItemNode(n, 0)
	case "graph":
		return v.graphNode(n)
	case "text":
		t := widget.Text(n.GetText()).Size(textSize(n.GetSize())).Weight(textWeight(n.GetWeight())).Color(toneColor(n.GetTone()))
		if inLine {
			t = t.NoWrap().Ellipsis()
		}
		return v.dragDrop(tappable(t, n, tap), n)
	case "icon":
		return v.dragDrop(tappable(pluginIcon(n.GetIcon()).Size(textSize(n.GetSize())+2).Color(toneColor(cmpTone(n.GetTone(), "muted"))), n, tap), n)
	case "pill":
		return v.dragDrop(tappable(pill(n.GetText(), toneColor(cmpTone(n.GetTone(), "muted"))), n, tap), n)
	case "chip":
		return v.dragDrop(tappable(chip(n.GetText()), n, tap), n)
	case "button":
		return v.buttonNode(n, tap)
	case "input":
		id := n.GetId()
		f := ui.TextField(v.form.input(id, n.GetValue())).Key(v.block + "\x00" + v.scope + id).Placeholder(n.GetText()).Name(cmpTone(n.GetText(), T("Input")))
		if n.GetChange() {
			f = f.OnSubmit(func(s string) { v.send(id, protocol.UiSubmit, s) }).
				OnCommit(func(s string) { v.send(id, protocol.UiChange, s) })
		}
		return f.Disabled(n.GetDisabled())
	case "textarea":
		id := n.GetId()
		lines := 4
		switch n.GetSize() {
		case "large":
			lines = 14
		case "small":
			lines = 3
		}
		f := ui.TextField(v.form.input(id, n.GetValue())).Key(v.block + "\x00" + v.scope + id).Lines(lines).Placeholder(n.GetText()).Name(cmpTone(n.GetText(), T("Input")))
		if n.GetChange() {
			f = f.OnSubmit(func(s string) { v.send(id, protocol.UiSubmit, s) }).
				OnCommit(func(s string) { v.send(id, protocol.UiChange, s) })
		}
		return f.Disabled(n.GetDisabled())
	case "toggle":
		id := n.GetId()
		field := v.form.field(id, strconv.FormatBool(n.GetChecked()))
		sw := ui.Switch(ggui.Bind(func() bool { return field.Get() == "true" }, func(on bool) {
			field.Set(strconv.FormatBool(on))
			if n.GetChange() {
				v.send(id, protocol.UiChange, strconv.FormatBool(on))
			}
		}), n.GetText()).Name(n.GetText())
		return sw.Disabled(n.GetDisabled())
	case "select":
		id := n.GetId()
		field := v.form.field(id, n.GetValue())
		return widget.Box(ui.Select(ggui.Bind(field.Get, func(x string) {
			field.Set(x)
			if n.GetChange() {
				v.send(id, protocol.UiChange, x)
			}
		})).Options(n.GetOptions()).Name(T("Choose")).Disabled(n.GetDisabled())).MaxWidth(200)
	case "tabs":
		id := n.GetId()
		field := v.form.field(id, n.GetValue())
		opts := n.GetOptions()
		return ggui.View(field, func(active string) ggui.Widget {
			btns := make([]ggui.Widget, 0, len(opts))
			for _, opt := range opts {
				o := opt
				b := ui.Button(o, func() {
					if n.GetDisabled() {
						return
					}
					field.Set(o)
					if n.GetChange() {
						v.send(id, protocol.UiChange, o)
					}
				}).Disabled(n.GetDisabled()).Name(o)
				if o == active {
					b = b.Secondary()
				} else {
					b = b.Ghost()
				}
				btns = append(btns, b)
			}
			return widget.Box(looseHeight{widget.Scroll(widget.Row(btns...).Gap(4).Align(widget.AlignCenter)).Horizontal().Shrink().Bar(color.Transparent)}).Fill(skin.Surface1).Border(1, skin.Border).Radius(8).Pad(3)
		})
	case "markdown":
		host := pluginPage{pageHost: pageHost{m: v.m}}
		if n.GetChange() && !n.GetDisabled() {
			id := n.GetId()
			host.onLink = func(dest string) {
				v.send(id, protocol.UiChange, dest)
			}
		}
		return mdview.Column(host, n.GetText())
	case "code":
		return toolBlock(n.GetText(), false)
	case "progress":
		return ui.Progress(ggui.Const(n.GetProgress()))
	case "divider":
		return hline()
	case "chart":
		return chartNode(n)
	}
	return widget.Box() // the daemon sends no other kind
}

// looseHeight passes MaxH=Unbounded to its child so horizontal ScrollWidgets
// shrink vertically to their content's natural height instead of expanding to a
// bounded parent's full height (such as inside a fill:true root column).
type looseHeight struct{ child ggui.Widget }

func (w looseHeight) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	inner := c
	inner.MinH = 0
	inner.MaxH = ggui.Unbounded
	return c.Constrain(w.child.Layout(inner, env))
}

func (w looseHeight) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(w.child, r)
}

// tappable makes a node that sends a tap answer one.
func tappable(w ggui.Widget, n *protocol.UiNode, tap func()) ggui.Widget {
	if !n.GetTap() {
		return w
	}
	return ui.ButtonOf(w, tap).Ghost().Pad(2, 4).Radius(6).Name(n.GetText())
}

func cmpTone(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// buttonNode renders a plugin button respecting variant, size, tone, icon and icon-only layouts.
func (v *treeView) buttonNode(n *protocol.UiNode, tap func()) ggui.Widget {
	text := n.GetText()
	iconName := n.GetIcon()
	var b *ui.ButtonWidget
	iconOnly := text == "" && iconName != ""
	if iconOnly {
		b = ui.ButtonOf(widget.Box(), tap).Icon(pluginIcon(iconName))
		switch n.GetSize() {
		case "small":
			b = b.Size(ui.ButtonIconSmall)
		case "large":
			b = b.Size(ui.ButtonIconLarge)
		default:
			b = b.Size(ui.ButtonIcon)
		}
	} else {
		b = ui.Button(text, tap)
		if iconName != "" {
			b = b.Icon(pluginIcon(iconName))
		}
		switch n.GetSize() {
		case "small":
			b = b.Size(ui.ButtonSmall)
		case "large":
			b = b.Size(ui.ButtonLarge)
		}
	}

	switch n.GetVariant() {
	case "primary":
		// ui.Button defaults to ButtonPrimary.
	case "secondary":
		b = b.Secondary()
	case "outline":
		b = b.Outline()
	case "ghost":
		b = b.Ghost()
	case "danger":
		b = b.Destructive()
	default:
		switch n.GetTone() {
		case "accent":
		case "danger":
			b = b.Destructive()
		default:
			b = b.Outline()
		}
	}

	name := text
	if name == "" {
		name = iconName
	}
	return v.dragDrop(b.Disabled(n.GetDisabled() || !n.GetTap()).Name(name), n)
}

// collapsibleNode renders a native disclosure section whose open state is
// persisted locally in blockForm and optionally emits UiChange ("true"|"false").
func (v *treeView) collapsibleNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	st := v.form.local(id, strconv.FormatBool(n.GetChecked()))
	open := ggui.Bind(
		func() bool { return st.Get() == "true" },
		func(next bool) {
			val := strconv.FormatBool(next)
			st.Set(val)
			if n.GetChange() {
				v.send(id, protocol.UiChange, val)
			}
		},
	)

	trigger := ggui.View(st, func(cur string) ggui.Widget {
		chev := "chevron-right"
		if cur == "true" {
			chev = "chevron-down"
		}
		head := []ggui.Widget{icon(chev).Size(13).Color(skin.Muted)}
		if n.GetIcon() != "" {
			head = append(head, pluginIcon(n.GetIcon()).Size(14).Color(toneColor(cmpTone(n.GetTone(), "muted"))))
		}
		head = append(head, widget.Expanded(widget.Text(n.GetText()).Size(12.5).Weight(ggui.WeightMedium).Color(toneColor(n.GetTone())).NoWrap().Ellipsis()))
		return widget.Box(widget.Row(head...).Gap(6).Align(widget.AlignCenter)).Pad(6, 8).Radius(6).Fill(skin.Surface1)
	})

	gap := n.GetGap()
	if gap == 0 {
		gap = 6
	}
	kids := make([]ggui.Widget, 0, len(n.GetChildren()))
	for _, c := range n.GetChildren() {
		w := v.node(c, false)
		switch g := c.GetGrow(); {
		case g == 1:
			w = widget.Expanded(w)
		case g > 0:
			w = widget.Flex(w, g)
		}
		kids = append(kids, w)
	}
	body := widget.Padding(widget.Column(kids...).Gap(gap).Stretch(), 6, 8, 4, 16)
	cw := ui.CollapsibleOf(open, trigger, body).Disabled(n.GetDisabled())
	cw.SetName(n.GetText())
	return v.dragDrop(cw, n)
}

// breadcrumbNode renders clickable path segments separated by chevrons.
func (v *treeView) breadcrumbNode(n *protocol.UiNode) ggui.Widget {
	opts := n.GetOptions()
	id := n.GetId()
	crumbs := make([]ui.BreadcrumbEntry, len(opts))
	for i, seg := range opts {
		var onTap func()
		if n.GetChange() && !n.GetDisabled() && i < len(opts)-1 {
			idx := i
			onTap = func() { v.send(id, protocol.UiChange, strconv.Itoa(idx)) }
		}
		crumbs[i] = ui.Crumb(seg, onTap)
	}
	bc := ui.Breadcrumb(crumbs...)
	if n.GetText() != "" {
		bc = bc.Name(n.GetText())
	}
	return bc
}

// treeNode renders a hierarchical list of tree_item nodes.
func (v *treeView) treeNode(n *protocol.UiNode) ggui.Widget {
	gap := n.GetGap()
	kids := make([]ggui.Widget, 0, len(n.GetChildren()))
	for _, c := range n.GetChildren() {
		kids = append(kids, v.treeItemNode(c, 0))
	}
	return v.dragDrop(widget.Column(kids...).Gap(gap).Stretch(), n)
}

// treeItemNode renders a single tree_item row with vertical indent guides,
// persisted expand/collapse state, icon, primary label, secondary value text,
// active accent styling, tap handling, and full drag/drop support.
func (v *treeView) treeItemNode(n *protocol.UiNode, depth int) ggui.Widget {
	id := n.GetId()
	hasKids := len(n.GetChildren()) > 0
	expandable := hasKids || n.GetChange()
	st := v.form.local(id, strconv.FormatBool(n.GetChecked()))
	toggle := func() {
		if n.GetDisabled() || !expandable {
			return
		}
		next := ggui.Peek(st) != "true"
		val := strconv.FormatBool(next)
		st.Set(val)
		if n.GetChange() {
			v.send(id, protocol.UiChange, val)
		}
	}

	return ggui.View(st, func(cur string) ggui.Widget {
		expanded := cur == "true"
		active := n.GetTone() == "accent"

		var cells []ggui.Widget
		// Subtle vertical indent guides for ancestor depth levels.
		for range depth {
			guide := widget.Box(widget.Box().Width(1).Height(22).Fill(skin.Border)).Width(16).Pad(0, 0, 0, 7)
			cells = append(cells, guide)
		}

		// Expand/collapse chevron or alignment spacer for leaf nodes.
		if expandable {
			chev := "chevron-right"
			chevName := Tf("Expand %s", n.GetText())
			if expanded {
				chev = "chevron-down"
				chevName = Tf("Collapse %s", n.GetText())
			}
			cells = append(cells, ui.ButtonOf(icon(chev).Size(12).Color(skin.Muted), toggle).
				Ghost().Pad(2).Radius(4).Disabled(n.GetDisabled()).Name(chevName))
		} else {
			cells = append(cells, widget.Box().Width(16))
		}

		// Row body: icon, primary label, optional secondary value text.
		var bodyItems []ggui.Widget
		if n.GetIcon() != "" {
			bodyItems = append(bodyItems, pluginIcon(n.GetIcon()).Size(14).Color(toneColor(cmpTone(n.GetTone(), "muted"))))
		}
		weight := textWeight(n.GetWeight())
		if active && n.GetWeight() == "" {
			weight = ggui.WeightMedium
		}
		bodyItems = append(bodyItems, widget.Expanded(
			widget.Text(n.GetText()).Size(12.5).Weight(weight).Color(toneColor(n.GetTone())).NoWrap().Ellipsis(),
		))
		if n.GetValue() != "" {
			bodyItems = append(bodyItems, widget.Text(n.GetValue()).Size(11).Color(skin.Muted).NoWrap())
		}
		body := widget.Row(bodyItems...).Gap(6).Align(widget.AlignCenter)

		var rowContent ggui.Widget = widget.Expanded(widget.Padding(body, 4, 6))
		switch {
		case n.GetTap() && !n.GetDisabled():
			rowContent = widget.Expanded(ui.ButtonOf(body, func() { v.send(id, protocol.UiTap, "") }).
				Ghost().Pad(4, 6).Radius(6).Name(n.GetText()))
		case expandable && !n.GetDisabled():
			rowContent = widget.Expanded(ui.ButtonOf(body, toggle).
				Ghost().Pad(4, 6).Radius(6).Name(n.GetText()))
		}
		cells = append(cells, rowContent)

		rowBox := widget.Box(widget.Row(cells...).Gap(2).Align(widget.AlignCenter)).Radius(6).Pad(1, 4)
		if active {
			rowBox = rowBox.Fill(skin.Surface2)
		}
		row := v.treeItemDragDrop(rowBox, n)

		if !hasKids || !expanded {
			return row
		}
		sub := make([]ggui.Widget, 0, 1+len(n.GetChildren()))
		sub = append(sub, row)
		for _, child := range n.GetChildren() {
			sub = append(sub, v.treeItemNode(child, depth+1))
		}
		return widget.Column(sub...).Gap(1).Stretch()
	})
}

// treeItemDragDrop attaches DragSource/DropZone directly to a tree_item row
// without adding card padding or framing around it.
func (v *treeView) treeItemDragDrop(w ggui.Widget, n *protocol.UiNode) ggui.Widget {
	drag := n.GetDrag()
	drop := n.GetDrop()
	if drag == "" && !drop {
		return w
	}
	id := n.GetId()
	accept := n.GetDropAccept()
	accepts := func(d blockDrag) bool {
		if d.Block != v.block || d.Node == id || d.Value == "" {
			return false
		}
		if len(accept) > 0 && !slices.Contains(accept, d.Group) {
			return false
		}
		return true
	}
	if drop {
		inner := w
		over := ggui.State(false)
		framed := ggui.View(over, func(hot bool) ggui.Widget {
			if hot {
				return widget.Box(inner).Fill(skin.Surface2).Border(1.5, skin.AccentBright).Radius(6)
			}
			return inner
		})
		w = widget.DropZone(framed, func(d widget.Dropped[blockDrag]) {
			over.Set(false)
			if !accepts(d.Value) {
				return
			}
			v.send(id, protocol.UiDrop, d.Value.Value)
		}).Accept(accepts).OnHover(over.Set)
	}
	if drag != "" && !n.GetDisabled() {
		w = widget.DragSource(w, blockDrag{Block: v.block, Node: id, Value: drag, Group: n.GetDragGroup()})
	}
	return w
}

// container draws a column, row, stack, card, scroll, list, table, wrap, grid
// or split container.
func (v *treeView) container(n *protocol.UiNode) ggui.Widget {
	kind := n.GetKind()
	gap := n.GetGap()

	// Wrap, grid, and split handle child layout directly without flex wrappers.
	switch kind {
	case "wrap":
		if gap == 0 {
			gap = 6
		}
		kids := make([]ggui.Widget, 0, len(n.GetChildren()))
		for _, c := range n.GetChildren() {
			kids = append(kids, v.node(c, true))
		}
		return v.dragDrop(v.tappableBox(widget.Wrap(kids...).Gap(gap).RunGap(gap).Align(widget.AlignCenter), n), n)

	case "grid":
		if gap == 0 {
			gap = 8
		}
		cols := int(n.GetColumns())
		if cols < 1 {
			cols = 1
		}
		if cols > 6 {
			cols = 6
		}
		children := n.GetChildren()
		var rows []ggui.Widget
		for i := 0; i < len(children); i += cols {
			rowKids := make([]ggui.Widget, 0, cols)
			for j := range cols {
				if i+j < len(children) {
					rowKids = append(rowKids, widget.Expanded(v.node(children[i+j], false)))
				} else {
					rowKids = append(rowKids, widget.Expanded(widget.Box()))
				}
			}
			rows = append(rows, widget.Row(rowKids...).Gap(gap).Align(widget.AlignStart))
		}
		return v.dragDrop(v.tappableBox(widget.Column(rows...).Gap(gap).Stretch(), n), n)

	case "split":
		children := n.GetChildren()
		if len(children) < 2 {
			return widget.Box()
		}
		id := n.GetId()
		initial := n.GetRatio()
		if initial < 0.1 || initial > 0.9 {
			initial = 0.5
		}
		st := v.form.local(id, strconv.FormatFloat(initial, 'f', 4, 64))
		ratio := ggui.Bind(
			func() float64 {
				f, err := strconv.ParseFloat(st.Get(), 64)
				if err != nil || f < 0.1 || f > 0.9 {
					return initial
				}
				return f
			},
			func(f float64) {
				st.Set(strconv.FormatFloat(clampFloat(f, 0.1, 0.9), 'f', 4, 64))
			},
		)
		first := v.node(children[0], false)
		second := v.node(children[1], false)
		sp := ui.Resizable(ratio, first, second).WithHandle().MinSizes(60, 60).
			Disabled(n.GetDisabled()).Name(cmpTone(n.GetText(), T("Pane divider")))
		if n.GetDirection() == "vertical" {
			sp = sp.Vertical()
		}
		var out ggui.Widget = sp
		if h := n.GetMaxHeight(); h > 0 {
			out = widget.Box(out).MaxHeight(h)
		}
		return v.dragDrop(out, n)
	}

	row := kind == "row"
	kids := make([]ggui.Widget, 0, len(n.GetChildren()))
	for _, c := range n.GetChildren() {
		var w ggui.Widget
		if kind == "list" && c.GetTap() {
			id := c.GetId()
			inner := proto.CloneOf(c) // drawn as the row's content; the row answers tap/drag/drop
			inner.Tap, inner.Drag, inner.Drop = false, "", false
			w = listRow(v.node(inner, false), strings.Join(texts(c), " "), func() { v.send(id, protocol.UiTap, "") })
			w = v.dragDrop(w, c)
		} else {
			w = v.node(c, row)
		}
		switch g := c.GetGrow(); {
		case g == 1:
			w = widget.Expanded(w)
		case g > 0:
			w = widget.Flex(w, g)
		}
		kids = append(kids, w)
	}
	switch kind {
	case "row":
		if gap == 0 {
			gap = 8
		}
		align := widget.AlignCenter
		for _, c := range n.GetChildren() {
			if c.GetKind() == "column" || c.GetKind() == "list" || c.GetKind() == "card" || c.GetKind() == "scroll" || c.GetKind() == "grid" || c.GetKind() == "split" {
				align = widget.AlignStart
				break
			}
		}
		return v.dragDrop(v.tappableBox(widget.Row(kids...).Gap(gap).Align(align), n), n)
	case "stack":
		return v.dragDrop(widget.Stack(kids...), n)
	case "card":
		if gap == 0 {
			gap = 6
		}
		inner := v.tappableBox(widget.Column(kids...).Gap(gap).Stretch(), n)
		// If dragDrop will draw the card frame/highlight, avoid double framing.
		if n.GetDrag() != "" || n.GetDrop() {
			return v.dragDrop(inner, n)
		}
		var border color.Color = skin.Border
		if n.GetTone() != "" {
			border = toneColor(n.GetTone())
		}
		switch n.GetVariant() {
		case "ghost":
			return widget.Box(inner).Radius(8).Pad(8, 10)
		case "outline":
			return widget.Box(inner).Border(1, border).Radius(8).Pad(8, 10)
		case "secondary":
			return widget.Box(inner).Fill(skin.Surface2).Border(1, border).Radius(8).Pad(8, 10)
		case "danger":
			return widget.Box(inner).Fill(skin.DiffRemovedBg).Border(1, skin.DiffRemoved).Radius(8).Pad(8, 10)
		case "primary":
			return widget.Box(inner).Fill(skin.Surface2).Border(1, skin.AccentBright).Radius(8).Pad(8, 10)
		default:
			return widget.Box(inner).Fill(skin.Surface1).Border(1, border).Radius(8).Pad(8, 10)
		}
	case "scroll":
		horiz := n.GetDirection() == "horizontal"
		var body ggui.Widget
		if horiz {
			if gap == 0 {
				gap = 8
			}
			body = widget.Row(kids...).Gap(gap).Align(widget.AlignStart)
		} else {
			if gap == 0 {
				gap = 6
			}
			body = widget.Column(kids...).Gap(gap).Stretch()
		}
		sc := widget.Scroll(body).Shrink().Bar(skin.Surface3)
		var out ggui.Widget = sc
		if horiz {
			sc = sc.Horizontal()
			out = looseHeight{sc}
		}
		if h := n.GetMaxHeight(); h > 0 {
			out = widget.Box(out).MaxHeight(h)
		}
		return v.dragDrop(out, n)
	case "table":
		var trows []ggui.Widget
		if hdrs := n.GetLabels(); len(hdrs) > 0 {
			cols := make([]ggui.Widget, len(hdrs))
			for i, h := range hdrs {
				cols[i] = widget.Expanded(widget.Text(h).Size(11.5).Weight(ggui.WeightSemibold).Color(skin.Muted).NoWrap().Ellipsis())
			}
			trows = append(trows, widget.Box(widget.Row(cols...).Gap(8).Align(widget.AlignCenter)).Pad(6, 10).Fill(skin.Surface1), hline())
		}
		for i, k := range kids {
			if i > 0 {
				trows = append(trows, hline())
			}
			trows = append(trows, widget.Box(k).Pad(6, 10))
		}
		return v.dragDrop(widget.Box(widget.Column(trows...).Stretch()).Border(1, skin.Border).Radius(8), n)
	case "list":
		return v.dragDrop(widget.Column(kids...).Gap(gap).Stretch(), n)
	}
	if gap == 0 {
		gap = 6
	}
	return v.dragDrop(v.tappableBox(widget.Column(kids...).Gap(gap).Stretch(), n), n)
}

func isContainerKind(kind string) bool {
	switch kind {
	case "column", "row", "stack", "card", "scroll", "list", "table", "wrap", "grid", "split":
		return true
	}
	return false
}

// dragDrop wires DragSource and DropZone around a node that sets drag or
// onDrop, framing draggable containers as cards and highlighting active drop
// zones while a dragged node of the same block hovers over them.
func (v *treeView) dragDrop(w ggui.Widget, n *protocol.UiNode) ggui.Widget {
	drag := n.GetDrag()
	drop := n.GetDrop()
	if drag == "" && !drop {
		return w
	}
	id := n.GetId()
	accept := n.GetDropAccept()
	isCard := n.GetKind() == "card" || (drag != "" && isContainerKind(n.GetKind()))
	isLane := !isCard && isContainerKind(n.GetKind())
	accepts := func(d blockDrag) bool {
		if d.Block != v.block || d.Node == id || d.Value == "" {
			return false
		}
		if len(accept) > 0 && !slices.Contains(accept, d.Group) {
			return false
		}
		return true
	}
	if drop {
		inner := w
		over := ggui.State(false)
		framed := ggui.View(over, func(hot bool) ggui.Widget {
			switch {
			case hot && isCard:
				// Show a crisp top insertion bar when reordering onto another card.
				bar := widget.Box().Height(2).Fill(skin.AccentBright).Radius(1)
				card := widget.Box(inner).Fill(skin.Surface2).Border(1.5, skin.AccentBright).Radius(8).Pad(8, 10)
				return widget.Column(bar, card).Gap(4).Stretch()
			case isCard:
				return widget.Box(inner).Fill(skin.Surface1).Border(1, skin.Border).Radius(8).Pad(8, 10)
			case hot && isLane:
				return widget.Box(inner).Fill(skin.Surface2).Border(1.5, skin.AccentBright).Radius(8).Pad(8)
			case isLane:
				return widget.Box(inner).Fill(skin.Surface1).Border(1, skin.Border).Radius(8).Pad(8)
			case hot:
				return widget.Box(inner).Border(1.5, skin.AccentBright).Radius(6)
			default:
				return inner
			}
		})
		w = widget.DropZone(framed, func(d widget.Dropped[blockDrag]) {
			over.Set(false)
			if !accepts(d.Value) {
				return
			}
			v.send(id, protocol.UiDrop, d.Value.Value)
		}).Accept(accepts).OnHover(over.Set)
	} else if isCard {
		w = widget.Box(w).Fill(skin.Surface1).Border(1, skin.Border).Radius(8).Pad(8, 10)
	}
	if drag != "" && !n.GetDisabled() {
		w = widget.DragSource(w, blockDrag{Block: v.block, Node: id, Value: drag, Group: n.GetDragGroup()})
	}
	return w
}

func (v *treeView) tappableBox(w ggui.Widget, n *protocol.UiNode) ggui.Widget {
	if !n.GetTap() {
		return w
	}
	id := n.GetId()
	return listRow(w, strings.Join(texts(n), " "), func() { v.send(id, protocol.UiTap, "") })
}

// texts are a node's text, and its children's, for a row's name.
func texts(n *protocol.UiNode) []string {
	var out []string
	if n.GetText() != "" {
		out = append(out, n.GetText())
	}
	for _, c := range n.GetChildren() {
		out = append(out, texts(c)...)
	}
	return out
}

// pluginPage is the host of a plugin's Markdown. It reads no pictures,
// which would be fetched from wherever the plugin names, past the hosts
// it may reach, carrying what it put in their addresses; it draws local
// diagrams (pure Go, zero network); and it opens web links externally
// while forwarding internal/custom-scheme links to onLink when set.
type pluginPage struct {
	pageHost
	onLink func(dest string)
}

func (pluginPage) Image(string) *ggui.StateValue[mdview.Image] { return nil }
func (p pluginPage) Diagram(src, lang string) *ggui.StateValue[mdview.Image] {
	if p.m == nil || p.m.post == nil {
		return nil
	}
	return p.pageHost.Diagram(src, lang)
}
func (p pluginPage) Open(dest string) {
	if p.onLink != nil {
		if strings.HasPrefix(dest, "https://") || strings.HasPrefix(dest, "http://") {
			openPluginLink(dest)
			return
		}
		p.onLink(dest)
		return
	}
	openPluginLink(dest)
}

// blockForm is what the user set in a block's inputs, toggles and selects,
// by node id: kept across the tree's updates, so that typing survives
// one, and sent with every event, so that a form's submit carries all its
// fields. A value the tree gives anew replaces the user's.
//
// locals and graphs hold client-only UI state (collapsible/tree_item expanded
// flags, split ratios, graph pan/zoom/layout) that survives re-renders without
// being included in BlockEvent.Values.
type blockForm struct {
	fields     map[string]*ggui.StateValue[string]
	given      map[string]string // what the tree last gave each
	drawn      map[string]bool   // the fields of the tree drawn last
	locals     map[string]*ggui.StateValue[string]
	localGiven map[string]string
	graphs     map[string]*graphViewState
}

func newBlockForm() *blockForm {
	return &blockForm{
		fields:     map[string]*ggui.StateValue[string]{},
		given:      map[string]string{},
		drawn:      map[string]bool{},
		locals:     map[string]*ggui.StateValue[string]{},
		localGiven: map[string]string{},
		graphs:     map[string]*graphViewState{},
	}
}

// redraw starts a drawing of the tree: the fields it draws are the form's.
func (f *blockForm) redraw() { f.drawn = map[string]bool{} }

// reset clears all field and local UI state, used when a scoped block switches scope.
func (f *blockForm) reset() {
	f.fields = map[string]*ggui.StateValue[string]{}
	f.given = map[string]string{}
	f.drawn = map[string]bool{}
	f.locals = map[string]*ggui.StateValue[string]{}
	f.localGiven = map[string]string{}
	f.graphs = map[string]*graphViewState{}
}

// field is the value of node id, which the tree gives as given.
func (f *blockForm) field(id, given string) *ggui.StateValue[string] {
	f.drawn[id] = true
	st, ok := f.fields[id]
	if !ok {
		st = ggui.State(given)
		f.fields[id] = st
	} else if f.given[id] != given {
		st.Set(given)
	}
	f.given[id] = given
	return st
}

// local keeps client-only UI state for node id across updates without adding
// it to values() sent on BlockEvent. If the tree sends a new default value,
// the local state syncs to it.
func (f *blockForm) local(id, given string) *ggui.StateValue[string] {
	if f.locals == nil {
		f.locals = map[string]*ggui.StateValue[string]{}
		f.localGiven = map[string]string{}
	}
	st, ok := f.locals[id]
	if !ok {
		st = ggui.State(given)
		f.locals[id] = st
	} else if f.localGiven[id] != given {
		st.Set(given)
	}
	f.localGiven[id] = given
	return st
}

// graphState returns persistent view and layout state for graph node id.
func (f *blockForm) graphState(id string) *graphViewState {
	if f.graphs == nil {
		f.graphs = map[string]*graphViewState{}
	}
	st, ok := f.graphs[id]
	if !ok {
		st = newGraphViewState()
		f.graphs[id] = st
	}
	return st
}

// input is an input's text, as field.
func (f *blockForm) input(id, given string) ggui.Binding[string] { return f.field(id, given) }

// values are the fields of the tree drawn last, as they stand.
func (f *blockForm) values() map[string]string {
	if f == nil || len(f.drawn) == 0 {
		return nil
	}
	out := make(map[string]string, len(f.drawn))
	for id := range f.drawn {
		out[id] = ggui.Peek(f.fields[id])
	}
	return out
}
