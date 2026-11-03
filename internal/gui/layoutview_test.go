package gui

import (
	"testing"

	"github.com/ironpark/ggui"
)

// A press over a pane makes it current even where what is under it takes
// the press, as a chat's feed and composer do, and they still get it.
func TestAPressAnywhereInAPaneIsSeen(t *testing.T) {
	var watched, tapped int
	child := ggui.Tap(ggui.Box().Size(200, 100), func() { tapped++ })
	p := ggui.NewProbe(&pressWatch{child: child, fn: func() { watched++ }}, ggui.Sz(200, 100))
	defer p.Close()
	p.Click(ggui.Pt(50, 50))
	if watched != 1 || tapped != 1 {
		t.Fatalf("watched %d, tapped %d: want both once", watched, tapped)
	}
	p.Scroll(ggui.Pt(50, 50), ggui.Pt(0, 3))
	if watched != 1 {
		t.Fatal("a wheel over the pane made it current")
	}
}
