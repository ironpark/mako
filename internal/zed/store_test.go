package zed

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// archive is a gzipped tar of files, by path.
func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var zigExtension = map[string]string{
	"extension.toml": `id = "zig"
name = "Zig"
version = "0.4.2"
schema_version = 1
languages = ["languages/zig"]

[grammars.zig]
repository = "https://github.com/tree-sitter-grammars/tree-sitter-zig"
rev = "6479aa13"

[language_servers.zls]
language = "Zig"
`,
	"languages/zig/config.toml": `name = "Zig"
grammar = "zig"
path_suffixes = ["zig", "zon"]
line_comments = ["// ", "/// "]
`,
	"languages/zig/highlights.scm": `(comment) @comment`,
	"grammars/zig.wasm":            "\x00asm",
}

func TestInstallFromTheStore(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/extensions":
			asked = r.URL.Query().Get("filter")
			switch r.URL.Query().Get("provides") {
			case "languages":
				w.Write([]byte(`{"data":[{"id":"zig","name":"Zig","version":"0.4.2","provides":["languages","grammars"],"download_count":5}]}`))
			case "context-servers":
				w.Write([]byte(`{"data":[{"id":"zig-mcp","name":"Zig MCP","version":"0.1.0","provides":["context-servers"],"download_count":9},{"id":"zig","name":"Zig","version":"0.4.2","provides":["languages","grammars"],"download_count":5}]}`))
			default:
				w.Write([]byte(`{"data":[]}`))
			}
		case "/extensions/zig/download":
			w.Write(archive(t, zigExtension))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := &Store{Dir: t.TempDir(), API: srv.URL}

	found, err := s.Search(context.Background(), "zi")
	if err != nil || len(found) != 2 || found[0].ID != "zig-mcp" || found[1].ID != "zig" || asked != "zi" {
		t.Fatalf("search: %+v, %v (asked %q)", found, err, asked)
	}
	ext, err := s.Install(context.Background(), "zig")
	if err != nil {
		t.Fatal(err)
	}
	if ext.Name != "Zig" || !ext.LanguageServers["zls"].ServesLanguage("Zig") {
		t.Fatalf("installed %+v", ext.Manifest)
	}
	// Installed again, it replaces itself and leaves nothing behind.
	if _, err := s.Install(context.Background(), "zig"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 1 {
		t.Fatalf("left in the store: %v", entries)
	}

	exts, err := s.Installed()
	if err != nil || len(exts) != 1 {
		t.Fatalf("installed: %v, %v", exts, err)
	}
	langs := Languages(exts)
	if len(langs) != 1 {
		t.Fatalf("languages: %+v", langs)
	}
	l := langs[0]
	if l.Grammar != filepath.Join(s.Dir, "zig", "grammars", "zig.wasm") || l.Config.Comment() != "// " {
		t.Fatalf("language %+v", l)
	}
	if !l.Matches("build.zig", "") || !l.Matches("build.zig.zon", "") || l.Matches("zig.go", "") {
		t.Fatal("file names matched wrongly")
	}
	if q, err := l.Query("highlights"); err != nil || q != "(comment) @comment" {
		t.Fatalf("highlights %q, %v", q, err)
	}
	if q, err := l.Query("injections"); err != nil || q != "" {
		t.Fatalf("missing injections %q, %v", q, err)
	}

	if err := s.Uninstall("zig"); err != nil {
		t.Fatal(err)
	}
	if exts, _ := s.Installed(); len(exts) != 0 {
		t.Fatalf("still installed: %v", exts)
	}
}

func TestInstallRefusesAnArchiveThatEscapes(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	files := map[string]string{"extension.toml": zigExtension["extension.toml"], "../evil": "x"}
	if _, err := s.installArchive("zig", bytes.NewReader(archive(t, files))); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.Dir), "evil")); err == nil {
		t.Fatal("a file landed outside the store")
	}
	// Nor does it take an archive of another extension than asked for.
	if _, err := s.installArchive("lua", bytes.NewReader(archive(t, zigExtension))); err == nil {
		t.Fatal("installed zig as lua")
	}
	if _, err := s.Install(context.Background(), "../x"); err == nil {
		t.Fatal("took a path as an ID")
	}
}

func TestFirstLineFindsAScript(t *testing.T) {
	langs := []Language{{Config: LanguageConfig{Name: "Shell", PathSuffixes: []string{"sh"}}}}
	langs[0].firstLine = regexp.MustCompile(`^#!.*\b(ba|z)?sh\b`)
	if !langs[0].Matches("run", "#!/bin/bash") || langs[0].Matches("run", "hello") {
		t.Fatal("first line not matched")
	}
}

func TestUntarRejectsNegativeSizeAndTotalLimit(t *testing.T) {
	t.Run("negative size", func(t *testing.T) {
		// Craft a tar header with negative size by patching a valid header's size field or raw header.
		// archive/tar rejects writing negative Size directly, so craft a 512-byte ustar header with "-00000000001".
		var raw bytes.Buffer
		var hdr [512]byte
		copy(hdr[0:], "evil.txt")
		copy(hdr[100:], "0000644\x00")
		copy(hdr[108:], "0000000\x00")
		copy(hdr[116:], "0000000\x00")
		copy(hdr[124:], "-0000000001\x00")
		copy(hdr[136:], "00000000000\x00")
		hdr[156] = '0'
		copy(hdr[257:], "ustar\x00")
		copy(hdr[263:], "00")
		copy(hdr[148:], "        ")
		var sum int
		for _, b := range hdr {
			sum += int(b)
		}
		copy(hdr[148:], []byte(strings.ReplaceAll(
			func() string {
				return string([]byte{'0' + byte((sum>>15)&7), '0' + byte((sum>>12)&7), '0' + byte((sum>>9)&7), '0' + byte((sum>>6)&7), '0' + byte((sum>>3)&7), '0' + byte(sum&7), 0, ' '})
			}(),
			"", "")))
		gz := gzip.NewWriter(&raw)
		if _, err := gz.Write(hdr[:]); err != nil {
			t.Fatal(err)
		}
		var zero [1024]byte
		if _, err := gz.Write(zero[:]); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}

		if err := untarLimit(&raw, t.TempDir(), 1024); err == nil {
			t.Fatal("expected error for negative tar entry size")
		}
	})

	t.Run("cumulative limit across files", func(t *testing.T) {
		data := archive(t, map[string]string{
			"a.txt": strings.Repeat("a", 40),
			"b.txt": strings.Repeat("b", 40),
			"c.txt": strings.Repeat("c", 40),
		})
		if err := untarLimit(bytes.NewReader(data), t.TempDir(), 100); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("expected too large error for cumulative 120 > 100, got %v", err)
		}
	})
}
