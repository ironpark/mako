package svgdraw

import (
	"encoding/base64"
	"testing"
)

// An image an SVG holds is drawn at the size it takes on the page, which
// its width and its transform can make as large as they like: it is drawn
// within maxPixels, or not at all, rather than asking for more memory
// than there is.
func TestAnEmbedTooLargeIsNotDrawnAtItsSize(t *testing.T) {
	inner := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`))
	for _, image := range []string{
		`<image width="100000000" height="1" href="data:image/svg+xml;base64,` + inner + `"/>`,
		`<image width="1" height="1" transform="scale(1e200)" href="data:image/svg+xml;base64,` + inner + `"/>`,
		`<image width="1e308" height="1e308" x="-1e308" href="data:image/svg+xml;base64,` + inner + `"/>`,
	} {
		p, ok := Draw([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">` + image + `</svg>`))
		if !ok || p.Pixels.Bounds().Dx() != 10*Scale {
			t.Errorf("%s: drawn %v, %+v", image, ok, p.Size)
		}
	}
}

// A large view box is drawn within maxPixels, at less than Scale, and
// still sized as its box.
func TestALargeSVGIsDrawnWithinTheBudget(t *testing.T) {
	p, ok := Draw([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 4000 4000"><rect width="10" height="10"/></svg>`))
	if !ok {
		t.Fatal("not drawn")
	}
	if b := p.Pixels.Bounds(); b.Dx()*b.Dy() > maxPixels*101/100 || p.Size.W != 4000 {
		t.Errorf("drawn %v for a box of %+v", b, p.Size)
	}
}
