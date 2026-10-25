package gui

import (
	"bytes"
	"image"
	"io/fs"
	"testing"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggfx/svg"
)

func TestFileIconName(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Dockerfile", "lang-docker"},
		{"Dockerfile.prod", "lang-docker"},
		{".dockerignore", "lang-docker"},
		{"docker-compose.yml", "lang-docker"},
		{"Podfile.lock", "lock"},
		{"main.go", "lang-go"},
		{"go.mod", "lang-go"},
		{"go.sum", "lang-go"}, // Zed: stem "go" before the first dot
		{"lib.rs", "lang-rust"},
		{"App.tsx", "lang-react"},
		{"index.ts", "lang-typescript"},
		{"eslint.config.js", "lang-eslint"},
		{".eslintrc.json", "lang-eslint"},
		{"a.stories.tsx", "lang-react"},
		{".gitignore", "lang-git"},
		{"COMMIT_EDITMSG", "lang-git"},
		{".data.json", "file-json"},
		{"photo.png", "file-image"},
		{"photo.PNG", "file-image"}, // Zed: default; we retry lowercased
		{"model.R", "lang-r"},
		{"README.md", "lang-markdown"},
		{"notes.txt", "file-text"},
		{".zshrc", "terminal"},
		{"Cargo.lock", "lock"},
		{"bun.lockb", "lang-bun"},
		{"x.bak", "file"}, // Zed's "backup" key has no icon
		{"x.json.bak", "file"},
		{"Makefile", "file"},
		{"unknown.xyz", "file"},
		{"", "file"},
	} {
		if got := fileIconName(tc.name); got != tc.want {
			t.Errorf("fileIconName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Every key Zed's tables name has an icon, but for the one its theme leaves
// out.
func TestFileIconKeysCovered(t *testing.T) {
	for _, table := range []map[string]string{zedFileStems, zedFileSuffixes} {
		for assoc, key := range table {
			if _, ok := fileIconsByKey[key]; !ok && key != "backup" {
				t.Errorf("%q -> key %q has no icon", assoc, key)
			}
		}
	}
}

// Every icon the mapping can return loads, and draws something.
func TestFileIconsLoad(t *testing.T) {
	names := []string{"folder", "folder-open"}
	for _, n := range fileIconsByKey {
		names = append(names, n)
	}
	for _, n := range names {
		_ = icon(n) // panics on a missing or unparsable icon
		data, err := fs.ReadFile(iconFiles, "icons/"+n+".svg")
		if err != nil {
			t.Fatal(err)
		}
		doc, err := svg.Parse(bytes.NewReader(bytes.ReplaceAll(data, []byte("currentColor"), []byte("#ffffff"))))
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 32, 32))
		_, _, w, _ := doc.ViewBox()
		var g ggfx.GeoM
		g.Scale(32/w, 32/w)
		doc.Draw(img, g)
		covered := 0
		for i := 3; i < len(img.Pix); i += 4 {
			if img.Pix[i] > 0 {
				covered++
			}
		}
		if covered < 20 {
			t.Errorf("%s draws only %d pixels", n, covered)
		}
	}
	for _, c := range []struct {
		dir, open bool
	}{{true, true}, {true, false}, {false, false}} {
		if fileTypeIcon("main.go", c.dir, c.open) == nil {
			t.Errorf("fileTypeIcon(dir=%v, open=%v) = nil", c.dir, c.open)
		}
	}
}
