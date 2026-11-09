package gui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/zkrebbekx/dot2svg"
	mermaid "github.com/zkrebbekx/go-mermaid"

	"github.com/ironpark/mako/internal/gui/mdview"
)

// Diagrams a page's code blocks draw: a mermaid block, or a Graphviz one
// (dot, graphviz), is drawn as the diagram it describes, in the theme's
// light or dark, on the page's own ground. Each is drawn once for the
// window and kept with its images.

// diagramLangs are the languages drawn, by a code block's info string, to
// how each is drawn as SVG.
var diagramLangs = map[string]func(src string, opts ...mermaid.Option) ([]byte, error){
	"mermaid":  mermaid.Render,
	"dot":      func(src string, opts ...mermaid.Option) ([]byte, error) { return dot2svg.Render([]byte(src), opts...) },
	"graphviz": func(src string, opts ...mermaid.Option) ([]byte, error) { return dot2svg.Render([]byte(src), opts...) },
}

// Diagram is the picture a code block in a diagram's language draws.
func (h pageHost) Diagram(src, lang string) *ggui.StateValue[mdview.Image] {
	render := diagramLangs[strings.ToLower(lang)]
	if render == nil {
		return nil
	}
	theme := mermaid.Default
	if h.m.themePalette().Dark {
		theme = mermaid.Dark
	}
	m := h.m
	return m.cachedImage("diagram\x00"+lang+"\x00"+string(theme)+"\x00"+src, func(done func(decodedImage, error)) {
		go func() {
			d, err := drawDiagram(render, src, theme)
			m.post(func() { done(d, err) })
		}()
	})
}

// drawDiagram is the diagram src describes, made SVG by render and drawn
// as an SVG is, off the window's goroutine.
func drawDiagram(render func(string, ...mermaid.Option) ([]byte, error), src string, theme mermaid.Theme) (d decodedImage, err error) {
	// Off the window's goroutine, a panic on a diagram it cannot read
	// would take the window down with it.
	defer func() {
		if p := recover(); p != nil {
			d, err = decodedImage{}, fmt.Errorf("the diagram could not be read: %v", p)
		}
	}()
	svg, err := render(src, mermaid.WithTheme(theme), mermaid.WithTransparentBackground())
	if err != nil {
		return decodedImage{}, err
	}
	d = drawnSVG(nil, svg)
	if d.Drawn == nil {
		return d, errors.New("the diagram could not be drawn")
	}
	return shrinkImage(d, ggui.Sz(pageImageMax, pageImageMax)), nil
}
