package gui

import (
	"image/color"
	"math"

	"github.com/ironpark/ggui"
)

// Box drawing (U+2500–257F) and block elements (U+2580–259F) are drawn as
// shapes rather than glyphs, as terminals do. A font draws them for its own
// line height, so with any line spacing above 1.0 they stop short of the
// cell and a logo or a frame made of them shows gaps between rows. Drawn
// here they fill the cell exactly, whatever the font and the spacing.

// drawBoxChar draws r into the cell rect if it is one of the characters
// above, reporting whether it was. rect's edges are already on device
// pixels, so neighbouring cells meet without a seam.
func drawBoxChar(dst *ggui.Canvas, r rune, rect ggui.Rect, fg color.RGBA) bool {
	switch {
	case r >= 0x2580 && r <= 0x259F:
		drawBlock(dst, r, rect, fg)
		return true
	case r >= 0x2500 && r <= 0x257F:
		return drawLines(dst, r, rect, fg)
	}
	return false
}

// Blocks ------------------------------------------------------------------------

// quadrant bits: upper left, upper right, lower left, lower right.
const (
	qUL = 1 << iota
	qUR
	qLL
	qLR
)

// quadrants are U+2596–259F's.
var quadrants = [...]uint8{
	qLL, qLR, qUL, qUL | qLL | qLR,
	qUL | qLR, qUL | qUR | qLL, qUL | qUR | qLR,
	qUR, qUR | qLL, qUR | qLL | qLR,
}

func drawBlock(dst *ggui.Canvas, r rune, c ggui.Rect, fg color.RGBA) {
	x, y, w, h := c.Origin.X, c.Origin.Y, c.Size.W, c.Size.H
	fill := func(x0, y0, x1, y1 float64, col color.Color) { fillSnapped(dst, x0, y0, x1, y1, col) }
	switch {
	case r == 0x2580: // ▀ upper half
		fill(x, y, x+w, y+h/2, fg)
	case r >= 0x2581 && r <= 0x2588: // ▁ … █ lower eighths
		n := float64(r - 0x2580)
		fill(x, y+h-h*n/8, x+w, y+h, fg)
	case r >= 0x2589 && r <= 0x258F: // ▉ … ▏ left eighths, widest first
		n := float64(0x2590 - r)
		fill(x, y, x+w*n/8, y+h, fg)
	case r == 0x2590: // ▐ right half
		fill(x+w/2, y, x+w, y+h, fg)
	case r >= 0x2591 && r <= 0x2593: // ░ ▒ ▓ shades, as the colour at a quarter, half, three quarters
		fill(x, y, x+w, y+h, ggui.LerpColor(nil, fg, float64(r-0x2590)/4))
	case r == 0x2594: // ▔ upper eighth
		fill(x, y, x+w, y+h/8, fg)
	case r == 0x2595: // ▕ right eighth
		fill(x+w-w/8, y, x+w, y+h, fg)
	default:
		q := quadrants[r-0x2596]
		if q&qUL != 0 {
			fill(x, y, x+w/2, y+h/2, fg)
		}
		if q&qUR != 0 {
			fill(x+w/2, y, x+w, y+h/2, fg)
		}
		if q&qLL != 0 {
			fill(x, y+h/2, x+w/2, y+h, fg)
		}
		if q&qLR != 0 {
			fill(x+w/2, y+h/2, x+w, y+h, fg)
		}
	}
}

// Lines -------------------------------------------------------------------------

// Line weights of one arm from the cell's centre.
const (
	none = iota
	light
	heavy
	double
)

// arms are the weights of a box character's arms: up, right, down, left.
type arms [4]uint8

// boxArms are the arms of U+2500–257F, by r-0x2500, for every character
// made only of arms from the centre; the rest, dashes, arcs and diagonals,
// which are drawn apart, have none.
var boxArms = func() (m [0x80]arms) {
	// Rows of U+2500–254B, and of U+2550–256C and U+2574–257F, in order.
	set := func(start rune, rows ...string) {
		for i, s := range rows {
			var a arms
			for j := range 4 {
				a[j] = s[j] - '0'
			}
			m[start-0x2500+rune(i)] = a
		}
	}
	set(0x2500, "0101", "0202", "1010", "2020")
	set(0x250C,
		"0110", "0210", "0120", "0220", // ┌ ┍ ┎ ┏
		"0011", "0012", "0021", "0022", // ┐ ┑ ┒ ┓
		"1100", "1200", "2100", "2200", // └ ┕ ┖ ┗
		"1001", "1002", "2001", "2002", // ┘ ┙ ┚ ┛
		"1110", "1210", "2110", "1120", "2120", "2210", "1220", "2220", // ├ … ┣
		"1011", "1012", "2011", "1021", "2021", "2012", "1022", "2022", // ┤ … ┫
		"0111", "0112", "0211", "0212", "0121", "0122", "0221", "0222", // ┬ … ┳
		"1101", "1102", "1201", "1202", "2101", "2102", "2201", "2202", // ┴ … ┻
		"1111", "1112", "1211", "1212", "2111", "1121", "2121", // ┼ … ╂
		"2112", "2211", "1122", "1221", "2212", "1222", "2122", "2221", "2222", // ╃ … ╋
	)
	set(0x2550,
		"0303", "3030", // ═ ║
		"0310", "0130", "0330", // ╒ ╓ ╔
		"0013", "0031", "0033", // ╕ ╖ ╗
		"1300", "3100", "3300", // ╘ ╙ ╚
		"1003", "3001", "3003", // ╛ ╜ ╝
		"1310", "3130", "3330", // ╞ ╟ ╠
		"1013", "3031", "3033", // ╡ ╢ ╣
		"0313", "0131", "0333", // ╤ ╥ ╦
		"1303", "3101", "3303", // ╧ ╨ ╩
		"1313", "3131", "3333", // ╪ ╫ ╬
	)
	set(0x2574,
		"0001", "1000", "0100", "0010", // ╴ ╵ ╶ ╷
		"0002", "2000", "0200", "0020", // ╸ ╹ ╺ ╻
		"0201", "1020", "0102", "2010", // ╼ ╽ ╾ ╿
	)
	return m
}()

// dash is a dashed line: whether vertical, heavy, and how many dashes.
type dash struct {
	vertical, heavy bool
	n               int
}

// dashes are U+2504–250B's and U+254C–254F's, by r-0x2500; n is 0 for the
// other characters.
var dashes = func() (m [0x80]dash) {
	for i, d := range []dash{{false, false, 3}, {false, true, 3}, {true, false, 3}, {true, true, 3}, {false, false, 4}, {false, true, 4}, {true, false, 4}, {true, true, 4}} {
		m[0x04+i] = d
	}
	for i, d := range []dash{{false, false, 2}, {false, true, 2}, {true, false, 2}, {true, true, 2}} {
		m[0x4C+i] = d
	}
	return m
}()

// lineWidths are a light and a heavy stroke for a cell, in logical pixels on
// whole device pixels.
func lineWidths(dst *ggui.Canvas, c ggui.Rect) (thin, thick float64) {
	s := dst.Scale()
	thin = math.Max(1, math.Round(c.Size.W/8*s)) / s
	thick = math.Max(2, math.Round(c.Size.W/4*s)) / s
	return thin, thick
}

func drawLines(dst *ggui.Canvas, r rune, c ggui.Rect, fg color.RGBA) bool {
	x, y, w, h := c.Origin.X, c.Origin.Y, c.Size.W, c.Size.H
	thin, thick := lineWidths(dst, c)
	s := dst.Scale()
	cx, cy := snapTo(s, x+w/2-thin/2)+thin/2, snapTo(s, y+h/2-thin/2)+thin/2
	rect := func(x0, y0, x1, y1 float64) { fillSnapped(dst, x0, y0, x1, y1, fg) }

	if a := boxArms[r-0x2500]; a != (arms{}) {
		drawArms(a, x, y, w, h, cx, cy, thin, thick, rect)
		return true
	}
	if d := dashes[r-0x2500]; d.n > 0 {
		t := thin
		if d.heavy {
			t = thick
		}
		n := float64(d.n)
		if d.vertical {
			seg := h / n
			for i := range d.n {
				y0 := y + float64(i)*seg
				rect(cx-t/2, y0+seg*0.15, cx+t/2, y0+seg*0.85)
			}
		} else {
			seg := w / n
			for i := range d.n {
				x0 := x + float64(i)*seg
				rect(x0+seg*0.15, cy-t/2, x0+seg*0.85, cy+t/2)
			}
		}
		return true
	}
	switch r {
	case 0x256D, 0x256E, 0x256F, 0x2570: // ╭ ╮ ╯ ╰
		drawArc(dst, r, c, cx, cy, thin, fg)
		return true
	case 0x2571, 0x2572, 0x2573: // ╱ ╲ ╳
		if r != 0x2572 {
			dst.StrokeLine(ggui.Pt(x+w, y), ggui.Pt(x, y+h), thin, fg)
		}
		if r != 0x2571 {
			dst.StrokeLine(ggui.Pt(x, y), ggui.Pt(x+w, y+h), thin, fg)
		}
		return true
	}
	return false
}

// drawArms draws the arms of a and their joint. A single or heavy arm runs
// from the edge to just past the centre, so arms of any weights join. A
// double arm is two light lines a gap apart, each stopping where the line it
// meets runs: at the far line of a double arm across, at the centre of a
// single one, short of it where there is none, so corners and tees close
// the way ╔ and ╠ should.
func drawArms(a arms, x, y, w, h, cx, cy, thin, thick float64, rect func(x0, y0, x1, y1 float64)) {
	const up, right, down, left = 0, 1, 2, 3
	width := func(wt uint8) float64 {
		if wt == heavy {
			return thick
		}
		return thin
	}
	// How far past the centre a single or heavy arm reaches: half the
	// widest arm across it, so the joint is filled.
	across := func(i, j int) float64 {
		reach := 0.0
		for _, k := range []int{i, j} {
			switch a[k] {
			case light, heavy:
				reach = math.Max(reach, width(a[k])/2)
			case double:
				reach = math.Max(reach, thin*1.5)
			}
		}
		return reach
	}
	off := 2 * thin // each line of a double arm sits this far from the centre
	// stop is how far past the centre, away from the arm, a double arm's
	// line on one side reaches: the perpendicular arm on that side decides.
	stop := func(perp uint8) float64 {
		switch perp {
		case none:
			return -off
		case double:
			return off
		}
		return 0
	}
	for i, wt := range a {
		switch {
		case wt == none:
		case wt == double:
			// Two light lines along the arm, either side of the centre
			// across it, each ending where the arm on its side decides.
			vertical := i == up || i == down
			along, across, edge, sides := cy, cx, y, [2]int{left, right}
			if !vertical {
				along, across, edge, sides = cx, cy, x, [2]int{up, down}
			}
			dir := -1.0
			if i == down || i == right {
				dir = 1
				if vertical {
					edge += h
				} else {
					edge += w
				}
			}
			for k, side := range sides {
				pos := across + float64(2*k-1)*off
				end := along + dir*stop(a[side])
				lo, hi := math.Min(edge, end), math.Max(edge, end)
				if vertical {
					rect(pos-thin/2, lo, pos+thin/2, hi)
				} else {
					rect(lo, pos-thin/2, hi, pos+thin/2)
				}
			}
		default:
			t := width(wt)
			switch i {
			case up:
				rect(cx-t/2, y, cx+t/2, cy+across(left, right))
			case down:
				rect(cx-t/2, cy-across(left, right), cx+t/2, y+h)
			case left:
				rect(x, cy-t/2, cx+across(up, down), cy+t/2)
			case right:
				rect(cx-across(up, down), cy-t/2, x+w, cy+t/2)
			}
		}
	}
}

// drawArc draws a rounded corner: straight from the two edges it joins,
// bending through a quarter circle about the corner of the cell's centre.
func drawArc(dst *ggui.Canvas, r rune, c ggui.Rect, cx, cy, t float64, fg color.RGBA) {
	x, y, w, h := c.Origin.X, c.Origin.Y, c.Size.W, c.Size.H
	rad := math.Min(w, h) / 2
	// Which way the corner opens: the arms it joins.
	sx, sy := 1.0, 1.0 // toward right, toward down
	switch r {
	case 0x256E: // ╮ left and down
		sx = -1
	case 0x256F: // ╯ left and up
		sx, sy = -1, -1
	case 0x2570: // ╰ right and up
		sy = -1
	}
	// The straight parts, from each edge to where the bend starts at
	// (cx, oy) and (ox, cy).
	vEdge, hEdge := y+h, x+w
	if sy < 0 {
		vEdge = y
	}
	if sx < 0 {
		hEdge = x
	}
	ox, oy := cx+sx*rad, cy+sy*rad
	fillSnapped(dst, cx-t/2, math.Min(vEdge, oy), cx+t/2, math.Max(vEdge, oy), fg)
	fillSnapped(dst, math.Min(hEdge, ox), cy-t/2, math.Max(hEdge, ox), cy+t/2, fg)
	// The bend, a quarter circle about (ox, oy) as a cubic.
	const k = 0.5523 // 4/3·(√2−1)
	var p ggui.Path
	p.MoveTo(cx, oy)
	p.CubicTo(cx, oy-sy*k*rad, ox-sx*k*rad, cy, ox, cy)
	dst.StrokePath(&p, t, fg)
}

// fillSnapped fills the rect between two corners, its edges rounded to
// device pixels, if anything is left of it.
func fillSnapped(dst *ggui.Canvas, x0, y0, x1, y1 float64, col color.Color) {
	s := dst.Scale()
	x0, y0, x1, y1 = snapTo(s, x0), snapTo(s, y0), snapTo(s, x1), snapTo(s, y1)
	if x1 > x0 && y1 > y0 {
		dst.FillRect(ggui.Rect{Origin: ggui.Pt(x0, y0), Size: ggui.Sz(x1-x0, y1-y0)}, col)
	}
}

// snapTo rounds a logical coordinate to the nearest device pixel at scale.
func snapTo(scale, v float64) float64 {
	if scale <= 0 {
		return v
	}
	return math.Round(v*scale) / scale
}
