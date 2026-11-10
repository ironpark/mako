package svgdraw

import (
	"encoding/xml"
	"math"
	"strconv"
	"strings"
)

// Text set on a path (textPath) starts where the path does, turned as it
// runs there; ggfx has no text, so the path is read here, for its first
// vertex.

// pathsByID are the path data of the paths data names by id.
func pathsByID(data []byte) map[string]string {
	paths := map[string]string{}
	dec := newDecoder(data)
	for {
		tok, err := dec.Token()
		if err != nil {
			return paths
		}
		if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "path" {
			if id := attr(t.Attr, "id"); id != "" {
				paths[id] = attr(t.Attr, "d")
			}
		}
	}
}

// attr is the value of the attribute named k, "" for none.
func attr(attrs []xml.Attr, k string) string {
	for _, a := range attrs {
		if a.Name.Local == k {
			return a.Value
		}
	}
	return ""
}

// vertex is a point on a path, and the way the path runs there, in
// degrees.
type vertex struct{ x, y, angle float64 }

// dir is the way from a to b, in degrees.
func dir(ax, ay, bx, by float64) float64 { return math.Atan2(by-ay, bx-ax) * 180 / math.Pi }

// bisect is the angle halfway between a and b, the shorter way round.
func bisect(a, b float64) float64 {
	d := math.Mod(b-a+540, 360) - 180
	return a + d/2
}

// pathArgs are how many numbers each path command takes.
var pathArgs = map[byte]int{'m': 2, 'l': 2, 't': 2, 'h': 1, 'v': 1, 'c': 6, 's': 4, 'q': 4, 'a': 7, 'z': 0}

// pathVertices are the vertices of the path data d: where each segment
// starts and ends, turned as the segments run there. A curve runs toward
// and away from its control points; an arc, along its chord.
func pathVertices(d string) []vertex {
	var vs []vertex
	var cx, cy, sx, sy float64 // the current point, and the subpath's start
	var lastOut float64        // the way the last segment left its end
	var c2x, c2y float64       // the last curve's second control point
	// add is a segment from (fx, fy) to (tx, ty), leaving toward (ix, iy)
	// and arriving from (ox, oy): a line's are its ends.
	add := func(fx, fy, ix, iy, tx, ty, ox, oy float64) {
		in, out := dir(fx, fy, ix, iy), dir(ox, oy, tx, ty)
		if ix == fx && iy == fy {
			in = dir(fx, fy, tx, ty)
		}
		if ox == tx && oy == ty {
			out = dir(fx, fy, tx, ty)
		}
		if n := len(vs); n > 0 && vs[n-1].x == fx && vs[n-1].y == fy {
			if n > 1 {
				vs[n-1].angle = bisect(lastOut, in)
			} else {
				vs[n-1].angle = in
			}
		} else {
			vs = append(vs, vertex{fx, fy, in})
		}
		vs = append(vs, vertex{tx, ty, out})
		lastOut = out
	}
	toks := pathTokens(d)
	prev := byte(0)
	for i := 0; i < len(toks); {
		cmd := prev
		if t := toks[i]; len(t) == 1 && strings.ContainsAny(t, "MmLlHhVvCcSsQqTtAaZz") {
			cmd = t[0]
			i++
		}
		op := cmd | 0x20 // lower case
		argc, known := pathArgs[op]
		if !known || i+argc > len(toks) {
			return vs
		}
		var n [7]float64
		for k := range argc {
			v, err := strconv.ParseFloat(toks[i+k], 64)
			if err != nil {
				return vs
			}
			n[k] = v
		}
		i += argc
		ox, oy := 0.0, 0.0 // where its numbers count from: the origin, or the current point
		if cmd == op {
			ox, oy = cx, cy
		}
		prev = cmd
		switch op {
		case 'm':
			cx, cy = ox+n[0], oy+n[1]
			sx, sy = cx, cy
			prev = 'L' | cmd&0x20 // more pairs are lines
		case 'l', 't':
			add(cx, cy, ox+n[0], oy+n[1], ox+n[0], oy+n[1], cx, cy)
			cx, cy = ox+n[0], oy+n[1]
		case 'h':
			add(cx, cy, ox+n[0], cy, ox+n[0], cy, cx, cy)
			cx = ox + n[0]
		case 'v':
			add(cx, cy, cx, oy+n[0], cx, oy+n[0], cx, cy)
			cy = oy + n[0]
		case 'c':
			add(cx, cy, ox+n[0], oy+n[1], ox+n[4], oy+n[5], ox+n[2], oy+n[3])
			c2x, c2y = ox+n[2], oy+n[3]
			cx, cy = ox+n[4], oy+n[5]
		case 's', 'q':
			ix, iy := ox+n[0], oy+n[1]
			if op == 's' {
				ix, iy = 2*cx-c2x, 2*cy-c2y
			}
			add(cx, cy, ix, iy, ox+n[2], oy+n[3], ox+n[0], oy+n[1])
			c2x, c2y = ox+n[0], oy+n[1]
			cx, cy = ox+n[2], oy+n[3]
		case 'a':
			add(cx, cy, ox+n[5], oy+n[6], ox+n[5], oy+n[6], cx, cy)
			cx, cy = ox+n[5], oy+n[6]
		case 'z':
			if cx != sx || cy != sy {
				add(cx, cy, sx, sy, sx, sy, cx, cy)
			}
			cx, cy = sx, sy
			prev = 0
		}
	}
	return vs
}

// pathTokens splits path data into its commands and numbers.
func pathTokens(d string) []string {
	var toks []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			toks = append(toks, b.String())
			b.Reset()
		}
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		switch {
		case strings.IndexByte("MmLlHhVvCcSsQqTtAaZz", c) >= 0:
			flush()
			toks = append(toks, string(c))
		case c == ',' || c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == '-' || c == '+':
			// A sign starts a number, but for an exponent's.
			if b.Len() > 0 {
				if last := b.String()[b.Len()-1]; last != 'e' && last != 'E' {
					flush()
				}
			}
			b.WriteByte(c)
		case c == '.':
			if strings.Contains(b.String(), ".") && !strings.ContainsAny(b.String(), "eE") {
				flush() // .5.5 is two numbers
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return toks
}
