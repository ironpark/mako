package gui

import (
	"image"
	"runtime"
	"testing"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gui/mdview"
	"github.com/ironpark/mako/internal/gui/svgdraw"
)

// An image larger than it shows is kept with fewer pixels, at the size it
// had as read; a smaller one is kept as it came.
func TestAnImageIsKeptNoLargerThanItShows(t *testing.T) {
	box := ggui.Sz(480, 360)
	s := shrinkImage(decodedImage{Img: image.NewRGBA(image.Rect(0, 0, 4000, 1000))}, box)
	if b := s.Img.Bounds(); b.Dx() != 480 || b.Dy() != 120 || s.Shown != ggui.Sz(4000, 1000) {
		t.Fatalf("shrunk to %v, size %v", b, s.Shown)
	}
	if v := asMDImage(s, nil); v.Size != ggui.Sz(4000, 1000) {
		t.Fatalf("shown as %v", v.Size)
	}
	small := image.NewRGBA(image.Rect(0, 0, 30, 20))
	if s := shrinkImage(decodedImage{Img: small}, box); s.Img != image.Image(small) || s.Shown != (ggui.Size{}) {
		t.Fatal("a small image was scaled")
	}
	drawn := &svgdraw.Picture{Pixels: image.NewRGBA(image.Rect(0, 0, 2000, 2000)), Size: ggui.Sz(1000, 1000)}
	s = shrinkImage(decodedImage{Drawn: drawn}, box)
	if b := s.Drawn.Pixels.Bounds(); b.Dx() != 360 || s.Drawn.Size != drawn.Size || drawn.Pixels.Bounds().Dx() != 2000 {
		t.Fatalf("drawn shrunk to %v, size %v", b, s.Drawn.Size)
	}
}

// The images kept hold no more pixels than the cache's bound: those used
// longest ago go first, though one still shown is found again.
func TestTheImagesKeptAreBounded(t *testing.T) {
	c := newImageCache()
	c.max = 3 * 100 * 100 * 4
	load := func(key string) *ggui.StateValue[mdview.Image] {
		img := c.add(key)
		c.set(key, img, mdview.Image{Img: ggfx.NewImage(100, 100), Size: ggui.Sz(100, 100)})
		return img
	}
	load("a")
	load("b")
	shown := load("c")
	c.get("a")
	load("d") // lets go of b
	load("e") // and of c, which is still shown
	runtime.GC()
	if _, ok := c.get("b"); ok {
		t.Fatal("the image used longest ago, shown nowhere, was kept")
	}
	if img, ok := c.get("c"); !ok || img != shown {
		t.Fatal("an image still shown was read again")
	}
	runtime.KeepAlive(shown)
	if c.bytes > c.max || c.kept.Len() != 3 {
		t.Fatalf("%d bytes in %d kept", c.bytes, c.kept.Len())
	}
}
