package protocol

import (
	"fmt"
	"strings"
	"unicode"
)

// Text another party wrote, a registry, a package, a plugin or a server
// answering, reaches a terminal, a log or the window as part of an error
// or a listing. A control character in it could drive the terminal that
// prints it (an escape sequence clearing the screen or setting its
// title), and a reordering mark could make it read as something else, so
// such text passes through Printable on its way out of the daemon.

// Spoofing says r is a control character or one of Unicode's marks that
// reorder or hide text, which could make a title or a label read as
// something else.
func Spoofing(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
		return true
	case r == 0x200B || r == 0x200C || r == 0x200D || r == 0xFEFF:
		return true
	}
	return false
}

// Printable is s with every Spoofing character but a line break and a
// tab written as its escape (\x1b, \u202e), visible and inert; text with
// none is returned as it is.
func Printable(s string) string {
	if !strings.ContainsFunc(s, spoofingInline) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case !spoofingInline(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

func spoofingInline(r rune) bool { return r != '\n' && r != '\t' && Spoofing(r) }
