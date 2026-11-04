package gui

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/ggui"
)

func TestOutputRepaintsTheTerminalWhereItWasPainted(t *testing.T) {
	var wakes atomic.Int32
	var asked []ggui.Rect
	conn := &termConn{wake: func() { wakes.Add(1) }}
	repaint := func(r ggui.Rect) { asked = append(asked, r) }
	conn.repaint.Store(&repaint)

	// Never painted: the window whole.
	conn.requestPaint()
	if wakes.Load() != 1 || len(asked) != 0 {
		t.Fatalf("unpainted: %d wakes, %v asked; want a wake", wakes.Load(), asked)
	}
	conn.pending.Store(false)
	at := ggui.Rct(ggui.Pt(300, 40), ggui.Sz(800, 600))
	conn.painted(at, true)
	conn.requestPaint()
	conn.requestPaint() // coalesced until painted
	if wakes.Load() != 1 || len(asked) != 1 || asked[0] != at {
		t.Fatalf("painted: %d wakes, %v asked; want %v asked once", wakes.Load(), asked, at)
	}
	conn.pending.Store(false) // painted in time
	conn.painted(ggui.Rect{}, false) // under a transform
	conn.requestPaint()
	if wakes.Load() != 2 {
		t.Fatalf("placed nowhere: %d wakes; want the window whole", wakes.Load())
	}
}

// A paint asked of the terminal's region that does not come, as when no
// view paints it there any more, is asked of the window whole, rather than
// holding back every later one.
func TestAPaintThatDoesNotComeIsAskedOfTheWindow(t *testing.T) {
	var wakes atomic.Int32
	conn := &termConn{wake: func() { wakes.Add(1) }}
	repaint := func(ggui.Rect) {} // painted nowhere
	conn.repaint.Store(&repaint)
	conn.painted(ggui.Rct(ggui.Pt(0, 0), ggui.Sz(100, 100)), true)
	conn.requestPaint()
	deadline := time.Now().Add(5 * paintOverdue)
	for wakes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if wakes.Load() != 1 {
		t.Fatalf("%d wakes for a paint that did not come; want one", wakes.Load())
	}

	// One painted in time asks for nothing more.
	conn.pending.Store(false)
	conn.requestPaint()
	conn.pending.Store(false) // as frame does
	time.Sleep(2 * paintOverdue)
	if wakes.Load() != 1 {
		t.Fatalf("%d wakes after a paint that came", wakes.Load())
	}
}
