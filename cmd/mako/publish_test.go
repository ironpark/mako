package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// plugins index lists a package with its manifest's permissions, which a
// node checks the package against, so that an honest publisher's
// listing installs.
func TestIndexListsTheManifestsPermissions(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data, pkg, err := pack.Build(map[string][]byte{
		"plugin.toml": []byte("id = \"hello\"\nversion = \"0.1.0\"\npublisher = \"test\"\napi = 1\nentry = \"main.js\"\n" +
			"permissions = [\"storage\", \"net:api.example.com\"]\n"),
		"main.js": []byte("export default { activate() {} }"),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.makoplugin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := indexPackage([]string{dir, path, "-url", "https://example.com/hello.makoplugin"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, registry.IndexName))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := registry.Parse(b, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.Find("hello").Versions[0].Permissions; !slices.Equal(got, pkg.Manifest.Permissions) {
		t.Fatalf("listed %v; the manifest asks for %v", got, pkg.Manifest.Permissions)
	}
}

func TestInitPluginScaffoldsValidManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-board")
	if err := initPlugin([]string{"my-board", dir}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := plugin.ParseManifest(b)
	if err != nil {
		t.Fatalf("scaffolded manifest invalid: %v", err)
	}
	if m.ID != "my-board" || len(m.Blocks) != 1 || len(m.Tools) != 1 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.js")); err != nil {
		t.Fatalf("missing main.js: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "jsconfig.json")); err != nil {
		t.Fatalf("missing jsconfig.json: %v", err)
	}
}

func TestParsePluginSetScopeArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantID    string
		wantKey   string
		wantValue string
		wantScope string
		wantErr   bool
	}{
		{
			name:      "no scope",
			args:      []string{"board", "title", "Sprint", "1"},
			wantID:    "board",
			wantKey:   "title",
			wantValue: "Sprint 1",
			wantScope: "",
		},
		{
			name:      "double dash scope after value",
			args:      []string{"board", "title", "Alpha", "--scope", "project:p1"},
			wantID:    "board",
			wantKey:   "title",
			wantValue: "Alpha",
			wantScope: "project:p1",
		},
		{
			name:      "single dash scope after value",
			args:      []string{"board", "title", "Alpha", "-scope", "project:p1"},
			wantID:    "board",
			wantKey:   "title",
			wantValue: "Alpha",
			wantScope: "project:p1",
		},
		{
			name:      "double dash scope before key and value",
			args:      []string{"board", "--scope", "space:sp1", "title", "Work"},
			wantID:    "board",
			wantKey:   "title",
			wantValue: "Work",
			wantScope: "space:sp1",
		},
		{
			name:      "double dash scope before plugin id",
			args:      []string{"--scope", "project:p2", "board", "title", "Beta"},
			wantID:    "board",
			wantKey:   "title",
			wantValue: "Beta",
			wantScope: "project:p2",
		},
		{
			name:      "equals syntax",
			args:      []string{"board", "token", "secret", "--scope=session:s1"},
			wantID:    "board",
			wantKey:   "token",
			wantValue: "secret",
			wantScope: "session:s1",
		},
		{
			name:      "single dash equals syntax before key",
			args:      []string{"board", "-scope=project:p3", "token"},
			wantID:    "board",
			wantKey:   "token",
			wantValue: "",
			wantScope: "project:p3",
		},
		{
			name:      "double dash terminator preserves literal flag value",
			args:      []string{"board", "flag", "--", "--scope", "literal"},
			wantID:    "board",
			wantKey:   "flag",
			wantValue: "--scope literal",
			wantScope: "",
		},
		{
			name:    "missing scope value",
			args:    []string{"board", "title", "Alpha", "--scope"},
			wantErr: true,
		},
		{
			name:    "missing key",
			args:    []string{"board", "--scope", "project:p1"},
			wantErr: true,
		},
		{
			name:    "missing plugin id",
			args:    []string{"--scope", "project:p1"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePluginSetAction(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.GetOp() != protocol.PluginSet || got.GetPluginId() != tc.wantID || got.GetKey() != tc.wantKey || got.GetValue() != tc.wantValue || got.GetScope() != tc.wantScope {
				t.Fatalf("got %+v, want id=%q key=%q value=%q scope=%q", got, tc.wantID, tc.wantKey, tc.wantValue, tc.wantScope)
			}
		})
	}
}
