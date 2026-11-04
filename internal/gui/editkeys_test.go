package gui

import (
	"testing"

	"github.com/ironpark/ggui"
)

// A Mac's word and line keys, and others' word keys, are sent as the
// session's node reads those edits.
func TestEditKeysGoAsTheNodeReadsThem(t *testing.T) {
	key := func(k ggui.KeyboardKey, m ggui.Mods) ggui.KeyEvent {
		return ggui.KeyEvent{Kind: ggui.KeyPress, Key: k, Mods: m}
	}
	for _, c := range []struct {
		ev       ggui.KeyEvent
		on, node string
		want     string
	}{
		{key(ggui.KeyArrowLeft, ggui.Mods{Alt: true}), "darwin", "linux", "\x1bb"},
		{key(ggui.KeyArrowLeft, ggui.Mods{Alt: true}), "darwin", "windows", "\x1b[1;5D"},
		{key(ggui.KeyArrowRight, ggui.Mods{Meta: true}), "darwin", "darwin", "\x05"},
		{key(ggui.KeyArrowRight, ggui.Mods{Meta: true}), "darwin", "windows", "\x1b[F"},
		{key(ggui.KeyBackspace, ggui.Mods{Meta: true}), "darwin", "linux", "\x15"},
		{key(ggui.KeyBackspace, ggui.Mods{Alt: true}), "darwin", "windows", "\x08"},
		{key(ggui.KeyBackspace, ggui.Mods{Ctrl: true}), "windows", "darwin", "\x1b\x7f"},
		{key(ggui.KeyArrowRight, ggui.Mods{Ctrl: true}), "linux", "darwin", "\x1bf"},
		{key(ggui.KeyArrowLeft, ggui.Mods{Ctrl: true}), "darwin", "linux", ""},
		{key(ggui.KeyArrowLeft, ggui.Mods{Alt: true, Shift: true}), "darwin", "linux", ""},
		{key(ggui.KeyArrowLeft, ggui.Mods{Alt: true}), "windows", "linux", ""},
	} {
		if got := editBytes(editKeyOf(c.ev, c.on), c.node); got != c.want {
			t.Errorf("%v %+v on %s to %s: %q, want %q", c.ev.Key, c.ev.Mods, c.on, c.node, got, c.want)
		}
	}
}
