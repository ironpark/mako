package protocol

import (
	"bytes"
	"errors"
	"testing"
)

// Text written by another party is printed inert: escapes and reordering
// marks become visible, lines stay lines.
func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                    "plain",
		"two\nlines\tand a tab":    "two\nlines\tand a tab",
		"\x1b[2J\x1b]0;owned\x07!": `\x1b[2J\x1b]0;owned\x07!`,
		"abc\u202egnp.exe":         `abc\u202egnp.exe`,
		"\x7f\u200b":               `\x7f\u200b`,
	} {
		if got := Printable(in); got != want {
			t.Errorf("Printable(%q) = %q; want %q", in, got, want)
		}
	}
}

// An error sent to a front-end is printable.
func TestErrorsAreSentPrintable(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteError(&buf, "1", errors.New("x answered \x1b]0;owned\x07")); err != nil {
		t.Fatal(err)
	}
	env, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	e, err := Body[*Error](env)
	if err != nil {
		t.Fatal(err)
	}
	if e.GetMessage() != `x answered \x1b]0;owned\x07` {
		t.Fatalf("sent %q", e.GetMessage())
	}
}
