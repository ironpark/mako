package claude

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/detect"
)

// ansiSeq matches CSI/OSC/single-char escape sequences so we can strip styles
// for structural checks while inspecting SGR codes on the prompt line itself.
var ansiSeq = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// staticPlaceholders are the dim texts Claude Code shows in an empty prompt
// box that are not suggestions from the model: the rotating example prompt
// and the hints of particular composer states.
var staticPlaceholders = []string{
	`Try "`,
	"Describe something to build, change, or fix",
	"Type your message while the session is set up",
	"Tell Claude what to change",
	"and tell Claude what to do",
}

// ExtractGhost reads Claude Code's styled VT terminal screen and returns the
// active dimmed prompt suggestion ("ghost text") shown inside the idle prompt
// box, or "" when no prompt suggestion is visible. This serves as a fallback
// when the native mako-live mod's prompt.suggest hook is unavailable.
func (*Agent) ExtractGhost(styledScreen string) string {
	if styledScreen == "" {
		return ""
	}
	plain := ansiSeq.ReplaceAllString(styledScreen, "")
	// Only extract when Claude Code is genuinely resting at its prompt box
	// (not inside a permission dialog, picker, or transcript viewer).
	if r := screenRules.Detect(detect.Input{Screen: plain}); r.ID != "prompt_box" {
		return ""
	}

	styledLines := strings.Split(styledScreen, "\n")
	plainLines := strings.Split(plain, "\n")
	if len(styledLines) != len(plainLines) {
		return ""
	}
	top, bot, ok := promptBoxRange(plainLines)
	if !ok {
		return ""
	}

	var ghost strings.Builder
	started := false
	for i := top + 1; i < bot; i++ {
		line := styledLines[i]
		if !started {
			after, found := cutAfterPromptGlyph(line)
			if !found {
				continue
			}
			text, ok := extractDimGhostLine(after, true)
			if !ok {
				return ""
			}
			ghost.WriteString(text)
			started = true
			continue
		}
		// Continuation lines inside a multi-line wrapped ghost suggestion.
		if strings.TrimSpace(plainLines[i]) == "" {
			break
		}
		text, ok := extractDimGhostLine(line, false)
		if !ok {
			return ""
		}
		if text != "" {
			if ghost.Len() > 0 {
				ghost.WriteByte(' ')
			}
			ghost.WriteString(text)
		}
	}
	return cleanGhostSuggestion(ghost.String())
}

// promptBoxRange finds the line indices (top, bot) of the last pair of
// horizontal box rules ("───...") enclosing Claude Code's prompt box.
func promptBoxRange(plainLines []string) (top, bot int, ok bool) {
	isRule := func(s string) bool {
		t := strings.TrimSpace(s)
		if len(t) == 0 {
			return false
		}
		count := 0
		for _, r := range t {
			if r != '─' {
				return false
			}
			count++
		}
		return count >= 10
	}
	bot = -1
	for i := len(plainLines) - 1; i >= 0; i-- {
		if isRule(plainLines[i]) {
			if bot < 0 {
				bot = i
			} else if bot-i > 1 {
				return i, bot, true
			} else {
				bot = i
			}
		}
	}
	return 0, 0, false
}

// cutAfterPromptGlyph returns the styled substring immediately following the
// prompt marker "❯" on a prompt line.
func cutAfterPromptGlyph(styledLine string) (string, bool) {
	idx := strings.Index(styledLine, "❯")
	if idx < 0 {
		return "", false
	}
	return styledLine[idx+len("❯"):], true
}

// maxCursorForward bounds the blank cells one cursor-forward sequence may
// stand for, so a malformed parameter cannot blow up the extracted text.
const maxCursorForward = 512

// extractDimGhostLine walks a styled VT line fragment and extracts text drawn
// with SGR dim/faint (code 2). Claude Code separates the prompt marker from
// the input with a no-break space, and the terminal formatter writes runs of
// unstyled blank cells as cursor-forward (CSI n C) sequences, so both count
// as whitespace. On the first line of an empty Ink TextInput the first
// character of a ghost suggestion may be drawn with SGR inverse (code 7, a
// drawn block cursor) while the rest of the suggestion is dim. Any other
// non-whitespace character drawn in normal style is user-typed text and
// aborts extraction.
func extractDimGhostLine(s string, firstLine bool) (string, bool) {
	var dim, inverse bool
	var out strings.Builder
	seenVisible := false
	seenDim := false
	leadingInverseUsed := false

	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			end, final, params := parseEscape(s, i)
			switch final {
			case 'm':
				applySGR(params, &dim, &inverse)
			case 'C':
				if seenVisible {
					n, err := strconv.Atoi(params)
					if err != nil || n < 1 {
						n = 1
					}
					out.WriteString(strings.Repeat(" ", min(n, maxCursorForward)))
				}
			}
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == '\r' {
			continue
		}
		if unicode.IsSpace(r) {
			if seenVisible {
				out.WriteByte(' ')
			}
			continue
		}
		if dim {
			seenVisible = true
			seenDim = true
			out.WriteRune(r)
			continue
		}
		if firstLine && !seenVisible && !leadingInverseUsed && inverse {
			// Drawn cursor cell on the first character of an empty prompt's ghost text.
			seenVisible = true
			leadingInverseUsed = true
			out.WriteRune(r)
			continue
		}
		// Non-dim, non-cursor visible text means real user input, not ghost text.
		return "", false
	}

	if !seenDim {
		return "", false
	}
	return strings.TrimRight(out.String(), " "), true
}

// parseEscape advances past an ANSI escape sequence starting at s[start]. For
// a CSI sequence it returns its final byte and parameters; final is 0 for any
// other sequence.
func parseEscape(s string, start int) (end int, final byte, params string) {
	if start+1 >= len(s) {
		return len(s), 0, ""
	}
	switch s[start+1] {
	case '[':
		j := start + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		if j >= len(s) {
			return len(s), 0, ""
		}
		return j + 1, s[j], s[start+2 : j]
	case ']':
		j := start + 2
		for j < len(s) {
			if s[j] == 0x07 {
				return j + 1, 0, ""
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2, 0, ""
			}
			j++
		}
		return len(s), 0, ""
	default:
		return start + 2, 0, ""
	}
}

func applySGR(params string, dim, inverse *bool) {
	if params == "" {
		*dim = false
		*inverse = false
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		if parts[i] == "" {
			*dim = false
			*inverse = false
			continue
		}
		code, err := strconv.Atoi(parts[i])
		if err != nil {
			continue
		}
		switch code {
		case 0:
			*dim = false
			*inverse = false
		case 2:
			*dim = true
		case 7:
			*inverse = true
		case 22:
			*dim = false
		case 27:
			*inverse = false
		case 38, 48, 58:
			// Extended color: 38;5;n or 38;2;r;g;b. Skip its sub-parameters so
			// color components like "2" (RGB mode) are never misread as SGR dim.
			if i+1 < len(parts) {
				switch parts[i+1] {
				case "5":
					i += 2
				case "2":
					i += 4
				}
			}
		}
	}
}

// cleanGhostSuggestion strips trailing right-aligned shortcut hints (separated
// by 3+ spaces, e.g. "↵ send" or "tab to accept") and ignores Claude Code's
// static placeholders.
func cleanGhostSuggestion(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if idx := strings.Index(s, "   "); idx >= 0 {
		tail := strings.ToLower(strings.TrimSpace(s[idx:]))
		if strings.Contains(tail, "send") || strings.Contains(tail, "accept") || strings.Contains(tail, "tab") || strings.HasPrefix(tail, "↵") {
			s = strings.TrimSpace(s[:idx])
		}
	}
	for _, p := range staticPlaceholders {
		if strings.HasPrefix(s, p) {
			return ""
		}
	}
	return s
}
