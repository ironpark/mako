package svgdraw

import (
	"bytes"
	"cmp"
	"encoding/xml"
	"image"
	"strings"
)

// ggfx applies no clip and no mask, and a badge rounds its corners with a
// clip, a logo fades its edge with a mask. So an element that names one is
// drawn as a layer of its own, the rest of the document in layers before
// and after it: the clip's shapes, or the mask's, are drawn alone, under
// the element's transform, and what they cover, or how light they are,
// lets the element's layer through. The layers are laid over each other in
// the document's order.

// svgDoc is a cleaned SVG, and where it is cut into layers.
type svgDoc struct {
	data []byte
	cuts []svgCut
	defs []byte // its defs and gradients, for each layer to draw from
}

// svgCut is an element drawn through a clip or a mask: its bytes in the
// document, the elements open around it, and how the clip is placed.
type svgCut struct {
	start, end int
	around     []openTag
	clip       *svgClip
	transform  string
}

// openTag is an element written and not yet ended: its name, and its
// start tag's bytes in the document.
type openTag struct {
	name       string
	start, end int
}

// clipRef is the id of the clip or mask an element's attributes name, ""
// for none.
func clipRef(attrs []xml.Attr) string {
	props := svgProps(attrs)
	return urlID(cmp.Or(props["clip-path"], props["mask"]))
}

// layers draws doc at the scale k gives, each of its layers through its
// clip or mask, and lays them over each other.
func (doc svgDoc) layers(k func(w float64) float64) (px *image.RGBA, x, y, w, h float64, ok bool) {
	if len(doc.cuts) == 0 {
		return raster(doc.data, k)
	}
	// add draws the layer of the document's bytes from a to b, the elements
	// before and after it open around it, through the clip or mask through
	// draws, if any.
	add := func(a, b int, before, after []openTag, through []byte, mask bool) bool {
		if px != nil && !startsAny(doc.data[a:b]) {
			return true // nothing of its own to draw
		}
		layer, lx, ly, lw, lh, ok := raster(doc.segment(a, b, before, after), k)
		if !ok {
			return false
		}
		var by *image.RGBA
		if through != nil {
			if by, _, _, _, _, ok = raster(through, k); !ok {
				by = nil
			}
		}
		if px == nil && by == nil {
			px, x, y, w, h = layer, lx, ly, lw, lh
			return true
		}
		if px == nil {
			px, x, y, w, h = image.NewRGBA(layer.Bounds()), lx, ly, lw, lh
		}
		over(px, layer, by, mask)
		return true
	}
	at := 0
	var around []openTag // the elements open where the last layer ended
	for _, c := range doc.cuts {
		if !add(at, c.start, around, c.around, nil, false) {
			return nil, 0, 0, 0, 0, false
		}
		add(c.start, c.end, c.around, c.around, doc.through(c), c.clip.mask)
		at, around = c.end, c.around
	}
	if !add(at, len(doc.data), around, nil, nil, false) {
		return nil, 0, 0, 0, 0, false
	}
	return px, x, y, w, h, true
}

// segment is the document's bytes from a to b as a document of their own:
// the elements open at a opened again before them, with the defs, and
// those open at b ended after.
func (doc svgDoc) segment(a, b int, before, after []openTag) []byte {
	var out bytes.Buffer
	if a > 0 {
		doc.open(&out, before)
	}
	out.Write(doc.data[a:b])
	if b < len(doc.data) {
		closeTags(&out, after)
	}
	return out.Bytes()
}

// open writes the root's start tag, the defs, and the start tags of the
// elements around inside the root.
func (doc svgDoc) open(out *bytes.Buffer, around []openTag) {
	if len(around) == 0 {
		return
	}
	out.Write(doc.data[around[0].start:around[0].end])
	out.Write(doc.defs)
	for _, t := range around[1:] {
		out.Write(doc.data[t.start:t.end])
	}
}

// closeTags ends the elements around, innermost first.
func closeTags(out *bytes.Buffer, around []openTag) {
	for i := len(around) - 1; i >= 0; i-- {
		out.WriteString("</" + around[i].name + ">")
	}
}

// through is the document of c's clip or mask alone, where it lies over c:
// a clip's shapes in white, a mask's as they are.
func (doc svgDoc) through(c svgCut) []byte {
	var out bytes.Buffer
	doc.open(&out, c.around)
	out.WriteString(`<g transform="`)
	out.WriteString(strings.ReplaceAll(c.transform, `"`, "&quot;"))
	out.WriteString(`">`)
	if !c.clip.mask {
		out.WriteString(`<g fill="#fff" stroke="none">`)
	}
	writeTokens(&out, c.clip.tokens)
	if !c.clip.mask {
		out.WriteString("</g>")
	}
	out.WriteString("</g>")
	closeTags(&out, c.around)
	return out.Bytes()
}

// startsAny reports whether data starts an element: holds a start tag,
// not only end tags and the space between.
func startsAny(data []byte) bool {
	for i := 0; i+1 < len(data); i++ {
		if data[i] == '<' && data[i+1] != '/' {
			return true
		}
	}
	return false
}

// over lays layer over dst, letting through only what by covers, where by
// is not nil: by its coverage for a clip, by its light for a mask.
// Premultiplied, so each channel scales alike.
func over(dst, layer, by *image.RGBA, mask bool) {
	if dst.Bounds() != layer.Bounds() || by != nil && by.Bounds() != layer.Bounds() {
		return
	}
	d, l := dst.Pix, layer.Pix
	for i := 0; i+3 < len(l); i += 4 {
		if l[i+3] == 0 {
			continue
		}
		f := uint32(255)
		if by != nil {
			p := by.Pix[i : i+4 : i+4]
			if mask {
				// Premultiplied, so the light is already its alpha's.
				f = (2126*uint32(p[0]) + 7152*uint32(p[1]) + 722*uint32(p[2])) / 10000
			} else {
				f = uint32(p[3])
			}
			if f == 0 {
				continue
			}
		}
		a := uint32(l[i+3]) * f / 255
		for j := range 4 {
			src := uint32(l[i+j]) * f / 255
			d[i+j] = uint8(src + uint32(d[i+j])*(255-a)/255)
		}
	}
}
