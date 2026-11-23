package zedhost

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestUntarAndUnzipCumulativeCapAndTruncation(t *testing.T) {
	t.Run("untar cumulative cap", func(t *testing.T) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
			data := strings.Repeat("x", 40)
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(data)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := untarLimit(&buf, t.TempDir(), 100); err == nil || !strings.Contains(err.Error(), "exceeds maximum unpacked size") {
			t.Fatalf("expected cumulative cap error, got %v", err)
		}
	})

	t.Run("unzip cumulative cap", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(strings.Repeat("y", 40))); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		b := buf.Bytes()
		if err := unzipLimit(bytes.NewReader(b), int64(len(b)), t.TempDir(), 100); err == nil || !strings.Contains(err.Error(), "exceeds maximum unpacked size") {
			t.Fatalf("expected zip cumulative cap error, got %v", err)
		}
	})

	t.Run("download uncompressed explicit truncation error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("z", 120)))
		}))
		defer srv.Close()

		addr := strings.TrimPrefix(srv.URL, "http://")
		client := newSSRFClient(5*time.Second, addr)
		dst := filepath.Join(t.TempDir(), "file.bin")
		err := downloadWithClient(context.Background(), client, srv.URL+"/file.bin", dst, "uncompressed", 100)
		if err == nil || !strings.Contains(err.Error(), "exceeds maximum unpacked size") {
			t.Fatalf("expected explicit truncation error, got %v", err)
		}
		if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
			t.Fatalf("oversized partial file was not removed: %v", statErr)
		}
	})

	t.Run("untarRootLimit oversized non-regular headers at 512B boundary", func(t *testing.T) {
		const limit = int64(1024)
		streamCap := limit + max(limit, 16<<20) // 16 MiB + 1024, exact 512B boundary
		numDirs := int(streamCap/512) + 2

		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for i := range numDirs {
			name := "d"
			if i > 0 {
				name = "d" + string(rune('a'+(i%26)))
			}
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     name,
				Mode:     0o755,
			}); err != nil {
				t.Fatal(err)
			}
		}
		late := []byte("should not be extracted")
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "late.txt",
			Mode:     0o644,
			Size:     int64(len(late)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(late); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}

		dst := t.TempDir()
		err := untarLimit(&buf, dst, limit)
		if err == nil || !strings.Contains(err.Error(), "exceeds maximum unpacked size") {
			t.Fatalf("expected unpacked size error on oversized non-regular tar stream, got %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dst, "late.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("late.txt should not have been extracted: %v", statErr)
		}
	})
}
