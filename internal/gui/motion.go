package gui

import (
	"image/color"
	"math"
	"time"

	"github.com/ironpark/ggui"
)

// What opens and closes in the window does so in one time and one curve,
// short enough to stay out of the way: a fold's body eases open, pushing
// what is under it down, its chevron turns with it, and a card that asks
// for an answer rises into its place. Under reduced motion ggui shows each
// at once.

// motionDuration is how long a fold takes to open or close, and a card to
// arrive.
const motionDuration = 180 * time.Millisecond

// switchDuration is how long one view takes to give way to another: less
// than a fold's, as nothing moves.
const switchDuration = 120 * time.Millisecond

// reveal is what build makes, shown while open and eased open and closed.
// It is built only once opened, as ggui.If would, and kept until it has
// folded away.
func reveal(open ggui.Readable[bool], build func() ggui.Widget) ggui.Widget {
	return ggui.Component(func() ggui.Widget {
		share := ggui.TweenOf(ggui.Map(open, func(o bool) float64 { return pick(o, 1.0, 0.0) }), motionDuration)
		present := ggui.Derived(func() bool { return open.Get() || share.Get() > 0 })
		return ggui.Reveal(open, ggui.If(present, build)).Duration(motionDuration)
	})
}

// chevron is a fold's chevron: down while open, turned aside while closed,
// turning between them.
func chevron(open ggui.Readable[bool], size float64, c color.Color) ggui.Widget {
	return ggui.Component(func() ggui.Widget {
		turn := ggui.TweenOf(ggui.Map(open, func(o bool) float64 { return pick(o, 0.0, -math.Pi/2) }), motionDuration).Easing(ggui.EaseOut)
		return ggui.View(turn, func(a float64) ggui.Widget { return icon("chevron-down").Size(size).Color(c).Rotate(a) })
	})
}

// arrive is a card as it comes to ask something: faded in, risen a little.
func arrive(card ggui.Widget) ggui.Widget {
	return ggui.Transition(card).Fade().Slide(0, 10).Duration(motionDuration)
}

// crossfade shows what build makes of key's value, as ggui.Key does, one
// view fading into the next: a session's chat into its subagent's, the
// window into its settings. Not a session's chat and its terminal, which
// are one thing seen two ways, and change at once.
func crossfade[K comparable](key ggui.Readable[K], build func(K) ggui.Widget) ggui.Widget {
	return ggui.Switcher(key, build).Fade().Duration(switchDuration)
}
