package fsutil

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteNewAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.bin")

	// Empty payload creates a 0600 file with no temp leftovers.
	if err := Write(path, nil); err != nil {
		t.Fatalf("Write empty: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 0600", mode)
	}
	if got, err := os.ReadFile(path); err != nil || len(got) != 0 {
		t.Fatalf("read empty = %q, %v", got, err)
	}

	// Overwrite grow with arbitrary binary bytes.
	large := bytes.Repeat([]byte{0x00, 0x7f, 0xff, 'a', '\n'}, 200)
	if err := Write(path, large); err != nil {
		t.Fatalf("Write grow: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, large) {
		t.Fatalf("read large len=%d, %v", len(got), err)
	}

	// Overwrite shrink leaves no trailing bytes from the previous write.
	small := []byte("ok\n")
	if err := Write(path, small); err != nil {
		t.Fatalf("Write shrink: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, small) {
		t.Fatalf("read small = %q, %v", got, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.bin" {
		t.Fatalf("unexpected directory entries: %v", entries)
	}
}

func TestWriteErrorsAndCleanup(t *testing.T) {
	dir := t.TempDir()

	// Missing parent directory returns error and creates nothing.
	missing := filepath.Join(dir, "nope", "file.txt")
	if err := Write(missing, []byte("hi")); err == nil {
		t.Fatal("expected error for missing parent directory")
	}

	// Target path is an existing directory: Rename fails, directory intact, temp cleaned up.
	targetDir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(targetDir, []byte("hi")); err == nil {
		t.Fatal("expected error when target is a directory")
	}
	if fi, err := os.Stat(targetDir); err != nil || !fi.IsDir() {
		t.Fatalf("target directory damaged: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "subdir" {
		t.Fatalf("leftover temp file after error: %v", entries)
	}
}

func TestWriteConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.txt")

	payloads := [][]byte{
		bytes.Repeat([]byte("A"), 4096),
		bytes.Repeat([]byte("B"), 4096),
		bytes.Repeat([]byte("C"), 4096),
		bytes.Repeat([]byte("D"), 4096),
	}

	const n = 24
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- Write(path, payloads[i%len(payloads)])
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Write failed: %v", err)
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, p := range payloads {
		if bytes.Equal(got, p) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("final file content corrupted (len=%d)", len(got))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "shared.txt" {
		t.Fatalf("leftover temp files after concurrent Write: %v", entries)
	}
}
