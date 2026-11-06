package svgdraw

import (
	"bytes"
	"image"
	"math"

	"github.com/ironpark/ggfx"
	ggsvg "github.com/ironpark/ggfx/svg"
	"github.com/ironpark/ggui"
	xdraw "golang.org/x/image/draw"
)

// Scale is how many pixels an SVG is drawn with for each of its own, to
// stay sharp on a dense screen.
const Scale = 2

// Picture is an SVG drawn: its pixels, at Scale; the size of its view
// box, which they are Scale times of; and its text, to be set over them
// from the view box's corner, Origin.
type Picture struct {
	Pixels *image.RGBA
	Size   ggui.Size
	Labels []Text
	Origin ggui.Point
}

// Draw draws an SVG at Scale, sized as its view box, reporting false for
// one that cannot be drawn.
func Draw(data []byte) (Picture, bool) {
	clean, labels, embeds, err := clean(data)
	if err != nil {
		return Picture{}, false
	}
	doc, err := ggsvg.Parse(bytes.NewReader(clean))
	if err != nil {
		return Picture{}, false
	}
	x, y, w, h := doc.ViewBox()
	if w <= 0 || h <= 0 || math.IsNaN(w+h) || w*h > 4096*4096 {
		return Picture{}, false
	}
	px := image.NewRGBA(image.Rect(0, 0, int(math.Ceil(w*Scale)), int(math.Ceil(h*Scale))))
	var g ggfx.GeoM
	g.Translate(-x, -y)
	g.Scale(Scale, Scale)
	doc.Draw(px, g)
	for _, e := range embeds {
		drawEmbed(px, e, x, y)
	}
	return Picture{Pixels: px, Size: ggui.Sz(w, h), Labels: labels, Origin: ggui.Pt(x, y)}, true
}

// drawEmbed draws an image an SVG holds into px, the SVG drawn from its
// view box's corner x, y at Scale. An SVG it holds is drawn as an SVG
// is, but without images of its own.
func drawEmbed(px *image.RGBA, e svgEmbed, x, y float64) {
	var src image.Image
	if e.svg {
		clean, _, _, err := clean(e.data)
		if err != nil {
			return
		}
		doc, err := ggsvg.Parse(bytes.NewReader(clean))
		if err != nil {
			return
		}
		vx, vy, vw, vh := doc.ViewBox()
		if vw <= 0 || vh <= 0 || vw*vh > 4096*4096 {
			return
		}
		// Drawn at the size it takes on the page, so it is not scaled twice.
		k := e.w * e.m.scale() * Scale / vw
		img := image.NewRGBA(image.Rect(0, 0, int(math.Ceil(vw*k)), int(math.Ceil(vh*k))))
		var g ggfx.GeoM
		g.Translate(-vx, -vy)
		g.Scale(k, k)
		doc.Draw(img, g)
		src = img
	} else {
		img, _, err := image.Decode(bytes.NewReader(e.data))
		if err != nil {
			return
		}
		src = img
	}
	x0, y0 := e.m.apply(e.x, e.y)
	x1, y1 := e.m.apply(e.x+e.w, e.y+e.h)
	r := image.Rect(int(math.Round((min(x0, x1)-x)*Scale)), int(math.Round((min(y0, y1)-y)*Scale)),
		int(math.Round((max(x0, x1)-x)*Scale)), int(math.Round((max(y0, y1)-y)*Scale)))
	xdraw.CatmullRom.Scale(px, r, src, src.Bounds(), xdraw.Over, nil)
}
