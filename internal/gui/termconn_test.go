package gui

import (
	"testing"

	"github.com/ironpark/ggui"
)

func TestOutputRepaintsTheTerminalWhereItWasPainted(t *testing.T) {
	wakes := 0
	var asked []ggui.Rect
	conn := &termConn{wake: func() { wakes++ }}
	repaint := func(r ggui.Rect) { asked = append(asked, r) }
	conn.repaint.Store(&repaint)

	// Never painted: the window whole.
	conn.requestPaint()
	if wakes != 1 || len(asked) != 0 {
		t.Fatalf("unpainted: %d wakes, %v asked; want a wake", wakes, asked)
	}
	conn.pending.Store(false)
	at := ggui.Rct(ggui.Pt(300, 40), ggui.Sz(800, 600))
	conn.painted(at, true)
	conn.requestPaint()
	conn.requestPaint() // coalesced until painted
	if wakes != 1 || len(asked) != 1 || asked[0] != at {
		t.Fatalf("painted: %d wakes, %v asked; want %v asked once", wakes, asked, at)
	}
	conn.pending.Store(false)
	conn.painted(ggui.Rect{}, false) // under a transform
	conn.requestPaint()
	if wakes != 2 {
		t.Fatalf("placed nowhere: %d wakes; want the window whole", wakes)
	}
}
