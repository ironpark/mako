package gui

import (
	"strings"
	"sync"
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
	conn.pending.Store(false)        // painted in time
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

// Output that ends one screen and begins the next is fed up to the end of
// the first, so a paint shows it whole; what follows is fed once painted.
func TestAScreenFinishedIsPaintedBeforeTheNextBegins(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s1", link, 20, 4, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	defer close(link.in)
	link.output(t, "\x1b[?2026h\x1b[Hfirst\x1b[?2026l\x1b[?2026h\x1b[Hsecond")
	waitFor(t, func() bool { conn.mu.Lock(); defer conn.mu.Unlock(); return len(conn.next) > 0 })
	if got := screenText(conn); !strings.HasPrefix(got, "first") {
		t.Fatalf("the screen to paint is %q, want the finished one", got)
	}
	if _, err := conn.frame(); err != nil {
		t.Fatal(err)
	}
	if got := screenText(conn); !strings.HasPrefix(got, "second") {
		t.Fatalf("once painted, the screen is %q, want what followed fed", got)
	}
}

// A piece that ends a screen it begins is fed whole: nothing waits.
func TestAScreenEndedInItsPieceIsNotHeldBack(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s1", link, 20, 4, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	defer close(link.in)
	link.output(t, "\x1b[?2026h\x1b[Hfirst\x1b[?2026l\x1b[?2026h\x1b[Hsecond\x1b[?2026l")
	waitFor(t, func() bool { return strings.HasPrefix(screenText(conn), "second") })
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.next) != 0 {
		t.Fatalf("%q was held back", conn.next)
	}
}

// Output that changes a row or two of a screen already painted asks for
// those rows to be painted again, not all of the terminal; output that
// changes most of it, or comes before any paint, asks for all of it.
func TestOutputRepaintsTheRowsItChanged(t *testing.T) {
	link := newFakeLink()
	var mu sync.Mutex
	var asked []ggui.Rect
	conn, err := newTermConn("s1", link, 20, 10, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	defer close(link.in)
	repaint := func(r ggui.Rect) { mu.Lock(); asked = append(asked, r); mu.Unlock() }
	conn.repaint.Store(&repaint)
	at := ggui.Rct(ggui.Pt(300, 40), ggui.Sz(800, 200))
	conn.painted(at, true)
	conn.paintedRows(40, 20, true)
	last := func() (ggui.Rect, int) {
		mu.Lock()
		defer mu.Unlock()
		if len(asked) == 0 {
			return ggui.Rect{}, 0
		}
		return asked[len(asked)-1], len(asked)
	}
	paint := func() {
		t.Helper()
		if _, err := conn.frame(); err != nil {
			t.Fatal(err)
		}
	}

	link.output(t, "\x1b[8;1Hwork")
	waitFor(t, func() bool { return strings.Contains(screenText(conn), "work") })
	waitFor(t, func() bool { _, n := last(); return n > 0 })
	if r, _ := last(); r != at {
		t.Fatalf("before any paint, %v was asked; want all of the terminal, %v", r, at)
	}
	paint()
	_, before := last()

	// Row 8, where the cursor stays: it and a row each way.
	link.output(t, "\x1b[8;1Hdone")
	waitFor(t, func() bool { _, n := last(); return n > before })
	want := ggui.Rct(ggui.Pt(300, 40+6*20), ggui.Sz(800, 3*20))
	if r, _ := last(); r != want {
		t.Fatalf("a row changed and %v was asked; want its rows, %v", r, want)
	}
	paint()
	_, before = last()

	link.output(t, "\x1b[1;1Ha\x1b[9;1Hb")
	waitFor(t, func() bool { _, n := last(); return n > before })
	if r, _ := last(); r != at {
		t.Fatalf("rows across the screen changed and %v was asked; want all of the terminal", r)
	}
}
