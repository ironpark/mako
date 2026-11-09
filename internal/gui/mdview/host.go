package mdview

import (
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
	// Diagram is the picture a code block in a diagram's language, as
	// mermaid, draws, read once and kept; nil for a language the host
	// draws nothing of, which the page shows as code. One that cannot be
	// drawn comes without an image, and shows as code too.
	Diagram(src, lang string) *ggui.StateValue[Image]
	// Fold is whether the page's nth <details> is open, kept so it stays as
	// the reader left it; open is its first state.
	Fold(n int, open bool) *ggui.StateValue[bool]
}

// Base is a host for a page with nowhere to go: links lead nowhere, no
// pictures are read, code is plain and folds are forgotten.
type Base struct{}

func (Base) Open(string)                          {}
func (Base) Image(string) *ggui.StateValue[Image] { return nil }
func (Base) Diagram(_, _ string) *ggui.StateValue[Image] {
	return nil
}
func (Base) Fold(_ int, open bool) *ggui.StateValue[bool] {
	return ggui.State(open)
}
func (Base) Code(src, _ string) ggui.Widget {
	return ggui.RichText(ggui.Span(strings.TrimSuffix(src, "\n"))).NoWrap().Selectable().Font(ggui.DefaultMonoFont())
}
