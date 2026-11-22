package gui

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A project's panels show each of its folders: a session in a subfolder
// resolves back to its root's folder (so sibling files stay visible under the
// root's label), while an external worktree keeps its own folder; the other
// roots show on own's node, else on a node the window reaches, named with that
// node; a lone folder shows alone.
func TestPanelsShowTheProjectsFolders(t *testing.T) {
	defs := []project{
		{ID: "product", Name: "Product", Roots: []projectRoot{
			{Label: "server", Locations: []fileRoot{{"mac", "/src/server"}}},
			{Locations: []fileRoot{{"box", "/home/me/web"}}},
			{Locations: []fileRoot{{"far", "/docs"}}},
		}},
		{ID: "solo", Name: "Solo", Roots: []projectRoot{
			{Locations: []fileRoot{{"mac", "/src/solo"}}},
		}},
	}
	nodes := []*protocol.Node{{Id: "mac", Name: "Mac"}, {Id: "box", Name: "Box"}}
	reach := func(id string) bool { return id != "far" }

	// Subfolder inside a multi-root project's root resolves to the root folder.
	own := fileRoot{"mac", "/src/server/api"}
	got := projectRoots(defs, nodes, own, placeIn(defs, own.Node, own.Path, "", ""), reach)
	want := panelRoots{Name: "Product", Own: 0, Roots: []panelRoot{
		{fileRoot{"mac", "/src/server"}, "server"},
		{fileRoot{"box", "/home/me/web"}, "web · Box"},
	}}
	if !deepEqual(got, want) {
		t.Fatalf("subfolder got %+v, want %+v", got, want)
	}

	// External git worktree placed at the root via its repo keeps its own worktree folder.
	wt := fileRoot{"mac", "/src/server-wt"}
	gotWT := projectRoots(defs, nodes, wt, placeIn(defs, wt.Node, wt.Path, "/src/server", ""), reach)
	wantWT := panelRoots{Name: "Product", Own: 0, Roots: []panelRoot{
		{wt, "server"},
		{fileRoot{"box", "/home/me/web"}, "web · Box"},
	}}
	if !deepEqual(gotWT, wantWT) {
		t.Fatalf("worktree got %+v, want %+v", gotWT, wantWT)
	}

	// Single-root project with a subfolder session also resolves to its root folder.
	soloSub := fileRoot{"mac", "/src/solo/.github/workflows"}
	if got := projectRoots(defs, nodes, soloSub, placeIn(defs, soloSub.Node, soloSub.Path, "", ""), reach); got.multi() || got.Roots[0].fileRoot != (fileRoot{"mac", "/src/solo"}) {
		t.Fatalf("single-root subfolder: %+v", got)
	}

	// Standalone folder outside any project stays itself.
	lone := fileRoot{"mac", "/elsewhere"}
	if got := projectRoots(defs, nodes, lone, placeIn(defs, lone.Node, lone.Path, "", ""), reach); got.multi() || got.Roots[0].fileRoot != lone {
		t.Fatalf("a lone folder: %+v", got)
	}

	// sessionProjectRoot lifts repo subfolders to the repo root while leaving worktrees intact.
	if p := sessionProjectRoot(&protocol.SessionInfo{Project: "/src/mako/.github/workflows", Repo: "/src/mako"}); p != "/src/mako" {
		t.Fatalf("sessionProjectRoot(subfolder) = %q, want /src/mako", p)
	}
	if p := sessionProjectRoot(&protocol.SessionInfo{Project: "/tmp/mako-wt", Repo: "/src/mako"}); p != "/tmp/mako-wt" {
		t.Fatalf("sessionProjectRoot(worktree) = %q, want /tmp/mako-wt", p)
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
