package gui

import (
	"image/color"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/gui/skin"
)

// What the right panel's lists share: a state said as a word on a wash of
// its colour, a name of the machine's kind, a hash or a branch, set apart
// as a chip, rows that light up rounded, inset from the panel's edges, and
// a glyph over the words where a list has nothing in it.

// pill is a state in a word: label in its colour, on a faint wash of it.
func pill(label string, c color.Color) ggui.Widget {
	return &washed{child: ggui.Box(ggui.Text(label).Size(10.5).Weight(ggui.WeightMedium).Color(c).NoWrap()).Pad(2, 7), color: c}
}

// washed fills behind its child, rounded to a pill, with its colour faint.
type washed struct {
	child ggui.Widget
	color color.Color
	tint  color.Color // the wash, under the theme it was laid out in
}

func (w *washed) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	// A colour the theme in force does not have washes nothing.
	w.tint = color.Transparent
	if col := ggui.ResolveColor(w.color, env); col != nil {
		r, g, b, _ := col.RGBA()
		w.tint = color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0x2B}
	}
	return w.child.Layout(c, env)
}

func (w *washed) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.FillRoundRect(r, r.Size.H/2, w.tint)
	dst.Paint(w.child, r)
}

// chip sets apart a name of the machine's kind: a commit's hash, a branch,
// an item's number.
func chip(text string) ggui.Widget {
	return ggui.Box(ggui.Text(text).Size(10.5).Font(ggui.DefaultMonoFont()).Color(skin.Muted).NoWrap().Ellipsis()).
		Pad(1, 5).Radius(4).Fill(skin.Surface2).MaxWidth(140)
}

// listRow is a row of a panel's list that does something when tapped: lit
// rounded under the pointer, inset from the panel's edges.
func listRow(content ggui.Widget, name string, onTap func()) ggui.Widget {
	return ggui.Box(ui.ButtonOf(content, onTap).Ghost().Pad(6, 8).Radius(8).Name(name)).Pad(1, 6)
}

// emptyNote says a list has nothing in it: a quiet glyph, if one is named,
// over the words, in the middle of where the list would be.
func emptyNote(glyph, text string) ggui.Widget {
	rows := []ggui.Widget{muted(ggui.Text(text).Size(12.5))}
	if glyph != "" {
		rows = append([]ggui.Widget{icon(glyph).Size(22).Color(skin.ExtraMuted)}, rows...)
	}
	return ggui.Center(ggui.Column(rows...).Gap(8).Align(ggui.AlignCenter))
}
