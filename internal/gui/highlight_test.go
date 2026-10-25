package gui

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/sanbo/internal/zed"
)

// installTOML installs a Zed extension for TOML into dir, from the grammar
// and query the syntax package tests with.
func installTOML(t *testing.T, dir string) {
	t.Helper()
	ext := filepath.Join(dir, "toml")
	files := map[string]string{
		"extension.toml":             "id = \"toml\"\nname = \"TOML\"\nversion = \"0.1.0\"\nschema_version = 1\n\n[grammars.toml]\nrepository = \"x\"\n",
		"languages/toml/config.toml": "name = \"TOML\"\ngrammar = \"toml\"\npath_suffixes = [\"toml\", \"Cargo.lock\"]\nline_comments = [\"# \"]\n",
	}
	for name, body := range files {
		writeFile(t, filepath.Join(ext, name), []byte(body))
	}
	for from, to := range map[string]string{"toml.wasm": "grammars/toml.wasm", "highlights.scm": "languages/toml/highlights.scm"} {
		data, err := os.ReadFile(filepath.Join("..", "syntax", "testdata", "toml", from))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(ext, to), data)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHighlightsWithAZedExtension(t *testing.T) {
	dir := t.TempDir()
	installTOML(t, dir)
	exts, err := (&zed.Store{Dir: dir}).Installed()
	if err != nil {
		t.Fatal(err)
	}
	h := newHighlighter()
	defer h.close()
	h.setLanguages(zed.Languages(exts))
	if _, ok := h.languageFor("/p/notes.txt", "hello"); ok {
		t.Fatal("a text file got a language")
	}
	l, ok := h.languageFor("/p/Cargo.lock", "")
	if !ok || l.Name != "TOML" || l.Comment != "# " {
		t.Fatalf("language %+v, %v", l, ok)
	}

	woke := make(chan struct{}, 8)
	d := &docSyntax{h: h, lang: l, wake: func() { woke <- struct{}{} }}
	text := "name = \"sanbo\" # hi\n"
	d.colors(text, darkPalette)
	select {
	case <-woke:
	case <-time.After(10 * time.Second):
		t.Fatal("no spans")
	}
	spans := d.colors(text, darkPalette)
	at := func(sub string) styleSpan {
		i := strings.Index(text, sub)
		for _, s := range spans {
			if s.Start <= i && i < s.End {
				return s
			}
		}
		t.Fatalf("nothing colours %q in %+v", sub, spans)
		return styleSpan{}
	}
	if c := at("sanbo").Color; c != captureColor("string", darkPalette) {
		t.Fatalf("string coloured %v", c)
	}
	if c := at("# hi").Color; c != captureColor("comment", darkPalette) {
		t.Fatalf("comment coloured %v", c)
	}

	// Typed into before it is parsed again, the colours move along.
	edited := "x" + text
	moved := d.colors(edited, darkPalette)
	if len(moved) == 0 || moved[0].Start != spans[0].Start+1 {
		t.Fatalf("spans did not move: %+v", moved)
	}
}

func TestCaptureColorFallsBackByPrefix(t *testing.T) {
	if captureColor("function.method.call", darkPalette) != captureColor("function", darkPalette) {
		t.Fatal("no fallback to function")
	}
	if captureColor("variable", darkPalette) != nil {
		t.Fatal("a plain variable got a colour")
	}
}

// awaitColors asks d for the colours of text until its worker has them.
func awaitColors(t *testing.T, d *docSyntax, woke chan struct{}, text string) []styleSpan {
	t.Helper()
	d.colors(text, darkPalette)
	select {
	case <-woke:
	case <-time.After(20 * time.Second):
		t.Fatal("no spans")
	}
	return d.colors(text, darkPalette)
}

// colorAt is the colour of the first byte of sub in text.
func colorAt(spans []styleSpan, text, sub string) color.Color {
	i := strings.Index(text, sub)
	for _, s := range spans {
		if s.Start <= i && i < s.End {
			return s.Color
		}
	}
	return nil
}

func TestBuiltinLanguagesAndInjections(t *testing.T) {
	h := newHighlighter()
	defer h.close()
	l, ok := h.languageFor("/p/main.go", "")
	if !ok || l.Grammar != "go" || l.Comment != "// " {
		t.Fatalf("main.go: %+v, %v", l, ok)
	}
	if l, ok := h.languageFor("/p/run", "#!/usr/bin/env python3\n"); !ok || l.Grammar != "python" {
		t.Fatalf("a python script: %+v, %v", l, ok)
	}

	woke := make(chan struct{}, 64)
	wake := func() { woke <- struct{}{} }
	src := "package main\n\nfunc main() { return }\n"
	spans := awaitColors(t, &docSyntax{h: h, lang: l, wake: wake}, woke, src)
	if colorAt(spans, src, "func") != captureColor("keyword", darkPalette) {
		t.Fatalf("func not a keyword: %+v", spans)
	}

	// Markdown colours a fenced block in its language, and its inline
	// text in markdown's inline grammar.
	md, _ := h.languageFor("/p/README.md", "")
	doc := "# Title\n\nSome `code` here.\n\n```go\nfunc f() {}\n```\n"
	spans = awaitColors(t, &docSyntax{h: h, lang: md, wake: wake}, woke, doc)
	if colorAt(spans, doc, "func") != captureColor("keyword", darkPalette) {
		t.Fatalf("the fenced go is not coloured as go: %+v", spans)
	}
	if colorAt(spans, doc, "Title") == nil {
		t.Fatal("the heading has no colour")
	}
	if colorAt(spans, doc, "code") == nil {
		t.Fatal("the inline code has no colour")
	}
}

func TestCutSpanLeavesTheHoles(t *testing.T) {
	from := 0
	got := cutSpan(0, 10, [][2]int{{2, 4}, {6, 7}}, &from)
	if len(got) != 3 || got[0] != [2]int{0, 2} || got[1] != [2]int{4, 6} || got[2] != [2]int{7, 10} {
		t.Fatalf("got %v", got)
	}
}
