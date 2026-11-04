package gui

import (
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty"
)

// A program that asked for focus events hears of the terminal and its
// window gaining and losing the keyboard; one that did not hears nothing.
func TestFocusEventsReachAProgramThatAsks(t *testing.T) {
	link, conn, p := newTestTerminal(t) // the click focused the terminal
	link.typed()
	conn.focusWindow(false)
	if got := link.typed(); got != "" {
		t.Fatalf("unasked, the program was sent %q", got)
	}
	conn.focusWindow(true)

	link.output(t, "\x1b[?1004h")
	waitFor(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return conn.vt.Terminal().ModeEnabled(gostty.ModeFocusEvent)
	})
	conn.focusWindow(false)
	conn.focusWindow(false)
	if got := link.typed(); got != "\x1b[O" {
		t.Fatalf("window lost focus: sent %q", got)
	}
	conn.focusWindow(true)
	if got := link.typed(); got != "\x1b[I" {
		t.Fatalf("window gained focus: sent %q", got)
	}
	p.Click(ggui.Pt(400, 20)) // the target above it
	if got := link.typed(); got != "\x1b[O" {
		t.Fatalf("view lost focus: sent %q", got)
	}
}

// The daemon is told the colours the terminal draws with, once until they
// change.
func TestTheDaemonIsToldTheTerminalsColours(t *testing.T) {
	link, _, p := newTestTerminal(t)
	p.Frame()
	p.Frame()
	link.mu.Lock()
	defer link.mu.Unlock()
	if len(link.colors) != 1 {
		t.Fatalf("told %d times, want once", len(link.colors))
	}
	c := link.colors[0]
	if c.GetBackground() != rgbOf(darkPalette.Surface0) || c.GetForeground() != rgbOf(darkPalette.Fg) || !c.GetDark() || len(c.GetPalette()) != 16 {
		t.Fatalf("told %v", c)
	}
}
