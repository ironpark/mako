package gui

import (
	"runtime"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty/input"
)

// terminalKey names a ggui key in the terminal's key set. Both speak W3C key
// codes -- ggfx's String is the code with the letters' "Key" prefix dropped
// -- so the table is a translation rather than a list kept in step by hand.
func terminalKey(k ggui.KeyboardKey) (input.Key, bool) {
	if int(k) >= 0 && int(k) < len(keyTable) {
		named := keyTable[k]
		return named, named != input.KeyUnidentified
	}
	return input.KeyUnidentified, false
}

var keyTable = func() []input.Key {
	table := make([]input.Key, ggfx.KeyMax+1)
	for k := ggfx.Key(0); k <= ggfx.KeyMax; k++ {
		name := k.String()
		if len(name) == 1 && name[0] >= 'A' && name[0] <= 'Z' {
			name = "Key" + name
		}
		if named, ok := input.KeyFromW3C(name); ok {
			table[k] = named
		}
	}
	return table
}()

// altMakesText reports whether Option/Alt produces characters, as it does on
// a Mac keyboard: there an Alt+letter arrives as text and is sent as such.
var altMakesText = runtime.GOOS == "darwin"

func keyMods(m ggui.Mods) input.KeyMods {
	return input.KeyMods{Shift: m.Shift, Ctrl: m.Ctrl, Alt: m.Alt, Super: m.Meta}
}

// The terminal's clipboard chords: ⌘V, ⌘C and ⌘A on macOS, and with
// Ctrl+Shift elsewhere, since the bare Ctrl chords belong to the program.
func isPaste(ev ggui.KeyEvent) bool     { return terminalChord(ev, ggui.KeyV) }
func isCopy(ev ggui.KeyEvent) bool      { return terminalChord(ev, ggui.KeyC) }
func isSelectAll(ev ggui.KeyEvent) bool { return terminalChord(ev, ggui.KeyA) }

func terminalChord(ev ggui.KeyEvent, key ggui.KeyboardKey) bool {
	if ev.Kind != ggui.KeyPress || ev.Key != key || ev.Mods.Alt {
		return false
	}
	if runtime.GOOS == "darwin" {
		return ev.Mods.Meta && !ev.Mods.Ctrl
	}
	return ev.Mods.Ctrl && ev.Mods.Shift
}
