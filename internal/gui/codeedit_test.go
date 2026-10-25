package gui

import (
	"runtime"
	"testing"

	"github.com/ironpark/ggui"
)

// editorProbe shows an editor over text and focuses it.
func editorProbe(t *testing.T, text string) (*codeEditor, *ggui.StateValue[string], *ggui.Probe) {
	t.Helper()
	v := ggui.State(text)
	e := newCodeEditor(v, defaultPrefs)
	p := ggui.ProbeBuilder(func() ggui.Widget { return e }, ggui.Sz(600, 400))
	t.Cleanup(p.Close)
	p.Frame()
	p.Click(ggui.Pt(300, 380))
	p.Frame()
	if !e.focused {
		t.Fatal("a click did not focus the editor")
	}
	return e, v, p
}

func TestEnterKeepsTheIndent(t *testing.T) {
	e, v, p := editorProbe(t, "func f() {\n\tif x {")
	e.input.Select(len(ggui.Peek(v)), len(ggui.Peek(v)))
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	p.Text("y")
	if got := ggui.Peek(v); got != "func f() {\n\tif x {\n\ty" {
		t.Fatalf("got %q", got)
	}
}

func TestTabIndentsTheSelectedLines(t *testing.T) {
	e, v, p := editorProbe(t, "a\n  b\nc")
	e.input.Select(0, 4) // into the second line
	p.Type(ggui.Mods{}, ggui.KeyTab)
	if got := ggui.Peek(v); got != "    a\n      b\nc" {
		t.Fatalf("indented %q", got)
	}
	p.Type(ggui.Mods{Shift: true}, ggui.KeyTab)
	if got := ggui.Peek(v); got != "a\n  b\nc" {
		t.Fatalf("outdented %q", got)
	}
	// Without a selection Tab inserts an indent, as tabs where the text
	// has them.
	e2, v2, p2 := editorProbe(t, "\tx\ny")
	e2.input.Select(len("\tx\ny"), len("\tx\ny"))
	p2.Type(ggui.Mods{}, ggui.KeyTab)
	if got := ggui.Peek(v2); got != "\tx\ny\t" {
		t.Fatalf("tab inserted %q", got)
	}
}

func TestUpAndDownKeepTheColumn(t *testing.T) {
	// The caret after "\tab" is at column 6; the line above, of spaces,
	// has its column 6 after six of them, and the short line between
	// takes the caret to its end and gives it back after.
	text := "        long\nx\n\tab"
	e, _, p := editorProbe(t, text)
	e.input.Select(len(text), len(text))
	p.Type(ggui.Mods{}, ggui.KeyArrowUp)
	if c := e.input.EditingState().Caret; c != 14 {
		t.Fatalf("up to the short line: caret %d, want its end, 14", c)
	}
	p.Type(ggui.Mods{}, ggui.KeyArrowUp)
	if c := e.input.EditingState().Caret; c != 6 {
		t.Fatalf("up again: caret %d, want 6", c)
	}
	p.Type(ggui.Mods{Shift: true}, ggui.KeyArrowDown, ggui.KeyArrowDown)
	if st := e.input.EditingState(); st.Anchor != 6 || st.Caret != len(text) {
		t.Fatalf("shift-down selected %d..%d", st.Anchor, st.Caret)
	}
}

func TestColumnsCountTabsAndWideCharacters(t *testing.T) {
	e := newCodeEditor(ggui.State(""), defaultPrefs)
	line := "\t한a"
	if got := e.column(line, len(line)); got != 4+2+1 {
		t.Fatalf("column %d", got)
	}
	if got := e.offsetAtColumn(line, 5); got != 1 {
		t.Fatalf("column 5, inside 한, is offset %d, want before it", got)
	}
	if got := e.offsetAtColumn(line, 6); got != len("\t한") {
		t.Fatalf("column 6 is offset %d, want after 한", got)
	}
}

func TestReadOnlyEditorTakesNoEdits(t *testing.T) {
	e, v, p := editorProbe(t, "keep")
	e.ReadOnly(true)
	p.Text("x")
	p.Type(ggui.Mods{}, ggui.KeyBackspace, ggui.KeyEnter)
	if got := ggui.Peek(v); got != "keep" {
		t.Fatalf("read-only text became %q", got)
	}
}

func TestWordAt(t *testing.T) {
	s := "foo_bar(baz)  x"
	for at, want := range map[int]string{0: "foo_bar", 5: "foo_bar", 7: "(", 9: "baz", 12: "  "} {
		a, b := wordAt(s, at)
		if s[a:b] != want {
			t.Errorf("at %d: %q, want %q", at, s[a:b], want)
		}
	}
}

func TestCommentToggle(t *testing.T) {
	e, v, p := editorProbe(t, "\tif x {\n\t\ty()\n\t}")
	e.Comment("// ")
	e.input.Select(0, len(ggui.Peek(v)))
	cmd := ggui.Mods{Meta: runtime.GOOS == "darwin", Ctrl: runtime.GOOS != "darwin"}
	p.Type(cmd, ggui.KeySlash)
	if got := ggui.Peek(v); got != "\t// if x {\n\t// \ty()\n\t// }" {
		t.Fatalf("commented %q", got)
	}
	p.Type(cmd, ggui.KeySlash)
	if got := ggui.Peek(v); got != "\tif x {\n\t\ty()\n\t}" {
		t.Fatalf("uncommented %q", got)
	}
}
