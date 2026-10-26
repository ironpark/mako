package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func searchTree(t *testing.T, git bool) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"main.go":           "package main\n\nfunc Hello() {}\n// hello again\n",
		"sub/notes.txt":     "nothing here\nHELLO loud\n",
		"node_modules/x.js": "hello from deps\n",
		".gitignore":        "ignored.txt\n",
		"ignored.txt":       "hello ignored\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if git {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git is not installed")
		}
		if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\nnode_modules/\n"), 0o644)
	}
	return root
}

func TestSearchFindsTextAndLeavesOutIgnored(t *testing.T) {
	ctx := context.Background()
	for _, git := range []bool{true, false} {
		root := searchTree(t, git)
		files, err := listFiles(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(files.Files)
		if !slices.Contains(files.Files, "main.go") || !slices.Contains(files.Files, "sub/notes.txt") || slices.Contains(files.Files, "node_modules/x.js") {
			t.Fatalf("git=%v files %q", git, files.Files)
		}
		if git && slices.Contains(files.Files, "ignored.txt") {
			t.Fatalf("ignored listed: %q", files.Files)
		}

		res, err := searchText(ctx, root, "hello")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range res.Matches {
			got = append(got, m.Path)
			if m.Path == "main.go" && m.Line == 3 && m.Col != 6 {
				t.Errorf("column %d", m.Col)
			}
		}
		slices.Sort(got)
		for _, want := range []string{"main.go", "main.go", "sub/notes.txt"} {
			i := slices.Index(got, want)
			if i < 0 {
				t.Fatalf("git=%v: %s not found in %q", git, want, got)
			}
			got = slices.Delete(got, i, i+1)
		}
		if slices.Contains(got, "node_modules/x.js") || git && slices.Contains(got, "ignored.txt") {
			t.Fatalf("git=%v: ignored found: %q", git, got)
		}
		// A capital matches the case.
		if res, _ := searchText(ctx, root, "HELLO"); len(res.Matches) != 1 || res.Matches[0].Path != "sub/notes.txt" {
			t.Fatalf("git=%v: case %+v", git, res.Matches)
		}
	}
}

func TestWalkSearchWithoutTools(t *testing.T) {
	root := searchTree(t, false)
	res, err := walkSearch(context.Background(), root, "hello")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range res.Matches {
		got = append(got, m.Path)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"ignored.txt", "main.go", "main.go", "sub/notes.txt"}) {
		t.Fatalf("matches %q", got)
	}
}
