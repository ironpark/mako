package gui

import (
	"image/color"
	"iter"
	"math"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/mcp"
)

// backdrop is a background image, under the whole window or under each
// session's terminal and chat. Opacity is how much of it shows through the
// theme's colour, from 0 to 1.
//
// It is placed as a CSS background is: in the room the margins leave, at
// the size Width and Height fix, or, at 0, the size Fit gives it, at the
// edge or corner Position names, and for repeat tiled across the room.
// Sizes and margins are in logical pixels; one of Width and Height alone
// keeps the image's shape.
type backdrop struct {
	Image    string  `json:"image,omitempty"`
	Opacity  float64 `json:"opacity"`
	Fit      string  `json:"fit,omitempty"`      // one of backdropFits
	Position string  `json:"position,omitempty"` // one of backdropPositions
	Width    float64 `json:"width,omitempty"`
	Height   float64 `json:"height,omitempty"`
	MarginX  float64 `json:"margin_x,omitempty"`
	MarginY  float64 `json:"margin_y,omitempty"`
}

// The ways an image fills its room, and the places it can sit in it, in
// the order the settings offer them: the positions row by row, as their
// picker lays them out.
var (
	backdropFits      = []string{"cover", "contain", "fill", "repeat"}
	backdropPositions = []string{
		"top-left", "top", "top-right",
		"left", "center", "right",
		"bottom-left", "bottom", "bottom-right",
	}
)

// maxBackdropLength bounds a size or margin the settings take.
const maxBackdropLength = 4000

// normalized is b with a fit and position it knows, and lengths in bounds.
func (b backdrop) normalized() backdrop {
	b.Opacity = min(max(b.Opacity, 0), 1)
	if !slices.Contains(backdropFits, b.Fit) {
		b.Fit = "cover"
	}
	if !slices.Contains(backdropPositions, b.Position) {
		b.Position = "center"
	}
	for _, v := range []*float64{&b.Width, &b.Height, &b.MarginX, &b.MarginY} {
		*v = min(max(*v, 0), maxBackdropLength)
	}
	return b
}

// backdropSlot is one of the two images: under the window, or under each
// session.
type backdropSlot int

const (
	windowBackdrop backdropSlot = iota
	sessionBackdrop
)

// defaultBackdrop is a slot's settings before the user changes them.
func defaultBackdrop() backdrop { return backdrop{Opacity: defaultBackdropOpacity}.normalized() }

func (s backdropSlot) of(p *prefs) *backdrop {
	if s == sessionBackdrop {
		return &p.SessionBackdrop
	}
	return &p.WindowBackdrop
}

// backdropMaxSize bounds the image's larger side: a background never needs
// more than a large screen's pixels.
const backdropMaxSize = 3840

// backdropped lays child over the background image slot picks from the
// settings, under the theme's colour at the image's opacity. The
// workspace's and the sidebar's fills inside child turn clear, so the image
// shows through them once, however they nest. Without an image child is
// as it was; it is never rebuilt, so a terminal in it keeps its state.
func backdropped(m *model, slot backdropSlot, child ggui.Widget) ggui.Widget {
	image := ggui.Map(m.Prefs, func(p prefs) string { return slot.of(&p).Image })
	return &backdropView{
		image: image,
		seen:  &m.backdropSeen[slot],
		layer: backdropLayer(m, slot, 1),
		scrim: backdropScrim(m, slot),
		child: child,
	}
}

// backdropScrim is the theme over the slot's image: its colour, at what of
// it the image leaves. The fill carries that alpha itself, where an
// Opacity would paint it into a layer the size of the window every frame.
func backdropScrim(m *model, slot backdropSlot) ggui.Widget {
	alpha := ggui.Map(m.Prefs, func(p prefs) float64 { return 1 - slot.of(&p).Opacity })
	return ggui.Reactive(func() ggui.Widget {
		return ggui.Box().Fill(tokSolidBg.Alpha(alpha.Get()))
	})
}

// backdropLayer is the slot's image, placed as its settings say, with its
// lengths scaled by scale: 1 under the window, less in the settings'
// preview of it. It is rebuilt when the placement changes, which finds the
// image already decoded.
func backdropLayer(m *model, slot backdropSlot, scale float64) ggui.Widget {
	placement := ggui.Map(m.Prefs, func(p prefs) backdrop {
		b := *slot.of(&p)
		b.Opacity = 0 // the scrim's
		return b
	})
	return ggui.Reactive(func() ggui.Widget {
		b := placement.Get()
		if b.Image == "" {
			return ggui.Box()
		}
		img := ggui.AsyncImage(mcp.ExpandHome(b.Image)).MaxSize(backdropMaxSize).Fit(ggui.FitFill)
		return &backdropImage{img: img, b: b.scaled(scale), scale: scale}
	})
}

type backdropView struct {
	image         ggui.Readable[string]
	seen          *ggui.Size // where the size it was last laid out at is kept, for the preview
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
	*b.seen = size
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

// backdropImage paints an image as b places it, once loaded.
type backdropImage struct {
	img     *ggui.AsyncImageWidget
	b       backdrop // its lengths scaled
	scale   float64
	natural ggui.Size // the image's, scaled; zero until it loads
}

func (a *backdropImage) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	// Under no bounds of its own the image asks for its natural size,
	// which MaxSize already holds within these.
	n := a.img.Layout(ggui.Loose(ggui.Sz(backdropMaxSize, backdropMaxSize)), env)
	a.natural = ggui.Size{}
	if a.img.Status() == ggui.Ready {
		a.natural = ggui.Sz(n.W*a.scale, n.H*a.scale)
	}
	return c.Max()
}

func (a *backdropImage) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if a.natural.W <= 0 || a.natural.H <= 0 {
		return
	}
	room := a.b.room(r)
	if room.Empty() {
		return
	}
	at := a.b.place(a.natural, room)
	dst = dst.Clip(room)
	if a.b.Fit != "repeat" {
		dst.Paint(a.img, at)
		return
	}
	// The copies are one image's: painted straight, not each a widget of
	// the tree.
	for tile := range tiles(at, room) {
		a.img.Paint(dst, tile)
	}
}

// scaled is b with its lengths scaled by k.
func (b backdrop) scaled(k float64) backdrop {
	b.Width, b.Height, b.MarginX, b.MarginY = b.Width*k, b.Height*k, b.MarginX*k, b.MarginY*k
	return b
}

// room is r less b's margins.
func (b backdrop) room(r ggui.Rect) ggui.Rect {
	return ggui.Rct(ggui.Pt(r.Origin.X+b.MarginX, r.Origin.Y+b.MarginY),
		ggui.Sz(max(r.Size.W-2*b.MarginX, 0), max(r.Size.H-2*b.MarginY, 0)))
}

// place is where an image of natural size n is drawn in room: at the size
// b fixes or its fit gives it, at b's position. For repeat it is the tile
// the others are laid from.
func (b backdrop) place(n ggui.Size, room ggui.Rect) ggui.Rect {
	s := n
	switch {
	case b.Width > 0 && b.Height > 0:
		s = ggui.Sz(b.Width, b.Height)
	case b.Width > 0:
		s = ggui.Sz(b.Width, b.Width*n.H/n.W)
	case b.Height > 0:
		s = ggui.Sz(b.Height*n.W/n.H, b.Height)
	case b.Fit == "fill":
		s = room.Size
	case b.Fit == "contain":
		s = ggui.FitContain.Place(n, room).Size
	case b.Fit != "repeat": // cover
		s = ggui.FitCover.Place(n, room).Size
	}
	x, y := anchor(b.Position)
	return ggui.Rct(ggui.Pt(room.Origin.X+(room.Size.W-s.W)*x, room.Origin.Y+(room.Size.H-s.H)*y), s)
}

// anchor is where position puts the image along each axis, from 0 at the
// left or top to 1 at the right or bottom.
func anchor(position string) (x, y float64) {
	x, y = 0.5, 0.5
	if strings.Contains(position, "left") {
		x = 0
	} else if strings.Contains(position, "right") {
		x = 1
	}
	if strings.HasPrefix(position, "top") {
		y = 0
	} else if strings.HasPrefix(position, "bottom") {
		y = 1
	}
	return x, y
}

// maxTiles bounds the tiles a repeat paints: a tiny image over a large
// room is drawn at a size that keeps to it.
const maxTiles = 1024

// tiles are the copies of the tile at that cover room, on its grid.
func tiles(at, room ggui.Rect) iter.Seq[ggui.Rect] {
	return func(yield func(ggui.Rect) bool) {
		s := at.Size
		if s.W <= 0 || s.H <= 0 {
			return
		}
		if n := math.Ceil(room.Size.W/s.W+1) * math.Ceil(room.Size.H/s.H+1); n > maxTiles {
			k := math.Sqrt(n / maxTiles)
			s = ggui.Sz(s.W*k, s.H*k)
		}
		x0 := at.Origin.X - math.Ceil((at.Origin.X-room.Origin.X)/s.W)*s.W
		y0 := at.Origin.Y - math.Ceil((at.Origin.Y-room.Origin.Y)/s.H)*s.H
		for y := y0; y < room.Origin.Y+room.Size.H; y += s.H {
			for x := x0; x < room.Origin.X+room.Size.W; x += s.W {
				if !yield(ggui.Rct(ggui.Pt(x, y), s)) {
					return
				}
			}
		}
	}
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
