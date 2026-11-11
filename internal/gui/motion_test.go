package gui

import (
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
)

// A fold's body is built only once it opens, eases open so that what is
// under it moves down by degrees, and is taken away once folded.
func TestRevealEasesAndBuildsLate(t *testing.T) {
	open := ggui.State(false)
	built := 0
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Column(
			reveal(open, func() ggui.Widget {
				built++
				return ggui.Box().Height(100).Width(50)
			}),
			ui.Button("below", func() {}),
		)
	}, ggui.Sz(300, 300))
	defer p.Close()
	p.Advance(0)
	below := func() float64 {
		f, ok := p.Find("below")
		if !ok {
			t.Fatal("no button")
		}
		return f.Rect.Origin.Y
	}
	top := below()
	if built != 0 {
		t.Fatalf("built %d times while closed", built)
	}
	open.Set(true)
	p.Frame() // it starts from the frame that sees it open
	p.Advance(motionDuration / 2)
	if mid := below(); mid <= top || mid >= top+100 {
		t.Fatalf("halfway open the button is at %v, from %v", mid, top)
	}
	p.Advance(motionDuration)
	if end := below(); end != top+100 {
		t.Fatalf("open, the button is at %v, want %v", end, top+100)
	}
	open.Set(false)
	p.Frame()
	p.Advance(motionDuration / 2)
	if mid := below(); mid <= top || mid >= top+100 {
		t.Fatalf("halfway closed the button is at %v", mid)
	}
	p.Advance(2 * motionDuration)
	if end := below(); end != top || built != 1 {
		t.Fatalf("closed, the button is at %v (want %v), built %d", end, top, built)
	}
	open.Set(true)
	p.Advance(2 * motionDuration)
	if built != 2 {
		t.Fatalf("opened again, built %d: the body was kept after it folded", built)
	}
}
