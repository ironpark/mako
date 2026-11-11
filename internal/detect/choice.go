package detect

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A dialog in an agent's terminal often asks the user to pick one of a
// list of options, the one picked marked by a cursor, "❯" or "›", and
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

var (
	cursorLine   = regexp.MustCompile(`^(\s*)[❯›]\s+(\S.*)$`)
	numberedLine = regexp.MustCompile(`^(\s*)(?:[❯›]\s+)?(\d+)\.\s+(\S.*)$`)
	// checkboxes mark options picked together, which no single pick takes.
	checkboxes = regexp.MustCompile(`[☐☑☒◻◼]|\[[ xX✔✓]\]`)
)

// ParseChoice reads the dialog of options a screen shows at its foot, cols
// wide, false for none: a list with no cursor, a single option, or
// checkboxes.
func ParseChoice(screen string, cols int) (Choice, bool) {
	lines := strings.Split(strings.TrimRight(screen, " \t\r\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRightFunc(lines[i], unicode.IsSpace)
	}
	at := -1
	for i := len(lines) - 1; i >= max(0, len(lines)-choiceScan); i-- {
		if cursorLine.MatchString(lines[i]) {
			at = i
			break
		}
	}
	if at < 0 {
		return Choice{}, false
	}
	var c Choice
	var first int
	if numberedLine.MatchString(lines[at]) {
		first, c = numberedChoice(lines, at, cols)
	} else {
		first, c = plainChoice(lines, at)
	}
	if len(c.Options) < 2 {
		return Choice{}, false
	}
	for _, o := range c.Options {
		if checkboxes.MatchString(o) {
			return Choice{}, false
		}
	}
	c.Text = choiceAbove(lines, first)
	return c, true
}

// numberedChoice reads options numbered from 1 around the cursor's line
// at, the line of the first, and the choice: a line indented under one goes
// on with its label where the label filled its line, and is its
// description, left out, where it did not.
func numberedChoice(lines []string, at, cols int) (int, Choice) {
	col := func(m []string) int { return utf8.RuneCountInString(m[1]) }
	start := at
	// Up to option 1, past the lines under each.
	for i := at; i >= 0; i-- {
		m := numberedLine.FindStringSubmatch(lines[i])
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
		m := numberedLine.FindStringSubmatch(l)
		if m != nil && m[2] == strconv.Itoa(len(c.Options)+1) && (numberCol < 0 || col(m) == numberCol || cursorLine.MatchString(l)) {
			if numberCol < 0 && !cursorLine.MatchString(l) {
				numberCol = col(m)
			}
			if i == at {
				c.Selected = len(c.Options)
			}
			c.Options = append(c.Options, m[3])
			last = l
			continue
		}
		indent := utf8.RuneCountInString(l) - utf8.RuneCountInString(strings.TrimLeftFunc(l, unicode.IsSpace))
		if strings.TrimSpace(l) == "" || len(c.Options) == 0 || indent <= max(numberCol, 0) {
			break
		}
		if cols > 0 && utf8.RuneCountInString(last) >= cols-2 {
			c.Options[len(c.Options)-1] += " " + strings.TrimSpace(l) // its label, wrapped
		}
		last = l
	}
	return start, c
}

// plainChoice reads unnumbered options: the lines next to the cursor's
// line at whose text starts where its does.
func plainChoice(lines []string, at int) (int, Choice) {
	m := cursorLine.FindStringSubmatch(lines[at])
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
