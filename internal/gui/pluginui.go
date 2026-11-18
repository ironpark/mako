package gui

import (
	"image/color"
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

// A plugin's tree, as the daemon checked it, drawn with the window's own
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

// uiNodeView draws a node of block's tree; inLine says it is a row's
// child, where text keeps to a line.
func uiNodeView(m *model, block string, n *protocol.UiNode, inLine bool) ggui.Widget {
	tap := func() {}
	if n.GetTap() {
		id := n.GetId()
		tap = func() { m.blockEvent(block, id, protocol.UiTap, "") }
	}
	switch n.GetKind() {
	case "column", "row", "stack", "list":
		return uiContainer(m, block, n)
	case "text":
		t := widget.Text(n.GetText()).Size(textSize(n.GetSize())).Weight(textWeight(n.GetWeight())).Color(toneColor(n.GetTone()))
		if inLine {
			t = t.NoWrap().Ellipsis()
		}
		return tappable(t, n, tap)
	case "icon":
		return tappable(pluginIcon(n.GetIcon()).Size(textSize(n.GetSize())+2).Color(toneColor(cmpTone(n.GetTone(), "muted"))), n, tap)
	case "pill":
		return tappable(pill(n.GetText(), toneColor(cmpTone(n.GetTone(), "muted"))), n, tap)
	case "chip":
		return tappable(chip(n.GetText()), n, tap)
	case "button":
		var b *ui.ButtonWidget
		if n.GetIcon() != "" {
			b = ui.ButtonOf(widget.Row(pluginIcon(n.GetIcon()).Size(14), widget.Text(n.GetText()).Size(12.5).NoWrap()).Gap(6).Align(widget.AlignCenter), tap)
		} else {
			b = ui.Button(n.GetText(), tap)
		}
		switch n.GetTone() {
		case "accent":
		case "danger":
			b = b.Destructive()
		default:
			b = b.Outline()
		}
		return b.Disabled(n.GetDisabled() || !n.GetTap()).Name(n.GetText())
	case "input":
		value := ggui.State(n.GetValue())
		id := n.GetId()
		f := ui.TextField(value).Key(block + id).Placeholder(n.GetText()).Name(cmpTone(n.GetText(), T("Input")))
		if n.GetChange() {
			f = f.OnSubmit(func(s string) { m.blockEvent(block, id, protocol.UiSubmit, s) }).
				OnCommit(func(s string) { m.blockEvent(block, id, protocol.UiChange, s) })
		}
		return f.Disabled(n.GetDisabled())
	case "toggle":
		id, on := n.GetId(), n.GetChecked()
		sw := ui.Switch(ggui.Bind(func() bool { return on }, func(v bool) {
			on = v
			m.blockEvent(block, id, protocol.UiChange, strconv.FormatBool(v))
		}), n.GetText()).Name(n.GetText())
		return sw.Disabled(n.GetDisabled() || !n.GetChange())
	case "select":
		id, value := n.GetId(), n.GetValue()
		return widget.Box(ui.Select(ggui.Bind(func() string { return value }, func(v string) {
			value = v
			m.blockEvent(block, id, protocol.UiChange, v)
		})).Options(n.GetOptions()).Name(T("Choose")).Disabled(n.GetDisabled() || !n.GetChange())).Width(200)
	case "markdown":
		return mdview.Column(pluginPage{pageHost{m: m}}, n.GetText())
	case "code":
		return toolBlock(n.GetText(), false)
	case "progress":
		return ui.Progress(ggui.Const(n.GetProgress()))
	case "divider":
		return hline()
	}
	return widget.Box() // the daemon sends no other kind
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

// uiContainer draws a column, row, stack or list, a child that grows
// taking its share of the room left. A list's rows that send a tap are
// the right panel's rows, lit under the pointer.
func uiContainer(m *model, block string, n *protocol.UiNode) ggui.Widget {
	row := n.GetKind() == "row"
	kids := make([]ggui.Widget, 0, len(n.GetChildren()))
	for _, c := range n.GetChildren() {
		var w ggui.Widget
		if n.GetKind() == "list" && c.GetTap() {
			id := c.GetId()
			tapless := proto.CloneOf(c) // drawn as the row's content; the row answers the tap
			tapless.Tap = false
			w = listRow(uiNodeView(m, block, tapless, false), strings.Join(texts(c), " "), func() { m.blockEvent(block, id, protocol.UiTap, "") })
		} else {
			w = uiNodeView(m, block, c, row)
		}
		switch g := c.GetGrow(); {
		case g == 1:
			w = widget.Expanded(w)
		case g > 0:
			w = widget.Flex(w, g)
		}
		kids = append(kids, w)
	}
	gap := n.GetGap()
	switch n.GetKind() {
	case "row":
		if gap == 0 {
			gap = 8
		}
		w := widget.Row(kids...).Gap(gap).Align(widget.AlignCenter)
		return tappableBox(w, n, m, block)
	case "stack":
		return widget.Stack(kids...)
	case "list":
		return widget.Column(kids...).Gap(gap).Stretch()
	}
	if gap == 0 {
		gap = 6
	}
	return tappableBox(widget.Column(kids...).Gap(gap).Stretch(), n, m, block)
}

func tappableBox(w ggui.Widget, n *protocol.UiNode, m *model, block string) ggui.Widget {
	if !n.GetTap() {
		return w
	}
	id := n.GetId()
	return listRow(w, strings.Join(texts(n), " "), func() { m.blockEvent(block, id, protocol.UiTap, "") })
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
// it may reach, carrying what it put in their addresses; it draws no
// diagrams; and it opens web links alone, as the user taps them.
type pluginPage struct{ pageHost }

func (pluginPage) Image(string) *ggui.StateValue[mdview.Image]        { return nil }
func (pluginPage) Diagram(_, _ string) *ggui.StateValue[mdview.Image] { return nil }
func (pluginPage) Open(dest string)                                   { openPluginLink(dest) }
