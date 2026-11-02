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
