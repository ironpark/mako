package svgdraw

import (
	"bytes"
	"cmp"
	"encoding/xml"
	"image"
	"image/draw"
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
	id := ""
	for _, a := range attrs {
		switch a.Name.Local {
		case "clip-path", "mask":
			id = cmp.Or(markerID(a.Value), id)
		case "style":
			for decl := range strings.SplitSeq(a.Value, ";") {
				k, v, _ := strings.Cut(decl, ":")
				if k = strings.TrimSpace(k); k == "clip-path" || k == "mask" {
					id = cmp.Or(markerID(v), id)
				}
			}
		}
	}
	return id
}

// layers draws doc at the scale k gives, each of its layers through its
// clip or mask, and lays them over each other.
func (doc svgDoc) layers(k func(w float64) float64) (px *image.RGBA, x, y, w, h float64, ok bool) {
	if len(doc.cuts) == 0 {
		return raster(doc.data, k)
	}
	add := func(data []byte, through []byte, mask bool) bool {
		layer, lx, ly, lw, lh, ok := raster(data, k)
		if !ok {
			return false
		}
		if through != nil {
			if by, _, _, _, _, ok := raster(through, k); ok {
				apply(layer, by, mask)
			}
		}
		if px == nil {
			px, x, y, w, h = layer, lx, ly, lw, lh
			return true
		}
		draw.Draw(px, px.Bounds(), layer, image.Point{}, draw.Over)
		return true
	}
	at := 0
	var around []openTag // the elements open where the last layer ended
	for _, c := range doc.cuts {
		if !add(doc.segment(at, c.start, around, c.around), nil, false) {
			return nil, 0, 0, 0, 0, false
		}
		add(doc.segment(c.start, c.end, c.around, c.around), doc.through(c), c.clip.mask)
		at, around = c.end, c.around
	}
	if !add(doc.segment(at, len(doc.data), around, nil), nil, false) {
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

// apply lets through of layer only what by covers: by its coverage for a
// clip, by its light for a mask.
func apply(layer, by *image.RGBA, mask bool) {
	if layer.Bounds() != by.Bounds() {
		return
	}
	for i := 0; i+3 < len(layer.Pix); i += 4 {
		p := by.Pix[i : i+4 : i+4]
		var f uint32
		if mask {
			// Premultiplied, so the light is already its alpha's.
			f = (2126*uint32(p[0]) + 7152*uint32(p[1]) + 722*uint32(p[2])) / 10000
		} else {
			f = uint32(p[3])
		}
		for j := range 4 {
			layer.Pix[i+j] = uint8(uint32(layer.Pix[i+j]) * f / 255)
		}
	}
}
