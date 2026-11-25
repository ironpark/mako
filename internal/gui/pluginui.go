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

// uiCheckEvent is the BlockEvent kind sent when toggling a checkbox or
// tree_item check control (revision 46).
const uiCheckEvent = "check"

// autoSplitBreakpoint is the width (px) at which split direction "auto"
// switches from vertical (narrow) to horizontal (wide).
const autoSplitBreakpoint = 640.0

// treeView is a block's tree as it is drawn: the block, and what the user
// set in its fields.
type treeView struct {
	m       *model
	block   string
	scope   string
	plugin  string // optional plugin ID override for dialog provenance
	form    *blockForm
	menus   map[string]*protocol.UiNode // indexed variant=="context" menu nodes
	onEvent func(block, node, kind, value string, values map[string]string)
}

// newTreeView constructs a treeView with all context menus indexed from root
// (typically the full tree before liftToolbar splits off a leading toolbar).
func newTreeView(m *model, block, scope string, form *blockForm, root *protocol.UiNode) *treeView {
	v := &treeView{m: m, block: block, scope: scope, form: form}
	v.indexMenus(root)
	return v
}

// renderToolbar renders a lifted toolbar UiNode using this treeView's shared
// form state, event dispatch, and indexed context menus.
func (v *treeView) renderToolbar(toolbar *protocol.UiNode) ggui.Widget {
	if toolbar == nil {
		return nil
	}
	return v.node(toolbar, true)
}

// renderTreeNode renders a standalone UiNode (such as a toolbar lifted into
// layout chrome) using a treeView initialized from the full tree root. It does
// not reset form.drawn, so calling it alongside blockBody preserves form values.
func renderTreeNode(m *model, block, scope, pluginID string, form *blockForm, root, node *protocol.UiNode) ggui.Widget {
	if node == nil {
		return nil
	}
	v := newTreeView(m, block, scope, form, root)
	v.plugin = pluginID
	if node.GetKind() == "row" && node.GetVariant() == "toolbar" {
		return v.renderToolbar(node)
	}
	return v.node(node, false)
}

// liftToolbar extracts a leading toolbar row (kind=="row" && variant=="toolbar")
// from a vertical container root, returning the toolbar node and a shallow copy
// of the rest of the tree without that first child. Otherwise returns (nil, t).
func liftToolbar(t *protocol.UiNode) (toolbar, rest *protocol.UiNode) {
	if t == nil {
		return nil, nil
	}
	switch t.GetKind() {
	case "column", "list", "card":
	case "scroll":
		if t.GetDirection() == "horizontal" {
			return nil, t
		}
	default:
		return nil, t
	}
	children := t.GetChildren()
	if len(children) == 0 {
		return nil, t
	}
	first := children[0]
	if first.GetKind() != "row" || first.GetVariant() != "toolbar" {
		return nil, t
	}
	return first, shallowCopyUiNode(t, children[1:])
}

// shallowCopyUiNode copies n's scalar and slice header fields with a new
// children slice, avoiding copying protobuf internal MessageState.
func shallowCopyUiNode(n *protocol.UiNode, children []*protocol.UiNode) *protocol.UiNode {
	return &protocol.UiNode{
		Id:          n.GetId(),
		Kind:        n.GetKind(),
		Text:        n.GetText(),
		Tone:        n.GetTone(),
		Icon:        n.GetIcon(),
		Weight:      n.GetWeight(),
		Size:        n.GetSize(),
		Value:       n.GetValue(),
		Checked:     n.GetChecked(),
		Options:     n.GetOptions(),
		Progress:    n.GetProgress(),
		Language:    n.GetLanguage(),
		Disabled:    n.GetDisabled(),
		Grow:        n.GetGrow(),
		Gap:         n.GetGap(),
		Tap:         n.GetTap(),
		Change:      n.GetChange(),
		Children:    children,
		Chart:       n.GetChart(),
		Series:      n.GetSeries(),
		Labels:      n.GetLabels(),
		Drag:        n.GetDrag(),
		Drop:        n.GetDrop(),
		DragGroup:   n.GetDragGroup(),
		DropAccept:  n.GetDropAccept(),
		Direction:   n.GetDirection(),
		MaxHeight:   n.GetMaxHeight(),
		Variant:     n.GetVariant(),
		Columns:     n.GetColumns(),
		Ratio:       n.GetRatio(),
		Fill:        n.GetFill(),
		GraphNodes:  n.GetGraphNodes(),
		GraphEdges:  n.GetGraphEdges(),
		Badges:      n.GetBadges(),
		Check:       n.GetCheck(),
		Checks:      n.GetChecks(),
		ContextMenu: n.GetContextMenu(),
		ContextArg:  n.GetContextArg(),
		Rows:        n.GetRows(),
		Placeholder: n.GetPlaceholder(),
	}
}

// indexMenus walks root and records every variant=="context" menu node by its
// key (last path segment of Id, full Id, ContextMenu, or Value).
func (v *treeView) indexMenus(root *protocol.UiNode) {
	if v.menus == nil {
		v.menus = map[string]*protocol.UiNode{}
	}
	var walk func(n *protocol.UiNode)
	walk = func(n *protocol.UiNode) {
		if n == nil {
			return
		}
		if n.GetKind() == "menu" && n.GetVariant() == "context" {
			if id := n.GetId(); id != "" {
				v.menus[id] = n
				key := id
				if idx := strings.LastIndexByte(id, '/'); idx >= 0 {
					key = id[idx+1:]
				}
				if key != "" && !strings.HasPrefix(key, "#") {
					v.menus[key] = n
				}
			}
			if k := n.GetContextMenu(); k != "" {
				v.menus[k] = n
			}
			if k := n.GetValue(); k != "" {
				v.menus[k] = n
			}
		}
		for _, c := range n.GetChildren() {
			walk(c)
		}
	}
	walk(root)
}

// thirdPartyPluginID returns the plugin ID if this block belongs to a
// third-party (non-bundled) plugin, or "" for bundled plugins and agents.
func (v *treeView) thirdPartyPluginID() string {
	pid := v.plugin
	if pid == "" {
		if rest, ok := strings.CutPrefix(v.block, "plugin:"); ok {
			pid, _, _ = strings.Cut(rest, "/")
		}
	}
	if pid == "" {
		return ""
	}
	if v.m != nil {
		if v.m.isBundledPlugin(pid) {
			return ""
		}
		return pid
	}
	if strings.HasPrefix(pid, "mako-") {
		return ""
	}
	return pid
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
	return v.nodeLayout(n, inLine, false, false)
}

// nodeWith draws n; bare suppresses outer Tap/Drag/Drop/ContextMenu on n itself
// (for a tappable list row's inner content) while keeping descendants interactive.
func (v *treeView) nodeWith(n *protocol.UiNode, inLine, bare bool) ggui.Widget {
	return v.nodeLayout(n, inLine, bare, false)
}

// nodeLayout draws n with optional bare and fullWidth hints. fullWidth is true
// when the parent stretches or expands n horizontally (such as a column child
// or a row child with grow > 0), allowing ghost buttons to left-align cleanly.
func (v *treeView) nodeLayout(n *protocol.UiNode, inLine, bare, fullWidth bool) ggui.Widget {
	if n == nil {
		return widget.Box()
	}
	if v.menus == nil {
		v.indexMenus(n)
	}
	tap := func() {}
	isTap := !bare && n.GetTap()
	if isTap {
		id := n.GetId()
		tap = func() { v.send(id, protocol.UiTap, "") }
	}
	var out ggui.Widget
	switch n.GetKind() {
	case "column", "row", "stack", "card", "scroll", "list", "table", "wrap", "grid", "split":
		out = v.container(n, bare)
	case "collapsible":
		out = v.collapsibleNode(n, bare)
	case "breadcrumb":
		out = v.breadcrumbNode(n)
	case "tree":
		out = v.treeNode(n, bare)
	case "tree_item":
		// treeItemNode attaches its own per-row context menu so child rows
		// keep their own menus.
		return v.treeItemNode(n, 0, bare)
	case "graph":
		out = v.graphNode(n)
	case "dag":
		// dagNode attaches context_menu per row with arg = row.id.
		return v.dagNode(n, bare)
	case "menu":
		if n.GetVariant() == "context" {
			return widget.Box()
		}
		out = v.menuNode(n, bare)
	case "menu_item":
		return v.menuItemWidget(n, "")
	case "checkbox":
		out = v.checkboxNode(n, bare)
	case "dialog":
		return v.dialogNode(n)
	case "text":
		t := widget.Text(n.GetText()).Size(textSize(n.GetSize())).Weight(textWeight(n.GetWeight())).Color(toneColor(n.GetTone()))
		if inLine {
			t = t.NoWrap().Ellipsis()
		}
		out = v.maybeDragDrop(tappableWhen(appendBadges(t, n.GetBadges()), n, isTap, tap), n, bare)
	case "icon":
		w := tappableWhen(pluginIcon(n.GetIcon()).Size(textSize(n.GetSize())+2).Color(toneColor(cmpTone(n.GetTone(), "muted"))), n, isTap, tap)
		if ph := n.GetPlaceholder(); ph != "" && !bare {
			w = ui.Tooltip(w, ph)
		}
		out = v.maybeDragDrop(w, n, bare)
	case "pill":
		w := tappableWhen(appendBadges(v.pillNode(n), n.GetBadges()), n, isTap, tap)
		if ph := n.GetPlaceholder(); ph != "" && !bare {
			w = ui.Tooltip(w, ph)
		}
		out = v.maybeDragDrop(w, n, bare)
	case "chip":
		w := tappableWhen(appendBadges(v.chipNode(n), n.GetBadges()), n, isTap, tap)
		if ph := n.GetPlaceholder(); ph != "" && !bare {
			w = ui.Tooltip(w, ph)
		}
		out = v.maybeDragDrop(w, n, bare)
	case "button":
		out = v.buttonNode(n, tap, bare, fullWidth)
	case "input":
		id := n.GetId()
		ph := cmpTone(n.GetPlaceholder(), n.GetText())
		f := ui.TextField(v.form.input(id, n.GetValue())).Key(v.block + "\x00" + v.scope + id).Placeholder(ph).Name(cmpTone(ph, T("Input")))
		if n.GetChange() {
			f = f.OnSubmit(func(s string) { v.send(id, protocol.UiSubmit, s) }).
				OnCommit(func(s string) { v.send(id, protocol.UiChange, s) })
		}
		out = f.Disabled(n.GetDisabled())
	case "textarea":
		id := n.GetId()
		lines := 4
		switch n.GetSize() {
		case "large":
			lines = 14
		case "small":
			lines = 3
		}
		ph := cmpTone(n.GetPlaceholder(), n.GetText())
		f := ui.TextField(v.form.input(id, n.GetValue())).Key(v.block + "\x00" + v.scope + id).Lines(lines).Placeholder(ph).Name(cmpTone(ph, T("Input")))
		if n.GetChange() {
			f = f.OnSubmit(func(s string) { v.send(id, protocol.UiSubmit, s) }).
				OnCommit(func(s string) { v.send(id, protocol.UiChange, s) })
		}
		out = f.Disabled(n.GetDisabled())
	case "toggle":
		id := n.GetId()
		field := v.form.field(id, strconv.FormatBool(n.GetChecked()))
		sw := ui.Switch(ggui.Bind(func() bool { return field.Get() == "true" }, func(on bool) {
			field.Set(strconv.FormatBool(on))
			if n.GetChange() {
				v.send(id, protocol.UiChange, strconv.FormatBool(on))
			}
		}), n.GetText()).Name(n.GetText())
		out = sw.Disabled(n.GetDisabled())
	case "select":
		id := n.GetId()
		field := v.form.field(id, n.GetValue())
		out = widget.Box(ui.Select(ggui.Bind(field.Get, func(x string) {
			field.Set(x)
			if n.GetChange() {
				v.send(id, protocol.UiChange, x)
			}
		})).Options(n.GetOptions()).Name(T("Choose")).Disabled(n.GetDisabled())).MaxWidth(200)
	case "tabs":
		id := n.GetId()
		field := v.form.field(id, n.GetValue())
		opts := n.GetOptions()
		out = ggui.View(field, func(active string) ggui.Widget {
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
		out = mdview.Column(host, n.GetText())
	case "code":
		out = toolBlock(n.GetText(), false)
	case "progress":
		if n.GetVariant() == "spinner" {
			if n.GetText() == "" {
				out = ui.Spinner().Size(13)
			} else {
				out = widget.Row(
					ui.Spinner().Size(13),
					widget.Text(n.GetText()).Size(12).Color(toneColor(cmpTone(n.GetTone(), "muted"))).NoWrap().Ellipsis(),
				).Gap(6).Align(widget.AlignCenter)
			}
		} else {
			out = ui.Progress(ggui.Const(n.GetProgress()))
		}
	case "divider":
		return hline()
	case "chart":
		out = chartNode(n)
	default:
		return widget.Box() // the daemon sends no other kind
	}
	if bare {
		return out
	}
	return v.withContextMenu(out, n, "", "")
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
	return ui.ButtonOf(w, tap).Ghost().Pad(2, 4).Radius(6).Name(cmpTone(n.GetText(), n.GetPlaceholder()))
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

// renderBadge renders a single UiBadge matching native styling:
// "mono" matches counts() (+12/-3), "soft"/"" matches refBadges(),
// "solid" and "outline" match solid/outline chip tags.
func renderBadge(b *protocol.UiBadge) ggui.Widget {
	if b == nil {
		return widget.Box()
	}
	iconName := b.GetIcon()
	content := func(fg color.Color, w ggui.FontWeight) ggui.Widget {
		lbl := widget.Box(widget.Text(b.GetText()).Size(10.5).Weight(w).Color(fg).NoWrap().Ellipsis()).MaxWidth(90)
		if iconName == "" {
			return lbl
		}
		return widget.Row(pluginIcon(iconName).Size(10).Color(fg), lbl).Gap(3).Align(widget.AlignCenter)
	}
	switch b.GetVariant() {
	case "mono":
		fg := toneColor(cmpTone(b.GetTone(), "muted"))
		lbl := widget.Text(b.GetText()).Size(11.5).Font(ggui.DefaultMonoFont()).Color(fg).NoWrap()
		if iconName == "" {
			return lbl
		}
		return widget.Row(pluginIcon(iconName).Size(10.5).Color(fg), lbl).Gap(3).Align(widget.AlignCenter)
	case "solid":
		fill, fg := solidToneColors(b.GetTone())
		return widget.Box(content(fg, ggui.WeightMedium)).Pad(1, 5).Radius(4).Fill(fill).Clip()
	case "outline":
		c := toneColor(cmpTone(b.GetTone(), "muted"))
		border := color.Color(skin.Border)
		if b.GetTone() != "" {
			border = c
		}
		return widget.Box(content(c, ggui.WeightRegular)).Pad(1, 5).Radius(4).Border(1, border).Clip()
	default: // "" or "soft"
		fg := toneColor(cmpTone(b.GetTone(), "muted"))
		return widget.Box(content(fg, ggui.WeightRegular)).Pad(1, 5).Radius(4).Fill(skin.Surface2).Clip()
	}
}

// renderBadges renders a slice of UiBadges.
func renderBadges(badges []*protocol.UiBadge) []ggui.Widget {
	out := make([]ggui.Widget, 0, len(badges))
	for _, b := range badges {
		out = append(out, renderBadge(b))
	}
	return out
}

// appendBadges returns w unchanged when badges is empty, or a row of w
// followed by its trailing badges.
func appendBadges(w ggui.Widget, badges []*protocol.UiBadge) ggui.Widget {
	if len(badges) == 0 {
		return w
	}
	parts := make([]ggui.Widget, 0, 1+len(badges))
	parts = append(parts, w)
	parts = append(parts, renderBadges(badges)...)
	return widget.Row(parts...).Gap(5).Align(widget.AlignCenter)
}

// pluginDangerButton styles destructive plugin buttons with a solid fill and
// WCAG AA (>= 4.5:1) contrasting foreground across both light and dark themes.
var pluginDangerButton = ui.NewVariant("plugin-danger", pluginDangerButtonStyle)

func pluginDangerButtonStyle(t *uitheme.Theme) ui.ButtonStyle {
	fill := t.Destructive
	if fill == nil {
		fill = skin.Light.Destructive
	}
	return ui.ButtonStyle{
		Fill:  fill,
		Hover: shadeColor(fill, 0.88),
		Label: pickContrastInk(fill),
		Ring:  fill,
	}
}

func shadeColor(c color.Color, factor float64) color.Color {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	scale := func(v uint8) uint8 {
		f := math.Round(float64(v) * factor)
		if f < 0 {
			return 0
		}
		if f > 255 {
			return 255
		}
		return uint8(f)
	}
	return color.NRGBA{R: scale(n.R), G: scale(n.G), B: scale(n.B), A: n.A}
}

// leftFill aligns child to the left edge while filling all bounded width so
// stretched or expanded ghost buttons do not center their labels.
type leftFill struct {
	child ggui.Widget
	size  ggui.Size
}

func (l *leftFill) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	l.size = l.child.Layout(c.Loosen(), env)
	w := l.size.W
	if !math.IsInf(c.MaxW, 0) && c.MaxW > w {
		w = c.MaxW
	}
	return c.Constrain(ggui.Sz(w, l.size.H))
}

func (l *leftFill) Paint(dst *ggui.Canvas, r ggui.Rect) {
	y := r.Origin.Y + (r.Size.H-l.size.H)/2
	dst.Paint(l.child, ggui.Rct(ggui.Pt(r.Origin.X, y), l.size))
}

// buttonNode renders a plugin button respecting variant, size, tone, weight,
// icon, badges, icon-only layouts, and left alignment when stretched/expanded.
func (v *treeView) buttonNode(n *protocol.UiNode, tap func(), bare, fullWidth bool) ggui.Widget {
	text := n.GetText()
	iconName := n.GetIcon()
	badges := n.GetBadges()
	variant := n.GetVariant()
	size := n.GetSize()
	tone := n.GetTone()
	weight := n.GetWeight()
	var b *ui.ButtonWidget
	iconOnly := text == "" && iconName != "" && len(badges) == 0

	// Use one unified custom-content path for all non-icon-only ghost buttons
	// (so buttons with and without badges share identical padding, icon sizing,
	// text sizing, colors, and left alignment when stretched) as well as any
	// button carrying badges or explicit font weight.
	useCustom := !iconOnly && (variant == "ghost" || len(badges) > 0 || weight != "")

	switch {
	case iconOnly:
		if variant == "ghost" {
			iconSz, pad, rad := 15.0, 6.0, 6.0
			switch size {
			case "small":
				iconSz, pad = 14.0, 5.0
			case "large":
				iconSz, pad, rad = 16.0, 8.0, 8.0
			}
			iconCol := toneColor(cmpTone(tone, "muted"))
			b = ui.ButtonOf(pluginIcon(iconName).Size(iconSz).Color(iconCol), tap).Pad(pad).Radius(rad)
		} else {
			b = ui.ButtonOf(widget.Box(), tap).Icon(pluginIcon(iconName))
			switch size {
			case "small":
				b = b.Size(ui.ButtonIconSmall)
			case "large":
				b = b.Size(ui.ButtonIconLarge)
			default:
				b = b.Size(ui.ButtonIcon)
			}
		}
	case useCustom:
		iconSz := 14.0
		gap := 6.0
		switch size {
		case "small":
			iconSz = 13.0
			gap = 5.0
		case "large":
			iconSz = 16.0
			gap = 6.0
		}
		var iconCol, txtCol color.Color
		switch {
		case variant == "primary" || (variant == "" && tone == "accent"):
			iconCol, txtCol = uitheme.PrimaryFg, uitheme.PrimaryFg
		case variant == "danger" || (variant == "" && tone == "danger"):
			_, fg := solidToneColors("danger")
			iconCol, txtCol = fg, fg
		default:
			if iconName != "" {
				iconCol = toneColor(cmpTone(tone, "muted"))
				switch tone {
				case "muted", "faint", "accent":
					txtCol = toneColor(tone)
				case "danger":
					if len(badges) > 0 {
						txtCol = skin.Fg
					} else {
						txtCol = skin.DiffRemoved
					}
				default:
					txtCol = skin.Fg
				}
			} else {
				txtCol = toneColor(tone)
			}
		}

		leftAlign := fullWidth && variant == "ghost"
		allMono := len(badges) > 0 && !slices.ContainsFunc(badges, func(bg *protocol.UiBadge) bool {
			return bg.GetVariant() != "mono"
		})
		var parts []ggui.Widget
		if iconName != "" {
			parts = append(parts, pluginIcon(iconName).Size(iconSz).Color(iconCol))
		}
		if text != "" {
			lbl := widget.Text(text).Size(textSize(size)).Weight(textWeight(weight)).Color(txtCol).NoWrap().Ellipsis()
			switch {
			case leftAlign && allMono:
				parts = append(parts, widget.Expanded(lbl))
			case leftAlign:
				parts = append(parts, widget.Flexible(lbl))
			default:
				parts = append(parts, lbl)
			}
		}
		if len(badges) > 0 {
			parts = append(parts, renderBadges(badges)...)
		}
		var content ggui.Widget = widget.Row(parts...).Gap(gap).Align(widget.AlignCenter)
		if leftAlign && !allMono {
			content = &leftFill{child: content}
		}
		b = ui.ButtonOf(content, tap)
		switch size {
		case "small":
			b = b.Size(ui.ButtonSmall)
		case "large":
			b = b.Size(ui.ButtonLarge)
		}
		if variant == "ghost" {
			switch size {
			case "small":
				b = b.Pad(4, 6).Radius(6)
			case "large":
				b = b.Pad(8, 12).Radius(8)
			default:
				b = b.Pad(6, 8).Radius(6)
			}
		}
	default:
		b = ui.Button(text, tap)
		if iconName != "" {
			b = b.Icon(pluginIcon(iconName))
		}
		switch size {
		case "small":
			b = b.Size(ui.ButtonSmall)
		case "large":
			b = b.Size(ui.ButtonLarge)
		}
	}

	switch variant {
	case "primary":
		// ui.Button defaults to ButtonPrimary.
	case "secondary":
		b = b.Secondary()
	case "outline":
		b = b.Outline()
	case "ghost":
		b = b.Ghost()
	case "danger":
		b = b.Variant(pluginDangerButton)
	default:
		switch tone {
		case "accent":
		case "danger":
			b = b.Variant(pluginDangerButton)
		default:
			b = b.Outline()
		}
	}

	ph := n.GetPlaceholder()
	name := cmpTone(text, cmpTone(ph, iconName))
	var out ggui.Widget = b.Disabled(n.GetDisabled() || bare || !n.GetTap()).Name(name)
	if ph != "" && !bare {
		out = ui.Tooltip(out, ph)
	}
	return v.maybeDragDrop(out, n, bare)
}

// menuNode renders a dropdown menu button (variant "", "ghost", or "outline").
// Menus with variant "context" are declarations only and are not drawn.
func (v *treeView) menuNode(n *protocol.UiNode, bare bool) ggui.Widget {
	if n.GetVariant() == "context" {
		return widget.Box()
	}
	items := v.buildMenuItems(n.GetChildren(), "", 0)
	text := n.GetText()
	iconName := n.GetIcon()
	badges := n.GetBadges()

	var face ggui.Widget
	iconOnly := text == "" && iconName != "" && len(badges) == 0
	if iconOnly {
		face = pluginIcon(iconName).Size(14).Color(toneColor(cmpTone(n.GetTone(), "muted")))
	} else {
		var parts []ggui.Widget
		if iconName != "" {
			parts = append(parts, pluginIcon(iconName).Size(14).Color(toneColor(cmpTone(n.GetTone(), "muted"))))
		}
		if text != "" {
			parts = append(parts, widget.Box(widget.Text(text).Size(12.5).Weight(ggui.WeightMedium).Color(toneColor(n.GetTone())).NoWrap().Ellipsis()).MaxWidth(150))
		}
		if len(badges) > 0 {
			parts = append(parts, renderBadges(badges)...)
		}
		parts = append(parts, icon("chevron-down").Size(12).Color(skin.ExtraMuted))
		face = widget.Row(parts...).Gap(5).Align(widget.AlignCenter)
	}

	ph := n.GetPlaceholder()
	name := cmpTone(text, cmpTone(ph, cmpTone(iconName, T("Menu"))))
	menu := ui.MenuOf(face, items...).Disabled(n.GetDisabled() || bare).Name(name)
	var out ggui.Widget
	switch {
	case n.GetVariant() == "outline":
		out = menu
	case iconOnly:
		out = iconMenu(menu, 5)
	default:
		out = borderless(menu)
	}
	if ph != "" && !bare {
		out = ui.Tooltip(out, ph)
	}
	return v.maybeDragDrop(out, n, bare)
}

// buildMenuItems converts a menu's children (menu_item, divider, nested menu
// up to depth 2) into ggui menu entries, forwarding contextArg on item taps.
func (v *treeView) buildMenuItems(children []*protocol.UiNode, contextArg string, depth int) []ggui.Widget {
	out := make([]ggui.Widget, 0, len(children))
	for _, c := range children {
		if c == nil {
			continue
		}
		switch c.GetKind() {
		case "divider":
			out = append(out, ui.MenuDivider())
		case "menu":
			if depth >= 2 {
				continue
			}
			sub := ui.MenuSub(c.GetText(), v.buildMenuItems(c.GetChildren(), contextArg, depth+1)...).Disabled(c.GetDisabled())
			if c.GetIcon() != "" {
				sub = sub.Icon(pluginIcon(c.GetIcon()).Size(13).Color(toneColor(cmpTone(c.GetTone(), "muted"))))
			}
			out = append(out, sub)
		case "menu_item":
			out = append(out, v.menuItemWidget(c, contextArg))
		}
	}
	return out
}

// menuItemWidget renders a single menu_item entry.
func (v *treeView) menuItemWidget(c *protocol.UiNode, contextArg string) ggui.Widget {
	id := c.GetId()
	onTap := func() {}
	if c.GetTap() && !c.GetDisabled() {
		onTap = func() { v.send(id, protocol.UiTap, contextArg) }
	}
	it := ui.MenuItem(c.GetText(), onTap).Checked(c.GetChecked()).Disabled(c.GetDisabled())
	if c.GetTone() == "danger" {
		it = it.Destructive()
	}
	if c.GetIcon() != "" {
		col := toneColor(cmpTone(c.GetTone(), "muted"))
		if c.GetTone() == "danger" {
			col = skin.DiffRemoved
		}
		it = it.Icon(pluginIcon(c.GetIcon()).Size(13).Color(col))
	}
	return it
}

// withContextMenu attaches the referenced context menu (if any) to w, passing
// argOverride (or n.ContextArg when empty) as the value on menu_item tap.
func (v *treeView) withContextMenu(w ggui.Widget, n *protocol.UiNode, argOverride, labelOverride string) ggui.Widget {
	key := n.GetContextMenu()
	if key == "" || v.menus == nil {
		return w
	}
	menuNode := v.menus[key]
	if menuNode == nil {
		return w
	}
	arg := argOverride
	if arg == "" {
		arg = n.GetContextArg()
	}
	cm := ui.ContextMenuFunc(w, func() []ggui.Widget {
		return v.buildMenuItems(menuNode.GetChildren(), arg, 0)
	})
	label := labelOverride
	if label == "" {
		label = n.GetText()
	}
	if label != "" {
		cm = cm.Name(Tf("%s actions", label))
	}
	return cm
}

// checkboxNode renders a tri-state ("on", "off", "mixed") checkbox node.
func (v *treeView) checkboxNode(n *protocol.UiNode, bare bool) ggui.Widget {
	id := n.GetId()
	initial := n.GetCheck()
	if initial == "" {
		if n.GetChecked() {
			initial = "on"
		} else {
			initial = "off"
		}
	}
	field := v.form.field(id, initial)
	on := ggui.Bind(
		func() bool { return field.Get() == "on" },
		func(next bool) {
			if n.GetDisabled() {
				return
			}
			val := "off"
			if next {
				val = "on"
			}
			field.Set(val)
			if n.GetChange() {
				v.send(id, protocol.UiChange, val)
			} else if n.GetChecks() {
				v.send(id, uiCheckEvent, val)
			}
		},
	)
	ind := ggui.Map(field, func(s string) bool { return s == "mixed" })
	ph := n.GetPlaceholder()
	name := cmpTone(n.GetText(), cmpTone(ph, T("Checkbox")))
	cb := ui.Checkbox(on, n.GetText()).BindIndeterminate(ind).Disabled(n.GetDisabled() || bare).Name(name)
	out := appendBadges(cb, n.GetBadges())
	if ph != "" && !bare {
		out = ui.Tooltip(out, ph)
	}
	return v.maybeDragDrop(out, n, bare)
}

// pluginQuietBox styles list/tree-row checkboxes compactly with a raised
// surface fill instead of a high-contrast primary block on every row.
func pluginQuietBox(box ggui.Widget) ggui.Widget {
	return uitheme.Override(func(t *uitheme.Theme) {
		if raised, ok := t.Get(uitheme.CSSColor("surface-3")); ok {
			t.Primary, t.PrimaryFg = raised, t.Fg
		}
		t.ControlSize = min(t.ControlSize, 14)
	}, box)
}

// dialogNode renders a declarative modal dialog via keyedDialog. Closing via
// Escape, close button, or scrim sends a UiChange event with value "dismiss".
// Third-party plugins always display a provenance footer ("<plugin id> asks").
func (v *treeView) dialogNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	gap := n.GetGap()
	if gap == 0 {
		gap = 10
	}
	var kids []ggui.Widget
	if n.GetVariant() == "danger" && n.GetText() != "" {
		kids = append(kids, widget.Row(
			icon("shield-alert").Size(16).Color(skin.DiffRemoved),
			widget.Expanded(widget.Text(n.GetText()).Size(15).Weight(ggui.WeightSemibold).Color(skin.DiffRemoved)),
		).Gap(8).Align(widget.AlignCenter))
	}
	for _, c := range n.GetChildren() {
		if c.GetKind() == "menu" && c.GetVariant() == "context" {
			continue
		}
		w := v.nodeLayout(c, false, false, true)
		switch g := c.GetGrow(); {
		case g == 1:
			w = widget.Expanded(w)
		case g > 0:
			w = widget.Flex(w, g)
		}
		kids = append(kids, w)
	}
	body := widget.Column(kids...).Gap(gap).Stretch()

	open := ggui.State(true)
	dismissed := false
	sendDismiss := func() {
		if dismissed {
			return
		}
		dismissed = true
		if n.GetChange() {
			v.send(id, protocol.UiChange, "dismiss")
		}
	}
	dlg := keyedDialog(open, body).Width(420).Name(cmpTone(n.GetText(), T("Dialog"))).OnClose(sendDismiss)
	if n.GetVariant() != "danger" && n.GetText() != "" {
		dlg = dlg.Title(n.GetText())
	}
	if pid := v.thirdPartyPluginID(); pid != "" {
		dlg = dlg.Footer(widget.Row(
			icon("package").Size(12).Color(skin.ExtraMuted),
			widget.Text(Tf("%s asks", pid)).Size(11).Color(skin.ExtraMuted).NoWrap(),
		).Gap(5).Align(widget.AlignCenter))
	}
	return dlg
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
		if len(n.GetBadges()) > 0 {
			head = append(head, renderBadges(n.GetBadges())...)
		}
		return widget.Box(widget.Row(head...).Gap(6).Align(widget.AlignCenter)).Pad(6, 8).Radius(6).Fill(skin.Surface1)
	})

	gap := n.GetGap()
	if gap == 0 {
		gap = 6
	}
	kids := make([]ggui.Widget, 0, len(n.GetChildren()))
	var dialogs []ggui.Widget
	for _, c := range n.GetChildren() {
		if c.GetKind() == "menu" && c.GetVariant() == "context" {
			continue
		}
		if c.GetKind() == "dialog" {
			dialogs = append(dialogs, v.dialogNode(c))
			continue
		}
		w := v.nodeLayout(c, false, false, true)
		switch g := c.GetGrow(); {
		case g == 1:
			w = widget.Expanded(w)
		case g > 0:
			w = widget.Flex(w, g)
		}
		kids = append(kids, w)
	}
	var body ggui.Widget = widget.Padding(widget.Column(kids...).Gap(gap).Stretch(), 6, 8, 4, 16)
	if len(dialogs) > 0 {
		body = widget.Stack(append([]ggui.Widget{body}, dialogs...)...)
	}
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
		// Subtle 1px vertical indent guides for ancestor depth levels.
		for range depth {
			guide := widget.Row(
				widget.Box().Width(7),
				widget.Box().Width(1).Height(22).Fill(skin.Border),
				widget.Box().Width(8),
			)
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

		// Optional leading tri-state checkbox (check: "on"|"off"|"mixed").
		if chk := n.GetCheck(); chk != "" {
			chkState := v.form.local(id+"\x00check", chk)
			on := ggui.Bind(
				func() bool { return chkState.Get() != "off" },
				func(next bool) {
					if n.GetDisabled() || bare {
						return
					}
					val := "off"
					if next {
						val = "on"
					}
					chkState.Set(val)
					if n.GetChecks() {
						v.send(id, uiCheckEvent, val)
					}
				},
			)
			ind := ggui.Map(chkState, func(s string) bool { return s == "mixed" })
			chkName := n.GetPlaceholder()
			if chkName == "" {
				chkName = T("Select")
				if n.GetText() != "" {
					chkName = Tf("Select %s", n.GetText())
				}
			}
			cb := ui.Checkbox(on, "").BindIndeterminate(ind).Disabled(n.GetDisabled() || bare || !n.GetChecks()).Name(chkName)
			cells = append(cells, pluginQuietBox(cb))
		}

		// Row body: icon, primary label, optional secondary value text, and trailing badges.
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
		if len(n.GetBadges()) > 0 {
			bodyItems = append(bodyItems, renderBadges(n.GetBadges())...)
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
			row = v.withContextMenu(row, n, "", "")
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
					rowKids = append(rowKids, widget.Expanded(v.nodeLayout(children[i+j], false, false, true)))
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
		dividerName := cmpTone(n.GetText(), T("Pane divider"))
		var out ggui.Widget
		if n.GetDirection() == "auto" {
			horiz := ui.Resizable(ratio, first, second).WithHandle().MinSizes(60, 60).
				Disabled(n.GetDisabled()).Name(dividerName)
			vert := ui.Resizable(ratio, first, second).Vertical().WithHandle().MinSizes(60, 60).
				Disabled(n.GetDisabled()).Name(dividerName)
			out = &autoSplit{horiz: horiz, vert: vert}
		} else {
			sp := ui.Resizable(ratio, first, second).WithHandle().MinSizes(60, 60).
				Disabled(n.GetDisabled()).Name(dividerName)
			if n.GetDirection() == "vertical" {
				sp = sp.Vertical()
			}
			out = sp
		}
		if h := n.GetMaxHeight(); h > 0 {
			out = widget.Box(out).MaxHeight(h)
		}
		return v.maybeDragDrop(out, n, bare)

	case "table":
		return v.tableNode(n, bare)
	}

	row := kind == "row"
	toolbar := row && n.GetVariant() == "toolbar"
	hasGrow := false
	if row {
		for _, c := range n.GetChildren() {
			if c.GetKind() == "menu" && c.GetVariant() == "context" {
				continue
			}
			if c.GetGrow() > 0 {
				hasGrow = true
				break
			}
		}
	}
	stretchChildren := kind == "column" || kind == "card" || kind == "list" || (kind == "scroll" && n.GetDirection() != "horizontal")
	kids := make([]ggui.Widget, 0, len(n.GetChildren()))
	var dialogs []ggui.Widget
	for _, c := range n.GetChildren() {
		if c.GetKind() == "menu" && c.GetVariant() == "context" {
			continue
		}
		if c.GetKind() == "dialog" {
			dialogs = append(dialogs, v.dialogNode(c))
			continue
		}
		var w ggui.Widget
		if kind == "list" && c.GetTap() {
			id := c.GetId()
			inner := appendBadges(v.nodeLayout(c, false, true, true), c.GetBadges())
			w = listRow(inner, strings.Join(texts(c), " "), func() { v.send(id, protocol.UiTap, "") })
			w = v.dragDrop(w, c)
			w = v.withContextMenu(w, c, "", "")
		} else {
			fullWidth := stretchChildren || (row && c.GetGrow() > 0)
			w = v.nodeLayout(c, row, false, fullWidth)
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
	wrapDialogs := func(main ggui.Widget) ggui.Widget {
		if len(dialogs) == 0 {
			return main
		}
		return widget.Stack(append([]ggui.Widget{main}, dialogs...)...)
	}
	switch kind {
	case "row":
		if gap == 0 {
			if toolbar {
				gap = 4
			} else {
				gap = 8
			}
		}
		align := widget.AlignCenter
		if !toolbar {
			for _, c := range n.GetChildren() {
				if c.GetKind() == "column" || c.GetKind() == "list" || c.GetKind() == "card" || c.GetKind() == "scroll" || c.GetKind() == "grid" || c.GetKind() == "split" {
					align = widget.AlignStart
					break
				}
			}
		}
		return wrapDialogs(v.maybeDragDrop(v.tappableBoxWhen(widget.Row(kids...).Gap(gap).Align(align), n, !bare), n, bare))
	case "stack":
		return wrapDialogs(v.maybeDragDrop(widget.Stack(kids...), n, bare))
	case "card":
		if gap == 0 {
			gap = 6
		}
		inner := v.tappableBoxWhen(widget.Column(kids...).Gap(gap).Stretch(), n, !bare)
		// If dragDrop will draw the card frame/highlight, avoid double framing.
		if !bare && (n.GetDrag() != "" || n.GetDrop()) {
			return wrapDialogs(v.dragDrop(inner, n))
		}
		var border color.Color = skin.Border
		if n.GetTone() != "" {
			border = toneColor(n.GetTone())
		}
		var out ggui.Widget
		switch n.GetVariant() {
		case "ghost":
			out = widget.Box(inner).Radius(8).Pad(6, 10)
		case "outline":
			out = widget.Box(inner).Border(1, border).Radius(8).Pad(8, 10)
		case "secondary":
			out = widget.Box(inner).Fill(skin.Surface2).Border(1, border).Radius(8).Pad(8, 10)
		case "danger":
			out = widget.Box(inner).Fill(skin.DiffRemovedBg).Border(1, skin.DiffRemoved).Radius(8).Pad(8, 10)
		case "warning":
			out = &washed{child: widget.Box(inner).Border(1, skin.DotWaiting).Radius(8).Pad(8, 10), color: skin.DotWaiting, radius: 8}
		case "primary":
			out = widget.Box(inner).Fill(skin.Surface2).Border(1, skin.AccentBright).Radius(8).Pad(8, 10)
		case "soft":
			if n.GetTone() != "" {
				out = &washed{child: widget.Box(inner).Pad(8, 10), color: toneColor(n.GetTone()), radius: 10}
			} else {
				out = widget.Box(inner).Fill(skin.Surface2).Radius(10).Pad(8, 10)
			}
		default:
			out = widget.Box(inner).Fill(skin.Surface1).Border(1, border).Radius(8).Pad(8, 10)
		}
		return wrapDialogs(out)
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
		return wrapDialogs(v.maybeDragDrop(out, n, bare))
	case "list":
		return wrapDialogs(v.maybeDragDrop(widget.Column(kids...).Gap(gap).Stretch(), n, bare))
	}
	if gap == 0 {
		gap = 6
	}
	return wrapDialogs(v.maybeDragDrop(v.tappableBoxWhen(widget.Column(kids...).Gap(gap).Stretch(), n, !bare), n, bare))
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
			trows = append(trows, v.withContextMenu(v.dragDrop(wrapped, c), c, "", ""))
			continue
		}
		trows = append(trows, widget.Box(v.node(c, false)).Pad(6, 10))
	}
	return v.maybeDragDrop(widget.Box(widget.Column(trows...).Stretch()).Border(1, skin.Border).Radius(8), n, bare)
}

// autoSplit lays out two panes horizontally when available width is at least
// autoSplitBreakpoint (640 px) and vertically otherwise, sharing one ratio.
type autoSplit struct {
	horiz, vert ggui.Widget
	useHoriz    bool
}

func (a *autoSplit) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	a.useHoriz = !math.IsInf(c.MaxW, 0) && c.MaxW >= autoSplitBreakpoint
	if a.useHoriz {
		return a.horiz.Layout(c, env)
	}
	return a.vert.Layout(c, env)
}

func (a *autoSplit) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if a.useHoriz {
		dst.Paint(a.horiz, r)
		return
	}
	dst.Paint(a.vert, r)
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
	dags       map[string]*dagViewState
}

func newBlockForm() *blockForm {
	return &blockForm{
		fields:     map[string]*ggui.StateValue[string]{},
		given:      map[string]string{},
		drawn:      map[string]bool{},
		locals:     map[string]*ggui.StateValue[string]{},
		localGiven: map[string]string{},
		graphs:     map[string]*graphViewState{},
		dags:       map[string]*dagViewState{},
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
	f.dags = map[string]*dagViewState{}
}

// copyFrom restores cached field, local, graph, and dag state from src into f.
func (f *blockForm) copyFrom(src *blockForm) {
	if src == nil || src == f {
		return
	}
	f.fields = src.fields
	f.given = src.given
	f.drawn = src.drawn
	f.locals = src.locals
	f.localGiven = src.localGiven
	f.graphs = src.graphs
	f.dags = src.dags
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
