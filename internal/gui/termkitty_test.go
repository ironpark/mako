package gui

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty"

	"github.com/ironpark/mako/internal/kitty"
)

// withKeyFlags has the program push Kitty keyboard flags, and waits until
// the terminal here has read them.
func withKeyFlags(t *testing.T, link *fakeLink, conn *termConn, flags kitty.KeyFlags) {
	t.Helper()
	link.output(t, flags.Restore())
	waitFor(t, func() bool { return conn.keyFlags()&flags == flags })
}

// A program that asks for every key as a code gets the keys that type
// text as codes too, the text a platform's IME made alone still as text.
func TestKeysThatTypeAreCodesWhenTheProgramAsks(t *testing.T) {
	link, conn, p := newTestTerminal(t)
	withKeyFlags(t, link, conn, kitty.Disambiguate|kitty.EventTypes|kitty.AllKeysAsCodes)
	p.Frame()

	p.TypeText(ggui.Mods{}, ggui.KeyA, "a")
	if got := link.typed(); got != "\x1b[97u" {
		t.Fatalf("a sent %q, want its code", got)
	}
	p.KeyRelease(ggui.Mods{}, ggui.KeyA)
	if got := link.typed(); got != "\x1b[97;1:3u" {
		t.Fatalf("a's release sent %q", got)
	}
	p.TypeText(ggui.Mods{Shift: true}, ggui.KeyA, "A")
	if got := link.typed(); got != "\x1b[97;2u" {
		t.Fatalf("Shift+A sent %q", got)
	}
	p.KeyRelease(ggui.Mods{Shift: true}, ggui.KeyA)
	link.typed()

	// Text no key typed, as an accent menu's, goes as text.
	p.Text("é")
	p.Frame()
	if got := link.typed(); got != "é" {
		t.Fatalf("an IME's text sent %q", got)
	}
}

// A program that hears of event types hears a held key repeat and its
// release; one that does not, the press alone, as ever.
func TestReleasesAndRepeatsReachAProgramThatAsks(t *testing.T) {
	link, conn, p := newTestTerminal(t)
	p.Type(ggui.Mods{}, ggui.KeyArrowUp)
	p.KeyRelease(ggui.Mods{}, ggui.KeyArrowUp)
	if got := link.typed(); got != "\x1b[A" {
		t.Fatalf("legacy Up and its release sent %q", got)
	}

	withKeyFlags(t, link, conn, kitty.Disambiguate|kitty.EventTypes)
	p.Frame()
	p.Type(ggui.Mods{}, ggui.KeyArrowUp, ggui.KeyArrowUp)
	p.KeyRelease(ggui.Mods{}, ggui.KeyArrowUp)
	got := link.typed()
	if !strings.Contains(got, ":2A") || !strings.HasSuffix(got, ":3A") {
		t.Fatalf("Up held and let go sent %q, want a repeat and a release", got)
	}

	// A key that types text is still its text, and its release a code.
	p.TypeText(ggui.Mods{}, ggui.KeyA, "a")
	p.KeyRelease(ggui.Mods{}, ggui.KeyA)
	if got := link.typed(); got != "a\x1b[97;1:3u" {
		t.Fatalf("a pressed and let go sent %q", got)
	}
}

// An image the program shows is placed where it put it, with a texture to
// draw it from, and gone once it deletes it.
func TestImagesTheProgramPlacesAreDrawn(t *testing.T) {
	link, conn, p := newTestTerminal(t)
	p.Frame()
	px := base64.StdEncoding.EncodeToString([]byte{0xff, 0, 0, 0, 0xff, 0, 0, 0, 0xff, 0xff, 0xff, 0xff})
	link.output(t, fmt.Sprintf("\x1b[3;5H\x1b_Ga=T,f=24,s=2,v=2,i=7,c=4,r=2,q=2;%s\x1b\\", px))
	var f frameSnapshot
	waitFor(t, func() bool {
		var err error
		f, err = conn.frame()
		return err == nil && len(f.images) == 1
	})
	img := f.images[0]
	if img.X != 4 || img.Y != 2 || img.Cols != 4 || img.Rows != 2 || img.tex == nil {
		t.Fatalf("placed %+v", img.RenderImagePlacement)
	}
	if img.Layer != gostty.KittyLayerAboveText {
		t.Fatalf("layer %v, want above the text", img.Layer)
	}
	if f.cellPxW == 0 || int(img.Width) != 4*int(f.cellPxW) {
		t.Fatalf("drawn %d pixels wide, want four cells of %d", img.Width, f.cellPxW)
	}
	p.Frame() // painted

	link.output(t, "\x1b_Ga=d,d=I,i=7\x1b\\")
	waitFor(t, func() bool {
		f, err := conn.frame()
		return err == nil && len(f.images) == 0
	})
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.textures) != 0 {
		t.Fatal("the texture of a deleted image is kept")
	}
}
