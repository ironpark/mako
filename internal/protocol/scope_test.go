package protocol

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestFolderScopeRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		node     string
		wantNode string
		path     string
	}{
		{name: "empty node defaults to local", node: "", wantNode: LocalScopeNode, path: "/Users/alice/projects/mako"},
		{name: "explicit local node", node: "local", wantNode: "local", path: "/tmp/repo"},
		{name: "peer node id", node: "node-123_abc.def", wantNode: "node-123_abc.def", path: "/home/bob/work/app"},
		{name: "spaces and unicode in path", node: "peer1", wantNode: "peer1", path: "/Users/철수/My Projects/repo (1)"},
		{name: "windows path", node: "win-box", wantNode: "win-box", path: `C:\Users\dev\code\mako`},
		{name: "path with colons and at", node: "n1", wantNode: "n1", path: "/var/data/a:b@c/d"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := FolderScope(tc.node, tc.path)
			if key == "" {
				t.Fatalf("FolderScope(%q, %q) returned empty key", tc.node, tc.path)
			}
			if !strings.HasPrefix(key, ScopeFolder+":"+tc.wantNode+"/") {
				t.Fatalf("unexpected key prefix: %q", key)
			}
			if strings.Contains(key, "=") {
				t.Fatalf("key must not contain base64 padding: %q", key)
			}
			if strings.Contains(key, "..") {
				t.Fatalf("key must not contain ..: %q", key)
			}
			gotNode, gotPath, ok := ParseFolderScope(key)
			if !ok {
				t.Fatalf("ParseFolderScope(%q) failed", key)
			}
			if gotNode != tc.wantNode || gotPath != tc.path {
				t.Fatalf("ParseFolderScope(%q) = (%q, %q), want (%q, %q)", key, gotNode, gotPath, tc.wantNode, tc.path)
			}
			if again := FolderScope(gotNode, gotPath); again != key {
				t.Fatalf("re-encoding mismatch: %q != %q", again, key)
			}
		})
	}
}

func TestFolderScopeInvalidInputs(t *testing.T) {
	if got := FolderScope("local", ""); got != "" {
		t.Fatalf("expected empty key for empty path, got %q", got)
	}
	if got := FolderScope("", ""); got != "" {
		t.Fatalf("expected empty key for empty node and path, got %q", got)
	}
	if got := FolderScope("local", "/bad\x00path"); got != "" {
		t.Fatalf("expected empty key for NUL in path, got %q", got)
	}
	if got := FolderScope("local", string([]byte{0xff, 0xfe})); got != "" {
		t.Fatalf("expected empty key for invalid UTF-8 path, got %q", got)
	}
	if got := FolderScope("bad/node", "/tmp/repo"); got != "" {
		t.Fatalf("expected empty key for slash in node, got %q", got)
	}
	if got := FolderScope("bad..node", "/tmp/repo"); got != "" {
		t.Fatalf("expected empty key for .. in node, got %q", got)
	}
	if got := FolderScope("-leading-dash", "/tmp/repo"); got != "" {
		t.Fatalf("expected empty key for leading dash in node, got %q", got)
	}
}

func TestParseFolderScopeRejectsMalformed(t *testing.T) {
	padded := ScopeFolder + ":local/" + base64.URLEncoding.EncodeToString([]byte("/a"))
	nulEnc := ScopeFolder + ":local/" + base64.RawURLEncoding.EncodeToString([]byte("/a\x00b"))
	badUTF8 := ScopeFolder + ":local/" + base64.RawURLEncoding.EncodeToString([]byte{0xff, 0xfe})
	validEnc := base64.RawURLEncoding.EncodeToString([]byte("/repo"))

	bad := []string{
		"",
		"project:foo",
		"folder:",
		"folder:local",
		"folder:local/",
		"folder:/" + validEnc,
		"folder:../" + validEnc,
		"folder:a..b/" + validEnc,
		"folder:local/" + validEnc + "/extra",
		"folder:local/not!valid*base64",
		"folder:local/a", // invalid base64 length
		padded,
		nulEnc,
		badUTF8,
	}
	for _, k := range bad {
		if node, path, ok := ParseFolderScope(k); ok {
			t.Errorf("ParseFolderScope(%q) unexpectedly succeeded: (%q, %q)", k, node, path)
		}
	}
}
