package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/sanbo/internal/protocol"
)

func TestWorktreeLifecycle(t *testing.T) {
	root, run := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "one\ntwo\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")

	if _, _, err := AddWorktree(ctx, root, "-x", ""); err == nil {
		t.Fatal("an option taken for a branch")
	}
	repo, path, err := AddWorktree(ctx, root, "feat/x", "main")
	if err != nil {
		t.Fatal(err)
	}
	if repo != root && !sameDir(repo, root) || filepath.Base(path) != "feat-x" || filepath.Base(filepath.Dir(path)) != filepath.Base(root)+".worktrees" {
		t.Fatalf("repo %s path %s", repo, path)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(path)) })
	if Branch(path) != "feat/x" {
		t.Fatalf("branch %q", Branch(path))
	}

	// What the worktree did since main: a commit, an edit, a new file.
	write(t, path, "a.txt", "one\nTWO\n")
	git := func(args ...string) { t.Helper(); run(append([]string{"-C", path}, args...)...) }
	git("commit", "-q", "-am", "edit")
	write(t, path, "b.txt", "new\n")
	ch, err := BaseStatus(ctx, path, "main")
	if err != nil {
		t.Fatal(err)
	}
	want := []*protocol.GitFile{
		{Path: "a.txt", Status: protocol.GitModified, Added: 1, Removed: 1},
		{Path: "b.txt", Status: protocol.GitAdded, Added: 1},
	}
	if !protocol.Equal(ch.GetFiles(), want) || ch.GetBranch() != "feat/x" {
		t.Fatalf("changes %+v", ch)
	}
	d, err := BaseDiff(ctx, path, "main", "a.txt", "")
	if err != nil || len(d.GetLines()) != 4 || d.GetLines()[2].GetText() != "two" || d.GetLines()[3].GetText() != "TWO" {
		t.Fatalf("diff %+v %v", d, err)
	}

	w := &protocol.Worktree{Repo: repo, Path: path, Branch: "feat/x", Base: "main"}
	if _, err := RemoveWorktree(ctx, w); err == nil {
		t.Fatal("removed a worktree with changes")
	}
	os.Remove(filepath.Join(path, "b.txt"))
	if out, err := Merge(ctx, w); err != nil {
		t.Fatalf("merge: %v %s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "one\nTWO\n" {
		t.Fatalf("merged a.txt %q", b)
	}
	if _, err := RemoveWorktree(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("worktree left")
	}
	if strings.Contains(run("branch"), "feat/x") {
		t.Fatal("merged branch left")
	}
}

func sameDir(a, b string) bool {
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	return ra == rb
}

func TestDiscardHunkUndoesOnlyThatHunk(t *testing.T) {
	root, run := cliRepo(t)
	var lines []string
	for i := range 20 {
		lines = append(lines, string(rune('a'+i)))
	}
	write(t, root, "f.txt", strings.Join(lines, "\n")+"\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	changed := slices.Clone(lines)
	changed[1] = "B"                            // first hunk
	changed = slices.Insert(changed, 15, "new") // second hunk
	write(t, root, "f.txt", strings.Join(changed, "\n")+"\n")

	d, err := Diff(root, "f.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	var hunks [][]*protocol.DiffLine
	for _, l := range d.GetLines() {
		if l.GetOp() == "@" {
			hunks = append(hunks, nil)
			continue
		}
		hunks[len(hunks)-1] = append(hunks[len(hunks)-1], l)
	}
	if len(hunks) != 2 {
		t.Fatalf("hunks %+v", d.GetLines())
	}
	if err := DiscardHunk(root, "f.txt", hunks[1]); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	want := slices.Clone(lines)
	want[1] = "B"
	if string(got) != strings.Join(want, "\n")+"\n" {
		t.Fatalf("after discard:\n%s", got)
	}
	// The first hunk, read before, no longer matches where it says.
	write(t, root, "f.txt", "other\n")
	if err := DiscardHunk(root, "f.txt", hunks[0]); err == nil {
		t.Fatal("discarded a hunk the file no longer has")
	}
	if err := DiscardHunk(root, "../f.txt", hunks[0]); err == nil {
		t.Fatal("left the work tree")
	}
}
