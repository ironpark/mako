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
	cleaned, labels, embeds, err := clean(data)
	if err != nil {
		return Picture{}, false
	}
	px, x, y, w, h, ok := raster(cleaned, func(float64) float64 { return Scale })
	if !ok {
		return Picture{}, false
	}
	for _, e := range embeds {
		drawEmbed(px, e, x, y)
	}
	return Picture{Pixels: px, Size: ggui.Sz(w, h), Labels: labels, Origin: ggui.Pt(x, y)}, true
}

// raster draws a cleaned SVG from its view box's corner at the scale k
// gives for the box's width, with the box x, y, w, h, in the SVG's own
// units; false for an SVG that does not parse, or whose box is empty or
// too large.
func raster(cleaned []byte, k func(w float64) float64) (px *image.RGBA, x, y, w, h float64, ok bool) {
	doc, err := ggsvg.Parse(bytes.NewReader(cleaned))
	if err != nil {
		return nil, 0, 0, 0, 0, false
	}
	x, y, w, h = doc.ViewBox()
	if w <= 0 || h <= 0 || math.IsNaN(w+h) || w*h > 4096*4096 {
		return nil, 0, 0, 0, 0, false
	}
	s := k(w)
	px = image.NewRGBA(image.Rect(0, 0, int(math.Ceil(w*s)), int(math.Ceil(h*s))))
	var g ggfx.GeoM
	g.Translate(-x, -y)
	g.Scale(s, s)
	doc.Draw(px, g)
	return px, x, y, w, h, true
}

// drawEmbed draws an image an SVG holds into px, the SVG drawn from its
// view box's corner x, y at Scale. An SVG it holds is drawn as an SVG
// is, but without images of its own.
func drawEmbed(px *image.RGBA, e svgEmbed, x, y float64) {
	var src image.Image
	if e.svg {
		cleaned, _, _, err := clean(e.data)
		if err != nil {
			return
		}
		// Drawn at the size it takes on the page, so it is not scaled twice.
		img, _, _, _, _, ok := raster(cleaned, func(w float64) float64 { return e.w * e.m.scale() * Scale / w })
		if !ok {
			return
		}
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
