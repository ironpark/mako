package zedhost

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// An archive's links that stay inside it are kept, and no chain of them
// carries a file out of the folder it unpacks into.
func TestUntarStaysInside(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	link := func(name, to string) {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: to}); err != nil {
			t.Fatal(err)
		}
	}
	file := func(name, data string) {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	file("lib/cli.js", "js")
	link("bin/cli", "../lib/cli.js")
	link("abs", "/tmp")
	// Each link stays inside by its name alone, but up through the first
	// the second points out of the folder.
	link("a", ".")
	link("a/b", "../escaped")
	file("a/b/x", "out")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	top := t.TempDir()
	dir := filepath.Join(top, "ext")
	if err := os.Mkdir(filepath.Join(top, "escaped"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = untar(&buf, dir) // the chain fails, or is left out; either keeps it in
	if _, err := os.Stat(filepath.Join(top, "escaped", "x")); err == nil {
		t.Fatal("a file landed outside the archive's folder")
	}
	if _, err := os.Lstat(filepath.Join(dir, "abs")); err == nil {
		t.Fatal("a link to an absolute path was kept")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "bin", "cli")); err != nil || string(got) != "js" {
		t.Fatalf("the link inside the archive reads %q, %v", got, err)
	}
}
