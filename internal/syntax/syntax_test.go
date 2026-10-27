package syntax

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func newRuntime(t testing.TB) *Runtime {
	t.Helper()
	r, err := NewRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func readTestdata(t testing.TB, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func load(t testing.TB, r *Runtime, name string) *Language {
	t.Helper()
	l, err := r.LoadLanguage(name, readTestdata(t, name, name+".wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func highlights(t testing.TB, l *Language) *Query {
	t.Helper()
	q, err := l.NewQuery(string(readTestdata(t, l.Name(), "highlights.scm")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	return q
}

func parse(t testing.TB, l *Language, src string) *Tree {
	t.Helper()
	tree, err := l.Parse([]byte(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Close)
	return tree
}

// named renders captures as "text=capture" pairs for easy assertions.
func named(q *Query, src string, caps []Capture) map[string][]string {
	out := map[string][]string{}
	for _, c := range caps {
		k := src[c.Start:c.End]
		out[k] = append(out[k], q.CaptureNames()[c.Index])
	}
	return out
}

const htmlSrc = `<!DOCTYPE html>
<html lang="en">
<!-- note -->
<body class='x'>Hi &amp; bye<br/></body>
</html>
`

func TestHTMLParseAndHighlight(t *testing.T) {
	r := newRuntime(t)
	l := load(t, r, "html")
	if v := l.ABIVersion(); v < MinABIVersion || v > MaxABIVersion {
		t.Fatalf("abi %d", v)
	}
	tree := parse(t, l, htmlSrc)
	sexpr := tree.RootSExpr()
	want := "(document (doctype) (element (start_tag (tag_name) (attribute (attribute_name) (quoted_attribute_value (attribute_value)))) (comment) (element (start_tag (tag_name) (attribute (attribute_name) (quoted_attribute_value (attribute_value)))) (text) (entity) (text) (element (self_closing_tag (tag_name))) (end_tag (tag_name))) (end_tag (tag_name))))"
	if sexpr != want {
		t.Fatalf("sexpr:\n got %s\nwant %s", sexpr, want)
	}

	q := highlights(t, l)
	caps, err := q.Captures(tree, []byte(htmlSrc), 0, len(htmlSrc))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(caps); i++ {
		if caps[i].Start < caps[i-1].Start {
			t.Fatalf("captures not sorted at %d", i)
		}
	}
	got := named(q, htmlSrc, caps)
	for text, name := range map[string]string{
		"html":          "tag",
		"body":          "tag",
		"br":            "tag",
		"lang":          "attribute",
		"class":         "attribute",
		"en":            "string",
		"x":             "string",
		`"`:             "string",
		"'":             "string",
		"&amp;":         "string.special",
		"</":            "punctuation.bracket.html",
		"=":             "punctuation.delimiter.html",
		"<!-- note -->": "comment",
	} {
		if !slices.Contains(got[text], name) {
			t.Errorf("%q: got %v, want %s", text, got[text], name)
		}
	}

	// A sub-range only returns captures that intersect it.
	start := strings.Index(htmlSrc, "<body")
	end := start + len("<body")
	part, err := q.Captures(tree, []byte(htmlSrc), start, end)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range part {
		if c.End <= start || c.Start >= end {
			t.Errorf("capture %+v outside [%d,%d)", c, start, end)
		}
	}
	if len(part) == 0 || len(part) >= len(caps) {
		t.Errorf("range query returned %d of %d captures", len(part), len(caps))
	}
}

func TestPredicates(t *testing.T) {
	r := newRuntime(t)
	l := load(t, r, "html")
	src := `<a href="x" style="y" onclick="z" id="q"></a>`
	tree := parse(t, l, src)
	q, err := l.NewQuery(`
((attribute_name) @eq (#eq? @eq "href"))
((attribute_name) @neq (#not-eq? @neq "href"))
((attribute_name) @match (#match? @match "^on[a-z]+$"))
((attribute_name) @nomatch (#not-match? @nomatch "^on"))
((attribute_name) @of (#any-of? @of "id" "style"))
((attribute_name) @notof (#not-any-of? @notof "id" "style"))
((start_tag (tag_name) @same) (end_tag (tag_name) @end) (#eq? @same @end))
((attribute (attribute_name) @_n (quoted_attribute_value (attribute_value) @css))
  (#eq? @_n "style")
  (#set! injection.language "css"))
`)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	caps, err := q.Captures(tree, []byte(src), 0, len(src))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string][]string{}
	for _, c := range caps {
		n := q.CaptureNames()[c.Index]
		byName[n] = append(byName[n], src[c.Start:c.End])
	}
	want := map[string]string{
		"eq":      "href",
		"neq":     "style onclick id",
		"match":   "onclick",
		"nomatch": "href style id",
		"of":      "style id",
		"notof":   "href onclick",
		"same":    "a",
		"css":     "y",
	}
	for n, w := range want {
		if g := strings.Join(byName[n], " "); g != w {
			t.Errorf("@%s: got %q, want %q", n, g, w)
		}
	}
	if _, ok := byName["_n"]; ok {
		t.Error("private capture @_n was returned")
	}
	if p := q.PatternProperties(7)["injection.language"]; p != "css" {
		t.Errorf("injection.language = %q", p)
	}

	// Zed's own injections.scm compiles and finds the style attribute.
	inj, err := l.NewQuery(string(readTestdata(t, "html", "injections.scm")))
	if err != nil {
		t.Fatal(err)
	}
	defer inj.Close()
	ms, err := inj.Matches(tree, []byte(src), 0, len(src))
	if err != nil {
		t.Fatal(err)
	}
	content := inj.CaptureIndex("injection.content")
	var langs []string
	for _, m := range ms {
		for _, c := range m.Captures {
			if c.Index == content {
				langs = append(langs, inj.PatternProperties(m.Pattern)["injection.language"]+":"+src[c.Start:c.End])
			}
		}
	}
	if g := strings.Join(langs, " "); g != "css:y javascript:z" {
		t.Errorf("injections: %q", g)
	}
}

func TestQueryErrors(t *testing.T) {
	r := newRuntime(t)
	l := load(t, r, "html")
	for _, tc := range []struct{ src, kind string }{
		{"(tag_name) @a\n(no_such_node) @b", "node type"},
		{"(tag_name", "syntax"},
		{"(element nope: (tag_name))", "field"},
		{`((tag_name) @a (#match? @a "(?<=a)b"))`, "predicate"},
	} {
		_, err := l.NewQuery(tc.src)
		var qe *QueryError
		if !errors.As(err, &qe) {
			t.Errorf("%q: got %v, want QueryError", tc.src, err)
			continue
		}
		if qe.Kind != tc.kind {
			t.Errorf("%q: kind %q, want %q (%v)", tc.src, qe.Kind, tc.kind, err)
		}
	}
	if _, err := l.NewQuery("(tag_name) @a\n(no_such_node) @b"); err.(*QueryError).Row != 1 {
		t.Errorf("row: %v", err)
	}
	// The runtime still works after errors.
	parse(t, l, "<p></p>")
}

func TestIncrementalReparse(t *testing.T) {
	r := newRuntime(t)
	l := load(t, r, "html")
	src := "<p>hello</p>\n<div>x</div>\n"
	tree := parse(t, l, src)

	// Rename <div> to <section> in both tags.
	edits := []struct{ old, new string }{{"div>x", "section>x"}, {"</div>", "</section>"}}
	cur := src
	for _, e := range edits {
		i := strings.Index(cur, e.old)
		next := cur[:i] + e.new + cur[i+len(e.old):]
		row := strings.Count(cur[:i], "\n")
		col := i - (strings.LastIndex(cur[:i], "\n") + 1)
		tree.Edit(InputEdit{
			StartByte: i, OldEndByte: i + len(e.old), NewEndByte: i + len(e.new),
			StartPoint:  Point{row, col},
			OldEndPoint: Point{row, col + len(e.old)},
			NewEndPoint: Point{row, col + len(e.new)},
		})
		cur = next
	}
	next, err := l.Parse([]byte(cur), tree)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	fresh := parse(t, l, cur)
	if next.RootSExpr() != fresh.RootSExpr() {
		t.Fatalf("incremental tree differs:\n%s\n%s", next.RootSExpr(), fresh.RootSExpr())
	}
	q := highlights(t, l)
	caps, err := q.Captures(next, []byte(cur), 0, len(cur))
	if err != nil {
		t.Fatal(err)
	}
	if got := named(q, cur, caps)["section"]; len(got) != 2 || got[0] != "tag" {
		t.Errorf("section captures %v", got)
	}
}

func TestMultipleLanguages(t *testing.T) {
	r := newRuntime(t)
	html := load(t, r, "html")
	toml := load(t, r, "toml")
	lua := load(t, r, "lua")

	tomlSrc := "# c\n[server]\nname = \"mako\"\nport = 8080\nlist = [1, 2.5]\n\"quoted\" = true\n"
	tt := parse(t, toml, tomlSrc)
	if s := tt.RootSExpr(); !strings.HasPrefix(s, "(document (comment) (table (bare_key) (pair (bare_key) (string))") {
		t.Errorf("toml sexpr %s", s)
	}
	tq := highlights(t, toml)
	tc, err := tq.Captures(tt, []byte(tomlSrc), 0, len(tomlSrc))
	if err != nil {
		t.Fatal(err)
	}
	tn := named(tq, tomlSrc, tc)
	for text, name := range map[string]string{
		"server": "property", `"mako"`: "string", "8080": "number",
		"2.5": "number", "true": "constant", "# c": "comment", `"quoted"`: "property",
	} {
		if !slices.Contains(tn[text], name) {
			t.Errorf("toml %q: got %v, want %s", text, tn[text], name)
		}
	}

	luaSrc := "local M = {}\n--[[ long\ncomment ]]\nfunction M.f(self, x)\n  print([[raw]], self, MAX_N)\nend\nreturn M\n"
	lt := parse(t, lua, luaSrc)
	if s := lt.RootSExpr(); strings.Contains(s, "ERROR") || !strings.Contains(s, "(comment") {
		t.Errorf("lua sexpr %s", s)
	}
	lq := highlights(t, lua)
	lc, err := lq.Captures(lt, []byte(luaSrc), 0, len(luaSrc))
	if err != nil {
		t.Fatal(err)
	}
	ln := named(lq, luaSrc, lc)
	for text, name := range map[string]string{
		"local": "keyword", "print": "function.builtin", "self": "variable.special",
		"MAX_N": "constant", "[[raw]]": "string", "--[[ long\ncomment ]]": "comment",
	} {
		if !slices.Contains(ln[text], name) {
			t.Errorf("lua %q: got %v, want %s", text, ln[text], name)
		}
	}
	// Later patterns win: self is both @variable and @variable.special.
	var selfSpan []string
	for _, s := range Spans(lc) {
		if luaSrc[s.Start:s.End] == "self" {
			selfSpan = append(selfSpan, lq.CaptureNames()[s.Index])
		}
	}
	if len(selfSpan) == 0 || selfSpan[len(selfSpan)-1] != "variable.special" {
		t.Errorf("self spans %v", selfSpan)
	}

	// Languages stay independent after others load.
	parse(t, html, htmlSrc)
	if _, err := tq.Captures(parse(t, html, htmlSrc), nil, 0, 1); err == nil {
		t.Error("query ran on a tree of another language")
	}
}

func TestSpans(t *testing.T) {
	caps := []Capture{
		{Start: 0, End: 10, Index: 0, Pattern: 0},
		{Start: 2, End: 4, Index: 1, Pattern: 0},
		{Start: 2, End: 4, Index: 2, Pattern: 3},
		{Start: 6, End: 8, Index: 3, Pattern: 1},
	}
	got := Spans(caps)
	want := []Capture{
		{Start: 0, End: 2, Index: 0}, {Start: 2, End: 4, Index: 2, Pattern: 3},
		{Start: 4, End: 6, Index: 0}, {Start: 6, End: 8, Index: 3, Pattern: 1},
		{Start: 8, End: 10, Index: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("span %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	r := newRuntime(t)
	l := load(t, r, "html")
	q := highlights(t, l)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				tree, err := l.Parse([]byte(htmlSrc), nil)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := q.Captures(tree, []byte(htmlSrc), 0, len(htmlSrc)); err != nil {
					t.Error(err)
				}
				tree.Close()
			}
		}()
	}
	wg.Wait()
}

func TestClosedRuntime(t *testing.T) {
	r := newRuntime(t)
	l := load(t, r, "html")
	tree := parse(t, l, "<p></p>")
	r.Close()
	if _, err := l.Parse([]byte("<p></p>"), nil); !errors.Is(err, ErrClosed) {
		t.Errorf("parse after close: %v", err)
	}
	tree.Close()
}
