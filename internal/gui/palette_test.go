package gui

import (
	"slices"
	"testing"

	"github.com/ironpark/ggui"
)

func TestPaletteOpensExtensions(t *testing.T) {
	m, p, _, _ := docApp(t, "x")
	m.openPalette()
	p.Frame()
	label := T("App") + ": " + T("Extensions…")
	if _, ok := p.Find(label); !ok {
		t.Fatal("the palette lists no Extensions")
	}
	p.Tap(label)
	p.Frame()
	if ggui.Peek(m.PaletteOpen) || !ggui.Peek(m.ExtensionsOpen) {
		t.Fatalf("palette open %v, extensions open %v", ggui.Peek(m.PaletteOpen), ggui.Peek(m.ExtensionsOpen))
	}
}

func TestPaletteListsRecentCommandsFirstAndMatchesLetters(t *testing.T) {
	m, p, _, _ := docApp(t, "x")
	m.openPalette()
	p.Frame()
	m.palette.run(slices.IndexFunc(ggui.Peek(m.palette.rows), func(r pickRow) bool { return r.Label == T("View")+": "+T("Toggle sidebar") }))
	m.openPalette()
	p.Frame()
	if first := ggui.Peek(m.palette.rows)[0]; first.Label != T("View")+": "+T("Toggle sidebar") || !first.Recent {
		t.Fatalf("first %+v", first)
	}
	// Letters in order find a command, by its English name too.
	m.PaletteQuery.Set("extns")
	p.Frame()
	rows := ggui.Peek(m.palette.rows)
	if len(rows) == 0 || rows[0].Label != T("App")+": "+T("Extensions…") {
		t.Fatalf("rows %+v", rows)
	}
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	p.Frame()
	if !ggui.Peek(m.ExtensionsOpen) {
		t.Fatal("Enter did not run the command")
	}
}

func TestFuzzyMatchMarksTheLettersTyped(t *testing.T) {
	score, hits, ok := fuzzyMatch("View: Toggle sidebar", "tsb")
	if !ok || !slices.Equal(hits, []int{6, 13, 17}) || score <= 0 {
		t.Fatalf("score %d hits %v ok %v", score, hits, ok)
	}
	if _, _, ok := fuzzyMatch("세션: 새 세션", "새세"); !ok {
		t.Fatal("Hangul did not match")
	}
	if _, _, ok := fuzzyMatch("Settings", "zz"); ok {
		t.Fatal("matched letters it lacks")
	}
}

func TestEditorPublishesTheCaret(t *testing.T) {
	e, _, p := editorProbe(t, "ab\n한글x")
	e.input.Select(0, 0)
	p.Type(ggui.Mods{}, ggui.KeyArrowDown, ggui.KeyArrowRight, ggui.KeyArrowRight)
	if c := ggui.Peek(e.Caret); c != (caretPos{Line: 2, Col: 3, Lines: 2}) {
		t.Fatalf("caret %+v", c)
	}
	p.Type(ggui.Mods{Shift: true}, ggui.KeyArrowLeft)
	if c := ggui.Peek(e.Caret); c.Selected != 1 {
		t.Fatalf("selected %+v", c)
	}
}
