package gui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
)

// Every string the window translates has a translation in each catalog:
// the literals handed to T and Tf, here and in the packages under this
// one, those marked with i18n.N, and the English held in tables and
// translated where it is drawn.
func TestEveryStringIsTranslated(t *testing.T) {
	want := map[string]string{} // string -> where it is
	var files []string
	_ = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return err
	})
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			if !translates(call.Fun) {
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
	for _, labels := range []map[string]string{themeLabels, cursorLabels, daemonModeLabels, usageCornerLabels, backdropFitLabels, backdropPositionLabels} {
		for _, l := range labels {
			want[l] = "settings labels"
		}
	}
	for _, c := range skin.Colors {
		want[c.Label] = "skin.Colors"
		want[c.Group] = "skin.Colors"
	}
	if len(want) < 100 {
		t.Fatalf("found only %d strings to translate; is the scan broken?", len(want))
	}
	for _, l := range i18n.Languages[1:] { // English is the strings' own
		for s, at := range want {
			if l.Catalog[s] == "" {
				t.Errorf("%s: %q has no %s translation", at, s, l.Code)
			}
		}
	}
}

// translates reports whether a call is to T or Tf, gui's or i18n's, or
// to i18n.N.
func translates(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == "T" || f.Name == "Tf"
	case *ast.SelectorExpr:
		pkg, ok := f.X.(*ast.Ident)
		return ok && pkg.Name == "i18n" && (f.Sel.Name == "T" || f.Sel.Name == "Tf" || f.Sel.Name == "N")
	}
	return false
}
