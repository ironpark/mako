// Package svgdraw draws the SVGs a Markdown page shows: what ggfx can
// draw of them as pixels, and their text, taken out, set over those in
// the window's own font.
package svgdraw

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"image/color"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
)

// An SVG on a page is drawn by ggfx, which knows paths, shapes, groups,
// gradients and use, and fails on anything else. A README's SVG is seldom
// so plain: a badge clips its corners and is mostly text, a diagram wraps
// shapes in links. So the document is cleaned first: what ggfx cannot
// draw is left out, a clip or mask not applied, a link drawn as a group,
// and its text taken out, to be set over the drawing in the window's own
// font.

// Text is a run of an SVG's text, placed in its view box's units.
type Text struct {
	text      string
	x, y      float64 // the baseline's start, middle or end, as anchor says
	size      float64
	anchor    string // start, middle or end
	color     color.Color
	bold      bool
	monospace bool
}

// svgDrawn are the elements ggfx draws. a and switch are drawn as groups;
// anything else is left out with what it holds.
var svgDrawn = map[string]bool{
	"svg": true, "g": true, "line": true, "stop": true, "rect": true, "circle": true,
	"ellipse": true, "polyline": true, "polygon": true, "path": true, "desc": true,
	"defs": true, "style": true, "title": true, "linearGradient": true,
	"radialGradient": true, "use": true,
}

// svgUndrawnAttrs are the attributes of effects ggfx does not apply; the
// shapes they would affect are drawn without them.
var svgUndrawnAttrs = map[string]bool{"clip-path": true, "mask": true, "filter": true}

// svgStyle is what an element's text inherits.
type svgStyle struct {
	m         svgMatrix
	fill      color.Color
	opacity   float64 // fill-opacity times opacity
	size      float64
	anchor    string
	bold      bool
	monospace bool
	hidden    bool
}

// svgEmbed is an image an SVG holds in a data: address, as a badge holds
// its logo: placed at x, y, w by h of its user space, under m.
type svgEmbed struct {
	data       []byte
	svg        bool
	m          svgMatrix
	x, y, w, h float64
}

// clean is data with only what ggfx draws, and the text and the images
// it holds taken out of it.
func clean(data []byte) ([]byte, []Text, []svgEmbed, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	var out bytes.Buffer
	var texts []Text
	var embeds []svgEmbed
	styles := []svgStyle{{m: identity(), fill: color.Black, opacity: 1, size: 16, anchor: "start"}}
	skip := 0        // depth inside an element left out
	var text *svgRun // the text element being read
	textDepth := 0   // depth inside it
	rooted := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if skip > 0 {
				skip++
				continue
			}
			if text != nil {
				textDepth++
				st := styles[len(styles)-1].with(t.Attr)
				styles = append(styles, st)
				text.move(t.Attr)
				continue
			}
			if t.Name.Space != "" && t.Name.Space != svgNamespace {
				skip = 1 // an editor's own, as Inkscape's
				continue
			}
			st := styles[len(styles)-1].with(t.Attr)
			if name == "text" {
				styles = append(styles, st)
				text, textDepth = &svgRun{}, 1
				text.move(t.Attr)
				continue
			}
			if name == "image" && !st.hidden {
				if e, ok := embeddedImage(t.Attr, st.m); ok && len(embeds) < 16 {
					embeds = append(embeds, e)
				}
			}
			if name == "a" || name == "switch" {
				name = "g"
			}
			if !svgDrawn[name] {
				skip = 1
				continue
			}
			styles = append(styles, st)
			writeSVGStart(&out, name, t.Attr, !rooted)
			rooted = true
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			styles = styles[:len(styles)-1]
			if text != nil {
				if textDepth--; textDepth == 0 {
					text.flush()
					texts = append(texts, text.done...)
					text = nil
				}
				continue
			}
			name := t.Name.Local
			if name == "a" || name == "switch" {
				name = "g"
			}
			out.WriteString("</" + name + ">")
		case xml.CharData:
			if skip > 0 {
				continue
			}
			if text != nil {
				text.add(string(t), styles[len(styles)-1])
				continue
			}
			_ = xml.EscapeText(&out, t)
		}
	}
	if !rooted {
		return nil, nil, nil, errors.New("svg: no document")
	}
	return out.Bytes(), texts, embeds, nil
}

// embeddedImage is an <image> whose picture is in its data: address.
func embeddedImage(attrs []xml.Attr, m svgMatrix) (svgEmbed, bool) {
	var href string
	for _, a := range attrs {
		if a.Name.Local == "href" {
			href = strings.TrimSpace(a.Value)
		}
	}
	rest, ok := strings.CutPrefix(href, "data:")
	meta, payload, ok2 := strings.Cut(rest, ",")
	if !ok || !ok2 {
		return svgEmbed{}, false
	}
	var data []byte
	if strings.HasSuffix(meta, ";base64") {
		var err error
		if data, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(payload), "")); err != nil {
			return svgEmbed{}, false
		}
	} else {
		s, err := url.PathUnescape(payload)
		if err != nil {
			return svgEmbed{}, false
		}
		data = []byte(s)
	}
	x, _ := svgAttrNum(attrs, "x")
	y, _ := svgAttrNum(attrs, "y")
	w, okW := svgAttrNum(attrs, "width")
	h, okH := svgAttrNum(attrs, "height")
	if !okW || !okH || w <= 0 || h <= 0 {
		return svgEmbed{}, false
	}
	return svgEmbed{data: data, svg: strings.Contains(meta, "svg"), m: m, x: x, y: y, w: w, h: h}, true
}

const svgNamespace = "http://www.w3.org/2000/svg"

// writeSVGStart writes an element's start tag with the attributes ggfx
// reads: by their own names, the namespaces and effects left out.
func writeSVGStart(out *bytes.Buffer, name string, attrs []xml.Attr, root bool) {
	out.WriteString("<" + name)
	if root {
		out.WriteString(` xmlns="` + svgNamespace + `"`)
	}
	for _, a := range attrs {
		local := a.Name.Local
		switch {
		case a.Name.Space == "xmlns" || local == "xmlns":
			continue
		case a.Name.Space != "" && a.Name.Space != "xlink" && a.Name.Space != "http://www.w3.org/1999/xlink":
			continue
		case svgUndrawnAttrs[local]:
			continue
		}
		v := a.Value
		if local == "style" {
			v = withoutUndrawn(v)
		}
		if strings.EqualFold(strings.TrimSpace(v), "currentColor") {
			v = "#000"
		}
		out.WriteString(" " + local + `="`)
		_ = xml.EscapeText(out, []byte(v))
		out.WriteString(`"`)
	}
	out.WriteString(">")
}

// withoutUndrawn is a style attribute without the effects ggfx does not
// apply.
func withoutUndrawn(style string) string {
	var keep []string
	for decl := range strings.SplitSeq(style, ";") {
		k, _, _ := strings.Cut(decl, ":")
		if !svgUndrawnAttrs[strings.TrimSpace(k)] && strings.TrimSpace(decl) != "" {
			keep = append(keep, decl)
		}
	}
	return strings.Join(keep, ";")
}

// svgRun gathers a text element's runs: each tspan placed anew starts one.
type svgRun struct {
	cur  Text
	x, y float64
	done []Text
}

// move places what follows where an element's x, y, dx and dy say.
func (r *svgRun) move(attrs []xml.Attr) {
	x, okX := svgAttrNum(attrs, "x")
	y, okY := svgAttrNum(attrs, "y")
	dx, _ := svgAttrNum(attrs, "dx")
	dy, _ := svgAttrNum(attrs, "dy")
	if !okX && !okY && dx == 0 && dy == 0 {
		return
	}
	r.flush()
	if okX {
		r.x = x
	}
	if okY {
		r.y = y
	}
	r.x += dx
	r.y += dy
}

// add appends text in st to the run.
func (r *svgRun) add(s string, st svgStyle) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || st.hidden || st.fill == nil {
		return
	}
	if r.cur.text == "" {
		px, py := st.m.apply(r.x, r.y)
		c := color.NRGBAModel.Convert(st.fill).(color.NRGBA)
		c.A = uint8(math.Round(float64(c.A) * min(max(st.opacity, 0), 1)))
		r.cur = Text{x: px, y: py, size: st.size * st.m.scale(), anchor: st.anchor,
			color: c, bold: st.bold, monospace: st.monospace}
		r.cur.text = s
		return
	}
	r.cur.text += " " + s
}

// flush ends the run being gathered.
func (r *svgRun) flush() {
	if r.cur.text != "" && r.cur.size > 0 && r.cur.color.(color.NRGBA).A > 0 {
		r.done = append(r.done, r.cur)
	}
	r.cur = Text{}
}

// with is the style an element with attrs has under st.
func (st svgStyle) with(attrs []xml.Attr) svgStyle {
	props := map[string]string{}
	for _, a := range attrs {
		if a.Name.Local == "style" {
			for decl := range strings.SplitSeq(a.Value, ";") {
				if k, v, ok := strings.Cut(decl, ":"); ok {
					props[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			continue
		}
		if _, set := props[a.Name.Local]; !set {
			props[a.Name.Local] = strings.TrimSpace(a.Value)
		}
	}
	if v, ok := props["transform"]; ok {
		st.m = st.m.mul(parseSVGTransform(v))
	}
	if v, ok := props["fill"]; ok {
		if v == "none" {
			st.fill = nil
		} else if c, ok := parseSVGColor(v); ok {
			st.fill = c
		}
	}
	for _, k := range []string{"fill-opacity", "opacity"} {
		if v, ok := props[k]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				st.opacity *= f
			}
		}
	}
	if v, ok := props["font-size"]; ok {
		if f, ok := svgLength(v, st.size); ok {
			st.size = f
		}
	}
	if v, ok := props["text-anchor"]; ok {
		st.anchor = v
	}
	if v, ok := props["font-weight"]; ok {
		n, err := strconv.Atoi(v)
		st.bold = v == "bold" || v == "bolder" || (err == nil && n >= 600)
	}
	if v, ok := props["font-family"]; ok {
		v = strings.ToLower(v)
		st.monospace = strings.Contains(v, "mono") || strings.Contains(v, "courier") || strings.Contains(v, "consolas")
	}
	if props["display"] == "none" || props["visibility"] == "hidden" {
		st.hidden = true
	}
	return st
}

// svgAttrNum is a number attribute: the first of a list, as text's x may
// be.
func svgAttrNum(attrs []xml.Attr, name string) (float64, bool) {
	for _, a := range attrs {
		if a.Name.Local == name && a.Name.Space == "" {
			first, _, _ := strings.Cut(strings.TrimSpace(strings.ReplaceAll(a.Value, ",", " ")), " ")
			return svgLength(first, 16)
		}
	}
	return 0, false
}

// svgLength is a length in user units: px, pt, em, or none.
func svgLength(v string, em float64) (float64, bool) {
	v = strings.TrimSpace(v)
	unit := 1.0
	switch {
	case strings.HasSuffix(v, "px"):
		v = v[:len(v)-2]
	case strings.HasSuffix(v, "pt"):
		v, unit = v[:len(v)-2], 4.0/3
	case strings.HasSuffix(v, "em"):
		v, unit = v[:len(v)-2], em
	case strings.HasSuffix(v, "%"):
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f * unit, true
}

// parseSVGColor reads #rgb, #rrggbb, rgb() and the common names.
func parseSVGColor(v string) (color.Color, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if c, ok := svgColorNames[v]; ok {
		return c, true
	}
	if hex, ok := strings.CutPrefix(v, "#"); ok {
		if len(hex) == 3 {
			hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
		}
		n, err := strconv.ParseUint(hex, 16, 32)
		if err != nil || len(hex) != 6 {
			return nil, false
		}
		return color.NRGBA{uint8(n >> 16), uint8(n >> 8), uint8(n), 0xff}, true
	}
	if args, ok := strings.CutPrefix(v, "rgb("); ok {
		parts := strings.Split(strings.TrimSuffix(args, ")"), ",")
		if len(parts) != 3 {
			return nil, false
		}
		var c [3]uint8
		for i, p := range parts {
			p = strings.TrimSpace(p)
			scale := 1.0
			if pct, ok := strings.CutSuffix(p, "%"); ok {
				p, scale = pct, 2.55
			}
			f, err := strconv.ParseFloat(p, 64)
			if err != nil {
				return nil, false
			}
			c[i] = uint8(min(max(f*scale, 0), 255))
		}
		return color.NRGBA{c[0], c[1], c[2], 0xff}, true
	}
	return nil, false
}

var svgColorNames = map[string]color.Color{
	"black": color.Black, "white": color.White, "currentcolor": color.Black,
	"red": color.NRGBA{0xff, 0, 0, 0xff}, "green": color.NRGBA{0, 0x80, 0, 0xff},
	"blue": color.NRGBA{0, 0, 0xff, 0xff}, "gray": color.NRGBA{0x80, 0x80, 0x80, 0xff},
	"grey": color.NRGBA{0x80, 0x80, 0x80, 0xff}, "orange": color.NRGBA{0xff, 0xa5, 0, 0xff},
	"yellow": color.NRGBA{0xff, 0xff, 0, 0xff}, "purple": color.NRGBA{0x80, 0, 0x80, 0xff},
	"transparent": color.NRGBA{},
}

// svgMatrix is an affine transform: a b c d e f, as SVG writes one.
type svgMatrix [6]float64

func identity() svgMatrix { return svgMatrix{1, 0, 0, 1, 0, 0} }

// mul is m, then n inside it.
func (m svgMatrix) mul(n svgMatrix) svgMatrix {
	return svgMatrix{
		m[0]*n[0] + m[2]*n[1], m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3], m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4], m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

func (m svgMatrix) apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// scale is how much m scales a length, on average.
func (m svgMatrix) scale() float64 { return math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2])) }

// parseSVGTransform reads a transform list; what it cannot read is left
// out.
func parseSVGTransform(v string) svgMatrix {
	m := identity()
	for {
		open := strings.IndexByte(v, '(')
		end := strings.IndexByte(v, ')')
		if open < 0 || end < open {
			return m
		}
		name := strings.ToLower(strings.Trim(v[:open], " ,\t\n"))
		var n []float64
		for _, f := range strings.FieldsFunc(v[open+1:end], func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			if x, err := strconv.ParseFloat(f, 64); err == nil {
				n = append(n, x)
			}
		}
		v = v[end+1:]
		arg := func(i int, def float64) float64 {
			if i < len(n) {
				return n[i]
			}
			return def
		}
		switch name {
		case "translate":
			m = m.mul(svgMatrix{1, 0, 0, 1, arg(0, 0), arg(1, 0)})
		case "scale":
			sx := arg(0, 1)
			m = m.mul(svgMatrix{sx, 0, 0, arg(1, sx), 0, 0})
		case "matrix":
			if len(n) == 6 {
				m = m.mul(svgMatrix(n))
			}
		case "rotate":
			a := arg(0, 0) * math.Pi / 180
			cx, cy := arg(1, 0), arg(2, 0)
			m = m.mul(svgMatrix{1, 0, 0, 1, cx, cy}).
				mul(svgMatrix{math.Cos(a), math.Sin(a), -math.Sin(a), math.Cos(a), 0, 0}).
				mul(svgMatrix{1, 0, 0, 1, -cx, -cy})
		}
	}
}

// Labels sets an SVG's text over its drawing, scaled as it is.
type Labels struct {
	Texts    []Text
	Origin   ggui.Point // the view box's corner, in its units
	Scale    float64    // window units for each of the view box's
	laid     []ggui.Widget
	at       []ggui.Point
	measured []ggui.Size
}

// Layout sets each text at its place and size.
func (l *Labels) Layout(env ggui.Env) {
	l.laid, l.at, l.measured = l.laid[:0], l.at[:0], l.measured[:0]
	for _, t := range l.Texts {
		w := ggui.Text(t.text).Size(t.size * l.Scale).Color(t.color).NoWrap().LineHeight(1)
		if t.bold {
			w = w.Weight(ggui.WeightBold)
		}
		if t.monospace {
			w = w.Font(ggui.DefaultMonoFont())
		}
		size := w.Layout(ggui.Loose(ggui.Sz(math.Inf(1), math.Inf(1))), env)
		base, ok := w.Baseline()
		if !ok {
			base = size.H * 0.8
		}
		x := (t.x - l.Origin.X) * l.Scale
		switch t.anchor {
		case "middle":
			x -= size.W / 2
		case "end":
			x -= size.W
		}
		l.laid = append(l.laid, w)
		l.at = append(l.at, ggui.Pt(x, (t.y-l.Origin.Y)*l.Scale-base))
		l.measured = append(l.measured, size)
	}
}

// Paint paints the texts Layout set over r, the drawing's rectangle.
func (l *Labels) Paint(dst *ggui.Canvas, r ggui.Rect) {
	clip := dst.Clip(r)
	for i, w := range l.laid {
		at := l.at[i]
		clip.Paint(w, ggui.Rct(ggui.Pt(r.Origin.X+at.X, r.Origin.Y+at.Y), l.measured[i]))
	}
}
