package svgdraw

import (
	"bytes"
	"cmp"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ggfx draws no markers, the arrowheads a diagram's edges end in, as one
// Mermaid draws does. So a shape that names one has it drawn after it, as
// a group of the marker's own shapes: at the vertex, turned as the shape
// runs there, sized by its stroke.

// svgMarker is a <marker>: how its view box maps onto the vertex, and the
// elements it holds.
type svgMarker struct {
	refX, refY float64
	sx, sy     float64 // its view box's units, in the stroke's
	orient     string  // auto, auto-start-reverse, or an angle
	stroke     bool    // markerUnits is strokeWidth: sized by the stroke
	tokens     []xml.Token
}

// svgDefs are what an SVG defines that clean draws itself, ggfx not: its
// markers, its clips and masks, and its paths text follows, by id.
type svgDefs struct {
	markers map[string]*svgMarker
	clips   map[string]*svgClip
	paths   map[string]string // a path's d
}

// svgClip is a <clipPath>, or with mask set a <mask>, and the elements it
// holds.
type svgClip struct {
	mask   bool
	tokens []xml.Token
}

// scanDefs reads what data defines for clean to draw, its paths only when
// paths is set: text follows one.
func scanDefs(data []byte, paths bool) svgDefs {
	dec := newDecoder(data)
	defs := svgDefs{markers: map[string]*svgMarker{}, clips: map[string]*svgClip{}, paths: map[string]string{}}
	var into *[]xml.Token // the tokens of the marker or clip being read
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return defs
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if into != nil {
				depth++
				*into = append(*into, t.Copy())
				continue
			}
			id := attr(t.Attr, "id")
			if id == "" {
				continue
			}
			switch t.Name.Local {
			case "marker":
				m := newMarker(t.Attr)
				defs.markers[id], into, depth = m, &m.tokens, 0
			case "clipPath", "mask":
				c := &svgClip{mask: t.Name.Local == "mask"}
				defs.clips[id], into, depth = c, &c.tokens, 0
			case "path":
				if paths {
					defs.paths[id] = attr(t.Attr, "d")
				}
			}
		case xml.EndElement:
			if into == nil {
				continue
			}
			if depth == 0 {
				into = nil
				continue
			}
			depth--
			*into = append(*into, t)
		case xml.CharData:
			if into != nil {
				*into = append(*into, t.Copy())
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

// newMarker is the marker a <marker>'s attributes describe.
func newMarker(attrs []xml.Attr) *svgMarker {
	m := &svgMarker{orient: "0", stroke: true}
	w, h := 3.0, 3.0
	var box []float64
	m.refX, _ = svgAttrNum(attrs, "refX")
	m.refY, _ = svgAttrNum(attrs, "refY")
	if v, ok := svgAttrNum(attrs, "markerWidth"); ok {
		w = v
	}
	if v, ok := svgAttrNum(attrs, "markerHeight"); ok {
		h = v
	}
	for _, a := range attrs {
		switch a.Name.Local {
		case "orient":
			m.orient = strings.TrimSpace(a.Value)
		case "markerUnits":
			m.stroke = a.Value != "userSpaceOnUse"
		case "viewBox":
			box = numbers(a.Value)
		}
	}
	m.sx, m.sy = 1, 1
	if len(box) == 4 && box[2] > 0 && box[3] > 0 {
		// Meet: the box's shape kept, its larger side filling the marker's.
		s := min(w/box[2], h/box[3])
		m.sx, m.sy = s, s
		m.refX -= box[0]
		m.refY -= box[1]
	}
	return m
}

// markerRefs are the markers a shape's properties name at its start, its
// inner vertices and its end.
func markerRefs(props map[string]string) (start, mid, end string) {
	all := props["marker"]
	return urlID(cmp.Or(props["marker-start"], all)), urlID(cmp.Or(props["marker-mid"], all)), urlID(cmp.Or(props["marker-end"], all))
}

// urlID is the id a url(#id) names, "" for none.
func urlID(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "url(") || !strings.HasSuffix(v, ")") {
		return ""
	}
	v = strings.Trim(strings.TrimSpace(v[4:len(v)-1]), `'"`)
	id, ok := strings.CutPrefix(v, "#")
	if !ok {
		return ""
	}
	return id
}

// writeMarkers draws, after the shape name with attrs, the markers it
// names from defs.
func writeMarkers(out *bytes.Buffer, name string, attrs []xml.Attr, defs map[string]*svgMarker) {
	props := svgProps(attrs)
	start, mid, end := markerRefs(props)
	if start == "" && mid == "" && end == "" {
		return
	}
	vs := shapeVertices(name, attrs)
	if len(vs) == 0 {
		return
	}
	width, ok := svgLength(props["stroke-width"], 16)
	if !ok {
		width = 1
	}
	transform := attr(attrs, "transform")
	place := func(id string, v vertex, first bool) {
		m := defs[id]
		if m == nil {
			return
		}
		angle := v.angle
		switch m.orient {
		case "auto":
		case "auto-start-reverse":
			if first {
				angle += 180
			}
		default:
			angle, _ = strconv.ParseFloat(strings.TrimSuffix(m.orient, "deg"), 64)
		}
		s := 1.0
		if m.stroke {
			s = width
		}
		fmt.Fprintf(out, `<g transform="%s translate(%g,%g) rotate(%g) scale(%g,%g) translate(%g,%g)">`,
			transform, v.x, v.y, angle, s*m.sx, s*m.sy, -m.refX, -m.refY)
		writeTokens(out, m.tokens)
		out.WriteString("</g>")
	}
	place(start, vs[0], true)
	for _, v := range vs[1 : len(vs)-1] {
		place(mid, v, false)
	}
	if len(vs) > 1 {
		place(end, vs[len(vs)-1], false)
	}
}

// writeTokens writes a marker's elements as clean writes a document's:
// those ggfx draws, the rest left out with what they hold.
func writeTokens(out *bytes.Buffer, tokens []xml.Token) {
	skip := 0
	var names []string // the names written, for their end tags
	for _, tok := range tokens {
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 || !svgDrawn[t.Name.Local] || t.Name.Local == "svg" {
				skip++
				continue
			}
			names = append(names, writeSVGStart(out, t.Name.Local, t.Attr, false))
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if n := len(names); n > 0 {
				out.WriteString("</" + names[n-1] + ">")
				names = names[:n-1]
			}
		case xml.CharData:
			if skip == 0 {
				_ = xml.EscapeText(out, t)
			}
		}
	}
}

// vertex is where a shape's marker goes, and the way the shape runs there,
// in degrees.
type vertex struct{ x, y, angle float64 }

// shapeVertices are the vertices of a line, polyline, polygon or path, in
// order, each with the way the shape runs through it.
func shapeVertices(name string, attrs []xml.Attr) []vertex {
	get := func(k string) string { return attr(attrs, k) }
	var pts [][2]float64
	switch name {
	case "line":
		n := func(k string) float64 { v, _ := svgAttrNum(attrs, k); return v }
		pts = [][2]float64{{n("x1"), n("y1")}, {n("x2"), n("y2")}}
	case "polyline", "polygon":
		ns := numbers(get("points"))
		for i := 0; i+1 < len(ns); i += 2 {
			pts = append(pts, [2]float64{ns[i], ns[i+1]})
		}
		if name == "polygon" && len(pts) > 0 {
			pts = append(pts, pts[0])
		}
	case "path":
		return pathVertices(get("d"))
	}
	return lineVertices(pts)
}

// lineVertices are pts joined by straight segments, each turned halfway
// between the segments either side of it.
func lineVertices(pts [][2]float64) []vertex {
	if len(pts) < 2 {
		return nil
	}
	vs := make([]vertex, len(pts))
	for i, p := range pts {
		var a float64
		switch {
		case i == 0:
			a = dir(p[0], p[1], pts[1][0], pts[1][1])
		case i == len(pts)-1:
			a = dir(pts[i-1][0], pts[i-1][1], p[0], p[1])
		default:
			a = bisect(dir(pts[i-1][0], pts[i-1][1], p[0], p[1]), dir(p[0], p[1], pts[i+1][0], pts[i+1][1]))
		}
		vs[i] = vertex{p[0], p[1], a}
	}
	return vs
}

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

// numbers are the numbers in a list such as a viewBox's or points'.
func numbers(v string) []float64 {
	var out []float64
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		n, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return out
		}
		out = append(out, n)
	}
	return out
}
