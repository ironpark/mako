package gui

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/pkg/rope"
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

func TestLargeFileNavigationAndEditing(t *testing.T) {
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = "fmt.Println(\"line\")"
	}
	text := strings.Join(lines, "\n")
	e, v, p := editorProbe(t, text)

	// goToLine to line 500
	e.goToLine(500)
	p.Frame()
	if c := e.Caret.Get(); c.Line != 500 || c.Lines != 1000 {
		t.Fatalf("expected caret at line 500 (total 1000), got %+v", c)
	}

	// Press Enter to split line
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	p.Frame()
	if c := e.Caret.Get(); c.Line != 501 || c.Lines != 1001 {
		t.Fatalf("after Enter, expected line 501 / total 1001, got %+v", c)
	}

	// Verify Rope state is synchronized
	if e.rope.Lines() != 1001 {
		t.Fatalf("expected rope lines 1001, got %d", e.rope.Lines())
	}
	_ = v
}

func TestEnsureRopeIncrementalParityAndUTF8Boundaries(t *testing.T) {
	text := ggui.State("")
	ed := newCodeEditor(text, defaultPrefs)

	assertParity := func(step, s string) {
		t.Helper()
		r := ed.ensureRope(s)
		want := rope.New(s)
		if got := r.String(); got != s {
			t.Fatalf("%s: rope.String() = %q, want %q", step, got, s)
		}
		if ed.widest != want.LongestLine() {
			t.Fatalf("%s: widest = %d, want %d", step, ed.widest, want.LongestLine())
		}
		if r.Lines() != want.Lines() {
			t.Fatalf("%s: lines = %d, want %d", step, r.Lines(), want.Lines())
		}
		for i := range want.Lines() {
			if r.LineStart(i) != want.LineStart(i) || r.LineEnd(i) != want.LineEnd(i) {
				t.Fatalf("%s: line %d [%d,%d) != [%d,%d)", step, i, r.LineStart(i), r.LineEnd(i), want.LineStart(i), want.LineEnd(i))
			}
		}
		for pos := 0; pos <= len(s); pos++ {
			if r.LineOf(pos) != want.LineOf(pos) {
				t.Fatalf("%s: LineOf(%d) = %d, want %d", step, pos, r.LineOf(pos), want.LineOf(pos))
			}
		}
	}

	steps := []struct {
		name string
		text string
	}{
		{"initial", "alpha\nbeta\ngamma"},
		{"unchanged", "alpha\nbeta\ngamma"},
		{"insert-middle", "alpha\nbeta-two\ngamma"},
		{"repeated-char-insert", "aa"},
		{"repeated-char-grow", "aaa"},
		{"repeated-char-shrink", "aa"},
		{"prepend", "prefix-" + "aa"},
		{"append", "prefix-aa-suffix"},
		{"delete-middle", "prefix-suffix"},
		{"utf8-emoji-init", "hello 👋 world\nline2"},
		{"utf8-emoji-replace", "hello 🌍 world\nline2"},
		{"utf8-accent-init", "café"},
		{"utf8-accent-replace", "cafè"},
		{"utf8-hangul", "가나다\n라마바사"},
		{"utf8-hangul-edit", "가나X다\n라마바사"},
		{"full-replace", "completely different\ncontent\nhere"},
		{"empty", ""},
		{"from-empty", "back again\nwith lines"},
		{"large-prefix-edit", strings.Repeat("line\n", 200) + "middle\n" + strings.Repeat("tail\n", 200)},
		{"large-prefix-edit-2", strings.Repeat("line\n", 200) + "MIDDLE-UPDATED\n" + strings.Repeat("tail\n", 200)},
	}
	for _, st := range steps {
		assertParity(st.name, st.text)
	}
}

func TestRopeEditBoundsNoOverlapAndRuneBoundaries(t *testing.T) {
	pairs := [][2]string{
		{"aa", "aaa"},
		{"aaa", "aa"},
		{"ababa", "abababa"},
		{"é", "è"},
		{"hello 👋 world", "hello 🌍 world"},
		{"가나다", "가라다"},
		{"", "abc"},
		{"abc", ""},
	}
	for _, p := range pairs {
		prev, next := p[0], p[1]
		pre, suf := ropeEditBounds(prev, next)
		limit := min(len(prev), len(next))
		if pre < 0 || suf < 0 || pre+suf > limit {
			t.Fatalf("overlap for %q -> %q: pre=%d suf=%d limit=%d", prev, next, pre, suf, limit)
		}
		if pre < len(prev) && !utf8.RuneStart(prev[pre]) {
			t.Fatalf("pre=%d not rune start in %q", pre, prev)
		}
		if pre < len(next) && !utf8.RuneStart(next[pre]) {
			t.Fatalf("pre=%d not rune start in %q", pre, next)
		}
		if suf > 0 && (!utf8.RuneStart(prev[len(prev)-suf]) || !utf8.RuneStart(next[len(next)-suf])) {
			t.Fatalf("suf=%d not rune start for %q -> %q", suf, prev, next)
		}
		rebuilt := prev[:pre] + next[pre:len(next)-suf] + prev[len(prev)-suf:]
		if rebuilt != next {
			t.Fatalf("rebuilt %q != %q (pre=%d suf=%d)", rebuilt, next, pre, suf)
		}
	}
}
