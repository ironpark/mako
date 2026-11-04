package gui

import (
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A paint with no new output reuses the screen it copied; new output refreshes
// only the rows that changed.
func TestFramesCopyOnlyWhatChanged(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s", link, 40, 6, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()

	link.output(t, "first")
	var f frameSnapshot
	waitFor(t, func() bool {
		f, err = conn.frame()
		return err == nil && f.textAt(0) == "f"
	})
	before := slices.Clone(f.cells)

	// Nothing new: the cached cells come back untouched.
	g, err := conn.frame()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, g.cells) {
		t.Fatal("an unchanged screen was rewritten")
	}
	if g.textAt(0) != "f" {
		t.Fatalf("row 0 %q, want f", g.textAt(0))
	}

	// A row of output refreshes that row, and leaves the one above it alone.
	link.output(t, "\r\nsecond")
	waitFor(t, func() bool {
		f, err = conn.frame()
		return err == nil && f.textAt(f.cols) == "s"
	})
	if f.textAt(0) != "f" {
		t.Fatalf("row 0 %q after a change below, want f", f.textAt(0))
	}
}

// A snapshot resets the screen and its text arrives as output frames after it.
func TestSnapshotTextFollowsAsOutput(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s", link, 40, 6, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()

	link.in <- &protocol.StreamEvent{
		Kind:     protocol.EventSnapshot,
		Snapshot: &protocol.Snapshot{SessionId: "s", Cols: 80, Rows: 24, Title: "t", Styled: true},
	}
	link.output(t, "restored")
	waitFor(t, func() bool {
		return strings.Contains(screenText(conn), "restored")
	})
}

// A screen drawn inside a render hold, synchronized output's, shows only
// once the hold ends, however many pieces it arrives in: a program that
// redraws lines in place would otherwise show them blank and half drawn.
func TestARenderHoldShowsTheLastWholeScreen(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s", link, 40, 6, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()

	link.output(t, "old")
	var f frameSnapshot
	waitFor(t, func() bool {
		f, err = conn.frame()
		return err == nil && f.textAt(0) == "o"
	})
	// The redraw begins: the line is erased, and the rest is yet to come.
	link.output(t, "\x1b[?2026h\r\x1b[2K")
	link.output(t, "n")
	waitFor(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		text, _ := conn.vt.Terminal().PlainString()
		return conn.held && strings.HasPrefix(text, "n")
	})
	if f, _ = conn.frame(); f.textAt(0) != "o" {
		t.Fatalf("row 0 %q inside the hold, want the last screen's o", f.textAt(0))
	}
	link.output(t, "ew\x1b[?2026l")
	waitFor(t, func() bool {
		f, err = conn.frame()
		return err == nil && f.textAt(0) == "n" && f.textAt(2) == "w"
	})
}

// A hold that never ends gives way after renderHoldLimit.
func TestARenderHoldGivesWay(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s", link, 40, 6, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()
	link.output(t, "old")
	waitFor(t, func() bool { f, err := conn.frame(); return err == nil && f.textAt(0) == "o" })
	link.output(t, "\x1b[?2026h\rnew")
	waitFor(t, func() bool { conn.mu.Lock(); defer conn.mu.Unlock(); return conn.held })
	conn.mu.Lock()
	conn.heldSince = conn.heldSince.Add(-renderHoldLimit)
	conn.mu.Unlock()
	if f, _ := conn.frame(); f.textAt(0) != "n" {
		t.Fatalf("row 0 %q past the limit, want n", f.textAt(0))
	}
}
