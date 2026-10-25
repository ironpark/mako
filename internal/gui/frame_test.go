package gui

import (
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/sanbo/internal/protocol"
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
		Kind:     protocol.FrameSnapshot,
		Snapshot: protocol.Snapshot{SessionID: "s", Cols: 80, Rows: 24, Title: "t", Styled: true},
	}
	link.output(t, "restored")
	waitFor(t, func() bool {
		return strings.Contains(screenText(conn), "restored")
	})
}
