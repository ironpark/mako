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
