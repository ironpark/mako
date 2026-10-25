package syntax

import (
	"bytes"
	"container/heap"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Query is a compiled tree-sitter query, such as a highlights.scm.
//
// Text predicates, which the C runtime leaves to the host, are evaluated
// here: #eq?, #not-eq?, #any-eq?, #any-not-eq?, #match?, #not-match?,
// #any-match?, #any-not-match?, #any-of? and #not-any-of?. #set! is kept
// as per-pattern properties. Other predicates (#is?, #select-adjacent!,
// ...) are ignored.
type Query struct {
	lang     *Language
	ptr      uint32 // TSQuery *
	names    []string
	patterns []patternInfo
}

type patternInfo struct {
	preds []predicate
	props map[string]string
}

type predKind int

const (
	predEq predKind = iota
	predMatch
	predAnyOf
)

type predicate struct {
	kind    predKind
	neg     bool // not- variant
	any     bool // any- variant: one node of a quantified capture suffices
	capture uint32
	other   int // capture compared against for #eq? @a @b, else -1
	value   string
	re      *regexp.Regexp
	set     map[string]bool
}

// QueryError reports where a query failed to compile. Zed's queries can
// name nodes or fields that an older grammar lacks; that lands here.
type QueryError struct {
	Offset int    // byte offset into the query source
	Row    int    // zero-based line of Offset
	Kind   string // syntax, node type, field, capture, structure, language, predicate
	Text   string // the query text around Offset
}

func (e *QueryError) Error() string {
	return fmt.Sprintf("syntax: query %s error at line %d: %q", e.Kind, e.Row+1, e.Text)
}

var queryErrorKinds = []string{"", "syntax", "node type", "field", "capture", "structure", "language"}

func newQueryError(src string, off int, kind string) *QueryError {
	off = min(max(off, 0), len(src))
	end := off
	for end < len(src) && end-off < 40 && src[end] != '\n' {
		end++
	}
	return &QueryError{
		Offset: off,
		Row:    strings.Count(src[:off], "\n"),
		Kind:   kind,
		Text:   src[off:end],
	}
}

// NewQuery compiles a query for the language. A compile failure is a
// *QueryError.
func (l *Language) NewQuery(source string) (*Query, error) {
	r := l.rt
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usable(); err != nil {
		return nil, err
	}
	src, err := r.writeBytes([]byte(source))
	if err != nil {
		return nil, err
	}
	defer r.free(src)
	out, err := r.malloc(8)
	if err != nil {
		return nil, err
	}
	defer r.free(out)
	ptr, err := r.call1("shim_query_new", uint64(l.ptr), uint64(src), uint64(len(source)), uint64(out))
	if err != nil {
		return nil, err
	}
	if ptr == 0 {
		off, _ := r.readUint32(out)
		kind, _ := r.readUint32(out + 4)
		k := "unknown"
		if int(kind) < len(queryErrorKinds) {
			k = queryErrorKinds[kind]
		}
		return nil, newQueryError(source, int(off), k)
	}
	q := &Query{lang: l, ptr: ptr}
	if err := q.load(source); err != nil {
		r.call("shim_query_delete", uint64(ptr))
		return nil, err
	}
	return q, nil
}

// load reads capture names and predicates. Callers hold the lock.
func (q *Query) load(source string) error {
	r := q.lang.rt
	n, err := r.call1("shim_query_capture_count", uint64(q.ptr))
	if err != nil {
		return err
	}
	lenp, err := r.malloc(4)
	if err != nil {
		return err
	}
	defer r.free(lenp)
	str := func(fn string, id uint32) (string, error) {
		p, err := r.call1(fn, uint64(q.ptr), uint64(id), uint64(lenp))
		if err != nil {
			return "", err
		}
		n, _ := r.readUint32(lenp)
		return r.readString(p, n), nil
	}
	q.names = make([]string, n)
	for i := range q.names {
		if q.names[i], err = str("shim_query_capture_name", uint32(i)); err != nil {
			return err
		}
	}
	np, err := r.call1("shim_query_pattern_count", uint64(q.ptr))
	if err != nil {
		return err
	}
	q.patterns = make([]patternInfo, np)
	for i := range q.patterns {
		p, err := r.call1("shim_query_predicates", uint64(q.ptr), uint64(i), uint64(lenp))
		if err != nil {
			return err
		}
		steps, _ := r.readUint32(lenp)
		raw, ok := r.read(p, steps*8)
		if !ok {
			return errWasm
		}
		raw = bytes.Clone(raw)
		var args []predArg
		for s := range steps {
			typ := binary.LittleEndian.Uint32(raw[s*8:])
			val := binary.LittleEndian.Uint32(raw[s*8+4:])
			switch typ {
			case 0: // done
				if err := q.addPredicate(i, args, source); err != nil {
					return err
				}
				args = args[:0]
			case 1: // capture
				args = append(args, predArg{capture: true, id: val})
			case 2: // string
				s, err := str("shim_query_string_value", val)
				if err != nil {
					return err
				}
				args = append(args, predArg{id: val, str: s})
			}
		}
	}
	return nil
}

type predArg struct {
	capture bool
	id      uint32
	str     string
}

func (q *Query) addPredicate(pattern int, args []predArg, source string) error {
	if len(args) == 0 || args[0].capture {
		return nil
	}
	op := args[0].str
	args = args[1:]
	pi := &q.patterns[pattern]
	bad := func(msg string) error {
		e := newQueryError(source, q.patternStart(pattern), "predicate")
		e.Text = fmt.Sprintf("#%s %s", op, msg)
		return e
	}

	if op == "set!" {
		if len(args) > 0 && args[0].capture {
			args = args[1:] // old form: #set! @capture key value
		}
		if len(args) == 0 {
			return nil
		}
		if pi.props == nil {
			pi.props = map[string]string{}
		}
		v := ""
		if len(args) > 1 && !args[1].capture {
			v = args[1].str
		}
		pi.props[args[0].str] = v
		return nil
	}

	p := predicate{other: -1}
	name := op
	if n, ok := strings.CutPrefix(name, "any-"); ok && n != "of?" {
		p.any, name = true, n
	}
	if n, ok := strings.CutPrefix(name, "not-"); ok {
		p.neg, name = true, n
	}
	switch name {
	case "eq?":
		p.kind = predEq
	case "match?":
		p.kind = predMatch
	case "any-of?":
		p.kind = predAnyOf
	default:
		return nil // not a text predicate we know; ignore it
	}
	if len(args) < 2 || !args[0].capture {
		return bad("needs a capture and a value")
	}
	p.capture = args[0].id
	switch p.kind {
	case predEq:
		if len(args) != 2 {
			return bad("takes two arguments")
		}
		if args[1].capture {
			p.other = int(args[1].id)
		} else {
			p.value = args[1].str
		}
	case predMatch:
		if len(args) != 2 || args[1].capture {
			return bad("takes a capture and a regex")
		}
		re, err := compileRegex(args[1].str)
		if err != nil {
			return bad(err.Error())
		}
		p.re = re
	case predAnyOf:
		p.set = map[string]bool{}
		for _, a := range args[1:] {
			if a.capture {
				return bad("takes strings")
			}
			p.set[a.str] = true
		}
	}
	pi.preds = append(pi.preds, p)
	return nil
}

// compileRegex compiles a query regex. Zed's queries are written for
// Rust's regex crate, whose syntax Go's RE2 shares except for a few
// extensions (class set operations, Unicode-aware \d \w \b); those fail
// to compile and surface as a query error.
func compileRegex(s string) (*regexp.Regexp, error) {
	return regexp.Compile(s)
}

// patternStart is the byte offset of a pattern in the query source.
func (q *Query) patternStart(pattern int) int {
	r := q.lang.rt
	res, err := r.call1("shim_query_pattern_start", uint64(q.ptr), uint64(pattern))
	if err != nil {
		return 0
	}
	return int(res)
}

// CaptureNames lists the query's capture names; Capture.Index points here.
func (q *Query) CaptureNames() []string { return q.names }

// CaptureIndex returns the index of a capture name, or -1.
func (q *Query) CaptureIndex(name string) int { return slices.Index(q.names, name) }

// PatternProperties returns the #set! key/values of a pattern, such as
// injection.language for injections.scm. It is nil if there are none.
func (q *Query) PatternProperties(pattern int) map[string]string {
	if pattern < 0 || pattern >= len(q.patterns) {
		return nil
	}
	return q.patterns[pattern].props
}

// Close frees the query.
func (q *Query) Close() {
	r := q.lang.rt
	r.mu.Lock()
	defer r.mu.Unlock()
	if q.ptr == 0 {
		return
	}
	if r.usable() == nil {
		r.call("shim_query_delete", uint64(q.ptr))
	}
	q.ptr = 0
}

// Capture is one captured node.
type Capture struct {
	Start, End int // byte range of the node
	Index      int // into CaptureNames
	Pattern    int // index of the pattern that matched
}

// Match is one match of a pattern: its captures in pattern order.
type Match struct {
	Pattern  int
	Captures []Capture
}

// Matches runs the query over the part of the tree that intersects the
// bytes [start, end) and returns the matches whose predicates hold. src
// must be the text the tree was parsed from.
func (q *Query) Matches(t *Tree, src []byte, start, end int) ([]Match, error) {
	if t.lang != q.lang {
		return nil, errors.New("syntax: tree and query are for different languages")
	}
	r := q.lang.rt
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usable(); err != nil {
		return nil, err
	}
	if q.ptr == 0 || t.ptr == 0 {
		return nil, errors.New("syntax: query or tree is closed")
	}
	lenp, err := r.malloc(4)
	if err != nil {
		return nil, err
	}
	defer r.free(lenp)
	start = max(start, 0)
	end = max(end, start)
	p, err := r.call1("shim_query_matches", uint64(q.ptr), uint64(t.ptr),
		uint64(uint32(start)), uint64(uint32(min(end, 1<<32-1))), matchLimit, uint64(lenp))
	if err != nil {
		return nil, err
	}
	n, _ := r.readUint32(lenp)
	raw, ok := r.read(p, n*4)
	if !ok {
		return nil, errWasm
	}

	var out []Match
	var caps []Capture
	for i := 0; i+2 <= len(raw)/4; {
		pattern := int(binary.LittleEndian.Uint32(raw[i*4:]))
		nc := int(binary.LittleEndian.Uint32(raw[i*4+4:]))
		i += 2
		if i+nc*3 > len(raw)/4 {
			return nil, errWasm
		}
		caps = caps[:0]
		for range nc {
			caps = append(caps, Capture{
				Index:   int(binary.LittleEndian.Uint32(raw[i*4:])),
				Start:   int(binary.LittleEndian.Uint32(raw[i*4+4:])),
				End:     int(binary.LittleEndian.Uint32(raw[i*4+8:])),
				Pattern: pattern,
			})
			i += 3
		}
		if pattern < len(q.patterns) && !q.patterns[pattern].holds(caps, src) {
			continue
		}
		out = append(out, Match{Pattern: pattern, Captures: slices.Clone(caps)})
	}
	return out, nil
}

// Captures returns the captures of every match intersecting [start, end),
// sorted by Start, then longer (outer) first, then by pattern. Captures
// whose names start with "_" are helpers for predicates and left out.
//
// Captures overlap. A painter should let the inner node win over the
// outer, and of two captures on the same node the later pattern, as
// tree-sitter-highlight (0.25 and later), Neovim and Zed do; queries put
// generic patterns first and specific ones after. Applying captures in
// this order does that; Spans resolves them into non-overlapping runs.
func (q *Query) Captures(t *Tree, src []byte, start, end int) ([]Capture, error) {
	ms, err := q.Matches(t, src, start, end)
	if err != nil {
		return nil, err
	}
	var out []Capture
	for _, m := range ms {
		for _, c := range m.Captures {
			if strings.HasPrefix(q.names[c.Index], "_") {
				continue
			}
			if c.Start >= end || c.End <= start && !(c.Start == c.End && c.Start >= start) {
				continue
			}
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, byPosition)
	return out, nil
}

// byPosition orders captures by where they start, the longer first, then
// by pattern.
func byPosition(a, b Capture) int {
	if a.Start != b.Start {
		return a.Start - b.Start
	}
	if a.End != b.End {
		return b.End - a.End
	}
	return a.Pattern - b.Pattern
}

// holds evaluates the pattern's text predicates on one match.
func (p *patternInfo) holds(caps []Capture, src []byte) bool {
	for i := range p.preds {
		if !p.preds[i].holds(caps, src) {
			return false
		}
	}
	return true
}

func text(src []byte, c Capture) []byte {
	if c.Start < 0 || c.End > len(src) || c.Start > c.End {
		return nil
	}
	return src[c.Start:c.End]
}

func nodesOf(caps []Capture, id uint32) []Capture {
	var out []Capture
	for _, c := range caps {
		if c.Index == int(id) {
			out = append(out, c)
		}
	}
	return out
}

// holds follows tree-sitter's semantics: for a quantified capture every
// node must pass, or with the any- variant at least one. #any-of? and
// #not-any-of? always need every node to pass.
func (p *predicate) holds(caps []Capture, src []byte) bool {
	nodes := nodesOf(caps, p.capture)
	if p.kind == predAnyOf {
		for _, c := range nodes {
			if p.set[string(text(src, c))] == p.neg {
				return false
			}
		}
		return true
	}
	var others []Capture
	if p.other >= 0 {
		others = nodesOf(caps, uint32(p.other))
	}
	for i, c := range nodes {
		var ok bool
		switch {
		case p.kind == predMatch:
			ok = p.re.Match(text(src, c))
		case p.other >= 0:
			if i >= len(others) {
				break
			}
			ok = bytes.Equal(text(src, c), text(src, others[i]))
		default:
			ok = string(text(src, c)) == p.value
		}
		ok = ok != p.neg
		if p.any && ok {
			return true
		}
		if !p.any && !ok {
			return false
		}
	}
	return !p.any || len(nodes) == 0
}

// Spans resolves captures (as returned by Captures) into non-overlapping
// spans, each carrying the capture that wins its bytes: the innermost one,
// and for equal ranges the later pattern. Adjacent spans with the same
// capture are merged.
func Spans(caps []Capture) []Capture {
	if len(caps) == 0 {
		return nil
	}
	// Order so that the winner of any byte is the latest active capture,
	// as Captures already does.
	if !slices.IsSortedFunc(caps, byPosition) {
		caps = slices.Clone(caps)
		slices.SortStableFunc(caps, byPosition)
	}
	pts := make([]int, 0, len(caps)*2)
	for _, c := range caps {
		if c.End > c.Start {
			pts = append(pts, c.Start, c.End)
		}
	}
	slices.Sort(pts)
	pts = slices.Compact(pts)

	var out []Capture
	h := &activeHeap{}
	j := 0
	for k := 0; k+1 < len(pts); k++ {
		a, b := pts[k], pts[k+1]
		for j < len(caps) && caps[j].Start <= a {
			if caps[j].End > caps[j].Start {
				heap.Push(h, j)
			}
			j++
		}
		for h.Len() > 0 && caps[(*h)[0]].End <= a {
			heap.Pop(h)
		}
		if h.Len() == 0 {
			continue
		}
		w := caps[(*h)[0]]
		if n := len(out); n > 0 && out[n-1].End == a && out[n-1].Index == w.Index && out[n-1].Pattern == w.Pattern {
			out[n-1].End = b
			continue
		}
		out = append(out, Capture{Start: a, End: b, Index: w.Index, Pattern: w.Pattern})
	}
	return out
}

// activeHeap is a max-heap of capture positions in the sorted list.
type activeHeap []int

func (h activeHeap) Len() int           { return len(h) }
func (h activeHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h activeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *activeHeap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *activeHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}
