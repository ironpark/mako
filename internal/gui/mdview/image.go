package mdview

import (
	"cmp"
	"image"
	"path"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/gui/svgdraw"
)

// Images in Markdown: those beside the file, read from its node, and those
// on the web, read from this machine, as a browser would; an SVG, as a
// logo often is, drawn from its paths (not its text, which badges are
// mostly made of: those show as their links). Each is drawn at its own
// size, or the one its tag asks for, and no wider than the page.

// image shows the picture p: read once for the file, or for the window
// from the web, a spinner while it comes, its link where it cannot be
// read.
func (r *mdRenderer) image(p mdPicture) ggui.Widget {
	label := cmp.Or(p.alt, path.Base(p.dest))
	fallback := func() ggui.Widget {
		return ggui.RichText(r.linked(mdStyle{r: r}, p.dest).span("🖼 " + label)).Size(mdBodySize)
	}
	img := r.host.Image(p.dest)
	if img == nil {
		return fallback()
	}
	return ggui.View(img, func(v Image) ggui.Widget {
		switch {
		case v.Loading:
			return ggui.Box(ui.Spinner()).Pad(8)
		case v.Img == nil:
			return fallback()
		}
		f := &fitImage{img: v.Img, size: v.Size, want: ggui.Sz(p.width, p.height), alt: label}
		if len(v.Labels) > 0 {
			f.labels = &svgdraw.Labels{Texts: v.Labels, Origin: v.Origin}
		}
		return f
	})
}

// ImageOf is a read file as a page's picture: img as it decoded, or else
// the SVG svg drawn; none for neither.
func ImageOf(img image.Image, svg []byte) Image {
	if img != nil {
		b := img.Bounds()
		return Image{Img: ggfx.NewImageFromImage(img), Size: ggui.Sz(float64(b.Dx()), float64(b.Dy()))}
	}
	if svg != nil {
		return svgImage(svg)
	}
	return Image{}
}

// svgImage is an SVG drawn at svgdraw.Scale, sized as its view box;
// nothing for one that cannot be drawn.
func svgImage(data []byte) Image {
	p, ok := svgdraw.Draw(data)
	if !ok {
		return Image{}
	}
	return Image{Img: ggfx.NewImageFromImage(p.Pixels), Size: p.Size, Labels: p.Labels, Origin: p.Origin}
}

// fitImage draws an image at its own size, or the one asked for, the
// other side following its shape, and no wider than there is room for.
type fitImage struct {
	img        *ggfx.Image
	size, want ggui.Size
	alt        string
	child      ggui.Widget
	labels     *svgdraw.Labels // an SVG's text, set over it
}

func (f *fitImage) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w, h := f.size.W, f.size.H
	switch {
	case f.want.W > 0 && f.want.H > 0:
		w, h = f.want.W, f.want.H
	case f.want.W > 0:
		w, h = f.want.W, h*f.want.W/max(w, 1)
	case f.want.H > 0:
		w, h = w*f.want.H/max(h, 1), f.want.H
	}
	if c.MaxW > 0 && w > c.MaxW {
		w, h = c.MaxW, h*c.MaxW/w
	}
	f.child = ggui.Image(f.img).Alt(f.alt).Size(w, h)
	if f.labels != nil {
		f.labels.Scale = w / max(f.size.W, 1)
		f.labels.Layout(env)
	}
	return f.child.Layout(ggui.Loose(ggui.Sz(w, h)), env)
}

func (f *fitImage) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(f.child, r)
	if f.labels != nil {
		f.labels.Paint(dst, r)
	}
}
