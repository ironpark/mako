package layoutdoc

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The default document, written out, reads back as itself, in the form
// docs/protocol.md shows.
func TestTheDefaultDocumentReadsBackAsWritten(t *testing.T) {
	data, err := json.Marshal(Default())
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
	d, err := Parse(data, BuiltinCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d, Default()) {
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
	d, err := Parse([]byte(src), BuiltinCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if n := d.Node("ci"); n == nil || n.Block != "plugin:ci-status/ci" || n.Props["branch"] != "main" || !n.Collapsible {
		t.Fatalf("ci: %+v", n)
	}
	if n := d.Node("mako.sidebar"); n == nil || n.Size != 260 {
		t.Fatalf("a block without an id is not known by its name: %+v", n)
	}
	if n := d.Node("right"); n.ActiveBlock() != "plugin:notes/board" {
		t.Fatalf("right shows %q", n.ActiveBlock())
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
		{"version 0", `{"version":0,"root":{"panes":"main"}}`, "not a version"},
		{"a version of a string", `{"version":"1","root":{"panes":"main"}}`, "not a version"},
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
		_, err := Parse([]byte(c.src), BuiltinCatalog())
		if err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: error %v, want one saying %q", c.name, err, c.why)
		}
	}
}

// A document of a later version is read leniently: what this version does
// not know is left out, a node of a kind it does not know kept as a leaf;
// what it knows is checked as strictly, and it is not patched here. The
// same document at this version is refused.
func TestALaterVersionIsReadLeniently(t *testing.T) {
	later := `{"version":3,"variants":{"compact":{},"wide":{}},"layers":[],"root":{"id":"w","row":[
		{"id":"sidebar","block":"mako.sidebar","size":272,"pinned":true},
		{"id":"chart","canvas":{"ops":[]},"size":200},
		{"panes":"main","grow":1}]}}`
	d, err := Parse([]byte(later), BuiltinCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if n := d.Node("chart"); n == nil || n.Kind != "canvas" || Known(n.Kind) || n.Size != 200 {
		t.Fatalf("the node of a later kind: %+v", n)
	}
	if _, err := Apply(d, []Op{{Op: "remove", ID: "chart"}}, BuiltinCatalog()); err == nil {
		t.Fatal("a later version was patched")
	}
	canon, _, err := Canonical([]byte(later), BuiltinCatalog())
	if err != nil || !strings.Contains(string(canon), `"variants"`) {
		t.Fatalf("a later version was not kept as it came: %s, %v", canon, err)
	}
	if d.Compact != nil {
		t.Fatalf("a compact variant this version cannot read was kept: %+v", d.Compact)
	}
	for _, v := range []string{`"version":1`, `"version":2`} {
		if _, err := Parse([]byte(strings.Replace(later, `"version":3`, v, 1)), BuiltinCatalog()); err == nil {
			t.Fatalf("unknown fields read at %s", v)
		}
	}
	// What a later version is read for is still checked.
	broken := `{"version":3,"root":{"id":"w","row":[{"id":"x","stack":[]},{"panes":"main"}]}}`
	if _, err := Parse([]byte(broken), BuiltinCatalog()); err == nil || !strings.Contains(err.Error(), "no children") {
		t.Fatalf("a later version's broken tree: %v", err)
	}
}

// Block names are in one of three namespaces.
func TestBlockNamesAreInTheirNamespaces(t *testing.T) {
	for _, name := range []string{"mako.git", "plugin:ci-status/ci", "agent:822d51f133b0614b/board"} {
		if err := CheckBlockName(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"git", "Mako.git", "plugin:ci", "plugin:/ci", "agent:abc/", "agent:a b/c", ""} {
		if CheckBlockName(name) == nil {
			t.Errorf("%q is a block name", name)
		}
	}
}

// Props are checked against the block's schema: their types, the required
// ones, and none it does not declare unless it allows others.
func TestPropsAreCheckedAgainstTheSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session": map[string]any{"type": "string"},
			"lines":   map[string]any{"type": "integer"},
			"follow":  map[string]any{"type": "boolean"},
		},
		"required": []any{"session"},
	}
	for _, c := range []struct {
		props map[string]any
		ok    bool
	}{
		{map[string]any{"session": "a"}, true},
		{map[string]any{"session": "a", "lines": 3.0, "follow": true}, true},
		{map[string]any{"session": "a", "lines": 3.5}, false},
		{map[string]any{"session": 1.0}, false},
		{map[string]any{"lines": 3.0}, false},
		{map[string]any{"session": "a", "other": "x"}, false},
	} {
		if err := CheckProps(schema, c.props); (err == nil) != c.ok {
			t.Errorf("%v: %v", c.props, err)
		}
	}
	schema["additionalProperties"] = true
	if err := CheckProps(schema, map[string]any{"session": "a", "other": "x"}); err != nil {
		t.Errorf("others allowed: %v", err)
	}
}
