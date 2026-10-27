package gui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every string the window translates has a translation in each catalog:
// the literals handed to T and Tf, and the English held in tables and
// translated where it is drawn.
func TestEveryStringIsTranslated(t *testing.T) {
	want := map[string]string{} // string -> where it is
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || (id.Name != "T" && id.Name != "Tf") {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				s, _ := strconv.Unquote(lit.Value)
				want[s] = fset.Position(lit.Pos()).String()
			}
			return true
		})
	}
	for _, g := range settingsNav {
		want[g.title] = "settingsNav"
		for _, s := range g.sections {
			want[s.label] = "settingsNav"
		}
	}
	for _, s := range shortcutList {
		want[s.Action] = "shortcutList"
	}
	for _, labels := range []map[string]string{themeLabels, cursorLabels, daemonModeLabels, usageCornerLabels} {
		for _, l := range labels {
			want[l] = "settings labels"
		}
	}
	for _, c := range paletteColors {
		want[c.label] = "paletteColors"
		want[c.group] = "paletteColors"
	}
	if len(want) < 100 {
		t.Fatalf("found only %d strings to translate; is the scan broken?", len(want))
	}
	for _, l := range languages[1:] { // English is the strings' own
		for s, at := range want {
			if l.Catalog[s] == "" {
				t.Errorf("%s: %q has no %s translation", at, s, l.Code)
			}
		}
	}
}

func TestSystemLanguageIsOneTheWindowHas(t *testing.T) {
	got := systemLanguage()
	if _, ok := catalog(got); !ok {
		t.Fatalf("system language %q has no catalog", got)
	}
	if resolveLanguage("ko") != "ko" || resolveLanguage("en") != "en" {
		t.Fatal("a chosen language is not used")
	}
}
