package gui

import (
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
)

// A settings page uses a wide window's room: past settingsWide its
// sections' headings hang in a column of their own at the left, beside
// the cards, and past settingsTwoCol multi-section pages arrange cards
// into two balanced columns up to settingsMaxWidth, rather than leaving
// wide margins around a single narrow column. Below settingsWide, each
// heading sits over its cards in one column.

const (
	// settingsMaxWidth is the widest a page's content runs.
	settingsMaxWidth = 1600
	// settingsWide is the content width from which headings hang beside
	// their cards and listBeside lays its list beside its detail.
	settingsWide = 860
	// settingsTwoCol is the content width from which multi-section pages
	// arrange cards in two side-by-side columns.
	settingsTwoCol = 980
	// settingsGutter is the width of the headings' column, its gap to the
	// cards included.
	settingsGutter = 200
	// settingsGutterWide is the headings' column on wider single-column pages.
	settingsGutterWide = 220
	// settingsGutterGap is the room kept between a heading and the cards.
	settingsGutterGap = 28
	// settingsColumnGap is the horizontal gap between side-by-side columns.
	settingsColumnGap = 24
)

// settingsGutterKey is the headings' column a page's sections hang their
// headings in, to the left of where they are laid out; 0 when there is
// none, and a heading sits over its cards.
var settingsGutterKey = ggui.NewEnvKey[float64]("settings gutter")

// settingsColumn is a page: its title over its sections, which it lays
// out right of the headings' column once it is wide enough for one.
// When twoCol is true, the headings' column collapses once the page is
// wide enough for side-by-side columns so those columns use the full width.
type settingsColumn struct {
	head, body ggui.Widget
	noGutter   bool // a listPage's
	twoCol     bool // a widePage's

	gutter, headH, bodyH float64
}

// pageGap is the room between a page's title and between its sections.
const pageGap = 28

func (s *settingsColumn) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w := c.MaxW
	switch {
	case s.noGutter, s.twoCol && w >= settingsTwoCol:
		s.gutter = 0
	case w >= 1160:
		s.gutter = settingsGutterWide
	case w >= settingsWide:
		s.gutter = settingsGutter
	default:
		s.gutter = 0
	}
	s.headH = s.head.Layout(ggui.Constraints{MinW: w, MaxW: w, MaxH: ggui.Unbounded}, env).H
	bw := w - s.gutter
	s.bodyH = s.body.Layout(ggui.Constraints{MinW: bw, MaxW: bw, MaxH: ggui.Unbounded}, env.With(settingsGutterKey, s.gutter)).H
	return c.Constrain(ggui.Sz(w, s.headH+pageGap+s.bodyH))
}

func (s *settingsColumn) Paint(dst *ggui.Canvas, r ggui.Rect) {
	x, y, w := r.Origin.X, r.Origin.Y, r.Size.W
	dst.Paint(s.head, ggui.Rct(ggui.Pt(x, y), ggui.Sz(w, s.headH)))
	dst.Paint(s.body, ggui.Rct(ggui.Pt(x+s.gutter, y+s.headH+pageGap), ggui.Sz(w-s.gutter, s.bodyH)))
}

// sectionLayout is a heading and its cards: the heading over them, or,
// on a page with a headings' column, hung in it beside them.
type sectionLayout struct {
	over, beside, body ggui.Widget

	gutter, headH, bodyH, top float64
}

// sectionGap is the room between a heading over its cards and them, and
// between the cards.
const sectionGap = 10

// besideTop lowers a heading hung beside its cards, where neither has a
// baseline to line it up with the first line of theirs by.
const besideTop = 14

func (s *sectionLayout) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w := c.MaxW
	s.gutter, _ = env.Get(settingsGutterKey)
	body := ggui.Constraints{MinW: w, MaxW: w, MaxH: ggui.Unbounded}
	if s.gutter > 0 {
		s.headH = s.beside.Layout(ggui.Constraints{MaxW: s.gutter - settingsGutterGap, MaxH: ggui.Unbounded}, env).H
		s.bodyH = s.body.Layout(body, env).H
		s.top = besideTop
		if b, ok := ggui.BaselineOf(s.body); ok {
			if hb, ok := ggui.BaselineOf(s.beside); ok {
				s.top = max(b-hb, 0)
			}
		}
		return c.Constrain(ggui.Sz(w, max(s.bodyH, s.top+s.headH)))
	}
	s.headH = s.over.Layout(ggui.Constraints{MinW: w, MaxW: w, MaxH: ggui.Unbounded}, env).H
	s.bodyH = s.body.Layout(body, env).H
	return c.Constrain(ggui.Sz(w, s.headH+sectionGap+s.bodyH))
}

func (s *sectionLayout) Paint(dst *ggui.Canvas, r ggui.Rect) {
	x, y, w := r.Origin.X, r.Origin.Y, r.Size.W
	if s.gutter > 0 {
		dst.Paint(s.beside, ggui.Rct(ggui.Pt(x-s.gutter, y+s.top), ggui.Sz(s.gutter-settingsGutterGap, s.headH)))
		dst.Paint(s.body, ggui.Rct(ggui.Pt(x, y), ggui.Sz(w, s.bodyH)))
		return
	}
	dst.Paint(s.over, ggui.Rct(ggui.Pt(x, y), ggui.Sz(w, s.headH)))
	dst.Paint(s.body, ggui.Rct(ggui.Pt(x, y+s.headH+sectionGap), ggui.Sz(w, s.bodyH)))
}

// section is a heading over its cards, or beside them on a wide page.
func section(title string, children ...ggui.Widget) ggui.Widget {
	return &sectionLayout{
		over:   widget.Box(muted(widget.Text(title).Size(12.5))).Pad(0, 4),
		beside: widget.Text(title).Size(13).Weight(ggui.WeightMedium).Color(skin.Muted),
		body:   widget.Column(children...).Gap(sectionGap).Stretch(),
	}
}

// page is a settings page: its title and what it is for, over its
// sections.
func page(title, description string, sections ...ggui.Widget) ggui.Widget {
	return &settingsColumn{head: pageTitle(title, description), body: widget.Column(sections...).Gap(pageGap).Stretch()}
}

// widePage is a settings page whose sections arrange into side-by-side
// columns once the viewport is at least settingsTwoCol wide, dropping the
// left headings' gutter so the columns use the full content width.
func widePage(title, description string, sections ...ggui.Widget) ggui.Widget {
	return &settingsColumn{head: pageTitle(title, description), body: widget.Column(sections...).Gap(pageGap).Stretch(), twoCol: true}
}

func pageTitle(title, description string) ggui.Widget {
	return widget.Column(
		widget.Text(title).Size(20).Weight(ggui.WeightSemibold),
		// As wide as a line reads well, however wide the page.
		widget.Box(muted(widget.Text(description).Size(13))).MaxWidth(840),
	).Gap(6).Stretch()
}

// twoColumns places items in a single column below settingsTwoCol, and in
// two side-by-side columns at or above it. left and right name the indices
// of items in each wide column so every child widget is created once and
// keeps its state across window resizes.
func twoColumns(items []ggui.Widget, left, right []int) ggui.Widget {
	return &twoColLayout{
		breakpoint: settingsTwoCol,
		colGap:     settingsColumnGap,
		rowGap:     pageGap,
		items:      items,
		left:       left,
		right:      right,
	}
}

type twoColLayout struct {
	breakpoint  float64
	colGap      float64
	rowGap      float64
	items       []ggui.Widget
	left, right []int

	wide  bool
	rects []ggui.Rect
}

func (l *twoColLayout) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w := c.MaxW
	if len(l.rects) != len(l.items) {
		l.rects = make([]ggui.Rect, len(l.items))
	}
	l.wide = w >= l.breakpoint
	if !l.wide {
		var y float64
		childC := ggui.Constraints{MinW: w, MaxW: w, MaxH: ggui.Unbounded}
		for i, item := range l.items {
			if i > 0 {
				y += l.rowGap
			}
			sz := item.Layout(childC, env)
			l.rects[i] = ggui.Rct(ggui.Pt(0, y), ggui.Sz(w, sz.H))
			y += sz.H
		}
		return c.Constrain(ggui.Sz(w, y))
	}
	colW := max((w-l.colGap)/2, 0)
	colEnv := env.With(settingsGutterKey, float64(0))
	childC := ggui.Constraints{MinW: colW, MaxW: colW, MaxH: ggui.Unbounded}
	var leftY, rightY float64
	for k, idx := range l.left {
		if k > 0 {
			leftY += l.rowGap
		}
		sz := l.items[idx].Layout(childC, colEnv)
		l.rects[idx] = ggui.Rct(ggui.Pt(0, leftY), ggui.Sz(colW, sz.H))
		leftY += sz.H
	}
	for k, idx := range l.right {
		if k > 0 {
			rightY += l.rowGap
		}
		sz := l.items[idx].Layout(childC, colEnv)
		l.rects[idx] = ggui.Rct(ggui.Pt(colW+l.colGap, rightY), ggui.Sz(colW, sz.H))
		rightY += sz.H
	}
	return c.Constrain(ggui.Sz(w, max(leftY, rightY)))
}

func (l *twoColLayout) Paint(dst *ggui.Canvas, r ggui.Rect) {
	ox, oy := r.Origin.X, r.Origin.Y
	for i, item := range l.items {
		cr := l.rects[i]
		dst.Paint(item, ggui.Rct(ggui.Pt(ox+cr.Origin.X, oy+cr.Origin.Y), cr.Size))
	}
}

// responsiveGrid lays items in one column below breakpoint and in cols
// equal-width columns at or above it.
func responsiveGrid(breakpoint float64, cols int, colGap, rowGap float64, items ...ggui.Widget) ggui.Widget {
	return responsiveColumns(func(w float64) int {
		if w >= breakpoint {
			return cols
		}
		return 1
	}, colGap, rowGap, items...)
}

// responsiveColumns lays items in a number of equal-width columns chosen
// by colsFor from the available width, keeping each child widget alive
// across resizes.
func responsiveColumns(colsFor func(w float64) int, colGap, rowGap float64, items ...ggui.Widget) ggui.Widget {
	return &responsiveGridLayout{
		colsFor: colsFor,
		colGap:  colGap,
		rowGap:  rowGap,
		items:   items,
	}
}

type responsiveGridLayout struct {
	colsFor func(w float64) int
	colGap  float64
	rowGap  float64
	items   []ggui.Widget

	rects []ggui.Rect
}

func (g *responsiveGridLayout) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w := c.MaxW
	if len(g.rects) != len(g.items) {
		g.rects = make([]ggui.Rect, len(g.items))
	}
	cols := max(g.colsFor(w), 1)
	if cols == 1 {
		var y float64
		childC := ggui.Constraints{MinW: w, MaxW: w, MaxH: ggui.Unbounded}
		for i, item := range g.items {
			if i > 0 {
				y += g.rowGap
			}
			sz := item.Layout(childC, env)
			g.rects[i] = ggui.Rct(ggui.Pt(0, y), ggui.Sz(w, sz.H))
			y += sz.H
		}
		return c.Constrain(ggui.Sz(w, y))
	}
	colW := max((w-g.colGap*float64(cols-1))/float64(cols), 0)
	colEnv := env.With(settingsGutterKey, float64(0))
	childC := ggui.Constraints{MinW: colW, MaxW: colW, MaxH: ggui.Unbounded}
	var y float64
	for rowStart := 0; rowStart < len(g.items); rowStart += cols {
		if rowStart > 0 {
			y += g.rowGap
		}
		var rowH float64
		rowEnd := min(rowStart+cols, len(g.items))
		for i := rowStart; i < rowEnd; i++ {
			col := i - rowStart
			sz := g.items[i].Layout(childC, colEnv)
			g.rects[i] = ggui.Rct(ggui.Pt(float64(col)*(colW+g.colGap), y), ggui.Sz(colW, sz.H))
			rowH = max(rowH, sz.H)
		}
		y += rowH
	}
	return c.Constrain(ggui.Sz(w, y))
}

func (g *responsiveGridLayout) Paint(dst *ggui.Canvas, r ggui.Rect) {
	ox, oy := r.Origin.X, r.Origin.Y
	for i, item := range g.items {
		cr := g.rects[i]
		dst.Paint(item, ggui.Rct(ggui.Pt(ox+cr.Origin.X, oy+cr.Origin.Y), cr.Size))
	}
}

// listPage is page for a page that lists things, as Agents, Templates and
// Extensions do: it keeps no headings' column, so a wide one has the room
// to lay its list beside what is chosen in it (listBeside).
func listPage(title, description string, sections ...ggui.Widget) ggui.Widget {
	return &settingsColumn{head: pageTitle(title, description), body: widget.Column(sections...).Gap(pageGap).Stretch(), noGutter: true}
}

// listBesideMinWidth and listBesideMaxWidth bound the width of a
// listBeside's list as the page widens.
const (
	listBesideMinWidth = 320
	listBesideMaxWidth = 440
)

// listBeside lays list beside detail, what is chosen in it, where there is
// the room, and narrow, the list with what is chosen unfolded in it,
// where there is not.
func listBeside(list, detail, narrow ggui.Widget) ggui.Widget {
	return &listBesideLayout{list: list, detail: detail, narrow: narrow}
}

type listBesideLayout struct {
	list, detail, narrow ggui.Widget

	wide           bool
	listW, detailW float64
	listH, detailH float64
	gap            float64
	narrowH        float64
}

func (l *listBesideLayout) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w := c.MaxW
	l.wide = w >= settingsWide
	if !l.wide {
		l.narrowH = l.narrow.Layout(ggui.Constraints{MinW: w, MaxW: w, MaxH: ggui.Unbounded}, env).H
		return c.Constrain(ggui.Sz(w, l.narrowH))
	}
	l.gap = pick[float64](w >= settingsTwoCol, settingsColumnGap, 20)
	l.listW = min(max(w*0.30, listBesideMinWidth), listBesideMaxWidth)
	l.detailW = max(w-l.listW-l.gap, 0)
	l.listH = l.list.Layout(ggui.Constraints{MinW: l.listW, MaxW: l.listW, MaxH: ggui.Unbounded}, env).H
	l.detailH = l.detail.Layout(ggui.Constraints{MinW: l.detailW, MaxW: l.detailW, MaxH: ggui.Unbounded}, env).H
	return c.Constrain(ggui.Sz(w, max(l.listH, l.detailH)))
}

func (l *listBesideLayout) Paint(dst *ggui.Canvas, r ggui.Rect) {
	x, y, w := r.Origin.X, r.Origin.Y, r.Size.W
	if !l.wide {
		dst.Paint(l.narrow, ggui.Rct(ggui.Pt(x, y), ggui.Sz(w, l.narrowH)))
		return
	}
	dst.Paint(l.list, ggui.Rct(ggui.Pt(x, y), ggui.Sz(l.listW, l.listH)))
	dst.Paint(l.detail, ggui.Rct(ggui.Pt(x+l.listW+l.gap, y), ggui.Sz(l.detailW, l.detailH)))
}

// listItem is a row of a listBeside's list: what it shows, marked while
// it is the one chosen, which a tap chooses.
func listItem(content ggui.Widget, chosen ggui.Readable[bool], choose func(), name string) ggui.Widget {
	button := ui.ButtonOf(content, choose).Ghost().Pad(8, 10).Radius(7).Name(name)
	return ggui.View(chosen, func(on bool) ggui.Widget {
		if on {
			return widget.Box(button).Fill(skin.SidebarSel).Radius(7)
		}
		return button
	})
}

// detailCard is the card listBeside shows what is chosen in: its
// heading over its fields.
func detailCard(head, body ggui.Widget) ggui.Widget {
	return widget.Box(widget.Column(head, hline(), body).Gap(14).Stretch()).Pad(18, 22).
		Fill(skin.Surface1).Border(1, skin.Border).Radius(10)
}

// addField is how a list's page adds to it: a name typed and Add, and
// what went wrong under them. Compact, for listBeside's list, it
// puts its title over the field rather than beside it.
func addField(title, hint, placeholder, fieldName string, name, note *ggui.StateValue[string], add func(), compact bool) ggui.Widget {
	field := ui.TextField(name).Placeholder(placeholder).Name(fieldName).OnSubmit(func(string) { add() })
	button := ui.Button(T("Add"), add).Outline()
	hasNote := ggui.Map(note, func(s string) bool { return s != "" })
	noteText := ggui.If(hasNote, func() ggui.Widget { return widget.TextOf(note).Size(12).Color(skin.DotDown) })
	if compact {
		return widget.Box(widget.Column(
			ui.Tooltip(widget.Text(title).Size(13), hint),
			widget.Row(widget.Expanded(field), button).Gap(8),
			noteText,
		).Gap(8).Stretch()).Pad(12, 14)
	}
	return widget.Column(
		row(title, hint, widget.Row(widget.Box(field).Width(160), button).Gap(8)),
		ggui.If(hasNote, func() ggui.Widget { return widget.Box(noteText).Pad(0, 16, 12, 16) }),
	).Stretch()
}
