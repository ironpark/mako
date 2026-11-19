package gui

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A project's panels show each of its folders: the session's own as it
// works in it, the others on its node, else on a node the window reaches,
// named with that node; a lone folder shows alone.
func TestPanelsShowTheProjectsFolders(t *testing.T) {
	defs := []project{{ID: "product", Name: "Product", Roots: []projectRoot{
		{Label: "server", Locations: []fileRoot{{"mac", "/src/server"}}},
		{Locations: []fileRoot{{"box", "/home/me/web"}}},
		{Locations: []fileRoot{{"far", "/docs"}}},
	}}}
	nodes := []*protocol.Node{{Id: "mac", Name: "Mac"}, {Id: "box", Name: "Box"}}
	reach := func(id string) bool { return id != "far" }
	own := fileRoot{"mac", "/src/server/api"}
	got := projectRoots(defs, nodes, own, placeIn(defs, own.Node, own.Path, "", ""), reach)
	want := panelRoots{Name: "Product", Own: 0, Roots: []panelRoot{
		{own, "server"},
		{fileRoot{"box", "/home/me/web"}, "web · Box"},
	}}
	if !deepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	lone := fileRoot{"mac", "/elsewhere"}
	if got := projectRoots(defs, nodes, lone, placeIn(defs, lone.Node, lone.Path, "", ""), reach); got.multi() || got.Roots[0].fileRoot != lone {
		t.Fatalf("a lone folder: %+v", got)
	}
}

// A folder shows once though it is placed at one root and is another's:
// the Files tree keys its rows by folder, and a folder twice crashes it.
func TestPanelsShowAFolderOnce(t *testing.T) {
	defs := []project{{ID: "gg", Name: "GG", Roots: []projectRoot{
		{Locations: []fileRoot{{"mac", "/src/ggui"}}},
		{Locations: []fileRoot{{"mac", "/src/mako"}}},
		{Locations: []fileRoot{{"mac", "/src/ggui"}}},
	}}}
	reach := func(string) bool { return true }
	own := fileRoot{"mac", "/src/ggui"}
	// Placed at mako's root, as a session's placement would, it still
	// shows only once.
	got := projectRoots(defs, nil, own, placement{At: 0, Root: 1}, reach)
	seen := map[fileRoot]bool{}
	for _, r := range got.Roots {
		if seen[r.fileRoot] {
			t.Fatalf("%v shows twice: %+v", r.fileRoot, got.Roots)
		}
		seen[r.fileRoot] = true
	}
	if got.own() != own {
		t.Fatalf("own is %+v", got.own())
	}
}
