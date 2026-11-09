package gui

import (
	"crypto/sha256"
	"encoding/hex"
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

// renderDiagram makes a diagram's source SVG, in a theme, on no ground of
// its own.
type renderDiagram func(src string, theme mermaid.Theme) ([]byte, error)

func renderMermaid(src string, theme mermaid.Theme) ([]byte, error) {
	return mermaid.Render(src, mermaid.WithTheme(theme), mermaid.WithTransparentBackground())
}

func renderDot(src string, theme mermaid.Theme) ([]byte, error) {
	return dot2svg.Render([]byte(src), mermaid.WithTheme(theme), mermaid.WithTransparentBackground())
}

// diagramLangs are the languages drawn, by a code block's info string, to
// how each is made SVG.
var diagramLangs = map[string]renderDiagram{"mermaid": renderMermaid, "dot": renderDot, "graphviz": renderDot}

// Diagram is the picture a code block in a diagram's language draws.
func (h pageHost) Diagram(src, lang string) *ggui.StateValue[mdview.Image] {
	render := diagramLangs[strings.ToLower(lang)]
	if render == nil {
		return nil
	}
	theme := mermaid.Default
	if h.m.dark.Get() {
		theme = mermaid.Dark
	}
	m := h.m
	// Kept by a hash of its source, not the source: a page rebuilt builds
	// the key again for each of its diagrams.
	sum := sha256.Sum256([]byte(src))
	key := "diagram\x00" + lang + "\x00" + string(theme) + "\x00" + hex.EncodeToString(sum[:16])
	return m.cachedImage(key, func(done func(decodedImage, error)) {
		go func() {
			d, err := drawDiagram(render, src, theme)
			m.post(func() { done(d, err) })
		}()
	})
}

// drawDiagram is the diagram src describes, made SVG by render and drawn
// as an SVG is, off the window's goroutine.
func drawDiagram(render renderDiagram, src string, theme mermaid.Theme) (d decodedImage, err error) {
	// Off the window's goroutine, a panic on a diagram it cannot read
	// would take the window down with it.
	defer func() {
		if p := recover(); p != nil {
			d, err = decodedImage{}, fmt.Errorf("the diagram could not be read: %v", p)
		}
	}()
	svg, err := render(src, theme)
	if err != nil {
		return decodedImage{}, err
	}
	d = drawnSVG(nil, svg)
	if d.Drawn == nil {
		return d, errors.New("the diagram could not be drawn")
	}
	return shrinkImage(d, ggui.Sz(pageImageMax, pageImageMax)), nil
}
