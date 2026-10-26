package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

func TestChangedWordsMarkWhatDiffers(t *testing.T) {
	lines := []*protocol.DiffLine{
		{Op: "@", Text: "@@ -1,3 +1,3 @@"},
		{Op: " ", Text: "keep"},
		{Op: "-", Text: "return fooBar(x, y)", Old: 2},
		{Op: "+", Text: "return fooBaz(x, y)", New: 2},
		{Op: "-", Text: "entirely different", Old: 3},
		{Op: "+", Text: "nothing alike here", New: 3},
	}
	w := changedWords(lines)
	if s := w[2]; lines[2].GetText()[s.Lo:s.Hi] != "fooBar" {
		t.Fatalf("removed span %q", lines[2].GetText()[s.Lo:s.Hi])
	}
	if s := w[3]; lines[3].GetText()[s.Lo:s.Hi] != "fooBaz" {
		t.Fatalf("added span %q", lines[3].GetText()[s.Lo:s.Hi])
	}
	if w[4] != (wordSpan{}) || w[5] != (wordSpan{}) {
		t.Fatal("a line changed throughout marked")
	}
	if _, ok := w[1]; ok {
		t.Fatal("a kept line marked")
	}
}

func TestAgentNoteNamesTheLines(t *testing.T) {
	lines := []*protocol.DiffLine{
		{Op: " ", Text: "a", Old: 9, New: 10},
		{Op: "-", Text: "b", Old: 10},
		{Op: "+", Text: "B", New: 11},
	}
	note := agentNote("pkg/x.go", lines, "  why upper case?  ")
	want := "pkg/x.go:10-11\n```diff\n a\n-b\n+B\n```\nwhy upper case?"
	if note != want {
		t.Fatalf("note %q", note)
	}
	if lineRef("x", lines[1:2]) != "x:10" {
		t.Fatalf("ref %q", lineRef("x", lines[1:2]))
	}
	all := []*protocol.DiffLine{{Op: "@"}, {Op: " "}, {Op: "+"}, {Op: "@"}, {Op: "-"}}
	if start, h := hunkAt(all, 2); start != 0 || len(h) != 2 {
		t.Fatalf("hunk %d %d", start, len(h))
	}
	if start, h := hunkAt(all, 4); start != 3 || len(h) != 1 || !strings.Contains(h[0].GetOp(), "-") {
		t.Fatalf("hunk %d %+v", start, h)
	}
}

func TestPreviewDiffTabsGiveWayUntilKept(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	m := newModel(config.Default(), "127.0.0.1:1") // no daemon answers there
	m.post = func(func()) {}
	root := fileRoot{Node: "n", Path: "/p"}
	a, b, c := &protocol.GitFile{Path: "a.go"}, &protocol.GitFile{Path: "b.go"}, &protocol.GitFile{Path: "c.go"}
	m.openDiffTab(root, "/p", "", "", a, true)
	m.openDiffTab(root, "/p", "", "", b, true)
	tabs := ggui.Peek(m.Tabs)
	if len(tabs) != 1 || !strings.HasSuffix(tabs[0], "b.go") || len(ggui.Peek(m.Diffs)) != 1 {
		t.Fatalf("tabs %q", tabs)
	}
	m.keepDiff(tabs[0])
	m.openDiffTab(root, "/p", "", "", c, true)
	if tabs := ggui.Peek(m.Tabs); len(tabs) != 2 || !strings.HasSuffix(tabs[1], "c.go") {
		t.Fatalf("a kept tab gave way: %q", tabs)
	}
	// Against a base, the same file is a diff of its own.
	m.openDiffTab(root, "/p", "", "main", c, false)
	if len(ggui.Peek(m.Tabs)) != 3 {
		t.Fatalf("tabs %q", ggui.Peek(m.Tabs))
	}
}
