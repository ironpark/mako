package prompttmpl

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The prompt is written as Svelte writes markup, its values in double
// braces, so that the braces of JSON and code in a prompt stay text:
//
//	{{file}}                       an argument's value, or a built-in variable's
//	{{args.depth * 2}}             a JavaScript expression's
//	{#if expr} … {:else if expr} … {:else} … {/if}
//	{#each expr as item, i} … {:else} … {/each}
//	{@const area = box.width * box.height}
//	{#match subject} {:pattern} … {:else} … {/match}
//	{#snippet name(params)} … {/snippet}  {@render name(args)}
//	```js … ```                    a script, its value in its place
//	```js run … ```                a script that only runs
//	{#raw} … {/raw}                text kept as it is written
//
// Expressions are JavaScript, run on QuickJS in a sandbox (run.go). They
// read the arguments by name, or as args, and the built-in variables by
// name, or as vars; a {{name}} of an argument is its value as the call
// gave it. A line of tags alone goes with them, so blocks over lines of
// their own leave no blank lines; a { that begins no tag is text.
//
// A raw block holds text as it is written, tags, braces and fences and
// all, for a prompt that shows another its syntax; the raw blocks within
// it pair up, so that one can show a raw block too.

// Tag kinds.
const (
	TagIf      = "if"      // {#if expr}
	TagElseIf  = "elseif"  // {:else if expr}, in an if
	TagElse    = "else"    // {:else}
	TagEach    = "each"    // {#each expr as item, index}
	TagMatch   = "match"   // {#match subject}
	TagArm     = "arm"     // {:patterns}, in a match
	TagSnippet = "snippet" // {#snippet name(params)}
	TagEnd     = "end"     // {/if}, {/each}, {/match}, {/snippet}
	TagConst   = "const"   // {@const declaration}
	TagRender  = "render"  // {@render call}
	TagRaw     = "raw"     // {#raw}, text up to its {/raw} as it is
)

// Tag is a tag of a prompt: its kind; the block it opens, closes or is
// in, "if", "each", "match" or "snippet"; its bytes [Start, End); what it
// holds after its keyword; and [From, To), what it takes of the prompt:
// its line, when it stands on one with tags alone, and an arm's or an
// else's the spaces after it.
type Tag struct {
	Kind, Block string
	Start, End  int
	Arg         string
	From, To    int
}

var (
	openTagRe  = regexp.MustCompile(`^#(if|each|match|snippet|raw)(?:\s+(.*?))?\s*$`)
	closeTagRe = regexp.MustCompile(`^/(if|each|match|snippet|raw)\s*$`)
	atTagRe    = regexp.MustCompile(`^@(const|render)(?:\s+(.*?))?\s*$`)
	elseTagRe  = regexp.MustCompile(`^:else(?:\s+if(?:\s+(.*?))?)?\s*$`)
)

// rawTagRe is a raw block's start or end, which pair up within one.
var rawTagRe = regexp.MustCompile(`\{([#/])raw\s*\}`)

// scan finds body's tags and its {{…}}s, but for those in a script's code
// or a raw block, and its scripts, but for those in a raw block.
func scan(body string) ([]Tag, []Placeholder, []Script) {
	var tags []Tag
	var holes []Placeholder
	scripts := fences(body)
	var open []string
	for i := 0; i < len(body); i++ {
		if body[i] != '{' {
			continue
		}
		if sc, ok := scriptAt(scripts, i); ok {
			i = sc.End - 1
			continue
		}
		if strings.HasPrefix(body[i:], "{{") {
			end := closingDouble(body, i+2)
			if end < 0 {
				continue
			}
			h := Placeholder{Start: i, End: end + 2}
			if in := strings.TrimSpace(body[i+2 : end]); IsName(in) {
				h.Name = in
			} else {
				h.Expr = in
			}
			holes = append(holes, h)
			i = end + 1
			continue
		}
		if i+1 >= len(body) || !strings.ContainsRune("#/@:", rune(body[i+1])) {
			continue
		}
		end := closingBrace(body, i+1)
		if end < 0 {
			continue
		}
		t, ok := tagOf(body[i+1:end], open)
		if !ok {
			continue
		}
		t.Start, t.End = i, end+1
		if t.Kind == TagRaw {
			// What it holds is text, up to the end that pairs with it, or
			// the prompt's end, which the parser refuses.
			tags = append(tags, t)
			from, to := rawEnd(body, t.End)
			if from < 0 {
				from, to = len(body), len(body)
			} else {
				tags = append(tags, Tag{Kind: TagEnd, Block: TagRaw, Start: from, End: to})
			}
			scripts = slices.DeleteFunc(scripts, func(s Script) bool { return s.Start >= t.End && s.Start < from })
			i = to - 1
			continue
		}
		switch t.Kind {
		case TagIf, TagEach, TagMatch, TagSnippet:
			open = append(open, t.Block)
		case TagEnd:
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
		}
		tags = append(tags, t)
		i = end
	}
	for i := range tags {
		tags[i].From, tags[i].To = tags[i].Start, tags[i].End
		if k := tags[i].Kind; k == TagArm || k == TagElse || k == TagElseIf {
			// Its text starts after the spaces that follow it.
			rest := body[tags[i].End:]
			tags[i].To = tags[i].End + len(rest) - len(strings.TrimLeft(rest, " \t"))
		}
	}
	standalone(body, tags, holes, scripts)
	return tags, holes, scripts
}

// rawEnd is where the {/raw} that ends the raw block whose start ends at
// from is, [start, end); -1s when none does.
func rawEnd(body string, from int) (int, int) {
	depth := 1
	for _, m := range rawTagRe.FindAllStringSubmatchIndex(body[from:], -1) {
		if body[from+m[2]] == '#' {
			depth++
		} else if depth--; depth == 0 {
			return from + m[0], from + m[1]
		}
	}
	return -1, -1
}

// tagOf reads what a tag holds between its braces, within the blocks
// open; ok is false for what is no tag, as a { : … } outside a block.
func tagOf(in string, open []string) (Tag, bool) {
	inner := ""
	if len(open) > 0 {
		inner = open[len(open)-1]
	}
	if m := openTagRe.FindStringSubmatch(in); m != nil {
		return Tag{Kind: map[string]string{"if": TagIf, "each": TagEach, "match": TagMatch, "snippet": TagSnippet, "raw": TagRaw}[m[1]], Block: m[1], Arg: m[2]}, true
	}
	if m := closeTagRe.FindStringSubmatch(in); m != nil {
		return Tag{Kind: TagEnd, Block: m[1]}, true
	}
	if m := atTagRe.FindStringSubmatch(in); m != nil {
		return Tag{Kind: map[string]string{"const": TagConst, "render": TagRender}[m[1]], Arg: m[2]}, true
	}
	if in[0] != ':' || inner == "" {
		return Tag{}, false
	}
	if m := elseTagRe.FindStringSubmatch(in); m != nil {
		if strings.HasPrefix(strings.TrimSpace(in[len(":else"):]), "if") {
			return Tag{Kind: TagElseIf, Block: inner, Arg: m[1]}, true
		}
		return Tag{Kind: TagElse, Block: inner}, true
	}
	if inner == TagMatch {
		return Tag{Kind: TagArm, Block: inner, Arg: strings.TrimSpace(in[1:])}, true
	}
	return Tag{}, false
}

// standalone has a line of tags alone, spaces aside, go with them: the
// first takes the line's start and the last its end.
func standalone(body string, tags []Tag, holes []Placeholder, scripts []Script) {
	for i := 0; i < len(tags); {
		lineStart := strings.LastIndexByte(body[:tags[i].Start], '\n') + 1
		j, at := i, lineStart
		for j < len(tags) && strings.TrimSpace(body[at:tags[j].Start]) == "" && !strings.Contains(body[at:tags[j].Start], "\n") {
			at = tags[j].End
			j++
		}
		lineEnd := strings.IndexByte(body[at:], '\n')
		if lineEnd < 0 {
			lineEnd = len(body) - at
		}
		if j > i && strings.TrimSpace(body[at:at+lineEnd]) == "" {
			tags[i].From = lineStart
			for k := i; k < j; k++ {
				tags[k].To = min(at+lineEnd+1, len(body))
				if k+1 < j {
					tags[k].To = tags[k+1].Start
				}
			}
		}
		i = max(j, i+1)
	}
}

// closingBrace is the index of the } that closes the { before from, past
// braces nested in it and those in quotes, on its line; -1 when none does.
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
			return -1
		}
	}
	return -1
}

// closingDouble is the index of the }} that closes the {{ before from,
// past braces nested in it and those in quotes, on its line; -1 when none
// does.
func closingDouble(s string, from int) int {
	depth := 0
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
			if depth == 0 {
				if i+1 < len(s) && s[i+1] == '}' {
					return i
				}
				return -1
			}
			depth--
		case c == '\n':
			return -1
		}
	}
	return -1
}

// Tags are body's tags, in order.
func Tags(body string) []Tag { tags, _, _ := scan(body); return tags }

// Placeholders are body's {{…}}s, in order, but for those in a script's
// code or a raw block.
func Placeholders(body string) []Placeholder { _, holes, _ := scan(body); return holes }

// Scripts are the fenced blocks of JavaScript in body, in order, but for
// those in a raw block.
func Scripts(body string) []Script { _, _, scripts := scan(body); return scripts }

// node is a piece of a prompt read: text, a {{…}}, a script, a tag of its
// own or a block with what it holds.
type node struct {
	kind string // "text", "hole", "script", or a tag's kind
	text string // text's; a hole's name or expression; a script's code; a tag's
	raw  string // a hole as written, braces and all
	name bool   // a hole holds a name alone
	run  bool   // a script only runs
	nl   bool   // a script's block ended a line
	// if's branches, the last's cond "" for else; each's iterable, item
	// and index, its body and else; match's subject and arms; snippet's
	// name, params and body.
	branches     []branch
	expr, as, at string
	body, empty  []*node
	arms         []arm
}

type branch struct {
	cond string
	body []*node
}

type arm struct {
	patterns []string
	body     []*node
}

// piece is a tag, a hole or a script, by where it is in the prompt.
type piece struct {
	from, to int
	tag      *Tag
	hole     *Placeholder
	script   *Script
}

// parseBody reads body into what it holds.
func parseBody(body string) ([]*node, error) {
	tags, holes, scripts := scan(body)
	var pieces []piece
	for i := range tags {
		pieces = append(pieces, piece{from: tags[i].From, to: tags[i].To, tag: &tags[i]})
	}
	for i := range holes {
		pieces = append(pieces, piece{from: holes[i].Start, to: holes[i].End, hole: &holes[i]})
	}
	for i := range scripts {
		pieces = append(pieces, piece{from: scripts[i].Start, to: scripts[i].End, script: &scripts[i]})
	}
	slices.SortFunc(pieces, func(a, b piece) int { return a.from - b.from })
	p := &parser{body: body, pieces: pieces}
	nodes, stop, err := p.run()
	if err != nil {
		return nil, err
	}
	if stop != nil {
		return nil, fmt.Errorf("%s with no block open", tagText(stop))
	}
	return nodes, nil
}

type parser struct {
	body   string
	pieces []piece
	i, at  int
}

// run reads up to a tag that ends a run, an else, an arm or an end, which
// it gives, nil at the prompt's end.
func (p *parser) run() ([]*node, *Tag, error) {
	var nodes []*node
	for ; p.i < len(p.pieces); p.i++ {
		pc := p.pieces[p.i]
		if pc.from > p.at {
			nodes = append(nodes, &node{kind: "text", text: p.body[p.at:pc.from]})
		}
		p.at = max(p.at, pc.to)
		switch {
		case pc.hole != nil:
			nodes = append(nodes, &node{kind: "hole", text: pc.hole.Name + pc.hole.Expr, name: pc.hole.Name != "",
				raw: p.body[pc.hole.Start:pc.hole.End]})
		case pc.script != nil:
			nodes = append(nodes, &node{kind: "script", text: pc.script.Code, run: pc.script.Run,
				nl: strings.HasSuffix(p.body[pc.script.Start:pc.script.End], "\n")})
		default:
			t := pc.tag
			switch t.Kind {
			case TagElse, TagElseIf, TagArm, TagEnd:
				return nodes, t, nil
			case TagConst, TagRender:
				if t.Arg == "" {
					return nil, nil, fmt.Errorf("%s needs what to %s", tagText(t), t.Kind)
				}
				nodes = append(nodes, &node{kind: t.Kind, text: t.Arg})
			default:
				p.i++
				n, err := p.block(t)
				if err != nil {
					return nil, nil, err
				}
				nodes = append(nodes, n)
			}
		}
	}
	if p.at < len(p.body) {
		nodes = append(nodes, &node{kind: "text", text: p.body[p.at:]})
		p.at = len(p.body)
	}
	return nodes, nil, nil
}

// block reads the block open opens, up to its end, which it takes.
func (p *parser) block(open *Tag) (*node, error) {
	n := &node{kind: open.Kind}
	if open.Kind == TagRaw && open.Arg != "" {
		return nil, fmt.Errorf("%s: a raw block takes nothing after raw", tagText(open))
	}
	if open.Arg == "" && open.Kind != TagRaw {
		return nil, fmt.Errorf("%s needs %s", tagText(open), map[string]string{"if": "a condition", "each": "a list", "match": "what to match", "snippet": "a name"}[open.Block])
	}
	body, stop, err := p.run()
	if err != nil {
		return nil, err
	}
	end := func(stop *Tag) error {
		switch {
		case stop == nil:
			return fmt.Errorf("%s has no {/%s}", tagText(open), open.Block)
		case stop.Kind != TagEnd:
			return fmt.Errorf("%s in %s", tagText(stop), tagText(open))
		case stop.Block != open.Block:
			return fmt.Errorf("{/%s} closes %s", stop.Block, tagText(open))
		}
		return nil
	}
	switch open.Kind {
	case TagIf:
		n.branches = []branch{{cond: open.Arg, body: body}}
		for stop != nil && (stop.Kind == TagElseIf || stop.Kind == TagElse) {
			cond := stop.Arg
			if stop.Kind == TagElseIf && cond == "" {
				return nil, fmt.Errorf("{:else if} in %s needs a condition", tagText(open))
			}
			if n.branches[len(n.branches)-1].cond == "" {
				return nil, fmt.Errorf("%s after {:else} in %s", tagText(stop), tagText(open))
			}
			p.i++
			if body, stop, err = p.run(); err != nil {
				return nil, err
			}
			n.branches = append(n.branches, branch{cond: cond, body: body})
		}
	case TagEach:
		var err error
		if n.expr, n.as, n.at, err = eachParts(open.Arg); err != nil {
			return nil, fmt.Errorf("%s: %w", tagText(open), err)
		}
		n.body = body
		if stop != nil && stop.Kind == TagElse {
			p.i++
			if n.empty, stop, err = p.run(); err != nil {
				return nil, err
			}
		}
	case TagMatch:
		n.expr = open.Arg
		if strings.TrimSpace(nodesText(body)) != "" {
			return nil, fmt.Errorf("%s: text before its first {:pattern}", tagText(open))
		}
		for stop != nil && (stop.Kind == TagArm || stop.Kind == TagElse) {
			pats := stop.Arg
			if stop.Kind == TagElse || pats == "else" {
				pats = "_"
			}
			if !patternList.MatchString(pats) {
				return nil, fmt.Errorf("%s: {:%s} is not a pattern", tagText(open), stop.Arg)
			}
			p.i++
			if body, stop, err = p.run(); err != nil {
				return nil, err
			}
			n.arms = append(n.arms, arm{patterns: patternOne.FindAllString(pats, -1), body: body})
		}
		if len(n.arms) == 0 && stop != nil && stop.Kind == TagEnd {
			return nil, fmt.Errorf("%s has no arms", tagText(open))
		}
	case TagSnippet:
		m := snippetRe.FindStringSubmatch(open.Arg)
		if m == nil {
			return nil, fmt.Errorf("%s: a snippet is name(params)", tagText(open))
		}
		n.expr, n.as, n.body = m[1], m[2], body
	case TagRaw:
		// It is the text it holds, which the scan left as text.
		n.kind, n.text = "text", nodesText(body)
	}
	if err := end(stop); err != nil {
		return nil, err
	}
	return n, nil
}

// snippetRe is a snippet's name and its parameters.
var snippetRe = regexp.MustCompile(`^([A-Za-z_$][\w$]*)\s*\((.*)\)\s*$`)

// eachParts reads an each's "list as item, index (key)".
func eachParts(arg string) (list, item, index string, err error) {
	at := lastTopLevel(arg, " as ")
	if at < 0 {
		return "", "", "", fmt.Errorf("an each is list as item")
	}
	list, rest := strings.TrimSpace(arg[:at]), strings.TrimSpace(arg[at+4:])
	if k := strings.LastIndexByte(rest, '('); k > 0 && strings.HasSuffix(rest, ")") && topLevel(rest, k) {
		rest = strings.TrimSpace(rest[:k]) // a key, which a prompt has no use for
	}
	if c := lastTopLevel(rest, ","); c >= 0 {
		item, index = strings.TrimSpace(rest[:c]), strings.TrimSpace(rest[c+1:])
	} else {
		item = rest
	}
	if list == "" || item == "" {
		return "", "", "", fmt.Errorf("an each is list as item")
	}
	return list, item, index, nil
}

// lastTopLevel is the index of the last sep in s outside brackets and
// quotes, -1 for none.
func lastTopLevel(s, sep string) int {
	last := -1
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep && topLevel(s, i) {
			last = i
		}
	}
	return last
}

// topLevel reports whether s's byte i is outside brackets and quotes.
func topLevel(s string, i int) bool {
	depth := 0
	var quote byte
	for j := 0; j < i; j++ {
		c := s[j]
		switch {
		case quote != 0:
			if c == '\\' {
				j++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case strings.IndexByte("([{", c) >= 0:
			depth++
		case strings.IndexByte(")]}", c) >= 0:
			depth--
		}
	}
	return depth == 0 && quote == 0
}

// nodesText is the text of nodes, for a match's body before its first arm.
func nodesText(nodes []*node) string {
	var b strings.Builder
	for _, n := range nodes {
		if n.kind == "text" {
			b.WriteString(n.text)
		} else {
			b.WriteString("x")
		}
	}
	return b.String()
}

// tagText is a tag as it is written, for a message.
func tagText(t *Tag) string {
	switch t.Kind {
	case TagEnd:
		return "{/" + t.Block + "}"
	case TagElse:
		return "{:else}"
	case TagElseIf:
		return "{:else if " + t.Arg + "}"
	case TagArm:
		return "{:" + t.Arg + "}"
	case TagConst:
		return "{@const " + t.Arg + "}"
	case TagRender:
		return "{@render " + t.Arg + "}"
	}
	return strings.TrimSpace("{#"+t.Block+" "+t.Arg) + "}"
}
