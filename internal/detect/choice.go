package detect

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"
)

// A dialog in an agent's terminal often asks the user to pick one of a
// list of options, the one picked marked by a cursor, "❯" or "›", or
// another a driver's rules name (WithCursors), and
// moved with the arrow keys; Enter takes it. Numbered, as "❯ 1. Yes", or
// not, as Claude's "❯ No, exit" over "  Yes, I trust this folder". A list
// that takes several picks, its options checkboxes, is not one.

// Choice is the options a dialog offers to pick one of, and what it asks.
type Choice struct {
	// Text is what the dialog says above its options, a line each.
	Text     string
	Options  []string
	Selected int // the option the cursor is on
}

// choiceScan is how far up from the screen's foot a dialog's cursor is
// looked for.
const choiceScan = 30

// choiceText is how many lines above its options a dialog's text runs to,
// at most.
const choiceText = 8

// checkboxes mark options picked together, which no single pick takes.
var checkboxes = regexp.MustCompile(`[☐☑☒◻◼]|\[[ xX✔✓]\]`)

// cursors are the lines a dialog's cursor marks, by the glyphs it may be.
type cursors struct {
	cursorLine, numberedLine *regexp.Regexp
}

func newCursors(glyphs string) *cursors {
	class := "[" + regexp.QuoteMeta(glyphs) + "]"
	return &cursors{
		cursorLine:   regexp.MustCompile(`^(\s*)` + class + `\s+(\S.*)$`),
		numberedLine: regexp.MustCompile(`^(\s*)(?:` + class + `\s+)?(\d+)\.\s+(\S.*)$`),
	}
}

// defaultCursors are the cursors most agents draw.
var defaultCursors = newCursors("❯›")

// ParseChoice reads the dialog of options a screen shows at its foot, cols
// wide, false for none: a list with no cursor, a single option, or
// checkboxes. Its cursor is one most agents draw.
func ParseChoice(screen string, cols int) (Choice, bool) { return defaultCursors.parse(screen, cols) }

func (cur *cursors) parse(screen string, cols int) (Choice, bool) {
	lines := strings.Split(strings.TrimRight(screen, " \t\r\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRightFunc(lines[i], unicode.IsSpace)
	}
	at := -1
	for i := len(lines) - 1; i >= max(0, len(lines)-choiceScan); i-- {
		if cur.cursorLine.MatchString(lines[i]) {
			at = i
			break
		}
	}
	if at < 0 {
		return Choice{}, false
	}
	var c Choice
	var first int
	if cur.numberedLine.MatchString(lines[at]) {
		first, c = cur.numberedChoice(lines, at, cols)
	} else {
		first, c = cur.plainChoice(lines, at)
	}
	if len(c.Options) < 2 {
		return Choice{}, false
	}
	if slices.ContainsFunc(c.Options, checkboxes.MatchString) {
		return Choice{}, false
	}
	c.Text = choiceAbove(lines, first)
	return c, true
}

// numberedChoice reads options numbered from 1 around the cursor's line
// at, the line of the first, and the choice: a line indented under one goes
// on with its label where the label filled its line, and is its
// description, left out, where it did not.
func (cur *cursors) numberedChoice(lines []string, at, cols int) (int, Choice) {
	col := func(m []string) int { return utf8.RuneCountInString(m[1]) }
	start := at
	// Up to option 1, past the lines under each.
	for i := at; i >= 0; i-- {
		m := cur.numberedLine.FindStringSubmatch(lines[i])
		if m == nil {
			if strings.TrimSpace(lines[i]) == "" {
				break
			}
			continue
		}
		start = i
		if m[2] == "1" {
			break
		}
	}
	var c Choice
	numberCol, last := -1, ""
	for i := start; i < len(lines); i++ {
		l := lines[i]
		m := cur.numberedLine.FindStringSubmatch(l)
		if m != nil && m[2] == strconv.Itoa(len(c.Options)+1) && (numberCol < 0 || col(m) == numberCol || cur.cursorLine.MatchString(l)) {
			if numberCol < 0 && !cur.cursorLine.MatchString(l) {
				numberCol = col(m)
			}
			if i == at {
				c.Selected = len(c.Options)
			}
			c.Options = append(c.Options, m[3])
			last = l
			continue
		}
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || len(c.Options) == 0 || isRule(trimmed) {
			break
		}
		indent := utf8.RuneCountInString(l) - utf8.RuneCountInString(strings.TrimLeftFunc(l, unicode.IsSpace))
		if indent <= max(numberCol, 0) {
			// Some TUIs (notably agy) wrap long numbered option labels onto
			// column 0 without hanging indentation. Keep reading when the next
			// sequential option follows directly after the wrapped line(s), or
			// when the previous line filled the terminal width and this line is
			// not a dialog footer.
			if cur.hasSequentialOptionAhead(lines, i+1, len(c.Options)+1, numberCol) {
				if indent == 0 || wrapsFromLine(last, trimmed, cols) {
					c.Options[len(c.Options)-1] += " " + trimmed
				}
				last = l
				continue
			}
			if indent == 0 && !looksLikeChoiceFooter(trimmed) && wrapsFromLine(last, trimmed, cols) {
				c.Options[len(c.Options)-1] += " " + trimmed
				last = l
				continue
			}
			break
		}
		if wrapsFromLine(last, trimmed, cols) {
			c.Options[len(c.Options)-1] += " " + trimmed // its label, wrapped
		}
		last = l
	}
	return start, c
}

// hasSequentialOptionAhead reports whether numbered option next appears within
// a few contiguous non-blank, non-rule lines starting at from.
func (cur *cursors) hasSequentialOptionAhead(lines []string, from, next, numberCol int) bool {
	want := strconv.Itoa(next)
	limit := min(len(lines), from+4)
	for i := from; i < limit; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || isRule(trimmed) {
			return false
		}
		if m := cur.numberedLine.FindStringSubmatch(lines[i]); m != nil {
			if m[2] != want {
				return false
			}
			col := utf8.RuneCountInString(m[1])
			return numberCol < 0 || col == numberCol || cur.cursorLine.MatchString(lines[i])
		}
	}
	return false
}

// wrapsFromLine reports whether trimmed looks like a soft-wrapped continuation
// of last inside a terminal cols cells wide (measuring East Asian wide runes as
// two cells).
func wrapsFromLine(last, trimmed string, cols int) bool {
	if cols <= 0 {
		return false
	}
	lastW := choiceDisplayWidth(last)
	if lastW >= cols-2 {
		return true
	}
	firstW := choiceFirstWordWidth(trimmed)
	return firstW > 0 && lastW >= cols/2 && lastW+1+firstW > cols
}

func looksLikeChoiceFooter(trimmed string) bool {
	lower := strings.ToLower(trimmed)
	for _, hint := range []string{
		"esc to cancel",
		"esc cancel",
		"esc skip",
		"navigate",
		"enter select",
		"enter confirm",
		"enter submit",
		"tab amend",
		"ctrl+r review",
		"? for shortcuts",
		"space toggle",
	} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func choiceDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7f || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		switch width.LookupRune(r).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			w += 2
		default:
			w++
		}
	}
	return w
}

func choiceFirstWordWidth(s string) int {
	if i := strings.IndexFunc(s, unicode.IsSpace); i >= 0 {
		s = s[:i]
	}
	return choiceDisplayWidth(s)
}

// plainChoice reads unnumbered options: the lines next to the cursor's
// line at whose text starts where its does.
func (cur *cursors) plainChoice(lines []string, at int) (int, Choice) {
	m := cur.cursorLine.FindStringSubmatch(lines[at])
	textCol := utf8.RuneCountInString(lines[at]) - utf8.RuneCountInString(m[2])
	pad := strings.Repeat(" ", textCol)
	option := func(l string) (string, bool) {
		rest, ok := strings.CutPrefix(l, pad)
		if r, _ := utf8.DecodeRuneInString(rest); !ok || rest == "" || unicode.IsSpace(r) {
			return "", false
		}
		return rest, true
	}
	start := at
	for start > 0 {
		if _, ok := option(lines[start-1]); !ok {
			break
		}
		start--
	}
	var c Choice
	for i := start; i < len(lines); i++ {
		o, ok := m[2], true
		if i != at {
			o, ok = option(lines[i])
		} else {
			c.Selected = len(c.Options)
		}
		if !ok {
			break
		}
		c.Options = append(c.Options, o)
	}
	return start, c
}

// choiceAbove is the dialog's text above its options, which start at
// line first: its lines up to a rule or two blank lines, a blank line
// between its paragraphs, past the blank lines right above the options.
func choiceAbove(lines []string, first int) string {
	blank := func(i int) bool { return i >= 0 && strings.TrimSpace(lines[i]) == "" }
	i := first - 1
	for n := 0; n < 2 && blank(i); n++ {
		i--
	}
	var text []string
	for ; i >= 0 && len(text) < choiceText; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			if blank(i - 1) {
				break
			}
			continue
		}
		if isRule(l) {
			break
		}
		text = append([]string{l}, text...)
	}
	return strings.Join(text, "\n")
}

// isRule reports whether a line is a rule drawn across the screen.
func isRule(l string) bool {
	return strings.Trim(l, "─━╌╍═▔▁-") == ""
}
