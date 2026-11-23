package gui

import (
	"image/color"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/ui/icons"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

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
	return v.nodeWith(n, inLine, false)
}

// nodeWith draws n; bare suppresses outer Tap/Drag/Drop on n itself (for a
// tappable list row's inner content) while keeping descendants interactive.
func (v *treeView) nodeWith(n *protocol.UiNode, inLine, bare bool) ggui.Widget {
	tap := func() {}
	isTap := !bare && n.GetTap()
	if isTap {
		id := n.GetId()
		tap = func() { v.send(id, protocol.UiTap, "") }
	}
	switch n.GetKind() {
	case "column", "row", "stack", "card", "scroll", "list", "table", "wrap", "grid", "split":
		return v.container(n, bare)
	case "collapsible":
		return v.collapsibleNode(n, bare)
	case "breadcrumb":
		return v.breadcrumbNode(n)
	case "tree":
		return v.treeNode(n, bare)
	case "tree_item":
		return v.treeItemNode(n, 0, bare)
	case "graph":
		return v.graphNode(n)
	case "text":
		t := widget.Text(n.GetText()).Size(textSize(n.GetSize())).Weight(textWeight(n.GetWeight())).Color(toneColor(n.GetTone()))
		if inLine {
			t = t.NoWrap().Ellipsis()
		}
		return v.maybeDragDrop(tappableWhen(t, n, isTap, tap), n, bare)
	case "icon":
		return v.maybeDragDrop(tappableWhen(pluginIcon(n.GetIcon()).Size(textSize(n.GetSize())+2).Color(toneColor(cmpTone(n.GetTone(), "muted"))), n, isTap, tap), n, bare)
	case "pill":
		return v.maybeDragDrop(tappableWhen(v.pillNode(n), n, isTap, tap), n, bare)
	case "chip":
		return v.maybeDragDrop(tappableWhen(v.chipNode(n), n, isTap, tap), n, bare)
	case "button":
		return v.buttonNode(n, tap, bare)
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
		if n.GetVariant() == "spinner" {
			if n.GetText() == "" {
				return ui.Spinner().Size(13)
			}
			return widget.Row(
				ui.Spinner().Size(13),
				widget.Text(n.GetText()).Size(12).Color(toneColor(cmpTone(n.GetTone(), "muted"))).NoWrap().Ellipsis(),
			).Gap(6).Align(widget.AlignCenter)
		}
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

// tappableWhen makes a node that sends a tap answer one when active is true.
func tappableWhen(w ggui.Widget, n *protocol.UiNode, active bool, tap func()) ggui.Widget {
	if !active {
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

// solidToneColors returns a WCAG AA (>= 4.5:1) solid fill and foreground pair
// for solid pill and chip badges across both dark and light themes.
func solidToneColors(tone string) (fill, fg color.Color) {
	switch tone {
	case "accent":
		return skin.AccentBright, contrastInk{bg: skin.AccentBright}
	case "success":
		return skin.DiffAdded, contrastInk{bg: skin.DiffAdded}
	case "warning":
		return skin.DotWaiting, contrastInk{bg: skin.DotWaiting}
	case "danger":
		return uitheme.Destructive, contrastInk{bg: uitheme.Destructive}
	case "muted":
		return skin.Surface3, skin.Fg
	case "faint":
		return skin.Surface2, skin.Fg
	default:
		return uitheme.Primary, uitheme.PrimaryFg
	}
}

// contrastInk is an EnvColor that resolves to whichever of white or dark ink
// yields the higher WCAG contrast ratio against bg in the active theme.
type contrastInk struct{ bg color.Color }

func (c contrastInk) RGBA() (uint32, uint32, uint32, uint32) {
	return pickContrastInk(c.bg).RGBA()
}

func (c contrastInk) Resolve(env ggui.Env) color.Color {
	return pickContrastInk(ggui.ResolveColor(c.bg, env))
}

func pickContrastInk(bg color.Color) color.Color {
	light := skin.Hex(0xFFFFFF)
	dark := skin.Hex(0x181B1A)
	if bg == nil {
		return dark
	}
	if wcagContrast(bg, light) >= wcagContrast(bg, dark) {
		return light
	}
	return dark
}

func wcagContrast(a, b color.Color) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func relativeLuminance(c color.Color) float64 {
	if c == nil {
		return 0
	}
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	lin := func(v uint8) float64 {
		s := float64(v) / 255.0
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(n.R) + 0.7152*lin(n.G) + 0.0722*lin(n.B)
}

// pillNode renders a pill respecting soft (default), solid, and outline variants
// and an optional leading icon.
func (v *treeView) pillNode(n *protocol.UiNode) ggui.Widget {
	c := toneColor(cmpTone(n.GetTone(), "muted"))
	iconName := n.GetIcon()
	content := func(fg color.Color) ggui.Widget {
		lbl := widget.Text(n.GetText()).Size(10.5).Weight(ggui.WeightMedium).Color(fg).NoWrap().Ellipsis()
		if iconName == "" {
			return lbl
		}
		return widget.Row(pluginIcon(iconName).Size(11).Color(fg), lbl).Gap(4).Align(widget.AlignCenter)
	}
	switch n.GetVariant() {
	case "solid":
		fill, fg := solidToneColors(n.GetTone())
		return widget.Box(content(fg)).Pad(2, 8).Radius(999).Fill(fill).Clip().MaxWidth(200)
	case "outline":
		return widget.Box(content(c)).Pad(2, 8).Radius(999).Border(1, c).Clip().MaxWidth(200)
	default: // "" or "soft"
		return widget.Box(&washed{child: widget.Box(content(c)).Pad(2, 8).Clip(), color: c}).MaxWidth(200)
	}
}

// chipNode renders a chip respecting default (mono Surface2), soft, solid, and
// outline variants and an optional leading icon.
func (v *treeView) chipNode(n *protocol.UiNode) ggui.Widget {
	iconName := n.GetIcon()
	withIcon := func(fg color.Color, lbl ggui.Widget) ggui.Widget {
		if iconName == "" {
			return lbl
		}
		return widget.Row(pluginIcon(iconName).Size(11).Color(fg), lbl).Gap(4).Align(widget.AlignCenter)
	}
	switch n.GetVariant() {
	case "soft":
		c := toneColor(cmpTone(n.GetTone(), "muted"))
		lbl := widget.Text(n.GetText()).Size(11.5).Color(c).NoWrap().Ellipsis()
		return widget.Box(&washed{child: widget.Box(withIcon(c, lbl)).Pad(2, 8).Clip(), color: c}).MaxWidth(200)
	case "solid":
		fill, fg := solidToneColors(n.GetTone())
		lbl := widget.Text(n.GetText()).Size(11.5).Weight(ggui.WeightMedium).Color(fg).NoWrap().Ellipsis()
		return widget.Box(withIcon(fg, lbl)).Pad(2, 8).Radius(999).Fill(fill).Clip().MaxWidth(200)
	case "outline":
		var border color.Color = skin.Border
		if n.GetTone() != "" {
			border = toneColor(n.GetTone())
		}
		fg := toneColor(cmpTone(n.GetTone(), "muted"))
		lbl := widget.Text(n.GetText()).Size(11.5).Color(fg).NoWrap().Ellipsis()
		return widget.Box(withIcon(fg, lbl)).Pad(2, 8).Radius(999).Border(1, border).Clip().MaxWidth(200)
	default:
		lbl := widget.Text(n.GetText()).Size(10.5).Font(ggui.DefaultMonoFont()).Color(skin.Muted).NoWrap().Ellipsis()
		return widget.Box(withIcon(skin.Muted, lbl)).Pad(1, 5).Radius(4).Fill(skin.Surface2).Clip().MaxWidth(140)
	}
}

// buttonNode renders a plugin button respecting variant, size, tone, icon and icon-only layouts.
func (v *treeView) buttonNode(n *protocol.UiNode, tap func(), bare bool) ggui.Widget {
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
	return v.maybeDragDrop(b.Disabled(n.GetDisabled() || bare || !n.GetTap()).Name(name), n, bare)
}

// collapsibleNode renders a native disclosure section whose open state is
// persisted locally in blockForm and optionally emits UiChange ("true"|"false").
func (v *treeView) collapsibleNode(n *protocol.UiNode, bare bool) ggui.Widget {
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
	return v.maybeDragDrop(cw, n, bare)
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
func (v *treeView) treeNode(n *protocol.UiNode, bare bool) ggui.Widget {
	gap := n.GetGap()
	kids := make([]ggui.Widget, 0, len(n.GetChildren()))
	for _, c := range n.GetChildren() {
		kids = append(kids, v.treeItemNode(c, 0, false))
	}
	return v.maybeDragDrop(widget.Column(kids...).Gap(gap).Stretch(), n, bare)
}

// treeItemNode renders a single tree_item row with vertical indent guides,
// persisted expand/collapse state, icon, primary label, secondary value text,
// active accent styling, tap handling, and full drag/drop support.
func (v *treeView) treeItemNode(n *protocol.UiNode, depth int, bare bool) ggui.Widget {
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
		badgeMode := n.GetVariant() == "badge"
		var bodyItems []ggui.Widget
		if n.GetIcon() != "" {
			bodyItems = append(bodyItems, pluginIcon(n.GetIcon()).Size(14).Color(toneColor(cmpTone(n.GetTone(), "muted"))))
		}
		weight := textWeight(n.GetWeight())
		if active && n.GetWeight() == "" {
			weight = ggui.WeightMedium
		}
		labelColor := toneColor(n.GetTone())
		if badgeMode && !active {
			labelColor = skin.Fg
		}
		bodyItems = append(bodyItems, widget.Expanded(
			widget.Text(n.GetText()).Size(12.5).Weight(weight).Color(labelColor).NoWrap().Ellipsis(),
		))
		if n.GetValue() != "" {
			if badgeMode {
				bodyItems = append(bodyItems, widget.Box(widget.Text(n.GetValue()).Size(10.5).Color(skin.Muted).NoWrap()).
					Pad(1, 6).Radius(6).Fill(skin.Surface2))
			} else {
				bodyItems = append(bodyItems, widget.Text(n.GetValue()).Size(11).Color(skin.Muted).NoWrap())
			}
		}
		body := widget.Row(bodyItems...).Gap(6).Align(widget.AlignCenter)

		var rowContent ggui.Widget = widget.Expanded(widget.Padding(body, 4, 6))
		switch {
		case !bare && n.GetTap() && !n.GetDisabled():
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
		var row ggui.Widget = rowBox
		if !bare {
			row = v.treeItemDragDrop(rowBox, n)
		}

		if !hasKids || !expanded {
			return row
		}
		sub := make([]ggui.Widget, 0, 1+len(n.GetChildren()))
		sub = append(sub, row)
		for _, child := range n.GetChildren() {
			sub = append(sub, v.treeItemNode(child, depth+1, false))
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
func (v *treeView) container(n *protocol.UiNode, bare bool) ggui.Widget {
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
		return v.maybeDragDrop(v.tappableBoxWhen(widget.Wrap(kids...).Gap(gap).RunGap(gap).Align(widget.AlignCenter), n, !bare), n, bare)

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
		return v.maybeDragDrop(v.tappableBoxWhen(widget.Column(rows...).Gap(gap).Stretch(), n, !bare), n, bare)

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
		return v.maybeDragDrop(out, n, bare)

	case "table":
		return v.tableNode(n, bare)
	}

	row := kind == "row"
	hasGrow := false
	if row {
		for _, c := range n.GetChildren() {
			if c.GetGrow() > 0 {
				hasGrow = true
				break
			}
		}
	}
	kids := make([]ggui.Widget, 0, len(n.GetChildren()))
	for _, c := range n.GetChildren() {
		var w ggui.Widget
		if kind == "list" && c.GetTap() {
			id := c.GetId()
			w = listRow(v.nodeWith(c, false, true), strings.Join(texts(c), " "), func() { v.send(id, protocol.UiTap, "") })
			w = v.dragDrop(w, c)
		} else {
			w = v.node(c, row)
		}
		switch g := c.GetGrow(); {
		case g == 1:
			w = widget.Expanded(w)
		case g > 0:
			w = widget.Flex(w, g)
		case row && !hasGrow && (c.GetKind() == "pill" || c.GetKind() == "chip"):
			w = widget.Flexible(w)
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
		return v.maybeDragDrop(v.tappableBoxWhen(widget.Row(kids...).Gap(gap).Align(align), n, !bare), n, bare)
	case "stack":
		return v.maybeDragDrop(widget.Stack(kids...), n, bare)
	case "card":
		if gap == 0 {
			gap = 6
		}
		inner := v.tappableBoxWhen(widget.Column(kids...).Gap(gap).Stretch(), n, !bare)
		// If dragDrop will draw the card frame/highlight, avoid double framing.
		if !bare && (n.GetDrag() != "" || n.GetDrop()) {
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
		case "soft":
			if n.GetTone() != "" {
				return &washed{child: widget.Box(inner).Pad(8, 10), color: toneColor(n.GetTone()), radius: 10}
			}
			return widget.Box(inner).Fill(skin.Surface2).Radius(10).Pad(8, 10)
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
		if n.GetVariant() == "follow" {
			sc = sc.FollowEnd()
		}
		var out ggui.Widget = sc
		if horiz {
			sc = sc.Horizontal()
			out = looseHeight{sc}
		}
		if h := n.GetMaxHeight(); h > 0 {
			out = widget.Box(out).MaxHeight(h)
		}
		return v.maybeDragDrop(out, n, bare)
	case "list":
		return v.maybeDragDrop(widget.Column(kids...).Gap(gap).Stretch(), n, bare)
	}
	if gap == 0 {
		gap = 6
	}
	return v.maybeDragDrop(v.tappableBoxWhen(widget.Column(kids...).Gap(gap).Stretch(), n, !bare), n, bare)
}

// tableNode renders a table whose header and every body row share the exact
// same column flex weights, gap, horizontal padding, and left-aligned slots.
func (v *treeView) tableNode(n *protocol.UiNode, bare bool) ggui.Widget {
	hdrs := n.GetLabels()
	children := n.GetChildren()
	numCols := len(hdrs)
	for _, c := range children {
		if c.GetKind() == "row" && len(c.GetChildren()) > numCols {
			numCols = len(c.GetChildren())
		}
	}
	weights := tableColumnWeights(numCols, children)

	const colGap = 8.0
	var trows []ggui.Widget
	if len(hdrs) > 0 {
		cols := make([]ggui.Widget, numCols)
		for i := range numCols {
			label := ""
			if i < len(hdrs) {
				label = hdrs[i]
			}
			hdr := widget.Text(label).Size(11.5).Weight(ggui.WeightSemibold).Color(skin.Muted).NoWrap().Ellipsis()
			cols[i] = widget.Flex(widget.Align(hdr).At(0, 0.5), weights[i])
		}
		trows = append(trows, widget.Box(widget.Row(cols...).Gap(colGap).Align(widget.AlignCenter)).Pad(6, 10).Fill(skin.Surface1), hline())
	}
	for i, c := range children {
		if i > 0 {
			trows = append(trows, hline())
		}
		if c.GetKind() == "row" && numCols > 0 {
			cols := make([]ggui.Widget, numCols)
			rowKids := c.GetChildren()
			for j := range numCols {
				var cell ggui.Widget = widget.Box()
				if j < len(rowKids) {
					cell = v.node(rowKids[j], true)
				}
				cols[j] = widget.Flex(widget.Align(cell).At(0, 0.5), weights[j])
			}
			rowW := widget.Row(cols...).Gap(colGap).Align(widget.AlignCenter)
			var wrapped ggui.Widget
			if c.GetTap() {
				id := c.GetId()
				wrapped = ui.ButtonOf(rowW, func() { v.send(id, protocol.UiTap, "") }).Ghost().Pad(6, 10).Radius(0).Name(strings.Join(texts(c), " "))
			} else {
				wrapped = widget.Box(rowW).Pad(6, 10)
			}
			trows = append(trows, v.dragDrop(wrapped, c))
			continue
		}
		trows = append(trows, widget.Box(v.node(c, false)).Pad(6, 10))
	}
	return v.maybeDragDrop(widget.Box(widget.Column(trows...).Stretch()).Border(1, skin.Border).Radius(8), n, bare)
}

func tableColumnWeights(numCols int, rows []*protocol.UiNode) []float64 {
	if numCols <= 0 {
		return nil
	}
	maxGrow := make([]float64, numCols)
	anyGrow, allGrow := false, true
	for _, r := range rows {
		if r.GetKind() != "row" {
			continue
		}
		for i, cell := range r.GetChildren() {
			if i < numCols && cell.GetGrow() > maxGrow[i] {
				maxGrow[i] = cell.GetGrow()
			}
		}
	}
	for _, g := range maxGrow {
		if g > 0 {
			anyGrow = true
		} else {
			allGrow = false
		}
	}
	weights := make([]float64, numCols)
	for i, g := range maxGrow {
		switch {
		case !anyGrow:
			weights[i] = 1
		case allGrow:
			weights[i] = g
		case g > 0:
			weights[i] = 1 + g
		default:
			weights[i] = 1
		}
	}
	return weights
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

func (v *treeView) maybeDragDrop(w ggui.Widget, n *protocol.UiNode, bare bool) ggui.Widget {
	if bare {
		return w
	}
	return v.dragDrop(w, n)
}

func (v *treeView) tappableBoxWhen(w ggui.Widget, n *protocol.UiNode, active bool) ggui.Widget {
	if !active || !n.GetTap() {
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
