package kitty

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/ironpark/gostty"
	"github.com/ironpark/gostty/input"
)

// KeyFlags are the progressive enhancements of the Kitty keyboard protocol
// a program turned on, CSI > flags u, on the terminal's active screen.
type KeyFlags uint8

const (
	Disambiguate   KeyFlags = 1 << iota // escape codes for keys a legacy one confuses
	EventTypes                          // repeats and releases too
	AlternateKeys                       // the shifted key and the base layout's too
	AllKeysAsCodes                      // text keys as escape codes as well
	AssociatedText                      // the text a key types, with its code
)

// NeedsKeys reports whether keys that type text must be sent as keys, with
// the text they typed, rather than as the text alone: when the program
// hears of their releases or asked for every key as a code.
func (f KeyFlags) NeedsKeys() bool { return f&(EventTypes|AllKeysAsCodes) != 0 }

// Flags reads the flags term encodes keys by. ghostty does not say them,
// and a query written to the terminal would land in the middle of whatever
// the program printed last, so they are read off how it encodes keys that
// tell each flag apart; encoding changes nothing. ghostty disambiguates
// keys under any flags, so Disambiguate is read with any of them.
func Flags(term *gostty.Terminal) KeyFlags {
	encode := func(ev input.KeyEvent, text string) string {
		var b bytes.Buffer
		if input.EncodeKey(&b, term, ev, text) != nil {
			return ""
		}
		return b.String()
	}
	a := input.KeyEvent{Action: input.KeyActionPress, Key: input.KeyKeyA, UnshiftedCodepoint: 'a'}
	var f KeyFlags
	if encode(input.KeyEvent{Action: input.KeyActionPress, Key: input.KeyEscape}, "") != "\x1b" {
		f |= Disambiguate
	}
	release := a
	release.Action = input.KeyActionRelease
	if encode(release, "") != "" {
		f |= EventTypes
	}
	shifted := a
	shifted.Mods = input.KeyMods{Shift: true, Ctrl: true}
	if strings.Contains(encode(shifted, "A"), "97:65") {
		f |= AlternateKeys
	}
	if strings.HasPrefix(encode(a, "a"), "\x1b[") {
		f |= AllKeysAsCodes
	}
	shift := a
	shift.Mods = input.KeyMods{Shift: true}
	if strings.HasSuffix(encode(shift, "A"), ";65u") {
		f |= AssociatedText
	}
	return f
}

// Restore is what sets flags on a fresh terminal: pushed, so the program
// popping them leaves it as it was. Nothing for none.
func (f KeyFlags) Restore() string {
	if f == 0 {
		return ""
	}
	return "\x1b[>" + strconv.Itoa(int(f)) + "u"
}
