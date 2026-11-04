package gui

import (
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty/input"
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

// Enter with a modifier reaches a Windows node's console as a key event
// of its own, which keeps the modifier, for a program that asked for no
// Kitty keys: the console makes nothing of the sequence a terminal sends
// for it elsewhere.
func TestModifiedEnterReachesAWindowsConsole(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()
	enter := func(m input.KeyMods) string {
		conn.key(input.KeyEvent{Action: input.KeyActionPress, Key: input.KeyEnter, Mods: m}, "")
		conn.key(input.KeyEvent{Action: input.KeyActionRelease, Key: input.KeyEnter, Mods: m}, "")
		return link.typed()
	}
	conn.nodeOS = "windows"
	if got, want := enter(input.KeyMods{Shift: true}), "\x1b[13;28;13;1;16;1_\x1b[13;28;13;0;16;1_"; got != want {
		t.Fatalf("Shift+Enter sent %q, want %q", got, want)
	}
	if got, want := enter(input.KeyMods{Ctrl: true}), "\x1b[13;28;13;1;8;1_\x1b[13;28;13;0;8;1_"; got != want {
		t.Fatalf("Ctrl+Enter sent %q, want %q", got, want)
	}
	if got := enter(input.KeyMods{}); got != "\r" {
		t.Fatalf("Enter sent %q", got)
	}
	conn.nodeOS = "linux"
	if got, want := enter(input.KeyMods{Shift: true}), "\x1b[27;2;13~"; got != want {
		t.Fatalf("Shift+Enter to Linux sent %q, want %q", got, want)
	}
	conn.nodeOS = "windows"
	link.output(t, "\x1b[>1u") // the program asks for Kitty keys
	waitFor(t, func() bool { return conn.keyFlags() != 0 })
	if got, want := enter(input.KeyMods{Shift: true}), "\x1b[13;2u"; got != want {
		t.Fatalf("Shift+Enter with Kitty keys sent %q, want %q", got, want)
	}
}
