package gitstat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/ironpark/sanbo/internal/protocol"
)

func write(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// repo makes a repository with one commit of files.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	r, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, _ := r.Worktree()
	for name, body := range files {
		write(t, root, name, body)
		if _, err := wt.Add(name); err != nil {
			t.Fatal(err)
		}
	}
	sig := &object.Signature{Name: "t", Email: "t@t", When: time.Now()}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStatusCountsEachChange(t *testing.T) {
	root := repo(t, map[string]string{
		"main.go":       "package main\n\nfunc main() {}\n",
		"gone.txt":      "a\nb\n",
		"internal/x.go": "one\ntwo\nthree\n",
	})
	write(t, root, "main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println() }\n")
	write(t, root, "internal/new.go", "a\nb\nc")
	write(t, root, "bin.dat", "\x00\x01")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	got, err := Status(filepath.Join(root, "internal")) // from inside the tree
	if err != nil {
		t.Fatal(err)
	}
	if got.NotRepo || got.Branch != "master" {
		t.Fatalf("root %q branch %q not repo %v", got.Root, got.Branch, got.NotRepo)
	}
	want := map[string]protocol.GitFile{
		"bin.dat":         {Path: "bin.dat", Status: protocol.GitAdded, Binary: true},
		"gone.txt":        {Path: "gone.txt", Status: protocol.GitDeleted, Removed: 2},
		"internal/new.go": {Path: "internal/new.go", Status: protocol.GitAdded, Added: 3},
		"main.go":         {Path: "main.go", Status: protocol.GitModified, Added: 3, Removed: 1},
	}
	if len(got.Files) != len(want) {
		t.Fatalf("files %+v", got.Files)
	}
	for _, f := range got.Files {
		if f != want[f.Path] {
			t.Errorf("%s: %+v, want %+v", f.Path, f, want[f.Path])
		}
	}

	if out, err := Status(t.TempDir()); err != nil || !out.NotRepo {
		t.Fatalf("outside a repository: %+v, %v", out, err)
	}
}

func TestDiffWritesHunks(t *testing.T) {
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, "line"+string(rune('a'+i-1)))
	}
	old := strings.Join(lines, "\n") + "\n"
	root := repo(t, map[string]string{"f.txt": old})
	lines[1] = "changed b"                                                     // near the top
	lines = append(lines[:15], append([]string{"inserted"}, lines[15:]...)...) // far below
	write(t, root, "f.txt", strings.Join(lines, "\n")+"\n")

	d, err := Diff(root, "f.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	var headers []string
	for _, l := range d.Lines {
		if l.Op == "@" {
			headers = append(headers, l.Text)
		}
	}
	if strings.Join(headers, " | ") != "@@ -1,5 +1,5 @@ | @@ -13,6 +13,7 @@" {
		t.Fatalf("hunks %q", headers)
	}
	if d.Lines[1].Op != " " || d.Lines[1].Old != 1 || d.Lines[1].New != 1 {
		t.Fatalf("first line %+v", d.Lines[1])
	}
	if _, err := Diff(root, "../outside", ""); err == nil {
		t.Fatal("a path out of the tree was diffed")
	}
}
