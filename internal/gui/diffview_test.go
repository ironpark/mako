package gui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
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
	t.Setenv("MAKO_HOME", t.TempDir())
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

func TestALongDiffDrawsOnlyTheLinesInView(t *testing.T) {
	lines := []*protocol.DiffLine{{Op: "@", Text: "@@ -1,1999 +1,1999 @@"}}
	for i := 1; i < 2000; i++ {
		lines = append(lines, &protocol.DiffLine{Op: " ", Old: int64(i), New: int64(i), Text: fmt.Sprintf("line %d", i)})
	}
	// The longest line is far down, and the diff scrolls sideways as far.
	lines[1500].Text = "line 1500 " + strings.Repeat("x", 400)
	d := &protocol.Diff{Lines: lines}
	for _, mode := range []diffMode{{}, {Wrap: true}, {Split: true}} {
		p := ggui.ProbeBuilder(func() ggui.Widget {
			return diffRows(d, mode, lineSel{From: 0, To: -1}, func(int, bool) {}, func(int) ggui.Widget { return widget.Box() })
		}, ggui.Sz(900, 600))
		p.Frame()
		p.Frame()
		page := p.Semantics().String()
		if n := strings.Count(page, "line "); n == 0 || n > 120 {
			t.Errorf("%+v: %d of 1999 lines drawn, want only those near the window", mode, n)
		}
		p.Close()
	}
}

func TestLineCountFormatting(t *testing.T) {
	if lineCount(2712) != "2.7k" || lineCount(2000) != "2k" || lineCount(320) != "320" {
		t.Fatalf("unexpected lineCount formatting: %q %q %q", lineCount(2712), lineCount(2000), lineCount(320))
	}
}

func TestDiffTabSiblingsAndFollowChanges(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "127.0.0.1:1")
	m.post = func(func()) {}
	root := fileRoot{Node: "n", Path: "/p"}
	a := &protocol.GitFile{Path: "a.go", Status: protocol.GitModified, Added: 1}
	b := &protocol.GitFile{Path: "b.go", Status: protocol.GitModified, Added: 2}
	m.openDiffTab(root, "/p", "", "", a, true)

	g := diffGroup{Root: root, Repo: "/p"}
	m.diffChanges.Groups.Set(map[diffGroup]*protocol.GitChanges{
		g: {Root: "/p", Files: []*protocol.GitFile{a, b}},
	})
	if sibs := m.siblings(ggui.Peek(m.Diffs)[ggui.Peek(m.Selected)]); len(sibs) != 2 || sibs[1].GetPath() != "b.go" {
		t.Fatalf("worktree siblings = %+v, want [a.go b.go]", sibs)
	}
	m.stepDiff(1)
	if sel := ggui.Peek(m.Selected); !strings.HasSuffix(sel, "b.go") {
		t.Fatalf("stepDiff(1) selected %q, want b.go", sel)
	}

	// followChanges clears a diff whose file is no longer changed.
	m.followChanges(g, &protocol.GitChanges{Root: "/p", Files: []*protocol.GitFile{a}})
	cur := ggui.Peek(m.Diffs)[ggui.Peek(m.Selected)]
	if cur.Diff == nil || len(cur.Diff.GetLines()) != 0 || cur.Err != "" {
		t.Fatalf("after followChanges cleared b.go: %+v", cur)
	}

	// Commit diff siblings come from diffChanges.Commits.
	c1 := &protocol.GitFile{Path: "x.go", Status: protocol.GitAdded}
	c2 := &protocol.GitFile{Path: "y.go", Status: protocol.GitModified}
	m.openDiffTab(root, "/p", "abc1234", "", c1, true)
	ck := diffCommitKey{Root: root, Repo: "/p", Commit: "abc1234"}
	m.diffChanges.Commits.Set(map[diffCommitKey][]*protocol.GitFile{ck: {c1, c2}})
	if sibs := m.siblings(ggui.Peek(m.Diffs)[ggui.Peek(m.Selected)]); len(sibs) != 2 || sibs[1].GetPath() != "y.go" {
		t.Fatalf("commit siblings = %+v, want [x.go y.go]", sibs)
	}
	m.stepDiff(1)
	if sel := ggui.Peek(m.Selected); !strings.HasSuffix(sel, "y.go") {
		t.Fatalf("commit stepDiff(1) selected %q, want y.go", sel)
	}
}
