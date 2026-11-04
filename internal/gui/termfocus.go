package gui

import (
	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty"
	"github.com/ironpark/gostty/input"
)

// A program that asked for focus events (mode 1004) is told when its
// terminal gains the keyboard and loses it: the terminal view taking or
// giving up focus, or the window that has it doing so.

// focusView follows the terminal view gaining or losing the keyboard.
func (t *termConn) focusView(on bool) {
	t.mu.Lock()
	t.viewFocused = on
	buf := t.focusReportLocked()
	t.mu.Unlock()
	if len(buf) > 0 {
		t.send(buf)
	}
}

// focusWindow follows the window gaining or losing the keyboard.
func (t *termConn) focusWindow(on bool) {
	t.mu.Lock()
	t.windowAway = !on
	buf := t.focusReportLocked()
	t.mu.Unlock()
	if len(buf) > 0 {
		t.send(buf)
	}
}

// focusReportLocked is the focus event owed the program, if any: one each
// time the terminal's focus changes, while it asks for them.
func (t *termConn) focusReportLocked() frameBuffer {
	in := t.viewFocused && !t.windowAway
	if t.closed || in == t.focusIn {
		return nil
	}
	t.focusIn = in
	if !t.vt.Terminal().ModeEnabled(gostty.ModeFocusEvent) {
		return nil
	}
	ev := input.FocusEventLost
	if in {
		ev = input.FocusEventGained
	}
	var buf frameBuffer
	if input.EncodeFocus(&buf, ev) != nil {
		return nil
	}
	return buf
}

// followWindowFocus tells every terminal when the window gains or loses
// the keyboard.
func followWindowFocus(m *model) {
	if m.window == nil {
		return
	}
	ggui.Effect(func() ggui.Cleanup {
		on := m.window.Focused().Get()
		liveTerms.Range(func(k, _ any) bool {
			k.(*termConn).focusWindow(on)
			return true
		})
		return nil
	})
}
