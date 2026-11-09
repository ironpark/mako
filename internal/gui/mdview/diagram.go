package mdview

import (
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
)

// A diagram block shows as the picture the host draws of it, fitted to the
// page; a tap shows it at twice that, scrolling, and a button above it
// turns it back into its code and again. One that cannot be drawn shows as
// its code, with why under it. What the reader turned is kept with the
// page, as a details' fold is.

// diagramZoom is how much larger a diagram shows tapped: as many pixels as
// it was drawn with.
const diagramZoom = 2

// diagram is a diagram block: img, as the host draws it, or block, its
// code.
func (r *mdRenderer) diagram(img *ggui.StateValue[Image], block func() ggui.Widget, lang string) ggui.Widget {
	code, zoom := r.fold(false), r.fold(false)
	return ggui.View(img, func(v Image) ggui.Widget {
		switch {
		case v.Loading:
			return ggui.Box(ui.Spinner()).Pad(8)
		case v.Img == nil:
			if v.Err == "" {
				return block()
			}
			why := ggui.Text(v.Err).Size(12).Color(skin.DiffRemoved).Selectable()
			return ggui.Column(block(), why).Gap(6).Stretch()
		}
		return ggui.View(ggui.Combine(code, zoom, func(c, z bool) [2]bool { return [2]bool{c, z} }), func(s [2]bool) ggui.Widget {
			label := i18n.T("Code")
			if s[0] {
				label = i18n.T("Diagram")
			}
			bar := ggui.Row(ggui.Expanded(ggui.Box()),
				ui.Button(label, func() { code.Set(!s[0]) }).Ghost()).Align(ggui.AlignCenter)
			if s[0] {
				return ggui.Column(bar, block()).Gap(4).Stretch()
			}
			var pic ggui.Widget = fit(v, ggui.Size{}, lang)
			if s[1] {
				big := fit(v, ggui.Sz(v.Size.W*diagramZoom, v.Size.H*diagramZoom), lang)
				pic = ggui.Scroll(&unbounded{big}).Horizontal().Bar(skin.Surface3)
			}
			pic = ggui.Tap(pic, func() { zoom.Set(!s[1]) })
			return ggui.Column(bar, pic).Gap(4).Stretch()
		})
	})
}

// unbounded lays its child out with no bound on its width, so a picture
// in a sideways scroll keeps the size asked of it.
type unbounded struct{ child *fitImage }

func (u *unbounded) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	c.MaxW = 0
	return u.child.Layout(c, env)
}

func (u *unbounded) Paint(dst *ggui.Canvas, r ggui.Rect) { u.child.Paint(dst, r) }
