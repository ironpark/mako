package gui

import (
	"testing"

	"github.com/ironpark/ggui"
)

func TestPaletteOpensExtensions(t *testing.T) {
	m, p, _, _ := docApp(t, "x")
	m.openPalette()
	p.Frame()
	if _, ok := p.Find(T("Extensions…")); !ok {
		t.Fatal("the palette lists no Extensions")
	}
	p.Tap(T("Extensions…"))
	p.Frame()
	if ggui.Peek(m.PaletteOpen) || !ggui.Peek(m.ExtensionsOpen) {
		t.Fatalf("palette open %v, extensions open %v", ggui.Peek(m.PaletteOpen), ggui.Peek(m.ExtensionsOpen))
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
