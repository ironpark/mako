package kitty

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/ironpark/gostty"
)

func newVT(t *testing.T) *gostty.Session {
	t.Helper()
	vt, err := gostty.New(40, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vt.Close() })
	if err := vt.ResizeCells(40, 10, 8, 16); err != nil {
		t.Fatal(err)
	}
	return vt
}

func placementsOf(t *testing.T, term *gostty.Terminal) []gostty.KittyPlacement {
	t.Helper()
	images, err := gostty.NewKittyImages()
	if err != nil {
		t.Fatal(err)
	}
	defer images.Close()
	if err := images.Update(term); err != nil {
		t.Fatal(err)
	}
	out := make([]gostty.KittyPlacement, images.PlacementCount())
	n, _ := images.Placements(out)
	return out[:n]
}

// rgb is a 2x2 image of red, green, blue and white, as base64.
var rgb = base64.StdEncoding.EncodeToString([]byte{0xff, 0, 0, 0, 0xff, 0, 0, 0, 0xff, 0xff, 0xff, 0xff})

func TestPixelsOfAnRGBImage(t *testing.T) {
	vt := newVT(t)
	fmt.Fprintf(vt, "\x1b_Ga=T,f=24,s=2,v=2,i=7,q=2;%s\x1b\\", rgb)
	img := Pixels(vt.Terminal(), 7)
	if img == nil {
		t.Fatal("no pixels for a transmitted image")
	}
	if got := img.NRGBAAt(1, 0); got != (color.NRGBA{0, 0xff, 0, 0xff}) {
		t.Fatalf("pixel (1,0) = %v, want green", got)
	}
	if Pixels(vt.Terminal(), 8) != nil {
		t.Fatal("pixels for an image never sent")
	}
}

// A PNG is decoded as it arrives, and a program asking whether PNGs work
// is told they do.
func TestPNGsAreDecoded(t *testing.T) {
	vt := newVT(t)
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	src.Set(2, 1, color.NRGBA{10, 20, 30, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(buf.Bytes())
	fmt.Fprintf(vt, "\x1b_Ga=q,f=100,i=31;%s\x1b\\", data)
	var reply bytes.Buffer
	_ = vt.Stream().WriteReplies(&reply)
	if !strings.Contains(reply.String(), ";OK") {
		t.Fatalf("PNG query answered %q, want OK", reply.String())
	}
	fmt.Fprintf(vt, "\x1b_Ga=T,f=100,i=3,q=2;%s\x1b\\", data)
	img := Pixels(vt.Terminal(), 3)
	if img == nil || img.Rect.Dx() != 3 || img.Rect.Dy() != 2 {
		t.Fatalf("decoded PNG = %v", img)
	}
	if got := img.NRGBAAt(2, 1); got != (color.NRGBA{10, 20, 30, 255}) {
		t.Fatalf("pixel (2,1) = %v", got)
	}
}

// A screen fed to a fresh terminal with its replay has its images where
// they were, and the cursor where it was.
func TestReplayPlacesTheImagesAgain(t *testing.T) {
	vt := newVT(t)
	fmt.Fprintf(vt, "hello\r\n\x1b[3;5H\x1b_Ga=T,f=24,s=2,v=2,i=7,p=4,c=3,r=2,q=2;%s\x1b\\", rgb)
	fmt.Fprintf(vt, "\x1b[8;2Hthere")
	before := placementsOf(t, vt.Terminal())
	if len(before) != 1 {
		t.Fatalf("placements = %d, want 1", len(before))
	}

	text, err := vt.FormatString(gostty.FormatOptions{Format: gostty.FormatterFormatVt, Cursor: true})
	if err != nil {
		t.Fatal(err)
	}
	replay := Replay(vt.Terminal())
	if replay == "" {
		t.Fatal("nothing replayed for a screen with an image")
	}
	fresh := newVT(t)
	fresh.WriteString(text + replay)

	after := placementsOf(t, fresh.Terminal())
	if len(after) != 1 {
		t.Fatalf("placements after replay = %d, want 1", len(after))
	}
	a, b := before[0], after[0]
	if a.ImageID != b.ImageID || a.PlacementID != b.PlacementID || a.ViewportCol != b.ViewportCol || a.ViewportRow != b.ViewportRow ||
		a.GridCols != b.GridCols || a.GridRows != b.GridRows || a.PixelWidth != b.PixelWidth {
		t.Fatalf("replayed placement %+v\nwant %+v", b, a)
	}
	if Pixels(fresh.Terminal(), 7) == nil {
		t.Fatal("the image's pixels were not sent again")
	}
	c1, c2 := vt.Terminal().CursorX(), fresh.Terminal().CursorX()
	r1, r2 := vt.Terminal().CursorY(), fresh.Terminal().CursorY()
	if c1 != c2 || r1 != r2 {
		t.Fatalf("cursor after replay at (%d,%d), want (%d,%d)", c2, r2, c1, r1)
	}
}

func TestNoImagesReplayNothing(t *testing.T) {
	vt := newVT(t)
	vt.WriteString("text alone")
	if got := Replay(vt.Terminal()); got != "" {
		t.Fatalf("replay of a screen without images = %q", got)
	}
}
