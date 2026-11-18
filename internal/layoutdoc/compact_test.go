package layoutdoc

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// withCompact is a document of version 2 whose compact root puts a
// plugin's block before the git panel, and leaves the rest out.
const withCompact = `{"version":2,"root":{"id":"window","row":[
	{"id":"sidebar","block":"mako.sidebar","size":272,"collapsible":true},
	{"id":"main","panes":"main","grow":1},
	{"id":"details","stack":["mako.files","plugin:ci-status/ci"],"size":320,"collapsible":true}]},
	"variants":{"compact":{"id":"phone","column":[{"id":"ci","block":"plugin:ci-status/ci","props":{"branch":"main"}},"mako.git"]}}}`

// A document with a compact root reads at version 2, is written at it,
// and patched in its root alone; one without is written at version 1,
// as a window of the version before reads it strictly.
func TestADocumentWithVariantsIsOfVersion2(t *testing.T) {
	d, err := Parse([]byte(withCompact), BuiltinCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if d.Compact == nil || d.Compact.ID != "phone" || d.Written() != 2 {
		t.Fatalf("compact %+v, written at %d", d.Compact, d.Written())
	}
	canon, _, err := Canonical([]byte(withCompact), BuiltinCatalog())
	if err != nil || !strings.HasPrefix(string(canon), `{"version":2,`) || !strings.Contains(string(canon), `"variants":{"compact":{"id":"phone"`) {
		t.Fatalf("%s %v", canon, err)
	}
	again, err := Parse(canon, BuiltinCatalog())
	if err != nil || !reflect.DeepEqual(again, d) {
		t.Fatalf("read back: %v", err)
	}
	// A patch changes the root; the compact root stays as it was.
	next, err := Apply(d, []Op{{Op: "remove", ID: "sidebar"}}, BuiltinCatalog())
	if err != nil || next.Node("sidebar") != nil || !reflect.DeepEqual(next.Compact, d.Compact) || d.Node("sidebar") == nil {
		t.Fatalf("patched: %v", err)
	}
	// Without its variants, it is written at version 1.
	d.Compact = nil
	if out := mustJSON(t, d); !strings.HasPrefix(out, `{"version":1,`) || strings.Contains(out, "variants") {
		t.Fatalf("%s", out)
	}
	plain := `{"version":2,"root":{"id":"w","row":[{"panes":"main"}]},"variants":{}}`
	if canon, _, err := Canonical([]byte(plain), BuiltinCatalog()); err != nil || string(canon) != `{"version":1,"root":{"id":"w","row":[{"id":"main","panes":"main"}]}}` {
		t.Fatalf("%s %v", canon, err)
	}
}

// A window reads a document of a version later than its own leniently:
// the variants it does not know left out, it draws the root, which it
// checks as strictly as ever. A window of version 1 reads withCompact so
// (TestALaterVersionIsReadLeniently is that reading, which a build of
// version 1 has); here, this build reads it at a version later than its
// own, and draws the same root as it does reading it at its own.
func TestALaterWindowsVariantsLeaveTheRootDrawn(t *testing.T) {
	later := strings.Replace(withCompact, `"version":2`, `"version":3`, 1)
	later = strings.Replace(later, `"variants":{`, `"variants":{"wide":{"id":"x","grid":[]},`, 1)
	d, err := Parse([]byte(later), BuiltinCatalog())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Parse([]byte(withCompact), BuiltinCatalog())
	if !reflect.DeepEqual(d.Root, want.Root) || !reflect.DeepEqual(d.Compact, want.Compact) {
		t.Fatalf("read leniently: %s", mustJSON(t, d))
	}
}

// What a compact root may hold is what a root may, but for the panes,
// which it may leave out; its IDs are its own.
func TestACompactRootIsChecked(t *testing.T) {
	doc := func(compact string) string {
		return `{"version":2,"root":{"id":"w","row":[{"panes":"main"},"mako.git"]},"variants":{"compact":` + compact + `}}`
	}
	for _, ok := range []string{
		`"mako.git"`,
		`{"id":"c","column":["mako.git",{"panes":"main"}]}`,
		`{"id":"w","stack":["mako.git","plugin:notes/board"]}`, // the root's IDs again
		`null`,
	} {
		if _, err := Parse([]byte(doc(ok)), BuiltinCatalog()); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, c := range []struct{ compact, why string }{
		{`{"id":"c","column":[{"panes":"main"},{"id":"p2","panes":"main"}]}`, "2 panes"},
		{`{"id":"c","column":["mako.git","mako.git"]}`, "two nodes"},
		{`{"id":"c","column":["mako.git",{"block":"mako.git","width":3}]}`, `no field "width"`},
		{`{"id":"c","grid":[]}`, "no kind"},
		{`{}`, "no kind"},
		{`{"id":"c","row":["mako.git"],"size":3}`, "the root"},
		{`{"id":"c","stack":[{"id":"r","row":["mako.git"]}]}`, "blocks alone"},
	} {
		if _, err := Parse([]byte(doc(c.compact)), BuiltinCatalog()); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v, want one saying %q", c.compact, err, c.why)
		}
	}
	for _, c := range []struct{ src, why string }{
		{`{"version":1,"root":{"panes":"main"},"variants":{"compact":"mako.git"}}`, "unknown field"},
		{`{"version":2,"root":{"panes":"main"},"variants":{"wide":"mako.git"}}`, `no variant "wide"`},
		{`{"version":2,"root":{"panes":"main"},"variants":[]}`, "not an object"},
	} {
		if _, err := Parse([]byte(c.src), BuiltinCatalog()); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v, want one saying %q", c.src, err, c.why)
		}
	}
	// The bound on nodes is the whole document's.
	var kids []string
	for i := range MaxNodes - 2 {
		kids = append(kids, `"plugin:p/b`+strconv.Itoa(i)+`"`)
	}
	half := strings.Join(kids[:MaxNodes/2], ",")
	src := `{"version":2,"root":{"id":"w","row":[{"panes":"main"},` + half + `]},"variants":{"compact":{"id":"c","column":[` + half + `]}}}`
	if _, err := Parse([]byte(src), BuiltinCatalog()); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("a document of two roots of %d nodes each: %v", MaxNodes/2+2, err)
	}
}

// The document's pages: of its compact root, or its root folded.
func TestPagesFoldTheDocument(t *testing.T) {
	ids := func(ps []Page) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.ID+"="+p.Block)
		}
		return strings.Join(out, " ")
	}
	if got := ids(Pages(Default())); got != "mako.files=mako.files mako.git=mako.git mako.agents=mako.agents mako.forge=mako.forge" {
		t.Errorf("the default's: %s", got)
	}
	d, _ := Parse([]byte(withCompact), BuiltinCatalog())
	if got := Pages(d); ids(got) != "ci=plugin:ci-status/ci mako.git=mako.git" || got[0].Props["branch"] != "main" {
		t.Errorf("the compact root's: %v", got)
	}
	d.Compact = nil
	if got := ids(Pages(d)); got != "mako.files=mako.files plugin:ci-status/ci=plugin:ci-status/ci" {
		t.Errorf("folded: %s", got)
	}
	later, err := Parse([]byte(`{"version":3,"root":{"id":"w","column":[
		{"id":"top","row":[{"id":"chart","canvas":{}},"plugin:a/one",{"id":"h","block":"mako.header"}]},
		{"panes":"main"},{"id":"s","stack":["plugin:b/two","plugin:a/one2"],"active":1}]}}`), BuiltinCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(Pages(later)); got != "plugin:a/one=plugin:a/one plugin:b/two=plugin:b/two plugin:a/one2=plugin:a/one2" {
		t.Errorf("a later version's: %s", got)
	}
	if Pages(nil) != nil {
		t.Error("pages of no document")
	}
	if _, err := json.Marshal(Pages(d)); err != nil {
		t.Fatal(err)
	}
}
