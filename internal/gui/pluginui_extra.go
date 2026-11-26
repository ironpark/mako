package gui

import (
	"fmt"
	"image/color"
	"math"
	"strconv"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// Additional and enhanced plugin UI components (revision 49):
// diff, alert, empty, stat, radio, slider, kbd, skeleton, spacer, plus
// syntax-highlighted code, search/password/icon inputs, mono text/textarea,
// progress rings, and underline/pills tab bars.

func (v *treeView) textNode(n *protocol.UiNode, inLine bool) ggui.Widget {
	size := textSize(n.GetSize())
	weight := textWeight(n.GetWeight())
	tone := n.GetTone()
	mono := false
	switch n.GetVariant() {
	case "mono":
		mono = true
		if n.GetSize() == "" {
			size = 12
		}
	case "caption":
		if n.GetSize() == "" {
			size = 11
		}
		if tone == "" {
			tone = "muted"
		}
	case "heading":
		if n.GetWeight() == "" {
			weight = ggui.WeightSemibold
		}
		switch n.GetSize() {
		case "large":
			size = 17
		case "small":
			size = 13.5
		default:
			size = 15
		}
	}
	t := widget.Text(n.GetText()).Size(size).Weight(weight).Color(toneColor(tone))
	if mono {
		t = t.Font(ggui.DefaultMonoFont())
	}
	if inLine {
		t = t.NoWrap().Ellipsis()
	}
	return t
}

func (v *treeView) inputNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	ph := cmpTone(n.GetPlaceholder(), n.GetText())
	field := v.form.field(id, n.GetValue())
	f := ui.TextField(field).Key(v.block + "\x00" + v.scope + "\x00" + v.instance + id).
		Placeholder(ph).Name(cmpTone(ph, T("Input"))).Disabled(n.GetDisabled())
	if n.GetVariant() == "password" {
		f = f.Password()
	}
	if n.GetChange() {
		f = f.OnSubmit(func(s string) { v.send(id, protocol.UiSubmit, s) }).
			OnCommit(func(s string) { v.send(id, protocol.UiChange, s) })
	}
	leadIcon := n.GetIcon()
	isSearch := n.GetVariant() == "search"
	if isSearch && leadIcon == "" {
		leadIcon = "search"
	}
	if leadIcon == "" && !isSearch {
		return f
	}
	var parts []ggui.Widget
	if leadIcon != "" {
		parts = append(parts, pluginIcon(leadIcon).Size(14).Color(skin.Muted))
	}
	parts = append(parts, widget.Expanded(f))
	if isSearch && !n.GetDisabled() {
		clearBtn := ggui.View(field, func(cur string) ggui.Widget {
			if cur == "" {
				return widget.Box()
			}
			return ui.Tooltip(ui.ButtonOf(icon("x").Size(13).Color(skin.Muted), func() {
				field.Set("")
				if n.GetChange() {
					v.send(id, protocol.UiChange, "")
				}
			}).Ghost().Pad(4).Radius(5).Name(T("Clear")), T("Clear"))
		})
		parts = append(parts, clearBtn)
	}
	return widget.Row(parts...).Gap(6).Align(widget.AlignCenter)
}

func (v *treeView) textareaNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	lines := 4
	switch n.GetSize() {
	case "large":
		lines = 14
	case "small":
		lines = 3
	}
	ph := cmpTone(n.GetPlaceholder(), n.GetText())
	f := ui.TextField(v.form.input(id, n.GetValue())).Key(v.block + "\x00" + v.scope + "\x00" + v.instance + id).
		Lines(lines).Placeholder(ph).Name(cmpTone(ph, T("Input")))
	if n.GetVariant() == "mono" {
		f = f.Style(ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 12})
	}
	if n.GetChange() {
		f = f.OnSubmit(func(s string) { v.send(id, protocol.UiSubmit, s) }).
			OnCommit(func(s string) { v.send(id, protocol.UiChange, s) })
	}
	return f.Disabled(n.GetDisabled())
}

func (v *treeView) tabsNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	field := v.form.field(id, n.GetValue())
	opts := n.GetOptions()
	variant := n.GetVariant()
	return ggui.View(field, func(active string) ggui.Widget {
		btns := make([]ggui.Widget, 0, len(opts))
		for _, opt := range opts {
			o := opt
			on := o == active
			onTap := func() {
				if n.GetDisabled() {
					return
				}
				field.Set(o)
				if n.GetChange() {
					v.send(id, protocol.UiChange, o)
				}
			}
			switch variant {
			case "underline":
				fg := pick[color.Color](on, skin.Fg, skin.Muted)
				lbl := widget.Text(o).Size(12.5).Weight(pick(on, ggui.WeightMedium, ggui.WeightRegular)).Color(fg).NoWrap()
				btn := ui.ButtonOf(lbl, onTap).Ghost().Pad(6, 10).Radius(4).Disabled(n.GetDisabled()).Name(o)
				var cell ggui.Widget = btn
				if on {
					cell = &underlined{child: btn, color: skin.AccentBright}
				}
				btns = append(btns, cell)
			case "pills":
				b := ui.Button(o, onTap).Disabled(n.GetDisabled()).Radius(999).Name(o)
				if on {
					b = b.Secondary()
				} else {
					b = b.Ghost()
				}
				btns = append(btns, b)
			default:
				b := ui.Button(o, onTap).Disabled(n.GetDisabled()).Name(o)
				if on {
					b = b.Secondary()
				} else {
					b = b.Ghost()
				}
				btns = append(btns, b)
			}
		}
		strip := looseHeight{widget.Scroll(widget.Row(btns...).Gap(4).Align(widget.AlignCenter)).Horizontal().Shrink().Bar(color.Transparent)}
		switch variant {
		case "underline":
			return widget.Column(strip, hline()).Stretch()
		case "pills":
			return strip
		default:
			return widget.Box(strip).Fill(skin.Surface1).Border(1, skin.Border).Radius(8).Pad(3)
		}
	})
}

func (v *treeView) codeNode(n *protocol.UiNode) ggui.Widget {
	src := n.GetText()
	lang := n.GetLanguage()
	var body ggui.Widget
	if v.m != nil && v.m.hl != nil {
		l, ok := language{}, false
		if lang != "" {
			l, ok = v.m.hl.languageNamed(lang)
		}
		fontSz := 12.0
		if v.m.Prefs != nil {
			if fs := ggui.Peek(v.m.Prefs).FontSize; fs > 0 {
				fontSz = max(fs-1, 11)
			}
		}
		body = codeText(v.m, src, l, ok, fontSz)
	} else {
		body = widget.Text(src).Font(ggui.DefaultMonoFont()).Size(12)
	}
	if h := n.GetMaxHeight(); h > 0 {
		body = widget.Box(widget.Scroll(body).Bar(skin.Surface3)).MaxHeight(h)
	}
	copied := ggui.State(false)
	copyBtn := ggui.View(copied, func(done bool) ggui.Widget {
		glyph := pick(done, "check", "copy")
		col := pick[color.Color](done, skin.DiffAdded, skin.Muted)
		tip := pick(done, T("Copied"), T("Copy code"))
		return ui.Tooltip(ui.ButtonOf(icon(glyph).Size(12).Color(col), func() {
			ggui.CurrentClipboard().Write(src)
			copied.Set(true)
		}).Ghost().Pad(4).Radius(5).Name(tip), tip)
	})
	var headRow []ggui.Widget
	if lang != "" {
		headRow = append(headRow, widget.Text(lang).Font(ggui.DefaultMonoFont()).Size(11).Color(skin.Muted).NoWrap())
	}
	headRow = append(headRow, widget.Spacer(), copyBtn)
	header := widget.Box(widget.Row(headRow...).Gap(6).Align(widget.AlignCenter)).Pad(3, 8).Fill(skin.Surface2)
	return widget.Box(widget.Column(
		header,
		hline(),
		widget.Box(body).Pad(8, 10),
	).Stretch()).Fill(skin.Surface1).Border(1, skin.Border).Radius(8).Clip()
}

func (v *treeView) progressNode(n *protocol.UiNode) ggui.Widget {
	switch n.GetVariant() {
	case "spinner":
		if n.GetText() == "" {
			return ui.Spinner().Size(13)
		}
		return widget.Row(
			ui.Spinner().Size(13),
			widget.Text(n.GetText()).Size(12).Color(toneColor(cmpTone(n.GetTone(), "muted"))).NoWrap().Ellipsis(),
		).Gap(6).Align(widget.AlignCenter)
	case "ring":
		diam, stroke := 26.0, 3.0
		switch n.GetSize() {
		case "small":
			diam, stroke = 18, 2.5
		case "large":
			diam, stroke = 38, 3.5
		}
		ring := ui.ProgressRing(ggui.Const(n.GetProgress())).Size(diam).Stroke(stroke)
		if n.GetTone() != "" {
			ring = ring.Color(toneColor(n.GetTone()))
		}
		if n.GetText() == "" {
			return ring
		}
		return widget.Row(
			ring,
			widget.Text(n.GetText()).Size(12).Color(toneColor(cmpTone(n.GetTone(), "muted"))).NoWrap().Ellipsis(),
		).Gap(8).Align(widget.AlignCenter)
	default:
		if n.GetText() == "" {
			return ui.Progress(ggui.Const(n.GetProgress()))
		}
		return widget.Column(
			widget.Row(
				widget.Expanded(widget.Text(n.GetText()).Size(11.5).Color(toneColor(cmpTone(n.GetTone(), "muted"))).NoWrap().Ellipsis()),
				widget.Text(fmt.Sprintf("%d%%", int(math.Round(n.GetProgress()*100)))).Size(11).Font(ggui.DefaultMonoFont()).Color(skin.Muted).NoWrap(),
			).Gap(6).Align(widget.AlignCenter),
			ui.Progress(ggui.Const(n.GetProgress())),
		).Gap(4).Stretch()
	}
}

// diffNode renders a native unified or side-by-side diff viewer using Mako's
// diffRows engine with word-level change highlighting and local split/wrap toggles.
func (v *treeView) diffNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	rawLines := n.GetDiffLines()
	lines := make([]*protocol.DiffLine, len(rawLines))
	added, removed := 0, 0
	for i, l := range rawLines {
		op := l.GetOp()
		if op == "+" {
			added++
		} else if op == "-" {
			removed++
		}
		lines[i] = &protocol.DiffLine{
			Op:   op,
			Text: l.GetText(),
			Old:  int64(l.GetOldLine()),
			New:  int64(l.GetNewLine()),
		}
	}
	diff := &protocol.Diff{Path: n.GetText(), Lines: lines}

	initSplit := "false"
	if n.GetVariant() == "split" {
		initSplit = "true"
	}
	splitSt := v.form.local(id+"\x00split", initSplit)
	wrapSt := v.form.local(id+"\x00wrap", "false")
	selSt := v.form.local(id+"\x00sel", "-1")

	body := ggui.Reactive(func() ggui.Widget {
		split := splitSt.Get() == "true"
		wrap := wrapSt.Get() == "true"
		selIdx, _ := strconv.Atoi(selSt.Get())
		sel := noSel
		if selIdx >= 0 && selIdx < len(lines) {
			sel = lineSel{From: selIdx, To: selIdx}
		}

		toggleBtn := func(glyph, tip string, on bool, onTap func()) ggui.Widget {
			c := pick[color.Color](on, skin.Fg, skin.ExtraMuted)
			return ui.Tooltip(ui.ButtonOf(icon(glyph).Size(13).Color(c), onTap).Ghost().Pad(4).Radius(5).Name(tip), tip)
		}

		title := n.GetText()
		if title == "" && (n.GetDiffFrom() != "" || n.GetDiffTo() != "") {
			if n.GetDiffFrom() != "" && n.GetDiffTo() != "" {
				title = n.GetDiffFrom() + " → " + n.GetDiffTo()
			} else {
				title = cmpTone(n.GetDiffTo(), n.GetDiffFrom())
			}
		}
		var headItems []ggui.Widget
		headItems = append(headItems, icon("file-diff").Size(13).Color(skin.Muted))
		if title != "" {
			headItems = append(headItems, widget.Expanded(widget.Text(title).Size(12).Font(ggui.DefaultMonoFont()).Weight(ggui.WeightMedium).NoWrap().Ellipsis()))
		} else {
			headItems = append(headItems, widget.Spacer())
		}
		if added > 0 {
			headItems = append(headItems, widget.Text(fmt.Sprintf("+%d", added)).Size(11).Font(ggui.DefaultMonoFont()).Color(skin.DiffAdded).NoWrap())
		}
		if removed > 0 {
			headItems = append(headItems, widget.Text(fmt.Sprintf("-%d", removed)).Size(11).Font(ggui.DefaultMonoFont()).Color(skin.DiffRemoved).NoWrap())
		}
		headItems = append(headItems,
			toggleBtn("columns-2", T("Side by side"), split, func() { splitSt.Set(strconv.FormatBool(!split)) }),
			toggleBtn("wrap-text", T("Wrap long lines"), wrap, func() { wrapSt.Set(strconv.FormatBool(!wrap)) }),
		)
		head := widget.Box(widget.Row(headItems...).Gap(6).Align(widget.AlignCenter)).Pad(4, 8).Fill(skin.Surface2)

		var content ggui.Widget
		if len(lines) == 0 {
			content = widget.Box(muted(widget.Text(cmpTone(n.GetPlaceholder(), T("No changes."))).Size(12))).Pad(12)
		} else {
			pick := func(i int, _ bool) {
				if n.GetDisabled() {
					return
				}
				selSt.Set(strconv.Itoa(i))
				if n.GetChange() {
					v.send(id, protocol.UiChange, strconv.Itoa(i))
				}
			}
			rows := diffRows(diff, diffMode{Split: split, Wrap: wrap}, sel, pick, func(int) ggui.Widget { return widget.Box() })
			maxH := n.GetMaxHeight()
			if maxH <= 0 {
				maxH = min(float64(len(lines)*20+16), 420)
				if maxH < 80 {
					maxH = 80
				}
			}
			content = widget.Box(rows).Height(maxH)
		}
		return widget.Box(widget.Column(head, hline(), content).Stretch()).
			Fill(skin.Surface1).Border(1, skin.Border).Radius(8).Clip()
	})
	return body
}

// alertNode renders a semantic callout banner with optional icon, description,
// child actions, and dismiss button.
func (v *treeView) alertNode(n *protocol.UiNode, bare bool) ggui.Widget {
	tone := n.GetTone()
	iconName := n.GetIcon()
	if iconName == "" {
		switch tone {
		case "success":
			iconName = "circle-check"
		case "warning":
			iconName = "triangle-alert"
		case "danger":
			iconName = "circle-alert"
		default:
			iconName = "info"
		}
	}
	variant := n.GetVariant()
	accent := toneColor(cmpTone(tone, "accent"))
	var fg, bodyFg color.Color = skin.Fg, skin.Muted
	if variant == "solid" {
		_, fg = solidToneColors(cmpTone(tone, "accent"))
		bodyFg = fg
		accent = fg
	}
	var col []ggui.Widget
	if n.GetText() != "" {
		col = append(col, widget.Text(n.GetText()).Size(12.5).Weight(ggui.WeightSemibold).Color(fg))
	}
	if body := cmpTone(n.GetValue(), n.GetPlaceholder()); body != "" {
		col = append(col, widget.Text(body).Size(12).Color(bodyFg))
	}
	if len(n.GetChildren()) > 0 {
		var acts []ggui.Widget
		for _, c := range n.GetChildren() {
			if c.GetKind() == "menu" && c.GetVariant() == "context" {
				continue
			}
			acts = append(acts, v.node(c, true))
		}
		if len(acts) > 0 {
			col = append(col, widget.Box(widget.Row(acts...).Gap(6).Align(widget.AlignCenter)).Pad(4, 0, 0, 0))
		}
	}
	rowItems := []ggui.Widget{
		widget.Padding(pluginIcon(iconName).Size(15).Color(accent), 1, 0, 0, 0),
		widget.Expanded(widget.Column(col...).Gap(3).Stretch()),
	}
	if !bare && n.GetChange() && !n.GetDisabled() {
		id := n.GetId()
		rowItems = append(rowItems, ui.Tooltip(ui.ButtonOf(icon("x").Size(13).Color(bodyFg), func() {
			v.send(id, protocol.UiChange, "dismiss")
		}).Ghost().Pad(4).Radius(5).Name(T("Dismiss")), T("Dismiss")))
	}
	inner := widget.Row(rowItems...).Gap(8).Align(widget.AlignStart)
	switch variant {
	case "solid":
		fill, _ := solidToneColors(cmpTone(tone, "accent"))
		return widget.Box(inner).Pad(10, 12).Radius(8).Fill(fill)
	case "outline":
		return widget.Box(inner).Pad(10, 12).Radius(8).Border(1, accent)
	default: // "" or "soft"
		return &washed{child: widget.Box(inner).Pad(10, 12).Border(1, withAlpha(accent, 0x55)).Radius(8), color: accent, radius: 8}
	}
}

// emptyNode renders a centered empty-state placeholder with icon, title,
// description, and optional action children.
func (v *treeView) emptyNode(n *protocol.UiNode, bare bool) ggui.Widget {
	iconName := cmpTone(n.GetIcon(), "inbox")
	iconCol := toneColor(cmpTone(n.GetTone(), "faint"))
	var rows []ggui.Widget
	rows = append(rows, pluginIcon(iconName).Size(28).Color(iconCol))
	if n.GetText() != "" {
		rows = append(rows, widget.Text(n.GetText()).Size(13.5).Weight(ggui.WeightMedium).Color(skin.Fg))
	}
	if desc := cmpTone(n.GetValue(), n.GetPlaceholder()); desc != "" {
		rows = append(rows, widget.Text(desc).Size(12).Color(skin.Muted))
	}
	if len(n.GetChildren()) > 0 {
		var acts []ggui.Widget
		for _, c := range n.GetChildren() {
			if c.GetKind() == "menu" && c.GetVariant() == "context" {
				continue
			}
			acts = append(acts, v.node(c, true))
		}
		if len(acts) > 0 {
			rows = append(rows, widget.Box().Height(4), widget.Row(acts...).Gap(8).Align(widget.AlignCenter))
		}
	}
	inner := widget.Box(widget.Column(rows...).Gap(6).Align(widget.AlignCenter)).Pad(20, 24).MaxWidth(360)
	if n.GetGrow() > 0 {
		return widget.Center(inner)
	}
	return widget.Column(inner).Align(widget.AlignCenter)
}

// statNode renders a KPI/metric tile card with label, prominent value, optional
// subtext/delta (placeholder), icon, and trailing badges.
func (v *treeView) statNode(n *protocol.UiNode, tap func(), isTap, bare bool) ggui.Widget {
	var top []ggui.Widget
	if n.GetIcon() != "" {
		top = append(top, pluginIcon(n.GetIcon()).Size(13).Color(toneColor(cmpTone(n.GetTone(), "muted"))))
	}
	if n.GetText() != "" {
		top = append(top, widget.Expanded(widget.Text(n.GetText()).Size(11.5).Weight(ggui.WeightMedium).Color(skin.Muted).NoWrap().Ellipsis()))
	} else {
		top = append(top, widget.Spacer())
	}
	if len(n.GetBadges()) > 0 {
		top = append(top, renderBadges(n.GetBadges())...)
	}
	valSize := 20.0
	switch n.GetSize() {
	case "small":
		valSize = 16
	case "large":
		valSize = 26
	}
	var valCol color.Color = skin.Fg
	if n.GetTone() != "" {
		valCol = toneColor(n.GetTone())
	}
	rows := []ggui.Widget{
		widget.Row(top...).Gap(5).Align(widget.AlignCenter),
		widget.Text(n.GetValue()).Size(valSize).Weight(ggui.WeightSemibold).Color(valCol).NoWrap().Ellipsis(),
	}
	if sub := n.GetPlaceholder(); sub != "" {
		rows = append(rows, widget.Text(sub).Size(11.5).Color(skin.Muted).NoWrap().Ellipsis())
	}
	content := widget.Column(rows...).Gap(4).Stretch()
	card := widget.Box(content).Pad(10, 12).Radius(8).Fill(skin.Surface1).Border(1, skin.Border)
	if isTap && !n.GetDisabled() {
		return v.maybeDragDrop(ui.ButtonOf(card, tap).Ghost().Pad(0).Radius(8).Name(cmpTone(n.GetText(), n.GetValue())), n, bare)
	}
	return v.maybeDragDrop(card, n, bare)
}

// radioNode renders a native radio button group (vertical or horizontal).
func (v *treeView) radioNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	field := v.form.field(id, n.GetValue())
	rg := ui.Radios(ggui.Bind(field.Get, func(x string) {
		field.Set(x)
		if n.GetChange() {
			v.send(id, protocol.UiChange, x)
		}
	})).Options(n.GetOptions()).Disabled(n.GetDisabled()).Name(cmpTone(n.GetText(), T("Options")))
	if n.GetDirection() == "horizontal" {
		rg = rg.Horizontal()
	}
	if n.GetText() == "" {
		return rg
	}
	return widget.Column(
		widget.Text(n.GetText()).Size(12).Weight(ggui.WeightMedium).Color(skin.Muted),
		rg,
	).Gap(6).Stretch()
}

// sliderNode renders a native horizontal slider with optional label and value readout.
func (v *treeView) sliderNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	lo, hi, step := n.GetMin(), n.GetMax(), n.GetStep()
	if hi <= lo {
		lo, hi = 0, 100
	}
	initVal := n.GetValue()
	if initVal == "" {
		initVal = strconv.FormatFloat(lo, 'f', -1, 64)
	}
	field := v.form.field(id, initVal)
	parseVal := func(s string) float64 {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return lo
		}
		return clampFloat(f, lo, hi)
	}
	formatVal := func(f float64) string {
		if step >= 1 && math.Abs(step-math.Round(step)) < 1e-9 {
			return strconv.FormatInt(int64(math.Round(f)), 10)
		}
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	binding := ggui.Bind(
		func() float64 { return parseVal(field.Get()) },
		func(val float64) { field.Set(formatVal(clampFloat(val, lo, hi))) },
	)
	sl := ui.Slider(binding, lo, hi).Disabled(n.GetDisabled()).Name(cmpTone(n.GetText(), cmpTone(n.GetPlaceholder(), T("Slider"))))
	if step > 0 {
		sl = sl.Step(step)
	}
	if n.GetChange() {
		sl = sl.OnCommit(func(val float64) {
			s := formatVal(clampFloat(val, lo, hi))
			field.Set(s)
			v.send(id, protocol.UiChange, s)
		})
	}
	if n.GetText() == "" {
		return sl
	}
	readout := ggui.Map(field, func(s string) string { return s })
	return widget.Column(
		widget.Row(
			widget.Expanded(widget.Text(n.GetText()).Size(12).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
			widget.TextOf(readout).Size(11.5).Font(ggui.DefaultMonoFont()).Color(skin.Muted).NoWrap(),
		).Gap(6).Align(widget.AlignCenter),
		sl,
	).Gap(4).Stretch()
}

// kbdNode renders a single keyboard shortcut cap or a chord group.
func (v *treeView) kbdNode(n *protocol.UiNode) ggui.Widget {
	if opts := n.GetOptions(); len(opts) > 0 {
		caps := make([]ggui.Widget, len(opts))
		for i, k := range opts {
			caps[i] = ui.Kbd(k)
		}
		return ui.KbdGroup(caps...)
	}
	return ui.Kbd(n.GetText())
}

// skeletonNode renders a pulsing loading placeholder (text line, rectangle, or circle).
func (v *treeView) skeletonNode(n *protocol.UiNode) ggui.Widget {
	switch n.GetVariant() {
	case "circle":
		d := 32.0
		switch n.GetSize() {
		case "small":
			d = 22
		case "large":
			d = 44
		}
		return ui.Skeleton(d, d).Circle()
	case "rect":
		h := n.GetMaxHeight()
		if h <= 0 {
			switch n.GetSize() {
			case "small":
				h = 40
			case "large":
				h = 120
			default:
				h = 72
			}
		}
		return ui.Skeleton(220, h)
	default: // "" or "text"
		h := 14.0
		switch n.GetSize() {
		case "small":
			h = 11
		case "large":
			h = 18
		}
		if mh := n.GetMaxHeight(); mh > 0 {
			h = mh
		}
		return ui.Skeleton(180, h)
	}
}

// spacerNode renders flexible space or a fixed gap box.
func (v *treeView) spacerNode(n *protocol.UiNode) ggui.Widget {
	if g := n.GetGap(); g > 0 {
		return widget.Box().Size(g, g)
	}
	return widget.Spacer()
}
