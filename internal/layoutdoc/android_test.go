package layoutdoc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Android app reads a computer's window layout documents with a port
// of Parse and Pages (app/android, workspace/WindowLayout.kt), to list
// the blocks it shows a page each; its unit tests check the port against
// the fixture this test writes: documents, whether Parse refuses each,
// and the pages a phone shows for it, the default document's where it is
// refused, as a window draws the default. Run with
// MAKO_ANDROID_FIXTURES=1 to write it again after a change here;
// otherwise the test fails while the app's fixture is not what this
// package makes.

const androidFixtures = "../../app/android/app/src/test/resources/blocks"

// pageCases are documents, by name, "" for none.
var pageCases = []struct {
	name, doc string
	refused   bool
}{
	{"none", "", false},
	{"default", mustDefault(), false},
	{"plugins in a row, a column and a stack", `{"version":1,"root":{"id":"w","row":[
		{"block":"mako.sidebar","size":260},
		{"id":"c","column":[{"id":"h","block":"mako.header"},{"panes":"main","grow":1},
			{"id":"ci","block":"plugin:ci-status/ci","size":180,"collapsible":true,"props":{"branch":"main"}}],"grow":1},
		{"id":"right","stack":["mako.git","plugin:notes/board","agent:abc/form"],"size":320,"active":2}]}}`, false},
	{"a compact root", withCompact, false},
	{"a compact root holding the panes", `{"version":2,"root":{"id":"w","row":[{"panes":"main"},"plugin:a/one"]},
		"variants":{"compact":{"id":"c","column":[{"panes":"main"},"plugin:a/two",{"id":"s","stack":["plugin:a/three","mako.forge"]}]}}}`, false},
	{"version 2 without variants", `{"version":2,"root":{"id":"w","row":[{"panes":"main"},"plugin:a/one"]},"variants":{}}`, false},
	{"a compact root of null", `{"version":2,"root":{"id":"w","row":[{"panes":"main"},"plugin:a/one"]},"variants":{"compact":null}}`, false},
	{"a later version", `{"version":3,"layers":[1],"variants":{"wide":{"id":"x","grid":[]}},"root":{"id":"w","column":[
		{"id":"top","row":[{"id":"chart","canvas":{"ops":[]},"size":200},"plugin:a/one",{"id":"h","block":"mako.header","pinned":true}]},
		{"panes":"main"},{"id":"s","stack":["plugin:b/two","plugin:a/one2"],"active":1}]}}`, false},
	{"a later version's compact root", `{"version":3,"root":{"id":"w","row":[{"panes":"main"}]},
		"variants":{"compact":{"id":"c","column":["plugin:a/one",{"id":"g","grid":[]},"plugin:a/two"]}}}`, false},
	{"a later version's compact root that does not read", `{"version":3,"root":{"id":"w","row":[{"panes":"main"},"plugin:a/one"]},
		"variants":{"compact":{}}}`, false},
	{"a later version's compact root that does not check", `{"version":3,"root":{"id":"w","row":[{"panes":"main"},"plugin:a/one"]},
		"variants":{"compact":{"id":"c","column":["plugin:a/two","plugin:a/two"]}}}`, false},
	{"a later version's root that does not check", `{"version":3,"root":{"id":"w","row":[{"panes":"main"},{"id":"e","row":[]}]}}`, true},
	{"blocks named twice by other ids", `{"version":1,"root":{"id":"w","row":[{"panes":"main"},{"id":"a","block":"plugin:x/y"},{"id":"b","block":"plugin:x/y"}]}}`, false},
	{"not JSON", `{"version":1,`, true},
	{"two values", `{"version":1,"root":{"panes":"main"}}{}`, true},
	{"an array", `[1]`, true},
	{"no version", `{"root":{"panes":"main"}}`, true},
	{"version 0", `{"version":0,"root":{"panes":"main"}}`, true},
	{"a version of a string", `{"version":"1","root":{"panes":"main"}}`, true},
	{"no root", `{"version":1}`, true},
	{"variants at version 1", `{"version":1,"root":{"panes":"main"},"variants":{"compact":"plugin:a/one"}}`, true},
	{"a variant unknown", `{"version":2,"root":{"panes":"main"},"variants":{"wide":"plugin:a/one"}}`, true},
	{"variants of a list", `{"version":2,"root":{"panes":"main"},"variants":[]}`, true},
	{"a field unknown at the top", `{"version":2,"root":{"panes":"main"},"layers":[]}`, true},
	{"a kind unknown", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"id":"g","grid":[]}]}}`, true},
	{"two kinds", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"id":"x","row":["plugin:a/b"],"column":["plugin:a/c"]}]}}`, true},
	{"a field unknown", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"block":"plugin:a/b","width":3}]}}`, true},
	{"a field of another kind", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"block":"plugin:a/b","active":1}]}}`, true},
	{"a size of a string", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"block":"plugin:a/b","size":"big"}]}}`, true},
	{"a size of 0", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"block":"plugin:a/b","size":0}]}}`, true},
	{"a size too large", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"block":"plugin:a/b","size":99999}]}}`, true},
	{"a size and grow", `{"version":1,"root":{"id":"r","row":[{"panes":"main","grow":1,"size":3}]}}`, true},
	{"collapsible without a size", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"block":"plugin:a/b","collapsible":true}]}}`, true},
	{"an id of a number", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"id":3,"block":"plugin:a/b"}]}}`, true},
	{"a row without an id", `{"version":1,"root":{"row":[{"panes":"main"}]}}`, true},
	{"an id of a space", `{"version":1,"root":{"id":"a b","row":[{"panes":"main"}]}}`, true},
	{"an id twice", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},"plugin:a/b","plugin:a/b"]}}`, true},
	{"no panes", `{"version":1,"root":{"id":"r","row":["plugin:a/b"]}}`, true},
	{"two panes", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"id":"p2","panes":"main"}]}}`, true},
	{"panes of another name", `{"version":1,"root":{"id":"r","row":[{"panes":"side"}]}}`, true},
	{"an empty row", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"id":"e","row":[]}]}}`, true},
	{"children not a list", `{"version":1,"root":{"id":"r","row":{"panes":"main"}}}`, true},
	{"a stack of a row", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"id":"s","stack":[{"id":"x","row":["plugin:a/b"]}]}]}}`, true},
	{"a sized child of a stack", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"id":"s","stack":[{"block":"plugin:a/b","size":3}]}]}}`, true},
	{"active out of the stack", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"id":"s","stack":["plugin:a/b"],"active":1}]}}`, true},
	{"a block of no namespace", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},"git"]}}`, true},
	{"a node of a number", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},4]}}`, true},
	{"props a window's block does not take", `{"version":1,"root":{"id":"r","row":[{"panes":"main"},{"block":"mako.git","props":{"root":"/"}}]}}`, true},
	{"a sized root", `{"version":1,"root":{"panes":"main","size":3}}`, true},
	{"too deep", `{"version":1,"root":` + strings.Repeat(`{"id":"x","row":[`, 20) + `{"panes":"main"}` + strings.Repeat(`]}`, 20) + `}`, true},
	{"too many nodes", `{"version":1,"root":{"id":"r","row":[{"panes":"main"}` + manyBlocks(MaxNodes) + `]}}`, true},
	{"two panes in the compact root", `{"version":2,"root":{"id":"w","row":[{"panes":"main"}]},"variants":{"compact":{"id":"c","column":[{"panes":"main"},{"id":"p2","panes":"main"}]}}}`, true},
	{"a compact root of no kind", `{"version":2,"root":{"id":"w","row":[{"panes":"main"}]},"variants":{"compact":{}}}`, true},
	{"an id twice in the compact root", `{"version":2,"root":{"id":"w","row":[{"panes":"main"}]},"variants":{"compact":{"id":"c","column":["plugin:a/b","plugin:a/b"]}}}`, true},
	{"too many nodes in two roots", `{"version":2,"root":{"id":"w","row":[{"panes":"main"}` + manyBlocks(MaxNodes/2) + `]},"variants":{"compact":{"id":"c","column":["plugin:a/x"` + manyBlocks(MaxNodes/2) + `]}}}`, true},
}

func mustDefault() string {
	data, _ := json.Marshal(Default())
	return string(data)
}

// manyBlocks is n blocks of their own names, each after a comma.
func manyBlocks(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(`,"plugin:many/b`)
		b.WriteString(strings.Repeat("x", i/26))
		b.WriteByte(byte('a' + i%26))
		b.WriteByte('"')
	}
	return b.String()
}

// TestPagesFixtureForAndroid writes, or checks, the app's fixture of the
// pages of documents.
func TestPagesFixtureForAndroid(t *testing.T) {
	type page struct {
		ID    string `json:"id"`
		Block string `json:"block"`
	}
	type fixture struct {
		Name    string `json:"name"`
		Doc     string `json:"doc"`
		Refused bool   `json:"refused"`
		Pages   []page `json:"pages"`
	}
	var out []fixture
	for _, c := range pageCases {
		f := fixture{Name: c.name, Doc: c.doc, Pages: []page{}}
		d := Default()
		if c.doc != "" {
			read, err := Parse([]byte(c.doc), BuiltinCatalog())
			if err == nil {
				d = read
			}
			f.Refused = err != nil
		}
		if f.Refused != c.refused {
			t.Errorf("%s: refused %v", c.name, f.Refused)
		}
		for _, p := range Pages(d) {
			f.Pages = append(f.Pages, page{ID: p.ID, Block: p.Block})
		}
		out = append(out, f)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	androidFixture(t, "pages.json", append(data, '\n'))
}

// androidFixture writes data as the fixture name, or checks that the one
// the app has is data.
func androidFixture(t *testing.T, name string, data []byte) {
	t.Helper()
	path := filepath.Join(androidFixtures, name)
	if os.Getenv("MAKO_ANDROID_FIXTURES") != "" {
		if err := os.MkdirAll(androidFixtures, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if have, err := os.ReadFile(path); err != nil || !bytes.Equal(have, data) {
		t.Errorf("the app's fixture %s is not what this package makes (%v): run MAKO_ANDROID_FIXTURES=1 go test ./internal/uiblock ./internal/layoutdoc -run Android", name, err)
	}
}
