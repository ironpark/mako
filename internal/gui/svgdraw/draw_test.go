package svgdraw

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
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

// A picture whose header says it is larger than the budget is refused
// before it is decoded; one within it is decoded.
func TestAPictureTooLargeIsNotDecoded(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	small := buf.Bytes()
	if img, err := DecodeImage(small, maxPixels); err != nil || img.Bounds().Dx() != 1 {
		t.Fatalf("a small picture: %v", err)
	}
	// The same, saying it is 20000 by 20000: its IHDR, after the 8-byte
	// signature, the chunk's length and its type.
	large := bytes.Clone(small)
	binary.BigEndian.PutUint32(large[16:], 20000)
	binary.BigEndian.PutUint32(large[20:], 20000)
	binary.BigEndian.PutUint32(large[29:], crc32.ChecksumIEEE(large[12:29]))
	if cfg, err := png.DecodeConfig(bytes.NewReader(large)); err != nil || cfg.Width != 20000 {
		t.Fatalf("the large header does not read: %v", err)
	}
	if _, err := DecodeImage(large, ImagePixels); err == nil {
		t.Fatal("a picture saying it is 20000 by 20000 was decoded")
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
