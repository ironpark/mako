package e2e_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/zed"
)

func buildTarGz(t *testing.T, fn func(*tar.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	fn(tw)
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

func writeTarReg(t *testing.T, tw *tar.Writer, name, body string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("tar WriteHeader(%s): %v", name, err)
	}
	if _, err := io.WriteString(tw, body); err != nil {
		t.Fatalf("tar Write(%s): %v", name, err)
	}
}

const zedZigManifest = `id = "zig"
name = "Zig"
version = "0.4.2"
schema_version = 1
languages = ["languages/zig"]

[grammars.zig]
repository = "https://github.com/tree-sitter-grammars/tree-sitter-zig"
rev = "6479aa13"

[language_servers.zls]
language = "Zig"
`

func serveZedArchive(t *testing.T, id string, payload []byte) (*httptest.Server, *zed.Store) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/extensions":
			switch r.URL.Query().Get("provides") {
			case "languages":
				_, _ = w.Write([]byte(`{"data":[{"id":"zig","name":"Zig","version":"0.4.2","provides":["languages","grammars"],"download_count":5}]}`))
			case "context-servers":
				_, _ = w.Write([]byte(`{"data":[{"id":"zig-mcp","name":"Zig MCP","version":"0.1.0","provides":["context-servers"],"download_count":9}]}`))
			default:
				_, _ = w.Write([]byte(`{"data":[]}`))
			}
		case "/extensions/" + id + "/download":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	store := &zed.Store{
		Dir:    t.TempDir(),
		API:    srv.URL,
		Client: srv.Client(),
	}
	return srv, store
}

func TestZedStoreTier1(t *testing.T) {
	validArchive := buildTarGz(t, func(tw *tar.Writer) {
		writeTarReg(t, tw, "extension.toml", zedZigManifest)
		writeTarReg(t, tw, "languages/zig/config.toml", `name = "Zig"
grammar = "zig"
path_suffixes = ["zig", "zon"]
line_comments = ["// ", "/// "]
`)
		writeTarReg(t, tw, "languages/zig/highlights.scm", `(comment) @comment`)
		writeTarReg(t, tw, "grammars/zig.wasm", "\x00asm")
	})

	_, store := serveZedArchive(t, "zig", validArchive)
	ctx := context.Background()

	t.Run("search_extensions", func(t *testing.T) {
		listings, err := store.Search(ctx, "zig")
		if err != nil || len(listings) != 2 {
			t.Fatalf("Search: listings=%+v err=%v", listings, err)
		}
	})

	var exts []zed.Extension
	t.Run("install_and_installed", func(t *testing.T) {
		ext, err := store.Install(ctx, "zig")
		if err != nil {
			t.Fatalf("Install(zig): %v", err)
		}
		if ext.ID != "zig" || ext.Name != "Zig" || !ext.LanguageServers["zls"].ServesLanguage("Zig") {
			t.Fatalf("unexpected installed extension: %+v", ext.Manifest)
		}
		var err2 error
		exts, err2 = store.Installed()
		if err2 != nil || len(exts) != 1 {
			t.Fatalf("Installed: exts=%+v err=%v", exts, err2)
		}
	})

	t.Run("languages_matches_and_query", func(t *testing.T) {
		langs := zed.Languages(exts)
		if len(langs) != 1 {
			t.Fatalf("Languages: got %d, want 1", len(langs))
		}
		l := langs[0]
		if l.Grammar != filepath.Join(store.Dir, "zig", "grammars", "zig.wasm") {
			t.Fatalf("Grammar path = %q", l.Grammar)
		}
		if !l.Matches("main.zig", "") || !l.Matches("build.zig.zon", "") || l.Matches("main.go", "") {
			t.Fatal("Matches failed on zig/zon/go filenames")
		}
		if q, err := l.Query("highlights"); err != nil || q != "(comment) @comment" {
			t.Fatalf("Query(highlights) = %q, err=%v", q, err)
		}
	})

	t.Run("reinstall_idempotent", func(t *testing.T) {
		ext, err := store.Install(ctx, "zig")
		if err != nil || ext.Version != "0.4.2" {
			t.Fatalf("re-Install(zig): ext=%+v err=%v", ext, err)
		}
	})

	t.Run("uninstall_and_empty", func(t *testing.T) {
		if err := store.Uninstall("zig"); err != nil {
			t.Fatalf("Uninstall(zig): %v", err)
		}
		if after, err := store.Installed(); err != nil || len(after) != 0 {
			t.Fatalf("expected 0 installed extensions after Uninstall, got %+v err=%v", after, err)
		}
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func buildRawNegativeSizeTarGz(t *testing.T) []byte {
	t.Helper()
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	writeTarReg(t, tw, "extension.toml", zedZigManifest)
	// Flush tw without closing yet by closing a temporary writer? Better: write full tar, strip 1024-byte EOF trailer, append raw 512-byte header + 1024-byte EOF trailer.
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	rawTar := tarBuf.Bytes()
	// Strip the two 512-byte zero blocks at the end.
	if len(rawTar) >= 1024 {
		rawTar = rawTar[:len(rawTar)-1024]
	}

	var hdr [512]byte
	copy(hdr[0:100], "evil.txt")
	copy(hdr[100:108], "0000644\x00")
	copy(hdr[108:116], "0000000\x00")
	copy(hdr[116:124], "0000000\x00")
	// Base-256 negative size (-100 in 12-byte two's complement: all 0xff, last byte 0x9c).
	for i := 124; i < 135; i++ {
		hdr[i] = 0xff
	}
	hdr[135] = 0x9c
	copy(hdr[136:148], "00000000000\x00")
	for i := 148; i < 156; i++ {
		hdr[i] = ' '
	}
	hdr[156] = '0'
	copy(hdr[257:263], "ustar\x00")
	copy(hdr[263:265], "00")

	var sum int
	for _, b := range hdr {
		sum += int(b)
	}
	copy(hdr[148:156], fmt.Sprintf("%06o\x00 ", sum))

	var finalTar bytes.Buffer
	finalTar.Write(rawTar)
	finalTar.Write(hdr[:])
	finalTar.Write(make([]byte, 1024))

	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write(finalTar.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return gzBuf.Bytes()
}

func buildRawTarHeader(name string, sizeOctal string, typeflag byte) [512]byte {
	var hdr [512]byte
	copy(hdr[0:100], name)
	copy(hdr[100:108], "0000644\x00")
	copy(hdr[108:116], "0000000\x00")
	copy(hdr[116:124], "0000000\x00")
	copy(hdr[124:136], sizeOctal)
	copy(hdr[136:148], "00000000000\x00")
	for i := 148; i < 156; i++ {
		hdr[i] = ' '
	}
	hdr[156] = typeflag
	copy(hdr[257:263], "ustar\x00")
	copy(hdr[263:265], "00")
	var sum int
	for _, b := range hdr {
		sum += int(b)
	}
	copy(hdr[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return hdr
}

func buildPAXNegativeSizeTarGz(t *testing.T) []byte {
	t.Helper()
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	writeTarReg(t, tw, "extension.toml", zedZigManifest)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	prefix := tarBuf.Bytes()
	if len(prefix) >= 1024 {
		prefix = prefix[:len(prefix)-1024]
	}

	// Build raw PAX extended header ('x') claiming size=-100 for the following entry.
	rec := "13 size=-100\n"
	paxHdr := buildRawTarHeader("PaxHeaders.0/neg.txt", fmt.Sprintf("%011o\x00", len(rec)), tar.TypeXHeader)
	var paxData [512]byte
	copy(paxData[:], rec)

	var fileBuf bytes.Buffer
	ftw := tar.NewWriter(&fileBuf)
	writeTarReg(t, ftw, "neg.txt", "abc")
	if err := ftw.Close(); err != nil {
		t.Fatal(err)
	}

	var fullTar bytes.Buffer
	fullTar.Write(prefix)
	fullTar.Write(paxHdr[:])
	fullTar.Write(paxData[:])
	fullTar.Write(fileBuf.Bytes())

	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write(fullTar.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return gzBuf.Bytes()
}

func TestZedStoreTier2SecurityB12(t *testing.T) {
	ctx := context.Background()

	t.Run("B12_negative_tar_size_raw_base256", func(t *testing.T) {
		payload := buildRawNegativeSizeTarGz(t)
		_, store := serveZedArchive(t, "zig", payload)
		if _, err := store.Install(ctx, "zig"); err == nil {
			t.Fatal("expected Install to reject archive with raw negative tar size")
		}
		if _, err := os.Stat(filepath.Join(store.Dir, "zig")); !os.IsNotExist(err) {
			t.Fatalf("expected no zig dir after failed install, stat err=%v", err)
		}
	})

	t.Run("B12_negative_tar_size_pax", func(t *testing.T) {
		payload := buildPAXNegativeSizeTarGz(t)
		_, store := serveZedArchive(t, "zig", payload)
		if _, err := store.Install(ctx, "zig"); err == nil {
			t.Fatal("expected Install to reject archive with PAX negative size")
		}
		if _, err := os.Stat(filepath.Join(store.Dir, "zig")); !os.IsNotExist(err) {
			t.Fatalf("expected no zig dir after failed install, stat err=%v", err)
		}
	})

	t.Run("B12_archive_bomb_over_256MiB", func(t *testing.T) {
		const bombSize = 257 << 20
		payload := buildTarGz(t, func(tw *tar.Writer) {
			writeTarReg(t, tw, "extension.toml", zedZigManifest)
			if err := tw.WriteHeader(&tar.Header{
				Name:     "bomb.bin",
				Mode:     0o644,
				Size:     bombSize,
				Typeflag: tar.TypeReg,
			}); err != nil {
				t.Fatalf("WriteHeader bomb: %v", err)
			}
			if _, err := io.CopyN(tw, zeroReader{}, bombSize); err != nil {
				t.Fatalf("CopyN bomb: %v", err)
			}
		})
		_, store := serveZedArchive(t, "zig", payload)
		_, err := store.Install(ctx, "zig")
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "large") {
			t.Fatalf("expected too-large error for >256 MiB archive bomb, got %v", err)
		}
		if entries, err := os.ReadDir(store.Dir); err != nil || len(entries) != 0 {
			t.Fatalf("expected empty store dir after bomb rejection, got entries=%v err=%v", entries, err)
		}
	})

	t.Run("B12_path_traversal_dotdot", func(t *testing.T) {
		payload := buildTarGz(t, func(tw *tar.Writer) {
			writeTarReg(t, tw, "extension.toml", zedZigManifest)
			writeTarReg(t, tw, "../evil", "pwned")
		})
		_, store := serveZedArchive(t, "zig", payload)
		if _, err := store.Install(ctx, "zig"); err == nil {
			t.Fatal("expected Install to reject ../evil path traversal")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(store.Dir), "evil")); !os.IsNotExist(err) {
			t.Fatal("path traversal wrote file outside store.Dir")
		}
	})

	t.Run("B12_path_traversal_absolute", func(t *testing.T) {
		payload := buildTarGz(t, func(tw *tar.Writer) {
			writeTarReg(t, tw, "extension.toml", zedZigManifest)
			writeTarReg(t, tw, "/etc/evil", "pwned")
		})
		_, store := serveZedArchive(t, "zig", payload)
		if _, err := store.Install(ctx, "zig"); err == nil {
			t.Fatal("expected Install to reject /etc/evil absolute path")
		}
		if _, err := os.Stat(filepath.Join(store.Dir, "zig")); !os.IsNotExist(err) {
			t.Fatal("expected no installed extension after absolute path rejection")
		}
	})

	t.Run("B12_symlink_escape_standalone_abs", func(t *testing.T) {
		payload := buildTarGz(t, func(tw *tar.Writer) {
			writeTarReg(t, tw, "extension.toml", zedZigManifest)
			if err := tw.WriteHeader(&tar.Header{Name: "abs", Typeflag: tar.TypeSymlink, Linkname: "/tmp", Mode: 0o777}); err != nil {
				t.Fatalf("WriteHeader symlink abs: %v", err)
			}
		})
		_, store := serveZedArchive(t, "zig", payload)
		_, err := store.Install(ctx, "zig")
		// store.Install skips tar.TypeSymlink entries (err == nil) or may reject symlinks (err != nil);
		// in neither case may the symlink exist in the installed extension dir.
		if _, lerr := os.Lstat(filepath.Join(store.Dir, "zig", "abs")); !errors.Is(lerr, os.ErrNotExist) {
			t.Fatalf("dangerous symlink 'abs -> /tmp' materialized inside extension dir (install err=%v, lstat err=%v)", err, lerr)
		}
		if err != nil {
			if _, serr := os.Lstat(filepath.Join(store.Dir, "zig")); !errors.Is(serr, os.ErrNotExist) {
				t.Fatalf("failed Install(%v) left behind extension dir (lstat err=%v)", err, serr)
			}
		}
	})

	t.Run("B12_symlink_escape_chained_target", func(t *testing.T) {
		parent := t.TempDir()
		outsideEscaped := filepath.Join(parent, "escaped")
		if err := os.MkdirAll(outsideEscaped, 0o755); err != nil {
			t.Fatal(err)
		}
		payload := buildTarGz(t, func(tw *tar.Writer) {
			writeTarReg(t, tw, "extension.toml", zedZigManifest)
			if err := tw.WriteHeader(&tar.Header{Name: "a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777}); err != nil {
				t.Fatalf("WriteHeader symlink a: %v", err)
			}
			if err := tw.WriteHeader(&tar.Header{Name: "a/b", Typeflag: tar.TypeSymlink, Linkname: "../../escaped", Mode: 0o777}); err != nil {
				t.Fatalf("WriteHeader symlink a/b: %v", err)
			}
			writeTarReg(t, tw, "a/b/x", "pwn")
		})
		_, store := serveZedArchive(t, "zig", payload)
		store.Dir = filepath.Join(parent, "store")
		_, err := store.Install(ctx, "zig")

		if _, serr := os.Lstat(filepath.Join(outsideEscaped, "x")); !errors.Is(serr, os.ErrNotExist) {
			t.Fatalf("chained symlink escaped store.Dir to %s (install err=%v, lstat err=%v)", filepath.Join(outsideEscaped, "x"), err, serr)
		}
		if _, serr := os.Lstat(filepath.Join(store.Dir, "zig", "escaped", "x")); !errors.Is(serr, os.ErrNotExist) {
			t.Fatalf("chained symlink resolved inside store.Dir (install err=%v, lstat err=%v)", err, serr)
		}
		if err != nil {
			if _, serr := os.Lstat(filepath.Join(store.Dir, "zig", "a", "b")); !errors.Is(serr, os.ErrNotExist) {
				t.Fatalf("failed Install(%v) left behind zig/a/b (lstat err=%v)", err, serr)
			}
			return
		}
		for _, p := range []string{
			filepath.Join(store.Dir, "zig", "a"),
			filepath.Join(store.Dir, "zig", "a", "b"),
		} {
			if info, lerr := os.Lstat(p); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("symlink %s was materialized during Install", p)
			}
		}
	})

	t.Run("invalid_extension_id_rejected", func(t *testing.T) {
		_, store := serveZedArchive(t, "zig", nil)
		if _, err := store.Install(ctx, "../x"); err == nil {
			t.Fatal("expected Install('../x') to fail")
		}
	})
}
