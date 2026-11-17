package gui

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestTabsByProjectKeepThemTogether(t *testing.T) {
	keys := map[string]string{"a": "p2", "b": "p2", "c": "p2", "d": "p1", "e": "p1"}
	key := func(id string) string { return keys[id] }
	for _, c := range []struct{ in, want []string }{
		{[]string{"d", "a", "b", "c"}, []string{"d", "a", "b", "c"}},
		{[]string{"a", "d", "b", "e", "c"}, []string{"a", "b", "c", "d", "e"}},
		{[]string{"d", "a", "e", "b"}, []string{"d", "e", "a", "b"}},
		{[]string{"x", "a", "y"}, []string{"x", "y", "a"}},
	} {
		if got := tabsByProject(c.in, key); !slices.Equal(got, c.want) {
			t.Errorf("tabsByProject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTabPlaceFollowsTheTabsProject(t *testing.T) {
	s := &protocol.SessionInfo{Id: "s1", NodeId: "n", Project: "/w/api"}
	m := &protocol.SessionInfo{Id: "s2", NodeId: "n", Project: "/w/two/b"}
	merged := project{ID: "merged", Roots: []projectRoot{
		{Locations: []fileRoot{{Node: "n", Path: "/w/two/a"}}},
		{Locations: []fileRoot{{Node: "n", Path: "/w/two/b"}}},
	}}
	groups := []sessionGroup{
		{Key: "web", Roots: []fileRoot{{Node: "n", Path: "/w/web"}}},
		{Key: "api", Sessions: []*protocol.SessionInfo{s}, Roots: []fileRoot{{Node: "n", Path: "/w/api"}}},
		{Key: "lib", Roots: []fileRoot{{Node: "n", Path: "/w/api/lib"}}},
		{Key: "merged", Def: &merged, Sessions: []*protocol.SessionInfo{m}, RootOf: map[string]int{"s2": 1}, Roots: merged.locations()},
	}
	doc, deep, other, inA := docTabID("n", "/w/web/x.go"), docTabID("n", "/w/api/lib/y.go"), docTabID("m", "/w/web/z.go"), docTabID("n", "/w/two/a/r.md")
	// A file is of the folder holding it, whichever it was opened from.
	opened := fileRoot{Node: "n", Path: "/w/api"}
	docs := map[string]docInfo{
		doc:   {Node: "n", Path: "/w/web/x.go", Root: opened},
		deep:  {Node: "n", Path: "/w/api/lib/y.go", Root: opened},
		other: {Node: "m", Path: "/w/web/z.go", Root: opened},
		inA:   {Node: "n", Path: "/w/two/a/r.md", Root: opened},
	}
	type place struct{ section, root int }
	for id, want := range map[string]place{"s1": {1, -1}, doc: {0, -1}, deep: {2, -1}, other: {-1, -1}, "gone": {-1, -1}, "s2": {3, 1}, inA: {3, 0}} {
		if s, r := tabPlace(id, groups, docs, nil); s != want.section || r != want.root {
			t.Errorf("tabPlace(%q) = %d, %d, want %d, %d", id, s, r, want.section, want.root)
		}
	}
}

func TestRootColorsTurnPastTheGrey(t *testing.T) {
	seen := map[string]bool{}
	for root := range len(lookColors) - 1 {
		c := rootColor("teal", root)
		if c == "gray" || seen[c] {
			t.Fatalf("root %d is %s, after %v", root, c, seen)
		}
		seen[c] = true
	}
	if rootColor("teal", 0) != "teal" {
		t.Fatal("the first folder is not in the project's colour")
	}
}
