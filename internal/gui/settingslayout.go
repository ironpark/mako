package gui

import (
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/gui/skin"
)

// A settings page uses a wide window's room: past settingsWide its
// sections' headings hang in a column of their own at the left, beside
// the cards, and the content runs as wide as settingsMaxWidth, rather than
// a column as narrow as a small window's in the middle of a wide one.
// Below it, a heading sits over its cards as before.

const (
	// settingsMaxWidth is the widest a page's content runs.
	settingsMaxWidth = 1120
	// settingsWide is the content width from which headings hang beside
	// their cards.
	settingsWide = 860
	// settingsGutter is the width of the headings' column, its gap to the
	// cards included.
	settingsGutter = 200
	// settingsGutterGap is the room kept between a heading and the cards.
	settingsGutterGap = 28
)

// settingsGutterKey is the headings' column a page's sections hang their
// headings in, to the left of where they are laid out; 0 when there is
// none, and a heading sits over its cards.
var settingsGutterKey = ggui.NewEnvKey[float64]("settings gutter")

// settingsColumn is a page: its title over its sections, which it lays
// out right of the headings' column once it is wide enough for one.
type settingsColumn struct {
	head, body ggui.Widget
	noGutter   bool // a listPage's

	gutter, headH, bodyH float64
}

// pageGap is the room between a page's title and between its sections.
const pageGap = 28

func (s *settingsColumn) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w := c.MaxW
	s.gutter = pick[float64](w >= settingsWide && !s.noGutter, settingsGutter, 0)
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
		over:   ggui.Box(muted(ggui.Text(title).Size(12.5))).Pad(0, 4),
		beside: ggui.Text(title).Size(13).Weight(ggui.WeightMedium).Color(skin.Muted),
		body:   ggui.Column(children...).Gap(sectionGap).Stretch(),
	}
}

// page is a settings page: its title and what it is for, over its
// sections.
func page(title, description string, sections ...ggui.Widget) ggui.Widget {
	return &settingsColumn{head: pageTitle(title, description), body: ggui.Column(sections...).Gap(pageGap).Stretch()}
}

func pageTitle(title, description string) ggui.Widget {
	return ggui.Column(
		ggui.Text(title).Size(20).Weight(ggui.WeightSemibold),
		// As wide as a line reads well, however wide the page.
		ggui.Box(muted(ggui.Text(description).Size(13))).MaxWidth(760),
	).Gap(6).Stretch()
}

// listPage is page for a page that lists things, as Agents, Templates and
// Extensions do: it keeps no headings' column, so a wide one has the room
// to lay its list beside what is chosen in it (listBeside).
func listPage(title, description string, sections ...ggui.Widget) ggui.Widget {
	return &settingsColumn{head: pageTitle(title, description), body: ggui.Column(sections...).Gap(pageGap).Stretch(), noGutter: true}
}

// listBesideWidth is the width of a listBeside's list.
const listBesideWidth = 320

// listBeside lays list beside detail, what is chosen in it, where there is
// the room, and narrow, the list with what is chosen unfolded in it,
// where there is not.
func listBeside(list, detail, narrow ggui.Widget) ggui.Widget {
	return ggui.Responsive(settingsWide,
		ggui.Row(ggui.Box(list).Width(listBesideWidth), ggui.Expanded(detail)).Gap(20).Align(ggui.AlignStart),
		narrow)
}

// listItem is a row of a listBeside's list: what it shows, marked while
// it is the one chosen, which a tap chooses.
func listItem(content ggui.Widget, chosen ggui.Readable[bool], choose func(), name string) ggui.Widget {
	button := ui.ButtonOf(content, choose).Ghost().Pad(8, 10).Radius(7).Name(name)
	return ggui.View(chosen, func(on bool) ggui.Widget {
		if on {
			return ggui.Box(button).Fill(skin.SidebarSel).Radius(7)
		}
		return button
	})
}

// detailCard is the card listBeside shows what is chosen in: its
// heading over its fields.
func detailCard(head, body ggui.Widget) ggui.Widget {
	return ggui.Box(ggui.Column(head, hline(), body).Gap(14).Stretch()).Pad(16, 18).
		Fill(skin.Surface1).Border(1, skin.Border).Radius(10)
}

// addField is how a list's page adds to it: a name typed and Add, and
// what went wrong under them. Compact, for listBeside's list, it
// puts its title over the field rather than beside it.
func addField(title, hint, placeholder, fieldName string, name, note *ggui.StateValue[string], add func(), compact bool) ggui.Widget {
	field := ui.TextField(name).Placeholder(placeholder).Name(fieldName).OnSubmit(func(string) { add() })
	button := ui.Button(T("Add"), add).Outline()
	hasNote := ggui.Map(note, func(s string) bool { return s != "" })
	noteText := ggui.If(hasNote, func() ggui.Widget { return ggui.TextOf(note).Size(12).Color(skin.DotDown) })
	if compact {
		return ggui.Box(ggui.Column(
			ui.Tooltip(ggui.Text(title).Size(13), hint),
			ggui.Row(ggui.Expanded(field), button).Gap(8),
			noteText,
		).Gap(8).Stretch()).Pad(12, 14)
	}
	return ggui.Column(
		row(title, hint, ggui.Row(ggui.Box(field).Width(160), button).Gap(8)),
		ggui.If(hasNote, func() ggui.Widget { return ggui.Box(noteText).Pad(0, 16, 12, 16) }),
	).Stretch()
}
