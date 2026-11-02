package gui

import (
	"sync"
	"time"

	"github.com/ironpark/gostty"
)

// A terminal keeps its scrollback in ghostty's pages, up to Scrollback
// lines, for every session followed: tens of megabytes across a day's
// sessions. ghostty compresses the pages no one looks at or writes to,
// a few at a time, and gives their memory back to the system; a page is
// restored when it is read again, by scrolling to it or searching it. As
// ghostty's own renderer does, a terminal asks for that once its pages
// stood still for compressQuiet, and goes on in steps while there is more.
const (
	// compressQuiet is how long a terminal's pages must go unchanged.
	compressQuiet = 250 * time.Millisecond
	// compressStep is the pause between steps of one pass, and the wait
	// for the terminal's lock when the UI holds it.
	compressStep = time.Millisecond
)

// noteCompressionLocked asks for the pages to be compressed once they stand
// still, when ghostty's activity says they changed since the last pass.
func (t *termConn) noteCompressionLocked() {
	if t.closed || t.compressUnsupported {
		return
	}
	if a := t.vt.Terminal().CompressionActivity(); a == t.compressSeen {
		return
	}
	if t.compressTimer == nil {
		t.compressTimer = time.AfterFunc(compressQuiet, t.compressPass)
	} else {
		t.compressTimer.Reset(compressQuiet)
	}
}

// compressPass runs one step of compression, and arranges the next while
// ghostty has more. It never waits on the UI for the terminal's lock.
func (t *termConn) compressPass() {
	if !t.mu.TryLock() {
		t.compressTimer.Reset(compressStep)
		return
	}
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	term := t.vt.Terminal()
	switch term.Compress(gostty.CompressionModeIncremental) {
	case gostty.CompressionResultPending:
		t.compressTimer.Reset(compressStep)
		return
	case gostty.CompressionResultUnsupported:
		t.compressUnsupported = true
	}
	t.compressSeen = term.CompressionActivity()
}

// TerminalMemory is what the terminals followed hold in ghostty's pages:
// how many there are, the address space their pages reserve, the memory
// they use, and how much of it is compressed scrollback; the pages, those
// compressed, and the most one terminal holds, as only full pages of
// scrollback above the screen are compressed.
type TerminalMemory struct {
	Terminals                     int
	Virtual, Resident, Compressed uint64
	Pages, CompressedPages        uint64
	MostPages                     uint64
}

// liveTerms are the terminals followed, for ReadTerminalMemory.
var liveTerms sync.Map // *termConn → struct{}

// ReadTerminalMemory sums what the terminals followed hold. It is safe to
// call from any goroutine, and takes each terminal's lock a moment.
func ReadTerminalMemory() TerminalMemory {
	var out TerminalMemory
	for _, m := range readTerminalMemory(func(*termConn) bool { return true }) {
		out.Terminals += m.Terminals
		out.Virtual += m.Virtual
		out.Resident += m.Resident
		out.Compressed += m.Compressed
		out.Pages += m.Pages
		out.CompressedPages += m.CompressedPages
		out.MostPages = max(out.MostPages, m.Pages)
	}
	return out
}

// readTerminalMemory is what each live terminal that which picks holds.
func readTerminalMemory(which func(*termConn) bool) []TerminalMemory {
	var out []TerminalMemory
	liveTerms.Range(func(k, _ any) bool {
		t := k.(*termConn)
		if !which(t) {
			return true
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.closed {
			return true
		}
		m := TerminalMemory{Terminals: 1}
		for _, key := range []gostty.ScreenKey{gostty.ScreenKeyPrimary, gostty.ScreenKeyAlternate} {
			if s, ok := t.vt.Terminal().Screen(key); ok {
				u := s.MemoryUsage()
				m.Virtual += u.VirtualBytes
				m.Resident += u.ResidentBytes
				m.Compressed += u.CompressedBytes
				m.Pages += u.Pages
				m.CompressedPages += u.CompressedPages
			}
		}
		out = append(out, m)
		return true
	})
	return out
}
