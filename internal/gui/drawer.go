package gui

import (
	"time"

	"github.com/ironpark/ggui"
)

// drawer is a side panel that slides open and closed: it takes from none
// to all of width as open turns true, and back. The panel keeps its width
// and is clipped as it moves, so its contents slide rather than reflow,
// and while closed it is not painted.
type drawer struct {
	child ggui.Widget
	open  ggui.Readable[bool]
	width float64
	right bool // on the window's right edge, sliding in from it
	shown *ggui.Tweened[float64]

	w float64 // the share of width shown, as last laid out
}

const drawerDuration = 220 * time.Millisecond

func newDrawer(child ggui.Widget, open ggui.Readable[bool], width float64, right bool) *drawer {
	share := ggui.Map(open, func(o bool) float64 {
		if o {
			return 1
		}
		return 0
	})
	return &drawer{
		child: child, open: open, width: width, right: right,
		shown: ggui.TweenOf(share, drawerDuration).Easing(ggui.EaseOut),
	}
}

func (d *drawer) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	f := d.shown.Get()
	if env.ReducedMotion() {
		f = 0
		if d.open.Get() {
			f = 1
		}
	}
	d.w = d.width * f
	if d.w >= 0.5 { // closed, it is not painted, so needs no layout
		d.child.Layout(ggui.Tight(ggui.Sz(d.width, c.MaxH)), env)
	}
	return c.Constrain(ggui.Sz(d.w, c.MaxH))
}

func (d *drawer) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if d.w < 0.5 {
		return
	}
	// The panel's inner edge follows the workspace: a left one shows its
	// right part first, a right one its left part.
	at := r.Origin
	if !d.right {
		at.X += d.w - d.width
	}
	dst.Clip(ggui.Rect{Origin: r.Origin, Size: ggui.Sz(d.w, r.Size.H)}).
		Paint(d.child, ggui.Rect{Origin: at, Size: ggui.Sz(d.width, r.Size.H)})
}
