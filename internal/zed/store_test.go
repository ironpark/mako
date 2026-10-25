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
