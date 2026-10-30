package protocol

import (
	"strings"
	"unicode"
)

// BareTitle is a terminal's title without the spinner an agent leads it
// with while it works, which turns several times a second and says only
// that it is at work: a window shows that with a dot, and the title stays
// put; a daemon takes two titles a turn of it apart as the same.
func BareTitle(title string) string {
	return strings.TrimSpace(strings.TrimLeftFunc(title, func(r rune) bool { return isSpinner(r) || unicode.IsSpace(r) }))
}

// isSpinner reports whether r is a glyph agents animate in their titles:
// Braille dots, half circles, stars and the like, never a letter or digit.
func isSpinner(r rune) bool {
	switch {
	case r >= 0x2800 && r <= 0x28FF: // Braille
		return true
	case r >= 0x25A0 && r <= 0x25FF: // geometric shapes: ◐ ◑ ◒ ◓ ● ○
		return true
	case r >= 0x2700 && r <= 0x27BF: // dingbats: ✳ ✶ ✻ ✽ ✢
		return true
	case strings.ContainsRune("·•∙⋅*✱✲✳✴✵✶✷✸✹✺✻✼✽✾✿❀⁕⁎", r):
		return true
	}
	return false
}
