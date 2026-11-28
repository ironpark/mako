package claude

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/detect"
)

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
	plain := plainScreen(styledScreen)
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
	if !ok || dialogBelow(plainLines[bot+1:]) {
		return ""
	}

	var rows []string
	for i := top + 1; i < bot; i++ {
		line := styledLines[i]
		first := len(rows) == 0
		if first {
			after, found := cutAfterPromptGlyph(line)
			if !found {
				continue
			}
			line = after
		} else if strings.TrimSpace(plainLines[i]) == "" {
			// The rows of a suggestion Claude Code wrapped end at a blank one.
			break
		}
		text, ok := extractDimGhostLine(line, first)
		if !ok {
			return ""
		}
		rows = append(rows, stripHint(text))
	}
	return cleanGhostSuggestion(joinRows(rows))
}

// plainScreen is styledScreen without its escape sequences, the blank cells
// the formatter writes as cursor-forward sequences put back as spaces, so
// that the words of a footer such as "Esc to cancel" stay apart.
func plainScreen(styledScreen string) string {
	var b strings.Builder
	b.Grow(len(styledScreen))
	for i := 0; i < len(styledScreen); {
		if styledScreen[i] != 0x1b {
			j := strings.IndexByte(styledScreen[i:], 0x1b)
			if j < 0 {
				j = len(styledScreen) - i
			}
			b.WriteString(styledScreen[i : i+j])
			i += j
			continue
		}
		end, final, params := parseEscape(styledScreen, i)
		if final == 'C' {
			b.WriteString(strings.Repeat(" ", cursorForward(params)))
		}
		i = end
	}
	return b.String()
}

// dialogFooters are what the footer of a dialog, picker or form drawn under
// the prompt box says, in lower case. Claude Code is not resting at its
// prompt while one shows, whatever the box above holds.
var dialogFooters = []string{
	"esc to cancel",
	"enter to select",
	"enter to confirm",
	"to navigate",
	"do you want to proceed?",
	"select model",
}

// dialogBelow reports whether the rows under the prompt box's bottom rule
// show a dialog's footer.
func dialogBelow(rows []string) bool {
	for _, row := range rows {
		row = strings.ToLower(row)
		for _, f := range dialogFooters {
			if strings.Contains(row, f) {
				return true
			}
		}
	}
	return false
}

// joinRows joins the rows Claude Code wrapped a suggestion into. It wraps at
// spaces, which the rows leave out, and splits a word only when the word is
// wider than a whole row, filling the row with it: a long path or URL, or a
// run of CJK text written without spaces. So two rows join with a space,
// unless the word ending one and the word starting the next are together
// wider than any row, and so could only have been one word split in two.
func joinRows(rows []string) string {
	widest := 0
	for _, row := range rows {
		widest = max(widest, cells(row))
	}
	var b strings.Builder
	for i, row := range rows {
		if row == "" {
			continue
		}
		if b.Len() > 0 {
			prev := rows[i-1]
			if cells(lastWord(prev))+cells(firstWord(row)) <= widest {
				b.WriteByte(' ')
			}
		}
		b.WriteString(row)
	}
	return b.String()
}

func lastWord(s string) string {
	if i := strings.LastIndexFunc(s, unicode.IsSpace); i >= 0 {
		_, size := utf8.DecodeRuneInString(s[i:])
		return s[i+size:]
	}
	return s
}

func firstWord(s string) string {
	if i := strings.IndexFunc(s, unicode.IsSpace); i >= 0 {
		return s[:i]
	}
	return s
}

// cells is how many terminal cells s takes: two for each wide (East Asian)
// rune, one for any other.
func cells(s string) int {
	n := 0
	for _, r := range s {
		n++
		if wideRune(r) {
			n++
		}
	}
	return n
}

func wideRune(r rune) bool {
	return r >= 0x1100 && r <= 0x115F || r >= 0x2E80 && r <= 0xA4CF && r != 0x303F ||
		r >= 0xAC00 && r <= 0xD7A3 || r >= 0xF900 && r <= 0xFAFF || r >= 0xFE30 && r <= 0xFE4F ||
		r >= 0xFF00 && r <= 0xFF60 || r >= 0xFFE0 && r <= 0xFFE6 || r >= 0x20000 && r <= 0x3FFFD
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
					out.WriteString(strings.Repeat(" ", cursorForward(params)))
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

// cursorForward is how many cells a cursor-forward sequence with params
// moves: one when it names none, at most maxCursorForward.
func cursorForward(params string) int {
	n, err := strconv.Atoi(params)
	if err != nil || n < 1 {
		n = 1
	}
	return min(n, maxCursorForward)
}

// parseEscape advances past an ANSI escape sequence starting at s[start]. For
// a plain CSI sequence it returns its final byte and parameters; final is 0
// for any other sequence, a CSI sequence with a private parameter prefix
// (such as the "\x1b[>4;2m" of a keyboard mode) or intermediate bytes among
// them, whose parameters do not mean what an SGR's or a cursor move's do.
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
		params = s[start+2 : j]
		if params != "" && params[0] >= '<' && params[0] <= '?' || strings.ContainsFunc(params, func(r rune) bool { return r >= 0x20 && r <= 0x2f }) {
			return j + 1, 0, ""
		}
		return j + 1, s[j], params
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

// stripHint cuts a right-aligned shortcut hint, such as "↵ send" or "tab to
// accept", off the end of one row of a suggestion: a few words set off by
// three spaces or more, as a suggestion's own words never are.
func stripHint(row string) string {
	row = strings.TrimRight(row, " ")
	idx := strings.LastIndex(row, "   ")
	if idx < 0 {
		return row
	}
	tail := strings.ToLower(strings.TrimSpace(row[idx:]))
	words := strings.Fields(tail)
	if len(words) == 0 || len(words) > 4 {
		return row
	}
	hint := strings.HasPrefix(tail, "↵") || strings.HasPrefix(tail, "→")
	for _, w := range words {
		hint = hint || w == "send" || w == "accept" || w == "tab"
	}
	if !hint {
		return row
	}
	return strings.TrimRight(row[:idx], " ")
}

// cleanGhostSuggestion trims a suggestion and ignores Claude Code's static
// placeholders.
func cleanGhostSuggestion(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, p := range staticPlaceholders {
		if strings.HasPrefix(s, p) {
			return ""
		}
	}
	return s
}
