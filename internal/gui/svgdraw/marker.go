package svgdraw

import (
	"bytes"
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

// markers are the markers data defines, by id.
func markers(data []byte) map[string]*svgMarker {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	var found map[string]*svgMarker
	var cur *svgMarker
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return found
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if cur != nil {
				depth++
				cur.tokens = append(cur.tokens, t.Copy())
				continue
			}
			if t.Name.Local != "marker" {
				continue
			}
			m, id := newMarker(t.Attr)
			if id == "" {
				continue
			}
			if found == nil {
				found = map[string]*svgMarker{}
			}
			found[id], cur, depth = m, m, 0
		case xml.EndElement:
			if cur == nil {
				continue
			}
			if depth == 0 {
				cur = nil
				continue
			}
			depth--
			cur.tokens = append(cur.tokens, t)
		case xml.CharData:
			if cur != nil {
				cur.tokens = append(cur.tokens, t.Copy())
			}
		}
	}
}

// newMarker is the marker a <marker>'s attributes describe, and its id.
func newMarker(attrs []xml.Attr) (*svgMarker, string) {
	m := &svgMarker{orient: "0", stroke: true}
	w, h := 3.0, 3.0
	var box []float64
	id := ""
	for _, a := range attrs {
		switch a.Name.Local {
		case "id":
			id = a.Value
		case "refX":
			m.refX, _ = strconv.ParseFloat(a.Value, 64)
		case "refY":
			m.refY, _ = strconv.ParseFloat(a.Value, 64)
		case "markerWidth":
			w, _ = strconv.ParseFloat(a.Value, 64)
		case "markerHeight":
			h, _ = strconv.ParseFloat(a.Value, 64)
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
	return m, id
}

// markerRefs are the markers a shape's attributes name at its start, its
// inner vertices and its end.
func markerRefs(attrs []xml.Attr) (start, mid, end string) {
	for _, a := range attrs {
		switch a.Name.Local {
		case "marker-start":
			start = a.Value
		case "marker-mid":
			mid = a.Value
		case "marker-end":
			end = a.Value
		case "marker":
			start, mid, end = a.Value, a.Value, a.Value
		case "style":
			for decl := range strings.SplitSeq(a.Value, ";") {
				k, v, _ := strings.Cut(decl, ":")
				switch strings.TrimSpace(k) {
				case "marker-start":
					start = v
				case "marker-mid":
					mid = v
				case "marker-end":
					end = v
				case "marker":
					start, mid, end = v, v, v
				}
			}
		}
	}
	return markerID(start), markerID(mid), markerID(end)
}

// markerID is the id a url(#id) names, "" for none.
func markerID(v string) string {
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
	start, mid, end := markerRefs(attrs)
	if start == "" && mid == "" && end == "" {
		return
	}
	vs := shapeVertices(name, attrs)
	if len(vs) == 0 {
		return
	}
	width := 1.0
	transform := ""
	for _, a := range attrs {
		switch a.Name.Local {
		case "stroke-width":
			if w, ok := svgLength(a.Value, 16); ok {
				width = w
			}
		case "transform":
			transform = a.Value
		case "style":
			for decl := range strings.SplitSeq(a.Value, ";") {
				k, v, _ := strings.Cut(decl, ":")
				if strings.TrimSpace(k) == "stroke-width" {
					if w, ok := svgLength(strings.TrimSpace(v), 16); ok {
						width = w
					}
				}
			}
		}
	}
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
	for i, v := range vs {
		switch {
		case i == 0:
			place(start, v, true)
		case i == len(vs)-1:
			place(end, v, false)
		default:
			place(mid, v, false)
		}
	}
}

// writeTokens writes a marker's elements as clean writes a document's:
// those ggfx draws, the rest left out with what they hold.
func writeTokens(out *bytes.Buffer, tokens []xml.Token) {
	skip := 0
	for _, tok := range tokens {
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 || !svgDrawn[t.Name.Local] || t.Name.Local == "svg" {
				skip++
				continue
			}
			writeSVGStart(out, t.Name.Local, t.Attr, false)
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			out.WriteString("</" + t.Name.Local + ">")
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
	get := func(k string) string {
		for _, a := range attrs {
			if a.Name.Local == k {
				return a.Value
			}
		}
		return ""
	}
	var pts [][2]float64
	switch name {
	case "line":
		n := func(k string) float64 { v, _ := strconv.ParseFloat(get(k), 64); return v }
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
	dir := func(a, b [2]float64) float64 { return math.Atan2(b[1]-a[1], b[0]-a[0]) * 180 / math.Pi }
	vs := make([]vertex, len(pts))
	for i, p := range pts {
		var a float64
		switch {
		case i == 0:
			a = dir(p, pts[1])
		case i == len(pts)-1:
			a = dir(pts[i-1], p)
		default:
			a = bisect(dir(pts[i-1], p), dir(p, pts[i+1]))
		}
		vs[i] = vertex{p[0], p[1], a}
	}
	return vs
}

// bisect is the angle halfway between a and b, the shorter way round.
func bisect(a, b float64) float64 {
	d := math.Mod(b-a+540, 360) - 180
	return a + d/2
}

// pathVertices are the vertices of the path data d: where each segment
// starts and ends, turned as the segments run there. A curve runs toward
// and away from its control points; an arc, along its chord.
func pathVertices(d string) []vertex {
	var vs []vertex
	var cx, cy, sx, sy float64 // the current point, and the subpath's start
	var lastOut float64        // the way the last segment left its end
	var c2x, c2y float64       // the last curve's second control point
	prev := byte(0)
	add := func(fromX, fromY, inX, inY, toX, toY, outX, outY float64) {
		in := math.Atan2(inY-fromY, inX-fromX) * 180 / math.Pi
		if inX == fromX && inY == fromY {
			in = math.Atan2(toY-fromY, toX-fromX) * 180 / math.Pi
		}
		out := math.Atan2(toY-outY, toX-outX) * 180 / math.Pi
		if outX == toX && outY == toY {
			out = math.Atan2(toY-fromY, toX-fromX) * 180 / math.Pi
		}
		if n := len(vs); n > 0 && vs[n-1].x == fromX && vs[n-1].y == fromY {
			if n > 1 {
				vs[n-1].angle = bisect(lastOut, in)
			} else {
				vs[n-1].angle = in
			}
		} else {
			vs = append(vs, vertex{fromX, fromY, in})
		}
		vs = append(vs, vertex{toX, toY, out})
		lastOut = out
	}
	toks := pathTokens(d)
	for i := 0; i < len(toks); {
		cmd := prev
		if t := toks[i]; len(t) == 1 && strings.ContainsAny(t, "MmLlHhVvCcSsQqTtAaZz") {
			cmd = t[0]
			i++
		}
		if cmd == 0 {
			return vs
		}
		num := func() (float64, bool) {
			if i >= len(toks) {
				return 0, false
			}
			v, err := strconv.ParseFloat(toks[i], 64)
			if err != nil {
				return 0, false
			}
			i++
			return v, true
		}
		rel := cmd >= 'a'
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = cx, cy
		}
		ok := true
		switch cmd | 0x20 { // lower case
		case 'm':
			x, ok1 := num()
			y, ok2 := num()
			if ok = ok1 && ok2; ok {
				cx, cy = ox+x, oy+y
				sx, sy = cx, cy
				prev = map[bool]byte{true: 'l', false: 'L'}[rel]
				continue
			}
		case 'l', 't':
			x, ok1 := num()
			y, ok2 := num()
			if ok = ok1 && ok2; ok {
				add(cx, cy, ox+x, oy+y, ox+x, oy+y, cx, cy)
				cx, cy = ox+x, oy+y
			}
		case 'h':
			x, ok1 := num()
			if ok = ok1; ok {
				add(cx, cy, ox+x, cy, ox+x, cy, cx, cy)
				cx = ox + x
			}
		case 'v':
			y, ok1 := num()
			if ok = ok1; ok {
				add(cx, cy, cx, oy+y, cx, oy+y, cx, cy)
				cy = oy + y
			}
		case 'c':
			var n [6]float64
			for k := range n {
				if n[k], ok = num(); !ok {
					break
				}
			}
			if ok {
				add(cx, cy, ox+n[0], oy+n[1], ox+n[4], oy+n[5], ox+n[2], oy+n[3])
				c2x, c2y = ox+n[2], oy+n[3]
				cx, cy = ox+n[4], oy+n[5]
			}
		case 's', 'q':
			var n [4]float64
			for k := range n {
				if n[k], ok = num(); !ok {
					break
				}
			}
			if ok {
				in := [2]float64{ox + n[0], oy + n[1]}
				if cmd|0x20 == 's' {
					in = [2]float64{2*cx - c2x, 2*cy - c2y}
				}
				add(cx, cy, in[0], in[1], ox+n[2], oy+n[3], ox+n[0], oy+n[1])
				c2x, c2y = ox+n[0], oy+n[1]
				cx, cy = ox+n[2], oy+n[3]
			}
		case 'a':
			var n [7]float64
			for k := range n {
				if n[k], ok = num(); !ok {
					break
				}
			}
			if ok {
				add(cx, cy, ox+n[5], oy+n[6], ox+n[5], oy+n[6], cx, cy)
				cx, cy = ox+n[5], oy+n[6]
			}
		case 'z':
			if cx != sx || cy != sy {
				add(cx, cy, sx, sy, sx, sy, cx, cy)
			}
			cx, cy = sx, sy
			prev = 0
			continue
		}
		if !ok {
			return vs
		}
		prev = cmd
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
