package gui

import (
	"cmp"
	"runtime"

	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty"
)

// The keys that move and delete by word or line at a prompt are not the
// same on every system: a Mac's are ⌥ and ⌘ with the arrows and Delete,
// which a terminal sends as nothing a shell binds, and a Unix shell's
// readline and a Windows console's PSReadLine read different bytes for the
// same edit. On the main screen, where a shell or an agent's prompt takes
// lines, such a key is sent as the node's own shell reads that edit, so it
// does the same wherever the session runs.

// editKey is an edit at a prompt.
type editKey int

const (
	editNone editKey = iota
	editWordLeft
	editWordRight
	editWordDelete // the word before the cursor
	editLineStart
	editLineEnd
	editLineDelete // the line before the cursor
)

// editKeyOf is the edit ev asks for with this system's keys, typed on
// goos's keyboard.
func editKeyOf(ev ggui.KeyEvent, goos string) editKey {
	m := ev.Mods
	if m.Shift {
		return editNone // selects, where a program selects
	}
	word, line := m.Ctrl && !m.Alt && !m.Meta, false
	if goos == "darwin" {
		word = m.Alt && !m.Ctrl && !m.Meta
		line = m.Meta && !m.Ctrl && !m.Alt
	}
	switch {
	case word && ev.Key == ggui.KeyArrowLeft:
		return editWordLeft
	case word && ev.Key == ggui.KeyArrowRight:
		return editWordRight
	case word && ev.Key == ggui.KeyBackspace:
		return editWordDelete
	case line && ev.Key == ggui.KeyArrowLeft:
		return editLineStart
	case line && ev.Key == ggui.KeyArrowRight:
		return editLineEnd
	case line && ev.Key == ggui.KeyBackspace:
		return editLineDelete
	}
	return editNone
}

// editBytes is what a shell of a node on goos reads as e: readline's
// emacs keys on Unix, and on Windows the keys PSReadLine binds, as its
// console makes them of what is sent.
func editBytes(e editKey, goos string) string {
	if goos == "windows" {
		switch e {
		case editWordLeft:
			return "\x1b[1;5D" // Ctrl+←
		case editWordRight:
			return "\x1b[1;5C" // Ctrl+→
		case editWordDelete:
			return "\x08" // Ctrl+Backspace
		case editLineStart:
			return "\x1b[H" // Home
		case editLineEnd:
			return "\x1b[F" // End
		case editLineDelete:
			return "\x1b[1;5H" // Ctrl+Home
		}
		return ""
	}
	switch e {
	case editWordLeft:
		return "\x1bb"
	case editWordRight:
		return "\x1bf"
	case editWordDelete:
		return "\x1b\x7f"
	case editLineStart:
		return "\x01"
	case editLineEnd:
		return "\x05"
	case editLineDelete:
		return "\x15"
	}
	return ""
}

// edit sends e as the session's node reads it, and reports whether it
// did: not on the alternate screen, where a full screen program reads its
// keys as they are.
func (t *termConn) edit(e editKey) bool {
	t.mu.Lock()
	if t.closed || t.vt.Terminal().ActiveScreenKey() == gostty.ScreenKeyAlternate {
		t.mu.Unlock()
		return false
	}
	t.vt.Terminal().ScrollViewport(gostty.ScrollViewportBottom())
	goos := t.nodeOS
	t.mu.Unlock()
	t.send([]byte(editBytes(e, cmp.Or(goos, runtime.GOOS))))
	return true
}
