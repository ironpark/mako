package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/config"
)

// The default document, written out, reads back as itself, in the form
// docs/protocol.md shows.
func TestTheDefaultDocumentReadsBackAsWritten(t *testing.T) {
	data, err := json.Marshal(defaultWindowDoc())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"root":{"id":"window","row":[` +
		`{"id":"sidebar","block":"mako.sidebar","size":272,"collapsible":true},` +
		`{"id":"workspace","column":[{"id":"header","block":"mako.header"},{"id":"main","panes":"main","grow":1}],"grow":1},` +
		`{"id":"details","stack":["mako.files","mako.git","mako.agents","mako.forge"],"size":320,"collapsible":true}]}}`
	if string(data) != want {
		t.Fatalf("default document:\n%s\nwant\n%s", data, want)
	}
	d, err := parseWindowDoc(data, builtinBlocks())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d, defaultWindowDoc()) {
		t.Fatalf("read back as %s", mustJSON(t, d))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The plan's example, a plugin's block under the panes and a stack of the
// right panel's tabs and a plugin's, reads: blocks no registry holds are
// kept, to be drawn as missing.
func TestADocumentOfBlocksNotRegisteredReads(t *testing.T) {
	src := `{
	  "version": 1,
	  "root": { "id": "w", "row": [
	    { "block": "mako.sidebar", "size": 260 },
	    { "id": "c", "column": [
	      { "panes": "main", "grow": 1 },
	      { "id": "ci", "block": "plugin:ci-status/ci", "size": 180, "collapsible": true, "props": {"branch": "main"} }
	    ], "grow": 1 },
	    { "id": "right", "stack": ["mako.git", "mako.files", "plugin:notes/board"], "size": 320, "active": 2 }
	  ]}
	}`
	d, err := parseWindowDoc([]byte(src), builtinBlocks())
	if err != nil {
		t.Fatal(err)
	}
	if n := d.node("ci"); n == nil || n.Block != "plugin:ci-status/ci" || n.Props["branch"] != "main" || !n.Collapsible {
		t.Fatalf("ci: %+v", n)
	}
	if n := d.node("mako.sidebar"); n == nil || n.Size != 260 {
		t.Fatalf("a block without an id is not known by its name: %+v", n)
	}
	if n := d.node("right"); n.activeBlock() != "plugin:notes/board" {
		t.Fatalf("right shows %q", n.activeBlock())
	}
}

// What the window could not draw, or does not know, is refused, with why.
func TestMalformedDocumentsAreRefused(t *testing.T) {
	row := func(kids string) string { return `{"version":1,"root":{"id":"r","row":[` + kids + `]}}` }
	panes := `{"panes":"main","grow":1}`
	for _, c := range []struct{ name, src, why string }{
		{"not JSON", `{"version":1,`, "unexpected EOF"},
		{"two values", row(panes) + `{}`, "more than one"},
		{"no version", `{"root":{"panes":"main"}}`, "no version"},
		{"a later version", `{"version":2,"root":{"panes":"main"}}`, "version 2"},
		{"no root", `{"version":1}`, "no root"},
		{"a field unknown at the top", `{"version":1,"root":{"panes":"main"},"variants":{}}`, "unknown field"},
		{"a kind unknown", row(panes + `,{"id":"g","grid":[]}`), "no kind"},
		{"two kinds", row(`{"id":"x","row":[],"column":[]}`), "both"},
		{"a field unknown", row(panes + `,{"block":"mako.git","width":3}`), `no field "width"`},
		{"a field of another kind", row(panes + `,{"block":"mako.git","active":1}`), `no field "active"`},
		{"a size of a string", row(panes + `,{"block":"mako.git","size":"big"}`), "cannot unmarshal"},
		{"a size of 0", row(panes + `,{"block":"mako.git","size":0}`), "not above 0"},
		{"a size too large", row(panes + `,{"block":"mako.git","size":99999}`), "not within"},
		{"a size and grow", row(`{"panes":"main","grow":1,"size":3}`), "both a size and grow"},
		{"collapsible without a size", row(panes + `,{"block":"mako.git","collapsible":true}`), "without a size"},
		{"a row without an id", `{"version":1,"root":{"row":[` + panes + `]}}`, "no id"},
		{"an id twice", row(panes + `,"mako.git","mako.git"`), "two nodes"},
		{"no panes", row(`"mako.git"`), "0 panes"},
		{"two panes", row(panes + `,{"id":"p2","panes":"main"}`), "2 panes"},
		{"panes of another name", row(`{"panes":"side"}`), `panes "side"`},
		{"an empty row", row(panes + `,{"id":"e","row":[]}`), "no children"},
		{"a stack of a row", row(panes + `,{"id":"s","stack":[{"id":"x","row":["mako.git"]}]}`), "blocks alone"},
		{"a sized child of a stack", row(panes + `,{"id":"s","stack":[{"block":"mako.git","size":3}]}`), "has a size"},
		{"active out of the stack", row(panes + `,{"id":"s","stack":["mako.git"],"active":1}`), "active 1"},
		{"a block of no namespace", row(panes + `,"git"`), "is not mako.<name>"},
		{"a node of a number", row(panes + `,4`), "neither an object"},
		{"props a block does not take", row(panes + `,{"block":"mako.git","props":{"root":"/"}}`), `prop "root"`},
		{"a sized root", `{"version":1,"root":{"panes":"main","size":3}}`, "the root"},
		{"too deep", `{"version":1,"root":` + strings.Repeat(`{"id":"x","row":[`, 20) + panes + strings.Repeat(`]}`, 20) + `}`, "deeper"},
	} {
		_, err := parseWindowDoc([]byte(c.src), builtinBlocks())
		if err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: error %v, want one saying %q", c.name, err, c.why)
		}
	}
}

// layout.json is read when there is one: the default without one, and for
// one that does not read, the default and why.
func TestTheLayoutFileFallsBackToTheDefault(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	reg := builtinBlocks()
	d, err := loadWindowDoc(reg)
	if err != nil || !reflect.DeepEqual(d, defaultWindowDoc()) {
		t.Fatalf("no file: %v, %s", err, mustJSON(t, d))
	}
	dir, _ := config.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, layoutFileName)
	if err := os.WriteFile(path, []byte(`{"version":1,"root":{"id":"r","row":[{"panes":"main","grow":1},"mako.git"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err = loadWindowDoc(reg)
	if err == nil || !strings.Contains(err.Error(), layoutFileName) || !reflect.DeepEqual(d, defaultWindowDoc()) {
		t.Fatalf("a broken file: %v, %s", err, mustJSON(t, d))
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"root":{"id":"r","row":[{"panes":"main","grow":1},"mako.git"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err = loadWindowDoc(reg)
	if err != nil || d.node("mako.git") == nil || d.node(sidebarNode) != nil {
		t.Fatalf("a good file: %v, %s", err, mustJSON(t, d))
	}
}
