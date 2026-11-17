package workspace

import (
	"encoding/json"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/crdt"
	"github.com/ironpark/mako/internal/protocol"
)

// docOf is a window's document of the spaces, projects and pins given.
func docOf(spaces []docSpace, projects []docProject, pinned []string) []byte {
	body, _ := json.Marshal(document{Spaces: spaces, Projects: projects, Pinned: pinned})
	return body
}

// The edit from one of a window's documents to the next is what the
// window changed, and nothing of what another node changed meanwhile.
func TestDiff(t *testing.T) {
	n := newNode("n", realTime, true)
	spaces := []docSpace{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}, {ID: "c", Name: "C"}}
	projects := []docProject{{ID: "p", Name: "P", Roots: []docRoot{{Locations: []docFolder{{"n", "/p"}}}}}}
	first := docOf(spaces, projects, []string{"t1", "t2"})
	file, _ := json.Marshal(docFile{Version: 1, NodeID: "n", Doc: document{Spaces: spaces, Projects: projects, Pinned: []string{"t1", "t2"}}})
	st, err := FromLegacy(file, n.phys)
	if err != nil {
		t.Fatal(err)
	}
	n.st = st
	// Another node adds a space, and pins a session, meanwhile.
	theirs := &protocol.WorkspaceState{}
	theirSpace := space(theirs, "theirs")
	theirSpace.CreatedAt, theirSpace.Rank = mark(), sreg(crdt.Between(st.GetSpaces()["c"].GetRank().GetValue(), ""))
	add(&theirs.PinnedSessions, "t9")
	other := newNode("o", realTime, true)
	other.edit(t, theirs)
	n.pull(other)

	// The window renames a, moves c to the front, deletes b, adds a
	// project folder, and unpins t1.
	spaces = []docSpace{{ID: "c", Name: "C"}, {ID: "a", Name: "A2"}}
	projects[0].Roots[0].Locations = append(projects[0].Roots[0].Locations, docFolder{"n", "/q"})
	second := docOf(spaces, projects, []string{"t2"})
	delta, err := Diff(n.st, first, second)
	if err != nil {
		t.Fatal(err)
	}
	// Of the two spaces left, one moved: one rank is written.
	ranked := 0
	for _, sp := range delta.GetSpaces() {
		if sp.GetRank() != nil {
			ranked++
		}
	}
	if ranked != 1 {
		t.Errorf("%d ranks written: %v", ranked, delta.GetSpaces())
	}
	n.edit(t, delta)
	if got := spaceIDs(n.st); !slices.Equal(got, []string{"c", "a", "theirs"}) {
		t.Errorf("spaces %v", got)
	}
	if got := n.st.GetSpaces()["a"].GetName().GetValue(); got != "A2" {
		t.Errorf("a's name %q", got)
	}
	if got := PinnedSessions(n.st); !slices.Equal(got, []string{"t2", "t9"}) && !slices.Equal(got, []string{"t9", "t2"}) {
		t.Errorf("pins %v", got)
	}
	if got := Scopes(n.st, "n"); len(got) != 1 || !slices.Equal(got[0].GetPaths(), []string{"/p", "/q"}) {
		t.Errorf("scopes %v", got)
	}

	// The same document again is no edit; nor is a first that was none.
	if d, _ := Diff(n.st, second, second); !Empty(d) {
		t.Errorf("no edit gave %v", d)
	}
	if _, err := Diff(n.st, []byte("tabs"), second); err != nil {
		t.Errorf("a prev that is no document: %v", err)
	}
	if _, err := Diff(n.st, second, []byte("tabs")); err == nil {
		t.Error("a next that is no document gave a delta")
	}
}

// edited is a state's view, as a window holds it, changed by edit.
func edited(t *testing.T, st *protocol.WorkspaceState, edit func(*document)) []byte {
	t.Helper()
	var d document
	if err := json.Unmarshal(View(st), &d); err != nil {
		t.Fatal(err)
	}
	edit(&d)
	body, _ := json.Marshal(d)
	return body
}

// A window's edit of its view, as a delta merged into the state, is what
// the state's view then is; a view not edited is no edit.
func TestViewRoundTrip(t *testing.T) {
	r := seeded(t)
	for range 20 {
		n := newNode("n", realTime, true)
		for _, d := range generate(t, r, testNodes(), 60) {
			Merge(n.st, d, n.clock, never)
		}
		if d, err := Diff(n.st, View(n.st), View(n.st)); err != nil || !Empty(d) {
			t.Fatalf("an unedited view gave %v, %v", d, err)
		}
		next := edited(t, n.st, func(d *document) {
			d.Spaces = append([]docSpace{{ID: "new", Name: "New", Owners: []string{"o"}}}, d.Spaces...)
			if len(d.Spaces) > 2 {
				d.Spaces[1], d.Spaces[2] = d.Spaces[2], d.Spaces[1]
				d.Spaces[2].Name += " renamed"
			}
			if len(d.Projects) > 0 {
				// A deleted project's references go from the view with it.
				gone := d.Projects[0].ID
				d.Projects = d.Projects[1:]
				delete(d.Names, gone)
				delete(d.Looks, gone)
				d.PinnedProjects = slices.DeleteFunc(d.PinnedProjects, func(s docScope) bool { return s.Section == gone })
				for i := range d.Spaces {
					d.Spaces[i].Projects = slices.DeleteFunc(d.Spaces[i].Projects, func(id string) bool { return id == gone })
					d.Spaces[i].Hidden = slices.DeleteFunc(d.Spaces[i].Hidden, func(id string) bool { return id == gone })
				}
			}
			d.Pinned = append(d.Pinned, "t-new")
			d.NoAll = !d.NoAll
		})
		delta, err := Diff(n.st, View(n.st), next)
		if err != nil {
			t.Fatal(err)
		}
		n.edit(t, delta)
		if got := View(n.st); string(got) != string(next) {
			t.Fatalf("after the edit the view is\n%s\nnot\n%s", got, next)
		}
	}
}

// A root keeps its ID through the window's view, so an edit of it is of
// the same root, though its first folder is not the one it was made with.
func TestARootKeepsItsID(t *testing.T) {
	n := newNode("n", realTime, true)
	d := &protocol.WorkspaceState{}
	p := project(d, "p")
	p.CreatedAt = mark()
	rt := root(p, LocationKey("n", "/a"))
	rt.CreatedAt = mark()
	add(&rt.Locations, LocationKey("n", "/a"))
	add(&rt.Locations, LocationKey("n", "/b"))
	n.edit(t, d)
	d = &protocol.WorkspaceState{}
	remove(&root(project(d, "p"), LocationKey("n", "/a")).Locations, LocationKey("n", "/a"))
	n.edit(t, d)

	delta, err := Diff(n.st, View(n.st), edited(t, n.st, func(d *document) { d.Projects[0].Roots[0].Label = "B" }))
	if err != nil {
		t.Fatal(err)
	}
	roots := delta.GetProjects()["p"].GetRoots()
	if len(roots) != 1 || roots[LocationKey("n", "/a")].GetLabel().GetValue() != "B" {
		t.Fatalf("delta %v", delta)
	}
}

// A clone joined to a root is one location added, and nothing else.
func TestAJoinedCloneIsOneElement(t *testing.T) {
	n := newNode("n", realTime, true)
	d := &protocol.WorkspaceState{}
	p := project(d, "p")
	p.CreatedAt, p.Name = mark(), sreg("P")
	rt := root(p, "github.com/x/y")
	rt.CreatedAt, rt.Remote = mark(), sreg("github.com/x/y")
	element(&rt.Locations, LocationKey("n", "/a"), &protocol.ElementMeta{AddedAt: mark(), Rank: "V", RankClock: mark()})
	n.edit(t, d)
	delta, err := Diff(n.st, View(n.st), edited(t, n.st, func(d *document) {
		r := &d.Projects[0].Roots[0]
		r.Locations = append(r.Locations, docFolder{Node: "m", Path: "/clone"})
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := &protocol.WorkspaceState{Projects: map[string]*protocol.ProjectEntry{"p": {Roots: map[string]*protocol.ProjectRootEntry{
		"github.com/x/y": {Locations: map[string]*protocol.ElementMeta{
			LocationKey("m", "/clone"): {AddedAt: mark(), Rank: crdt.Between("V", ""), RankClock: mark()},
		}},
	}}}}
	if !proto.Equal(delta, want) {
		t.Fatalf("delta %v\nwant %v", delta, want)
	}
}
