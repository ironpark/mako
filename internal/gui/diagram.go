package gui

import (
	"errors"
	"fmt"

	"github.com/ironpark/ggui"
	mermaid "github.com/zkrebbekx/go-mermaid"

	"github.com/ironpark/mako/internal/gui/mdview"
)

// Diagrams a page's code blocks draw: a mermaid block is drawn as the
// diagram it describes, in the theme's light or dark, on the page's own
// ground. Each is drawn once for the window and kept with its images.

// Diagram is the picture a code block in a diagram's language draws.
func (h pageHost) Diagram(src, lang string) *ggui.StateValue[mdview.Image] {
	if lang != "mermaid" {
		return nil
	}
	theme := mermaid.Default
	if h.m.themePalette().Dark {
		theme = mermaid.Dark
	}
	m := h.m
	return m.cachedImage("mermaid\x00"+string(theme)+"\x00"+src, func(done func(decodedImage, error)) {
		go func() {
			d, err := drawMermaid(src, theme)
			m.post(func() { done(d, err) })
		}()
	})
}

// drawMermaid is the mermaid diagram src describes, drawn as an SVG is,
// off the window's goroutine.
func drawMermaid(src string, theme mermaid.Theme) (d decodedImage, err error) {
	// Off the window's goroutine, a panic on a diagram it cannot read
	// would take the window down with it.
	defer func() {
		if p := recover(); p != nil {
			d, err = decodedImage{}, fmt.Errorf("mermaid: %v", p)
		}
	}()
	svg, err := mermaid.Render(src, mermaid.WithTheme(theme), mermaid.WithTransparentBackground())
	if err != nil {
		return decodedImage{}, err
	}
	d = drawnSVG(nil, svg)
	if d.Drawn == nil {
		return d, errors.New("mermaid: the diagram could not be drawn")
	}
	return shrinkImage(d, ggui.Sz(pageImageMax, pageImageMax)), nil
}
