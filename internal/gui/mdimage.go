package gui

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"image"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

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

// webImageLimit bounds an image read from the web.
const webImageLimit = 20 << 20

// image shows the picture p: read once for the file, or for the window
// from the web, a spinner while it comes, its link where it cannot be
// read.
func (r *mdRenderer) image(p mdPicture) ggui.Widget {
	label := cmp.Or(p.alt, path.Base(p.dest))
	fallback := func() ggui.Widget {
		return ggui.RichText(r.linked(mdStyle{r: r}, p.dest).span("🖼 " + label)).Size(mdBodySize)
	}
	web := strings.HasPrefix(p.dest, "http://") || strings.HasPrefix(p.dest, "https://")
	var img *ggui.StateValue[mdImage]
	switch {
	case web:
		img = r.m.webImage(p.dest)
	case isImage(p.dest) || isSVG(p.dest):
		doc := r.m.docs[r.id]
		if doc == nil || r.doc.Path == "" {
			return fallback() // nothing it is beside
		}
		where := r.beside(p.dest)
		if doc.images == nil {
			doc.images = map[string]*ggui.StateValue[mdImage]{}
		}
		var ok bool
		if img, ok = doc.images[where]; !ok {
			img = ggui.State(mdImage{Loading: true})
			doc.images[where] = img
			fetchImage(r.m, r.doc.Node, where, func(d decodedImage, err error) { img.Set(asMDImage(d, err)) })
		}
	default:
		return fallback()
	}
	return ggui.View(img, func(v mdImage) ggui.Widget {
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

// isSVG reports whether a name or address names an SVG.
func isSVG(name string) bool {
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	return strings.EqualFold(path.Ext(name), ".svg")
}

// asMDImage is an image file as read, for a page: an SVG drawn, anything
// else decoded as it came.
func asMDImage(d decodedImage, err error) mdImage {
	if err == nil && d.Img != nil {
		b := d.Img.Bounds()
		return mdImage{Img: ggfx.NewImageFromImage(d.Img), Size: ggui.Sz(float64(b.Dx()), float64(b.Dy()))}
	}
	if d.SVG != nil {
		return svgImage(d.SVG)
	}
	return mdImage{}
}

// svgImage is an SVG drawn at svgdraw.Scale, sized as its view box;
// nothing for one that cannot be drawn.
func svgImage(data []byte) mdImage {
	p, ok := svgdraw.Draw(data)
	if !ok {
		return mdImage{}
	}
	return mdImage{Img: ggfx.NewImageFromImage(p.Pixels), Size: p.Size, Labels: p.Labels, Origin: p.Origin}
}

// webImage is the image at the web address url, read once for the window.
func (m *model) webImage(url string) *ggui.StateValue[mdImage] {
	key := "web\x00" + url
	if img, ok := m.chatImages[key]; ok {
		return img
	}
	img := ggui.State(mdImage{Loading: true})
	m.chatImages[key] = img
	go func() {
		d, err := readWebImage(url)
		m.post(func() { img.Set(asMDImage(d, err)) })
	}()
	return img
}

// readWebImage reads and decodes the image at url: an SVG's source is
// kept to be drawn, on the window's goroutine.
func readWebImage(url string) (decodedImage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return decodedImage{}, err
	}
	req.Header.Set("User-Agent", "mako")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return decodedImage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodedImage{}, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, webImageLimit+1))
	if err != nil {
		return decodedImage{}, err
	}
	if len(data) > webImageLimit {
		return decodedImage{}, fmt.Errorf("%s is too large", url)
	}
	if isSVG(url) || strings.Contains(resp.Header.Get("Content-Type"), "svg") {
		return decodedImage{SVG: data}, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return decodedImage{Img: img}, err
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
