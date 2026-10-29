package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
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
	for _, e := range got.GetEntries() {
		names = append(names, e.GetName())
	}
	if strings.Join(names, ",") != "link,z,A.txt,b.txt" || !got.GetEntries()[0].GetLink() || !got.GetEntries()[0].GetDir() {
		t.Fatalf("entries = %+v", got.GetEntries())
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
	if f, err := readFile(text, 0); err != nil || f.GetBinary() || string(f.GetData()) != "한글 text\n" || f.GetTruncated() {
		t.Fatalf("text = %+v, %v", f, err)
	}
	// Cut inside a rune, it is still text.
	if f, err := readFile(text, 2); err != nil || f.GetBinary() || !f.GetTruncated() {
		t.Fatalf("cut text = %+v, %v", f, err)
	}
	if f, err := readFile(write("b.bin", []byte{1, 0, 2}), 0); err != nil || !f.GetBinary() || f.GetData() != nil {
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
	for _, e := range got.GetEntries() {
		ignored[e.GetName()] = e.GetIgnored()
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
	if got, _ := listDir(plain); got.GetEntries()[0].GetIgnored() {
		t.Error("a file outside a repository is ignored")
	}
}

func TestWriteFileKeepsModeAndRefusesAStaleWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	read, err := readFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := writeFile(path, []byte("new"), protocol.TimeOf(read.GetModTime()))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if got, _ := os.ReadFile(path); string(got) != "new" || info.Mode().Perm() != 0o755 || f.GetSize() != 3 {
		t.Fatalf("after write: %q, mode %v, reply %+v", got, info.Mode(), f)
	}
	// Written with the time from before the last write, it is refused.
	if _, err := writeFile(path, []byte("stale"), protocol.TimeOf(read.GetModTime()).Add(-time.Second)); !protocol.HasCode(err, protocol.CodeChanged) {
		t.Fatalf("stale write: %v, want CodeChanged", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("a refused write changed the file to %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
	// A new file is created.
	if _, err := writeFile(filepath.Join(dir, "fresh.txt"), []byte("x"), time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func TestFileOpNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	at := func(name string) string { return filepath.Join(dir, name) }
	op := func(kind, path, to string) error {
		_, err := fileOp(&protocol.FileOp{Op: kind, Path: path, To: to})
		return err
	}
	if err := op(protocol.FileCreate, at("a.txt"), ""); err != nil {
		t.Fatal(err)
	}
	if err := op(protocol.FileCreate, at("a.txt"), ""); err == nil {
		t.Fatal("created over a file")
	}
	if err := op(protocol.FileMkdir, at("sub"), ""); err != nil {
		t.Fatal(err)
	}
	if err := op(protocol.FileCreate, at("sub/b.txt"), ""); err != nil {
		t.Fatal(err)
	}
	if err := op(protocol.FileRename, at("a.txt"), at("sub/b.txt")); err == nil {
		t.Fatal("renamed over a file")
	}
	if err := op(protocol.FileRename, at("a.txt"), at("c.txt")); err != nil {
		t.Fatal(err)
	}
	if err := op(protocol.FileDelete, at("sub"), ""); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "c.txt" {
		t.Fatalf("left %v", entries)
	}
	for _, bad := range []*protocol.FileOp{{Op: protocol.FileDelete, Path: "/"}, {Op: protocol.FileDelete, Path: "relative"}, {Op: "chmod", Path: at("c.txt")}} {
		if _, err := fileOp(bad); err == nil {
			t.Errorf("%+v allowed", bad)
		}
	}
}
