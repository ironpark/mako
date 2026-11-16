package prompttmpl

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A prompt picks text by a value as Rust's match does: a block names an
// argument or a built-in variable, and each arm under it, a line of its
// own, gives a pattern and, after =>, the text for a value it matches,
// going on over the lines under it until the next arm. The first arm that
// matches is taken; its text may hold {{name}}s, and other blocks.
//
//	{{match tone}}
//	kind | gentle => Be encouraging.
//	blunt => "Be direct.",
//	_ => Be neutral.
//	{{end}}
//
// A pattern is a word, as a value is spelt; a "quoted" text; a range of
// numbers, 1..=3 with its end, 1..3 without, or open at either end, 3..
// and ..=10; or _, for any value. Patterns are joined with | to match any
// of them. An arm's text written as a quoted string, a comma after it or
// not, is the string. As in Rust, a block must cover every value: a
// choice's options, true and false, or anything with _.

// tagPattern is {{match name}}, the name its first group, or {{end}}.
var tagPattern = regexp.MustCompile(`\{\{\s*(?:match\s+(` + namePattern + `)|end)\s*\}\}`)

// patternWord is one pattern of an arm, and armLine an arm's first line:
// its patterns, joined by |, then => and the start of its text.
var (
	patternWord = `"[^"\n]*"|(?:-?\d+(?:\.\d+)?)?\.\.=?(?:-?\d+(?:\.\d+)?)?|[^\s|"=]+`
	armLine     = regexp.MustCompile(`^[ \t]*((?:` + patternWord + `)(?:[ \t]*\|[ \t]*(?:` + patternWord + `))*)[ \t]*=>[ \t]?(.*)$`)
	patternOne  = regexp.MustCompile(patternWord)
	rangeWord   = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)?\.\.(=)?(-?\d+(?:\.\d+)?)?$`)
)

// segment is a stretch of a prompt: text as it is, {{name}}s and all, or a
// match block.
type segment struct {
	text  string
	block *matchBlock
}

// matchBlock is a {{match name}} block: what it matches, its arms, and
// whether it takes lines of its own, which it leaves no blank line in.
type matchBlock struct {
	subject string
	arms    []matchArm
	lines   bool
}

// matchArm is an arm of a block: its patterns, as written, and its text.
type matchArm struct {
	patterns []string
	body     []segment
}

// Tag is a keyword of a prompt's blocks, {{match name}} or {{end}}: its
// bytes [Start, End), and for a match the name it matches, "" for an end.
type Tag struct {
	Start, End int
	Subject    string
}

// Tags are s's {{match name}}s and {{end}}s, in order.
func Tags(s string) []Tag {
	var out []Tag
	for _, m := range tagPattern.FindAllStringSubmatchIndex(s, -1) {
		t := Tag{Start: m[0], End: m[1]}
		if m[2] >= 0 {
			t.Subject = s[m[2]:m[3]]
		}
		out = append(out, t)
	}
	return out
}

// closingEnd is the index in tags of the {{end}} that closes the block tags
// begin after, past the blocks within it; -1 when there is none.
func closingEnd(tags []Tag) int {
	depth := 0
	for i, t := range tags {
		switch {
		case t.Subject != "":
			depth++
		case depth == 0:
			return i
		default:
			depth--
		}
	}
	return -1
}

// parseSegments reads a prompt into its text and its blocks.
func parseSegments(s string) ([]segment, error) {
	var segs []segment
	for {
		tags := Tags(s)
		if len(tags) == 0 {
			if s != "" {
				segs = append(segs, segment{text: s})
			}
			return segs, nil
		}
		open := tags[0]
		if open.Subject == "" {
			return nil, fmt.Errorf("{{end}} with no {{match}} before it")
		}
		end := closingEnd(tags[1:])
		if end < 0 {
			return nil, fmt.Errorf("{{match %s}} has no {{end}}", open.Subject)
		}
		end++ // in tags
		before, inner, after := s[:open.Start], s[open.End:tags[end].Start], s[tags[end].End:]
		// A block on lines of its own leaves no line of its own behind.
		lineStart := strings.LastIndexByte(before, '\n') + 1
		lines := strings.TrimSpace(before[lineStart:]) == "" && strings.TrimLeft(inner[:strings.IndexByte(inner+"\n", '\n')], " \t") == ""
		if lines {
			before = before[:lineStart]
			if strings.HasPrefix(after, "\n") {
				after = after[1:]
			}
		}
		if before != "" {
			segs = append(segs, segment{text: before})
		}
		block, err := parseArms(open.Subject, inner)
		if err != nil {
			return nil, err
		}
		block.lines = lines
		segs = append(segs, segment{block: block})
		s = after
	}
}

// parseArms reads the arms of a block matching subject from inner, its
// text between its {{match}} and its {{end}}.
func parseArms(subject, inner string) (*matchBlock, error) {
	// Arms begin on lines at the block's own depth, not on those of a
	// block within.
	type armText struct {
		patterns []string
		lines    []string
	}
	var arms []armText
	depth := 0
	for line := range strings.SplitSeq(strings.TrimPrefix(inner, "\n"), "\n") {
		m := armLine.FindStringSubmatch(line)
		switch {
		case depth == 0 && m != nil:
			arms = append(arms, armText{patterns: patternOne.FindAllString(m[1], -1), lines: []string{m[2]}})
		case len(arms) > 0:
			arms[len(arms)-1].lines = append(arms[len(arms)-1].lines, line)
		case strings.TrimSpace(line) != "":
			return nil, fmt.Errorf("{{match %s}}: %q is not an arm, pattern => text", subject, strings.TrimSpace(line))
		}
		for _, t := range Tags(line) {
			if t.Subject != "" {
				depth++
			} else {
				depth--
			}
		}
	}
	if len(arms) == 0 {
		return nil, fmt.Errorf("{{match %s}} has no arms", subject)
	}
	block := &matchBlock{subject: subject}
	for _, a := range arms {
		body, err := parseSegments(armBody(a.lines))
		if err != nil {
			return nil, err
		}
		block.arms = append(block.arms, matchArm{patterns: a.patterns, body: body})
	}
	return block, nil
}

// armBody is an arm's text from its lines: the first's after =>, and the
// rest, their common indent taken off, blank lines at either end dropped,
// and a quoted string, a comma after it or not, as the string it is.
func armBody(lines []string) string {
	indent := -1
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	out := []string{lines[0]}
	for _, l := range lines[1:] {
		if len(l) >= indent && indent > 0 {
			l = l[indent:]
		}
		out = append(out, l)
	}
	body := strings.TrimSpace(strings.Join(out, "\n"))
	if q := strings.TrimSuffix(body, ","); len(q) >= 2 && q[0] == '"' {
		if s, err := strconv.Unquote(q); err == nil {
			return s
		}
	}
	return body
}

// matches reports whether value matches pattern.
func matches(pattern, value string) bool {
	switch {
	case pattern == "_":
		return true
	case len(pattern) >= 2 && pattern[0] == '"':
		return pattern[1:len(pattern)-1] == value
	}
	if m := rangeWord.FindStringSubmatch(pattern); m != nil {
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return false
		}
		if m[1] != "" {
			if lo, _ := strconv.ParseFloat(m[1], 64); v < lo {
				return false
			}
		}
		if m[3] != "" {
			hi, _ := strconv.ParseFloat(m[3], 64)
			return v < hi || (m[2] == "=" && v == hi)
		}
		return true
	}
	return pattern == value
}

// render is segs with each block given the text of its arm that matches
// its subject's value, which value gives.
func render(segs []segment, value func(name string) string) string {
	var b strings.Builder
	for _, s := range segs {
		if s.block == nil {
			b.WriteString(s.text)
			continue
		}
		v := value(s.block.subject)
		for _, arm := range s.block.arms {
			if slices.ContainsFunc(arm.patterns, func(p string) bool { return matches(p, v) }) {
				text := render(arm.body, value)
				b.WriteString(text)
				if s.block.lines && text != "" && !strings.HasSuffix(text, "\n") {
					b.WriteByte('\n')
				}
				break
			}
		}
	}
	return b.String()
}

// checkBlocks finds what is wrong with the blocks of segs: a subject that
// is neither an argument nor built in, a pattern its subject's values
// cannot match, and values no arm covers.
func (t *Template) checkBlocks(segs []segment) error {
	for _, s := range segs {
		if s.block == nil {
			continue
		}
		b := s.block
		var arg *Arg
		if arg = t.arg(b.subject); arg == nil && !IsBuiltin(b.subject) {
			return fmt.Errorf("{{match %s}}: no argument or built-in variable has that name", b.subject)
		}
		var covered []string
		for _, a := range b.arms {
			for _, p := range a.patterns {
				if err := checkPattern(arg, p); err != nil {
					return fmt.Errorf("{{match %s}}: %w", b.subject, err)
				}
			}
			covered = append(covered, a.patterns...)
			if err := t.checkBlocks(a.body); err != nil {
				return err
			}
		}
		if slices.Contains(covered, "_") {
			continue
		}
		values := arg.values()
		if values == nil {
			return fmt.Errorf("{{match %s}}: not every value is matched; add an arm _ => for the rest", b.subject)
		}
		for _, v := range values {
			if !slices.ContainsFunc(covered, func(p string) bool { return matches(p, v) }) {
				return fmt.Errorf("{{match %s}}: no arm matches %q; add one, or _ => for the rest", b.subject, v)
			}
		}
	}
	return nil
}

// values are every value a, as a call gives it, can have, nil when they
// are not few: a choice's options and a boolean's true and false, with ""
// for one a call may leave out with no default. A built-in variable's,
// nil a, are not few.
func (a *Arg) values() []string {
	if a == nil {
		return nil
	}
	var out []string
	switch a.Type {
	case Choice:
		out = slices.Clone(a.Options)
	case Boolean:
		out = []string{"true", "false"}
	default:
		return nil
	}
	if !a.Required && a.Default == nil {
		out = append(out, "")
	}
	return out
}

// checkPattern finds what is wrong with pattern p for arg, nil for a
// built-in variable: a range of what is not a number, or a value the
// argument does not take, or spells another way, which no call matches.
func checkPattern(arg *Arg, p string) error {
	if p == "_" || p == `""` || arg == nil {
		return nil
	}
	if rangeWord.MatchString(p) && p != ".." {
		if arg.Type != Integer && arg.Type != Number {
			return fmt.Errorf("%s: a range matches only numbers", p)
		}
		return nil
	}
	v := strings.Trim(p, `"`)
	as, err := arg.convert(v)
	if err != nil {
		return err
	}
	if as != v {
		return fmt.Errorf("%s: a call gives it as %s", p, as)
	}
	return nil
}
