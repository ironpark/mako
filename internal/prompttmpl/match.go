package prompttmpl

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A prompt picks text by a value in blocks written as Svelte writes its
// own, and matched as Rust's match matches: {#match subject} opens one,
// each {:pattern} begins an arm for the values its pattern matches, and
// {/match} closes it. The first arm that matches is taken; its text runs
// to the next arm, and may hold {{name}}s, blocks of its own and scripts.
//
//	{#match tone}
//	{:kind | gentle}
//	Be encouraging.
//	{:blunt}
//	Be direct.
//	{:else}
//	Be neutral.
//	{/match}
//
//	Read it {#match args.depth > 2}{:true}closely{:false}quickly{/match}.
//
// A subject is an argument's name or a built-in variable's, or a
// JavaScript expression, which reads them as args and vars and what the
// scripts before it defined (script.go). A pattern is a word, as a value
// is spelt; a "quoted" text; a range of numbers, 1..=3 with its end, 1..3
// without, or open at either end, 3.. and ..=10; or _ for any value, which
// {:else} is too. Patterns are joined with | to match any of them. A line
// of tags alone goes with them, and an arm's text starts after the spaces
// that follow its tag on its line. As in Rust, a block must cover every
// value: a choice's options, true and false, or anything with {:else}.

// patternWord is one pattern of an arm, and patternList an arm's
// patterns, joined by |.
var (
	patternWord = `"[^"\n]*"|(?:-?\d+(?:\.\d+)?)?\.\.=?(?:-?\d+(?:\.\d+)?)?|[^\s|"]+`
	patternList = regexp.MustCompile(`^\s*(?:` + patternWord + `)(?:\s*\|\s*(?:` + patternWord + `))*\s*$`)
	patternOne  = regexp.MustCompile(patternWord)
	rangeWord   = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)?\.\.(=)?(-?\d+(?:\.\d+)?)?$`)
)

// segment is a stretch of a prompt: text as it is, {{name}}s and scripts
// and all, or a match block.
type segment struct {
	text  string
	block *matchBlock
}

// matchBlock is a {#match subject} block: what it matches, and its arms.
type matchBlock struct {
	subject string
	arms    []matchArm
}

// matchArm is an arm of a block: its patterns, as written, and its text.
type matchArm struct {
	patterns []string
	body     []segment
}

// Tag kinds.
const (
	TagMatch = "match" // {#match subject}
	TagArm   = "arm"   // {:patterns}
	TagEnd   = "end"   // {/match}
)

// Tag is a tag of a prompt's blocks: its kind; its bytes [Start, End);
// the subject a match matches, or an arm's patterns as written; and
// [From, To), what it takes of the prompt, its line when it stands on one
// with tags alone, and an arm's the spaces after it.
type Tag struct {
	Kind       string
	Start, End int
	Arg        string
	From, To   int
}

// Tags are s's block tags, in order: {#match subject}s, the {:pattern}s
// within a block, and {/match}s; none in a script's code. A { that begins
// no tag, as JSON's, is text.
func Tags(s string) []Tag {
	var out []Tag
	scripts := Scripts(s)
	depth := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		if sc, ok := scriptAt(scripts, i); ok {
			i = sc.End - 1
			continue
		}
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, "{#match") && len(rest) > 7 && (rest[7] == ' ' || rest[7] == '\t'):
			end := closingBrace(s, i+1)
			if end < 0 {
				continue
			}
			out = append(out, Tag{Kind: TagMatch, Start: i, End: end + 1, Arg: strings.TrimSpace(s[i+7 : end])})
			depth++
			i = end
		case strings.HasPrefix(rest, "{:") && depth > 0:
			end := closingBrace(s, i+1)
			if end < 0 {
				continue
			}
			out = append(out, Tag{Kind: TagArm, Start: i, End: end + 1, Arg: strings.TrimSpace(s[i+2 : end])})
			i = end
		case strings.HasPrefix(rest, "{/match}"):
			out = append(out, Tag{Kind: TagEnd, Start: i, End: i + len("{/match}")})
			depth = max(depth-1, 0)
			i += len("{/match}") - 1
		}
	}
	for i := range out {
		out[i].From, out[i].To = out[i].Start, out[i].End
		if out[i].Kind == TagArm {
			// An arm's text starts after the spaces that follow it.
			rest := s[out[i].End:]
			out[i].To = out[i].End + len(rest) - len(strings.TrimLeft(rest, " \t"))
		}
	}
	// A line of tags alone, spaces aside, goes with them.
	for i := 0; i < len(out); {
		lineStart := strings.LastIndexByte(s[:out[i].Start], '\n') + 1
		j, at := i, lineStart
		for j < len(out) && strings.TrimSpace(s[at:out[j].Start]) == "" && !strings.Contains(s[at:out[j].Start], "\n") {
			at = out[j].End
			j++
		}
		lineEnd := strings.IndexByte(s[at:], '\n')
		if lineEnd < 0 {
			lineEnd = len(s) - at
		}
		if j > i && strings.TrimSpace(s[at:at+lineEnd]) == "" {
			out[i].From = lineStart
			for k := i; k < j; k++ {
				out[k].To = min(at+lineEnd+1, len(s))
				if k+1 < j {
					out[k].To = out[k+1].Start
				}
			}
		}
		i = max(j, i+1)
	}
	return out
}

// closingBrace is the index of the } that closes the { before from, past
// braces nested in it and those in quotes; -1 when none does.
func closingBrace(s string, from int) int {
	depth := 1
	var quote byte
	for i := from; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return i
			}
		case c == '\n':
			return -1 // a tag is on one line
		}
	}
	return -1
}

// parseSegments reads a prompt into its text and its blocks.
func parseSegments(s string) ([]segment, error) {
	tags := Tags(s)
	segs, i, at, err := parseRun(s, tags, 0, 0)
	if err != nil {
		return nil, err
	}
	if i < len(tags) {
		if tags[i].Kind == TagEnd {
			return nil, fmt.Errorf("{/match} with no {#match} before it")
		}
		return nil, fmt.Errorf("{:%s} is outside a {#match}", tags[i].Arg)
	}
	if at < len(s) {
		segs = append(segs, segment{text: s[at:]})
	}
	return segs, nil
}

// parseRun reads s from at, tags[i:] in it, up to an arm's or a block's
// end at this depth: the text and blocks before it, the index of the tag
// it stopped at, len(tags) at the end, and where in s it stopped.
func parseRun(s string, tags []Tag, i, at int) ([]segment, int, int, error) {
	var segs []segment
	for ; i < len(tags); i++ {
		t := tags[i]
		if t.From > at {
			segs = append(segs, segment{text: s[at:t.From]})
		}
		if t.Kind != TagMatch {
			return segs, i, max(at, t.From), nil
		}
		block, end, err := parseBlock(s, tags, i)
		if err != nil {
			return nil, 0, 0, err
		}
		segs = append(segs, segment{block: block})
		i, at = end, tags[end].To
	}
	return segs, i, at, nil
}

// parseBlock reads the block tags[i] opens, giving it and the index of the
// tag that closes it.
func parseBlock(s string, tags []Tag, i int) (*matchBlock, int, error) {
	open := tags[i]
	block := &matchBlock{subject: open.Arg}
	j := i + 1
	if j < len(tags) && strings.TrimSpace(s[open.To:tags[j].From]) != "" {
		return nil, 0, fmt.Errorf("{#match %s}: text before its first {:pattern}", open.Arg)
	}
	for j < len(tags) && tags[j].Kind == TagArm {
		arm := tags[j]
		if arm.Arg == "else" {
			arm.Arg = "_"
		}
		if !patternList.MatchString(arm.Arg) {
			return nil, 0, fmt.Errorf("{#match %s}: {:%s} is not a pattern", open.Arg, arm.Arg)
		}
		body, next, _, err := parseRun(s, tags, j+1, arm.To)
		if err != nil {
			return nil, 0, err
		}
		block.arms = append(block.arms, matchArm{patterns: patternOne.FindAllString(arm.Arg, -1), body: body})
		j = next
	}
	switch {
	case j >= len(tags):
		return nil, 0, fmt.Errorf("{#match %s} has no {/match}", open.Arg)
	case len(block.arms) == 0:
		return nil, 0, fmt.Errorf("{#match %s} has no arms", open.Arg)
	}
	return block, j, nil
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

// checkBlocks finds what is wrong with the blocks of segs: a subject that
// is neither an argument nor built in, a pattern its subject's values
// cannot match, and values no arm covers.
func (t *Template) checkBlocks(segs []segment) error {
	for _, s := range segs {
		if s.block == nil {
			continue
		}
		b := s.block
		arg := t.arg(b.subject)
		if t.isExpression(b.subject) && IsName(b.subject) {
			return fmt.Errorf("{#match %s}: no argument or built-in variable has that name", b.subject)
		}
		var covered []string
		for _, a := range b.arms {
			for _, p := range a.patterns {
				if err := checkPattern(arg, p); err != nil {
					return fmt.Errorf("{#match %s}: %w", b.subject, err)
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
		if values == nil && slices.Contains(covered, "true") && slices.Contains(covered, "false") {
			continue // an expression's true or false
		}
		if values == nil {
			return fmt.Errorf("{#match %s}: not every value is matched; add {:else} for the rest", b.subject)
		}
		for _, v := range values {
			if !slices.ContainsFunc(covered, func(p string) bool { return matches(p, v) }) {
				return fmt.Errorf("{#match %s}: no arm matches %q; add one, or {:else} for the rest", b.subject, v)
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

// isExpression reports whether a block's subject is JavaScript, rather
// than an argument's name or a built-in variable's.
func (t *Template) isExpression(subject string) bool {
	return t.arg(subject) == nil && !IsBuiltin(subject)
}

// expressions are the subjects of segs' blocks that are JavaScript, in
// order, those of blocks in arms too.
func (t *Template) expressions(segs []segment) []string {
	var out []string
	for _, s := range segs {
		if s.block == nil {
			continue
		}
		if t.isExpression(s.block.subject) {
			out = append(out, s.block.subject)
		}
		for _, a := range s.block.arms {
			out = append(out, t.expressions(a.body)...)
		}
	}
	return out
}
