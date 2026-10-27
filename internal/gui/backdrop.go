package gui

import (
	"image/color"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/mcp"
)

// backdrop is a background image, under the whole window or under each
// session's terminal and chat. Opacity is how much of it shows through the
// theme's colour, from 0 to 1.
type backdrop struct {
	Image   string  `json:"image,omitempty"`
	Opacity float64 `json:"opacity"`
}

// windowBackdrop and sessionBackdrop are the two images' settings.
func windowBackdrop(p *prefs) *backdrop  { return &p.WindowBackdrop }
func sessionBackdrop(p *prefs) *backdrop { return &p.SessionBackdrop }

// backdropMaxSize bounds the image's larger side: a background never needs
// more than a large screen's pixels.
const backdropMaxSize = 3840

// backdropped lays child over the background image slot picks from the
// settings, under the theme's colour at the image's opacity. The
// workspace's and the sidebar's fills inside child turn clear, so the image
// shows through them once, however they nest. Without an image child is
// as it was; it is never rebuilt, so a terminal in it keeps its state.
func backdropped(m *model, slot func(*prefs) *backdrop, child ggui.Widget) ggui.Widget {
	image := ggui.Map(m.Prefs, func(p prefs) string { return slot(&p).Image })
	return &backdropView{
		image: image,
		layer: ggui.Reactive(func() ggui.Widget {
			if path := image.Get(); path != "" {
				return ggui.AsyncImage(mcp.ExpandHome(path)).MaxSize(backdropMaxSize).Fit(ggui.FitCover)
			}
			return ggui.Box()
		}),
		// The theme over the image: its colour, at what of it the image
		// leaves.
		scrim: ggui.Opacity(ggui.Box().Fill(tokSolidBg), 1).
			BindOpacity(ggui.Map(m.Prefs, func(p prefs) float64 { return 1 - slot(&p).Opacity })),
		child: child,
	}
}

type backdropView struct {
	image         ggui.Readable[string]
	layer, scrim  ggui.Widget
	child         ggui.Widget
	shown         bool
	from, cleared uitheme.Theme // the theme last cleared, and what clearing made of it
}

func (b *backdropView) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	b.shown = ggui.Peek(b.image) != ""
	if !b.shown {
		return b.child.Layout(c, env)
	}
	size := b.child.Layout(c, b.clearSurfaces(env))
	b.layer.Layout(ggui.Tight(size), env)
	b.scrim.Layout(ggui.Tight(size), env)
	return size
}

func (b *backdropView) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if b.shown {
		dst.Paint(b.layer, r)
		dst.Paint(b.scrim, r)
	}
	dst.Paint(b.child, r)
}

// clearSurfaces is env with the workspace's and the sidebar's fills clear,
// the terminal's and the editor's too, which take theirs from surface-0.
// The cleared theme is kept while the theme it came from holds: a new one
// each layout would be a new environment, and every cache under the image
// would miss.
func (b *backdropView) clearSurfaces(env ggui.Env) ggui.Env {
	t := uitheme.From(env)
	if !sameTheme(t, b.from) {
		var none color.Color = color.RGBA{}
		b.from, b.cleared = t, t.Set(uitheme.CSSColor("surface-0"), none).Set(uitheme.CSSColor("sidebar-fill"), none)
	}
	return b.cleared.Apply(env)
}

// sameTheme compares themes by value; one holding a value that cannot be
// compared is never the same.
func sameTheme(a, b uitheme.Theme) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

// fillGround paints a surface's fill, but not one a background image cleared.
func fillGround(dst *ggui.Canvas, r ggui.Rect, c color.Color) {
	if c == nil {
		return
	}
	if _, _, _, a := c.RGBA(); a > 0 {
		dst.FillRect(r, c)
	}
}
