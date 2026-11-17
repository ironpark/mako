package workspace

import (
	"bytes"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/crdt"
	"github.com/ironpark/mako/internal/protocol"
)

func TestStamp(t *testing.T) {
	// A front-end's clocks, here far ahead, count for nothing.
	bogus := &protocol.HLC{WallTime: 1 << 60, Logical: 9, NodeId: "window"}
	delta := &protocol.WorkspaceState{
		NoAll: &protocol.BoolReg{Value: true, Clock: bogus},
		Spaces: map[string]*protocol.SpaceEntry{"s": {
			Id:        "wrong",
			CreatedAt: bogus,
			DeletedAt: &protocol.HLC{},
			Name:      &protocol.StringReg{Value: "S"},
			Owners: map[string]*protocol.ElementMeta{
				"in":  {AddedAt: bogus},
				"out": {RemovedAt: mark(), RankClock: bogus},
			},
		}},
		PinnedSessions: map[string]*protocol.ElementMeta{"t": {AddedAt: mark(), Rank: "V"}},
		Layouts:        map[string]*protocol.LayoutEntry{"all|": {Doc: []byte("x"), Clock: bogus}},
	}
	clock := crdt.NewClock("me", true)
	tick := Stamp(delta, clock, realTime)
	if tick != (crdt.HLC{WallTime: realTime, NodeID: "me"}) {
		t.Fatalf("tick %v", tick)
	}
	at := pbHLC(tick)
	want := &protocol.WorkspaceState{
		NoAll: &protocol.BoolReg{Value: true, Clock: at},
		Spaces: map[string]*protocol.SpaceEntry{"s": {
			Id:        "s",
			CreatedAt: at,
			Name:      &protocol.StringReg{Value: "S", Clock: at},
			Owners: map[string]*protocol.ElementMeta{
				"in":  {AddedAt: at},
				"out": {RemovedAt: at, RankClock: at},
			},
		}},
		PinnedSessions: map[string]*protocol.ElementMeta{"t": {AddedAt: at, Rank: "V", RankClock: at}},
		Layouts:        map[string]*protocol.LayoutEntry{"all|": {Doc: []byte("x"), Clock: at}},
	}
	if !proto.Equal(delta, want) {
		t.Errorf("stamped %v\nwant %v", delta, want)
	}
	// Each delta takes a stamp of its own.
	if next := Stamp(&protocol.WorkspaceState{}, clock, realTime); !next.After(tick, "") {
		t.Errorf("the next delta's stamp %v is not after %v", next, tick)
	}
}

func TestStampEmpty(t *testing.T) {
	// A delta of nothing, or of entries with nothing in them, stamps
	// nothing and merges nothing.
	delta := &protocol.WorkspaceState{
		Spaces:   map[string]*protocol.SpaceEntry{"s": {}, "n": nil},
		Projects: map[string]*protocol.ProjectEntry{"p": {Roots: map[string]*protocol.ProjectRootEntry{"r": {}}}},
		Looks:    map[string]*protocol.LookEntry{"l": {}},
	}
	Stamp(delta, crdt.NewClock("me", true), realTime)
	st := &protocol.WorkspaceState{}
	if Merge(st, delta, nil, never) || !proto.Equal(st, &protocol.WorkspaceState{}) {
		t.Errorf("an empty delta merged: %v", st)
	}
}

func TestViews(t *testing.T) {
	n := newNode("me", realTime, true)
	for i, id := range []string{"a", "b", "gone"} {
		n.edit(t, newSpace(id, id, crdt.Initial(2-i, 3)))
	}
	d := &protocol.WorkspaceState{}
	space(d, "gone").DeletedAt = mark()
	// Two projects, one deleted; one root of the other deleted, and one
	// location of a root removed.
	for _, id := range []string{"keep", "drop"} {
		p := project(d, id)
		p.CreatedAt, p.Name, p.GiveDirs = mark(), sreg("Project "+id), breg(id == "keep")
	}
	keep := project(d, "keep")
	keep.Rank = sreg("V")
	r0 := root(keep, "r0")
	r0.CreatedAt, r0.Rank = mark(), sreg("F")
	add(&r0.Locations, LocationKey("me", "/src/a"))
	add(&r0.Locations, LocationKey("me", "/src/b:colon"))
	add(&r0.Locations, LocationKey("other", "/src/a"))
	r1 := root(keep, "r1")
	r1.CreatedAt, r1.Rank = mark(), sreg("k")
	add(&r1.Locations, LocationKey("me", "/src/c"))
	add(&r1.Locations, LocationKey("me", "/src/a")) // the same folder again
	r2 := root(keep, "r2")
	r2.CreatedAt = mark()
	add(&r2.Locations, LocationKey("me", "/src/deleted-root"))
	add(&root(project(d, "drop"), "r").Locations, LocationKey("me", "/src/drop"))
	root(project(d, "drop"), "r").CreatedAt = mark()
	add(&space(d, "a").Projects, "keep")
	add(&space(d, "a").Projects, "drop")
	add(&space(d, "a").Projects, "repo:github.com/x/y")
	d.Names = map[string]*protocol.StringReg{"keep": sreg("K"), "drop": sreg("D"), "free:z": sreg("Z"), "cleared": sreg("")}
	d.Looks = map[string]*protocol.LookEntry{"keep": {Icon: sreg("star")}, "drop": {Color: sreg("red")}}
	add(&d.PinnedProjects, "keep\x00r0")
	add(&d.PinnedProjects, "drop\x00")
	layout(d, "a|", "A")
	layout(d, "gone|", "G")
	layout(d, "all|", "ALL")
	n.edit(t, d)
	d = &protocol.WorkspaceState{}
	project(d, "drop").DeletedAt = mark()
	root(project(d, "keep"), "r2").DeletedAt = mark()
	remove(&root(project(d, "keep"), "r0").Locations, LocationKey("me", "/src/b:colon"))
	n.edit(t, d)
	st := n.st

	if got := spaceIDs(st); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("spaces %v", got)
	}
	if got := SpaceProjects(st, st.GetSpaces()["a"]); !slices.Equal(got, []string{"keep", "repo:github.com/x/y"}) {
		t.Errorf("space projects %v", got)
	}
	if got := Projects(st); len(got) != 1 || got[0].GetId() != "keep" {
		t.Errorf("projects %v", got)
	}
	if got := PinnedProjects(st); !slices.Equal(got, []string{"keep\x00r0"}) {
		t.Errorf("pinned %q", got)
	}
	if got := Names(st); len(got) != 2 || got["keep"] != "K" || got["free:z"] != "Z" {
		t.Errorf("names %v", got)
	}
	if got := Looks(st); len(got) != 1 || got["keep"] != (Look{Icon: "star"}) {
		t.Errorf("looks %v", got)
	}
	for key, want := range map[string]string{"a|": "A", "gone|": "", "all|": "ALL", "none|": ""} {
		if got := string(Layout(st, key).GetDoc()); got != want {
			t.Errorf("layout %q = %q, want %q", key, got, want)
		}
	}

	want := []*protocol.ProjectScope{{Id: "keep", Name: "Project keep", GiveDirs: true, Paths: []string{"/src/a", "/src/c"}}}
	got := Scopes(st, "me")
	if !slices.EqualFunc(got, want, func(a, b *protocol.ProjectScope) bool { return proto.Equal(a, b) }) {
		t.Errorf("scopes %v, want %v", got, want)
	}
	if got := Scopes(st, "other"); len(got) != 1 || !slices.Equal(got[0].GetPaths(), []string{"/src/a"}) {
		t.Errorf("other's scopes %v", got)
	}
	if got := Scopes(st, "nowhere"); len(got) != 0 {
		t.Errorf("scopes of a node with no folders %v", got)
	}
}

func TestLocationKey(t *testing.T) {
	key := LocationKey("node", `C:\src:x`)
	node, path, ok := SplitLocation(key)
	if !ok || node != "node" || path != `C:\src:x` {
		t.Errorf("split %q: %q %q %v", key, node, path, ok)
	}
	if _, _, ok := SplitLocation("nothing"); ok {
		t.Error("split a key that is none")
	}
}

func TestDigest(t *testing.T) {
	empty := Digest(nil)
	if !bytes.Equal(empty, Digest(&protocol.WorkspaceState{})) {
		t.Error("nil and empty states differ")
	}
	r := seeded(t)
	deltas := generate(t, r, testNodes(), 60)
	a, b := &protocol.WorkspaceState{}, &protocol.WorkspaceState{}
	for _, d := range deltas {
		Merge(a, d, nil, never)
	}
	for _, i := range r.Perm(len(deltas)) {
		Merge(b, deltas[i], nil, never)
	}
	// The same state, built in another order, and so with its maps filled
	// in another order, has the same digest.
	sameState(t, a, b)
	if bytes.Equal(Digest(a), empty) {
		t.Error("a state with edits has the empty state's digest")
	}
	before := Digest(a)
	d := &protocol.WorkspaceState{NoAll: &protocol.BoolReg{Value: !a.GetNoAll().GetValue()}}
	Stamp(d, crdt.NewClock("z", true), never-1)
	Merge(a, d, nil, never)
	if bytes.Equal(Digest(a), before) {
		t.Error("a change left the digest as it was")
	}
}
