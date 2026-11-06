package mdview

import (
	"image/color"
	"strings"

	"github.com/ironpark/ggui"
)

// Host is what a Markdown page needs of the window around it: where
// its links lead, where its pictures come from, how its code is shown,
// and what it keeps between drawings. Embed Base to take its defaults.
type Host interface {
	// Open follows a link the reader tapped: a web address, or a path
	// relative to the page.
	Open(dest string)
	// Image is the picture dest names, read once and kept; nil when the
	// host cannot read it, which the page shows as its link.
	Image(dest string) *ggui.StateValue[Image]
	// Code is a code block's text, highlighted in lang ("" for none),
	// selectable and not wrapping. The page puts it in its box.
	Code(src, lang string) ggui.Widget
	// Fold is whether the page's nth <details> is open, kept so it stays as
	// the reader left it; open is its first state.
	Fold(n int, open bool) *ggui.StateValue[bool]
	// Text is s in the window's language.
	Text(s string) string
	// Icon is the window's icon named name, at size, in c.
	Icon(name string, size float64, c color.Color) ggui.Widget
}

// Base is a host for a page with nowhere to go: links lead nowhere, no
// pictures are read, code is plain, folds are forgotten and text is as
// written.
type Base struct{}

func (Base) Open(string)                          {}
func (Base) Image(string) *ggui.StateValue[Image] { return nil }
func (Base) Fold(_ int, open bool) *ggui.StateValue[bool] {
	return ggui.State(open)
}
func (Base) Text(s string) string { return s }
func (Base) Icon(string, float64, color.Color) ggui.Widget {
	return ggui.Box()
}
func (Base) Code(src, _ string) ggui.Widget {
	return ggui.RichText(ggui.Span(strings.TrimSuffix(src, "\n"))).NoWrap().Selectable().Font(ggui.DefaultMonoFont())
}

// Strings are the words a page shows of its own, which a Host's Text is
// asked for: the window's catalogs need them.
var Strings = []string{"Details", "Note", "Tip", "Important", "Warning", "Caution"}
