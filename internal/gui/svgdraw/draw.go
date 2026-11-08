package svgdraw

import (
	"bytes"
	"errors"
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

// maxPixels bounds the pixels an SVG, or an image it holds, is drawn
// with: 64 MB as RGBA.
const maxPixels = 4096 * 4096

// Draw draws an SVG at Scale, sized as its view box, reporting false for
// one that cannot be drawn.
func Draw(data []byte) (pic Picture, ok bool) {
	// An SVG is drawn off the window's goroutine, where a panic in a
	// parser or a rasterizer would take the window down with it.
	defer func() {
		if recover() != nil {
			pic, ok = Picture{}, false
		}
	}()
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
// too large. One that would take more than maxPixels at that scale is
// drawn at a smaller one, as it is shown at its box's size either way.
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
	if !(s > 0) || math.IsInf(s, 0) {
		return nil, 0, 0, 0, 0, false
	}
	if w*s*h*s > maxPixels {
		s = math.Sqrt(maxPixels / (w * h))
	}
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
		img, err := DecodeImage(e.data, maxPixels)
		if err != nil {
			return
		}
		src = img
	}
	x0, y0 := e.m.apply(e.x, e.y)
	x1, y1 := e.m.apply(e.x+e.w, e.y+e.h)
	// Placed where its corners land in px, which a transform can throw
	// beyond what a pixel coordinate holds.
	for _, v := range [...]float64{(x0 - x) * Scale, (y0 - y) * Scale, (x1 - x) * Scale, (y1 - y) * Scale} {
		if math.IsNaN(v) || math.Abs(v) > 1<<24 {
			return
		}
	}
	r := image.Rect(int(math.Round((min(x0, x1)-x)*Scale)), int(math.Round((min(y0, y1)-y)*Scale)),
		int(math.Round((max(x0, x1)-x)*Scale)), int(math.Round((max(y0, y1)-y)*Scale)))
	xdraw.CatmullRom.Scale(px, r, src, src.Bounds(), xdraw.Over, nil)
}

// ImagePixels bounds the pixels of a picture read to be shown: an 8K
// screen's, 128 MB as RGBA.
const ImagePixels = 1 << 25

// DecodeImage decodes a picture, refusing one whose header says it is
// larger than max pixels before its pixels are allocated: a PNG of a few
// KB can say it is 20000 by 20000, and be decoded into 1.6 GB.
func DecodeImage(data []byte, max int) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > max/cfg.Height {
		return nil, errors.New("image too large")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}
