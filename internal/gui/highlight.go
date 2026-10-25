package gui

import (
	"context"
	"image/color"
	"log"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/syntax"
	"github.com/ironpark/sanbo/internal/syntax/builtin"
	"github.com/ironpark/sanbo/internal/zed"
)

// Highlighting: a file is parsed with the tree-sitter grammar of its
// language and coloured by the language's highlight query, and the parts
// in another language, as a fenced code block or a script in HTML, by
// that one's, as its injection query finds them. The languages come from
// the Zed extensions installed, which carry their grammars as wasm, and
// from the ones sanbo ships for the languages Zed builds in. Parsing runs
// on a worker, again after each edit from the tree before it; until it is
// done the editor shows the colours from before, moved along with the
// edit.

// language is a language the editor can highlight.
type language struct {
	Key        string // where it is from and its name, unique
	Name       string
	Grammar    string // as in tree_sitter_<Grammar>
	Comment    string // its line comment
	TabSize    int    // 0 is the editor's default
	wasm       func() ([]byte, error)
	highlights func() (string, error)
	injections func() (string, error)
	// matches is a Zed language's test of a file's name and first line;
	// a builtin one, nil, is found through builtin.ForPath, and firstLine
	// is its test of a script's first line.
	matches   func(name, firstLine string) bool
	firstLine *regexp.Regexp
}

// zedLanguage is a language from an installed Zed extension.
func zedLanguage(l zed.Language) language {
	return language{
		Key: "zed:" + l.Extension + "/" + l.Config.Name, Name: l.Config.Name, Grammar: l.Config.Grammar,
		Comment: l.Config.Comment(), TabSize: l.Config.TabSize,
		wasm:       func() ([]byte, error) { return os.ReadFile(l.Grammar) },
		highlights: func() (string, error) { return l.Query("highlights") },
		injections: func() (string, error) { return l.Query("injections") },
		matches:    l.Matches,
	}
}

// builtinLanguages are the languages sanbo ships, for the ones Zed builds
// in rather than taking from extensions. One without file names, as
// markdown's inline grammar, is reached only from another's injections.
func builtinLanguages() []language {
	var out []language
	for _, l := range builtin.Languages() {
		lang := language{
			Key: "builtin:" + l.Grammar, Name: l.Name, Grammar: l.Grammar, Comment: l.LineComment,
			wasm:       l.Wasm,
			highlights: func() (string, error) { return l.Highlights, nil },
			injections: func() (string, error) { return l.Injections, nil },
		}
		if l.FirstLine != "" {
			lang.firstLine = regexp.MustCompile(l.FirstLine)
		}
		out = append(out, lang)
	}
	return out
}

// extensionsDir is where the Zed extensions are installed.
func extensionsDir() string {
	dir, err := config.ExtensionsDir()
	if err != nil {
		return ""
	}
	return dir
}

// highlighter parses and colours the open files. It is shared by the
// window's documents and safe for use from their workers.
type highlighter struct {
	mu     sync.Mutex
	rt     *syntax.Runtime // made on first use
	rtErr  error
	langs  []language
	loaded map[string]*loadedLanguage // by Key
}

// loadedLanguage is a language's grammar loaded into the runtime, with its
// queries.
type loadedLanguage struct {
	lang       *syntax.Language
	query      *syntax.Query
	injections *syntax.Query // nil without any
	err        error
}

func newHighlighter() *highlighter {
	return &highlighter{loaded: map[string]*loadedLanguage{}, langs: builtinLanguages()}
}

// setLanguages replaces the languages files are matched against: the Zed
// extensions' first, then the built-in ones.
func (h *highlighter) setLanguages(zl []zed.Language) {
	var langs []language
	for _, l := range zl {
		if l.Grammar != "" {
			langs = append(langs, zedLanguage(l))
		}
	}
	langs = append(langs, builtinLanguages()...)
	h.mu.Lock()
	h.langs = langs
	h.mu.Unlock()
}

// languageFor is the language of a file, by its name or its first line.
func (h *highlighter) languageFor(path, text string) (language, bool) {
	first, _, _ := strings.Cut(text, "\n")
	h.mu.Lock()
	langs := h.langs
	h.mu.Unlock()
	return match(langs, baseName(path), first)
}

// match finds the language for a file name, or failing that for a first
// line: a Zed extension's before a builtin one.
func match(langs []language, name, first string) (language, bool) {
	for _, l := range langs {
		if l.matches != nil && l.matches(name, "") {
			return l, true
		}
	}
	if b, ok := builtin.ForPath(name); ok {
		for _, l := range langs {
			if l.matches == nil && l.Grammar == b.Grammar {
				return l, true
			}
		}
	}
	if first == "" {
		return language{}, false
	}
	for _, l := range langs {
		if l.matches != nil && l.matches(name, first) || l.firstLine != nil && l.firstLine.MatchString(first) {
			return l, true
		}
	}
	return language{}, false
}

// languageNamed is the language an injection names: by its grammar or
// its name, or as a file extension, as a fenced code block's "py" does.
func (h *highlighter) languageNamed(name string) (language, bool) {
	h.mu.Lock()
	langs := h.langs
	h.mu.Unlock()
	for _, l := range langs {
		if strings.EqualFold(l.Grammar, name) || strings.EqualFold(l.Name, name) {
			return l, true
		}
	}
	return match(langs, "x."+strings.ToLower(name), "")
}

// syntaxFor is a new document's highlighting in a language.
func (m *model) syntaxFor(l language) *docSyntax {
	return &docSyntax{h: m.hl, lang: l, wake: m.wake}
}

// load is a language ready to parse with, loading it the first time.
func (h *highlighter) load(l language) (*loadedLanguage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ll, ok := h.loaded[l.Key]; ok {
		return ll, ll.err
	}
	ll := &loadedLanguage{}
	h.loaded[l.Key] = ll
	if h.rt == nil && h.rtErr == nil {
		h.rt, h.rtErr = syntax.NewRuntime(context.Background())
	}
	if ll.err = h.rtErr; ll.err != nil {
		return ll, ll.err
	}
	wasm, err := l.wasm()
	if err == nil {
		ll.lang, err = h.rt.LoadLanguage(l.Grammar, wasm)
	}
	var src string
	if err == nil {
		src, err = l.highlights()
	}
	if err == nil {
		ll.query, err = ll.lang.NewQuery(src)
	}
	if err == nil {
		// A language highlights without its injections if they do not
		// compile, as when they name a node its grammar lacks.
		if inj, ierr := l.injections(); ierr == nil && strings.TrimSpace(inj) != "" {
			if ll.injections, ierr = ll.lang.NewQuery(inj); ierr != nil {
				log.Printf("sanbo: injections of %s: %v", l.Name, ierr)
			}
		}
	}
	if err != nil {
		log.Printf("sanbo: highlighting %s: %v", l.Name, err)
		ll.err = err
	}
	return ll, ll.err
}

func (h *highlighter) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rt != nil {
		h.rt.Close()
	}
}

// hlLayer is the highlighting of one language in a file: the file's own,
// or a part of it another language was injected into.
type hlLayer struct {
	depth int // 0 for the file's own language
	spans []syntax.Capture
	names []string // its query's capture names
}

// Bounds on injections, so a file of many fenced blocks or a deep nest of
// them stays quick to highlight.
const (
	maxInjections = 2000
	maxDepth      = 2
)

// inject highlights the parts of src, parsed as tree with ll, that its
// injection query puts in another language, and theirs in turn.
func (h *highlighter) inject(ll *loadedLanguage, tree *syntax.Tree, src []byte, depth int, out []hlLayer) []hlLayer {
	q := ll.injections
	if q == nil || depth >= maxDepth {
		return out
	}
	ms, err := q.Matches(tree, src, 0, len(src))
	if err != nil {
		return out
	}
	content, named := q.CaptureIndex("injection.content"), q.CaptureIndex("injection.language")
	for _, m := range ms {
		if len(out) >= maxInjections {
			break
		}
		props := q.PatternProperties(m.Pattern)
		if _, combined := props["injection.combined"]; combined {
			continue // pieces parsed as one document, as PHP's HTML is
		}
		name, a, b := props["injection.language"], -1, -1
		for _, c := range m.Captures {
			switch c.Index {
			case content:
				a, b = c.Start, c.End
			case named:
				name = strings.TrimSpace(string(src[c.Start:c.End]))
			}
		}
		if a < 0 || b <= a || name == "" {
			continue
		}
		l, ok := h.languageNamed(name)
		if !ok {
			continue
		}
		sub, err := h.load(l)
		if err != nil {
			continue
		}
		part := src[a:b]
		t, err := sub.lang.Parse(part, nil)
		if err != nil {
			continue
		}
		caps, err := sub.query.Captures(t, part, 0, len(part))
		if err == nil {
			// This part's layers, and those injected into it, from the
			// part's start to the file's.
			own := []hlLayer{{depth: depth + 1, spans: syntax.Spans(caps), names: sub.query.CaptureNames()}}
			for _, in := range h.inject(sub, t, part, depth+1, own) {
				for i := range in.spans {
					in.spans[i].Start += a
					in.spans[i].End += a
				}
				out = append(out, in)
			}
		}
		t.Close()
	}
	return out
}

// docSyntax is one open file's highlighting: what its worker parses, and
// the layers it last found.
type docSyntax struct {
	h    *highlighter
	lang language
	wake func() // asks for a frame, once spans are ready

	mu      sync.Mutex
	want    string // the latest text asked for
	running bool
	src     string    // the text layers are for
	layers  []hlLayer // the file's own first
	failed  bool

	// On the window's goroutine: the layers coloured for a palette, and
	// moved along to the text last painted.
	painted struct {
		src, text string
		pal       string
		out       []styleSpan
	}
}

// request asks for the spans of text, parsed on the worker.
func (d *docSyntax) request(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed || text == d.want {
		return
	}
	d.want = text
	if !d.running {
		d.running = true
		go d.work()
	}
}

// work parses until it has caught up with the text asked for, each time
// from the tree before.
func (d *docSyntax) work() {
	ll, err := d.h.load(d.lang)
	var tree *syntax.Tree
	var treeSrc string
	defer func() {
		if tree != nil {
			tree.Close()
		}
	}()
	for {
		d.mu.Lock()
		text := d.want
		if err != nil || text == treeSrc && tree != nil {
			d.failed = d.failed || err != nil
			d.running = false
			d.mu.Unlock()
			return
		}
		d.mu.Unlock()

		if tree != nil {
			tree.Edit(editBetween(treeSrc, text))
		}
		src := []byte(text)
		next, perr := ll.lang.Parse(src, tree)
		if tree != nil {
			tree.Close()
		}
		tree, treeSrc = next, text
		if perr != nil {
			err = perr
			continue
		}
		caps, qerr := ll.query.Captures(tree, src, 0, len(src))
		if qerr != nil {
			err = qerr
			continue
		}
		layers := []hlLayer{{spans: syntax.Spans(caps), names: ll.query.CaptureNames()}}
		layers = d.h.inject(ll, tree, src, 0, layers)
		d.mu.Lock()
		d.src, d.layers = text, layers
		d.mu.Unlock()
		d.wake()
	}
}

// colors is the editor's colours for text under a palette: the layers
// last found, moved along with any edit made since.
func (d *docSyntax) colors(text string, pal palette) []styleSpan {
	d.request(text)
	d.mu.Lock()
	src, layers := d.src, d.layers
	d.mu.Unlock()
	p := &d.painted
	if p.src != src || p.pal != pal.Name || p.out == nil {
		p.out = colorLayers(layers, pal)
		p.src, p.text, p.pal = src, src, pal.Name
	}
	if p.text != text {
		p.out = shiftSpans(p.out, editBetween(p.text, text))
		p.text = text
	}
	return p.out
}

// colorLayers flattens the layers into the editor's spans, in order. A
// layer injected into another colours over it where it has a colour of
// its own, and leaves it showing where it has none, as a heading's
// inline text keeps the heading's.
func colorLayers(layers []hlLayer, pal palette) []styleSpan {
	type colored struct {
		depth int
		spans []styleSpan
	}
	all := make([]colored, len(layers))
	deepest := 0
	for i, l := range layers {
		all[i].depth = l.depth
		deepest = max(deepest, l.depth)
		// Each capture's colour, looked up once for the layer.
		byIndex := make([]color.Color, len(l.names))
		for j, name := range l.names {
			byIndex[j] = captureColor(name, pal)
		}
		for _, s := range l.spans {
			if c := byIndex[s.Index]; c != nil {
				all[i].spans = append(all[i].spans, styleSpan{Start: s.Start, End: s.End, Color: c})
			}
		}
	}
	if deepest == 0 && len(all) == 1 && all[0].spans != nil {
		return all[0].spans // one layer, in order already
	}
	// What the layers deeper than each depth colour, which cuts that
	// depth's spans.
	holes := make([][][2]int, deepest+1)
	for d := range holes {
		var rs [][2]int
		for _, c := range all {
			if c.depth > d {
				for _, s := range c.spans {
					rs = append(rs, [2]int{s.Start, s.End})
				}
			}
		}
		holes[d] = mergeRanges(rs)
	}
	out := []styleSpan{}
	for _, c := range all {
		from := 0
		for _, s := range c.spans {
			for _, piece := range cutSpan(s.Start, s.End, holes[c.depth], &from) {
				out = append(out, styleSpan{Start: piece[0], End: piece[1], Color: s.Color})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b styleSpan) int { return a.Start - b.Start })
	return out
}

// mergeRanges sorts ranges and joins those that meet.
func mergeRanges(rs [][2]int) [][2]int {
	slices.SortFunc(rs, func(a, b [2]int) int { return a[0] - b[0] })
	out := rs[:0]
	for _, r := range rs {
		if n := len(out); n > 0 && r[0] <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], r[1])
			continue
		}
		out = append(out, r)
	}
	return out
}

// cutSpan is [start, end) without the holes, which are sorted; *from is
// the first hole that can still meet a span, as spans come in order.
func cutSpan(start, end int, holes [][2]int, from *int) [][2]int {
	for *from < len(holes) && holes[*from][1] <= start {
		*from++
	}
	at := start
	var out [][2]int
	for i := *from; i < len(holes) && holes[i][0] < end; i++ {
		if holes[i][0] > at {
			out = append(out, [2]int{at, holes[i][0]})
		}
		at = max(at, holes[i][1])
	}
	if at < end {
		out = append(out, [2]int{at, end})
	}
	return out
}

// editBetween is the edit that turns a into b: what lies between their
// common start and their common end.
func editBetween(a, b string) syntax.InputEdit {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	return syntax.InputEdit{
		StartByte: pre, OldEndByte: len(a) - suf, NewEndByte: len(b) - suf,
		StartPoint: pointAt(a, pre), OldEndPoint: pointAt(a, len(a)-suf), NewEndPoint: pointAt(b, len(b)-suf),
	}
}

// pointAt is the row and byte column of offset i in s.
func pointAt(s string, i int) syntax.Point {
	row := strings.Count(s[:i], "\n")
	col := i - (strings.LastIndexByte(s[:i], '\n') + 1)
	return syntax.Point{Row: row, Column: col}
}

// shiftSpans moves spans along with an edit: those after it by its
// length, those across it cut to its start, those inside it dropped.
func shiftSpans(spans []styleSpan, e syntax.InputEdit) []styleSpan {
	delta := e.NewEndByte - e.OldEndByte
	out := make([]styleSpan, 0, len(spans))
	for _, s := range spans {
		switch {
		case s.End <= e.StartByte:
		case s.Start >= e.OldEndByte:
			s.Start += delta
			s.End += delta
		case s.Start < e.StartByte:
			s.End = e.StartByte
		default:
			continue
		}
		out = append(out, s)
	}
	return out
}

// Colours ---------------------------------------------------------------------------

// syntaxColors are the colours of the highlight captures, after Zed's One
// themes, by the capture's name and its prefixes: "function.method.call"
// takes "function.method", else "function".
var syntaxColors = map[bool]map[string]uint32{
	true: { // dark
		"comment": 0x7A8180, "comment.doc": 0x878E8D,
		"keyword": 0xB477CF, "operator": 0x9AA0A6, "punctuation": 0x9AA0A6,
		"string": 0xA1C181, "string.escape": 0x56B6C2, "string.regex": 0x56B6C2, "string.special": 0x56B6C2,
		"character": 0xA1C181, "escape": 0x56B6C2,
		"number": 0xBF956A, "boolean": 0xBF956A, "constant": 0xBF956A, "constant.builtin": 0xBF956A,
		"function": 0x73ADE9, "constructor": 0x73ADE9, "method": 0x73ADE9,
		"type": 0x6EB4BF, "type.builtin": 0x6EB4BF, "namespace": 0xD8DEE4, "module": 0xD8DEE4,
		"variable.special": 0xD07277, "variable.builtin": 0xD07277, "variable.parameter": 0xD8DEE4,
		"property": 0xD07277, "field": 0xD07277, "attribute": 0xBF956A, "label": 0x73ADE9,
		"tag": 0xD07277, "title": 0xD07277, "emphasis": 0x73ADE9, "emphasis.strong": 0xBF956A,
		"text.title": 0xD07277, "text.literal": 0xA1C181, "text.uri": 0x6EB4BF, "text.reference": 0x73ADE9,
		"link_text": 0x73ADE9, "link_uri": 0x6EB4BF, "embedded": 0xD8DEE4,
		"preproc": 0xB477CF, "diff.plus": 0xA1C181, "diff.minus": 0xD07277,
	},
	false: { // light
		"comment": 0x8E9096, "comment.doc": 0x7E8086,
		"keyword": 0xA626A4, "operator": 0x5C5F66, "punctuation": 0x5C5F66,
		"string": 0x50A14F, "string.escape": 0x0184BC, "string.regex": 0x0184BC, "string.special": 0x0184BC,
		"character": 0x50A14F, "escape": 0x0184BC,
		"number": 0x986801, "boolean": 0x986801, "constant": 0x986801, "constant.builtin": 0x986801,
		"function": 0x4078F2, "constructor": 0x4078F2, "method": 0x4078F2,
		"type": 0xC18401, "type.builtin": 0xC18401, "namespace": 0x383A42, "module": 0x383A42,
		"variable.special": 0xE45649, "variable.builtin": 0xE45649, "variable.parameter": 0x383A42,
		"property": 0xE45649, "field": 0xE45649, "attribute": 0x986801, "label": 0x4078F2,
		"tag": 0xE45649, "title": 0xE45649, "emphasis": 0x4078F2, "emphasis.strong": 0x986801,
		"text.title": 0xE45649, "text.literal": 0x50A14F, "text.uri": 0x0184BC, "text.reference": 0x4078F2,
		"link_text": 0x4078F2, "link_uri": 0x0184BC, "embedded": 0x383A42,
		"preproc": 0xA626A4, "diff.plus": 0x50A14F, "diff.minus": 0xE45649,
	},
}

// captureColor is a capture's colour under a palette, or nil for the
// text's own, as a plain variable has.
func captureColor(name string, pal palette) color.Color {
	colors := syntaxColors[pal.Dark]
	for n := name; n != ""; {
		if v, ok := colors[n]; ok {
			return hex(v)
		}
		i := strings.LastIndexByte(n, '.')
		if i < 0 {
			break
		}
		n = n[:i]
	}
	return nil
}

// wake asks the window for a frame, from any goroutine.
func (m *model) wake() {
	if post := m.post; post != nil {
		post(func() {})
	}
}

// extensionStore is the Zed extensions installed for this machine.
func extensionStore() *zed.Store { return &zed.Store{Dir: extensionsDir()} }

// loadExtensions reads the extensions installed, off the window's
// goroutine, and highlights with their languages.
func (m *model) loadExtensions() {
	go func() {
		exts, err := extensionStore().Installed()
		if err != nil {
			log.Printf("sanbo: extensions: %v", err)
		}
		langs := zed.Languages(exts)
		m.hl.setLanguages(langs)
		names := map[string][]string{}
		for _, l := range langs {
			names[l.Extension] = append(names[l.Extension], l.Config.Name)
		}
		m.post(func() {
			m.extLanguages = names
			m.Extensions.Set(exts)
			m.langGen++
		})
		m.loadMCPInfo(exts)
	}()
}
