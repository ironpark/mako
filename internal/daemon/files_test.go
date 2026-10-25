package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestListDirPutsDirectoriesFirst(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.txt", "A.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "z"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "z"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	got, err := listDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got.Entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "link,z,A.txt,b.txt" || !got.Entries[0].Link || !got.Entries[0].Dir {
		t.Fatalf("entries = %+v", got.Entries)
	}
	if _, err := listDir("relative"); err == nil {
		t.Fatal("a relative path was listed")
	}
}

func TestReadFileTellsTextFromBinary(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	text := write("t.txt", []byte("한글 text\n"))
	if f, err := readFile(text, 0); err != nil || f.Binary || string(f.Data) != "한글 text\n" || f.Truncated {
		t.Fatalf("text = %+v, %v", f, err)
	}
	// Cut inside a rune, it is still text.
	if f, err := readFile(text, 2); err != nil || f.Binary || !f.Truncated {
		t.Fatalf("cut text = %+v, %v", f, err)
	}
	if f, err := readFile(write("b.bin", []byte{1, 0, 2}), 0); err != nil || !f.Binary || f.Data != nil {
		t.Fatalf("binary = %+v, %v", f, err)
	}
	if _, err := readFile(dir, 0); err == nil {
		t.Fatal("a directory was read")
	}
}

func TestListDirMarksWhatGitIgnores(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for name, body := range map[string]string{".gitignore": "build/\n*.log\n", "main.go": "x", "run.log": "x", "build/out": "x"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := listDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ignored := map[string]bool{}
	for _, e := range got.Entries {
		ignored[e.Name] = e.Ignored
	}
	want := map[string]bool{".git": false, ".gitignore": false, "build": true, "main.go": false, "run.log": true}
	for name, w := range want {
		if ignored[name] != w {
			t.Errorf("%s ignored %v, want %v", name, ignored[name], w)
		}
	}
	// Outside a repository nothing is.
	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "run.log"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := listDir(plain); got.Entries[0].Ignored {
		t.Error("a file outside a repository is ignored")
	}
}
