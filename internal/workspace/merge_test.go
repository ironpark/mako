package workspace

import (
	"math/rand/v2"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/crdt"
	"github.com/ironpark/mako/internal/protocol"
)

// What random edits pick from: few of each, so that edits collide.
var (
	spacePool   = []string{"s0", "s1", "s2", "s3"}
	projectIDs  = []string{"p0", "p1", "p2"}
	rootIDs     = []string{"r0", "r1"}
	locations   = []string{LocationKey("n0", "/a"), LocationKey("n1", "/a"), LocationKey("n1", "/b:c"), LocationKey("n2", "/d")}
	words       = []string{"", "Work", "Home", "Backend", "mako"}
	sections    = []string{"p0", "p1", "p2", "repo:github.com/x/y", "free:z"}
	sessionIDs  = []string{"t0", "t1", "t2"}
	layoutKeys  = []string{"all|", "s0|", "s1|p0", "s2|free:z"}
	windowNames = []string{"default", "wide"}
)

func pick[T any](r *rand.Rand, xs []T) T { return xs[r.IntN(len(xs))] }

// randomElement is an add, a removal, or both, of a set's element, perhaps
// ranked.
func randomElement(r *rand.Rand) *protocol.ElementMeta {
	e := &protocol.ElementMeta{}
	switch r.IntN(5) {
	case 0, 1:
		e.AddedAt = mark()
	case 2, 3:
		e.RemovedAt = mark()
	default:
		e.AddedAt, e.RemovedAt = mark(), mark()
	}
	if r.IntN(3) == 0 {
		e.Rank, e.RankClock = crdt.Initial(r.IntN(6), 6), mark()
	}
	return e
}

// randomDelta is what a front-end might send: one to three edits.
func randomDelta(r *rand.Rand) *protocol.WorkspaceState {
	d := &protocol.WorkspaceState{}
	for range 1 + r.IntN(3) {
		switch r.IntN(14) {
		case 0:
			sp := space(d, pick(r, spacePool))
			sp.CreatedAt, sp.Name, sp.Rank = mark(), sreg(pick(r, words)), sreg(crdt.Initial(r.IntN(8), 8))
		case 1:
			space(d, pick(r, spacePool)).DeletedAt = mark()
		case 2:
			sp := space(d, pick(r, spacePool))
			switch r.IntN(4) {
			case 0:
				sp.Name = sreg(pick(r, words))
			case 1:
				sp.Rank = sreg(crdt.Between(crdt.Initial(r.IntN(4), 4), ""))
			case 2:
				sp.Color = sreg(pick(r, []string{"", "red", "blue"}))
			default:
				sp.Layout = sreg(pick(r, windowNames))
			}
		case 3:
			sp := space(d, pick(r, spacePool))
			set := pick(r, []*map[string]*protocol.ElementMeta{&sp.Owners, &sp.Projects, &sp.Hidden})
			element(set, pick(r, sections), randomElement(r))
		case 4:
			p := project(d, pick(r, projectIDs))
			p.CreatedAt, p.Name, p.Rank = mark(), sreg(pick(r, words)), sreg(crdt.Initial(r.IntN(5), 5))
			rt := root(p, pick(r, rootIDs))
			rt.CreatedAt = mark()
			add(&rt.Locations, pick(r, locations))
		case 5:
			p := project(d, pick(r, projectIDs))
			if r.IntN(2) == 0 {
				p.DeletedAt = mark()
			} else {
				root(p, pick(r, rootIDs)).DeletedAt = mark()
			}
		case 6:
			p := project(d, pick(r, projectIDs))
			switch r.IntN(3) {
			case 0:
				p.Name = sreg(pick(r, words))
			case 1:
				p.NoJoin = breg(r.IntN(2) == 0)
			default:
				p.GiveDirs = breg(r.IntN(2) == 0)
			}
		case 7:
			rt := root(project(d, pick(r, projectIDs)), pick(r, rootIDs))
			switch r.IntN(5) {
			case 0:
				element(&rt.Locations, pick(r, locations), randomElement(r))
			case 1:
				element(&rt.Declined, pick(r, locations), randomElement(r))
			case 2:
				rt.Label = sreg(pick(r, words))
			case 3:
				rt.Remote, rt.Owner = sreg("github.com/x/y"), sreg("github.com/x")
			default:
				rt.Rank = sreg(crdt.Initial(r.IntN(3), 3))
			}
		case 8:
			if d.Names == nil {
				d.Names = map[string]*protocol.StringReg{}
			}
			d.Names[pick(r, sections)] = sreg(pick(r, words))
		case 9:
			if d.Looks == nil {
				d.Looks = map[string]*protocol.LookEntry{}
			}
			l := &protocol.LookEntry{}
			if r.IntN(2) == 0 {
				l.Icon = sreg(pick(r, []string{"", "star", "folder"}))
			} else {
				l.Color = sreg(pick(r, []string{"", "red", "green"}))
			}
			d.Looks[pick(r, sections)] = l
		case 10:
			element(&d.PinnedProjects, pick(r, sections)+"\x00"+pick(r, []string{"", "r0"}), randomElement(r))
		case 11:
			element(&d.PinnedSessions, pick(r, sessionIDs), randomElement(r))
		case 12:
			if d.Layouts == nil {
				d.Layouts = map[string]*protocol.LayoutEntry{}
			}
			d.Layouts[pick(r, layoutKeys)] = &protocol.LayoutEntry{
				Doc:      []byte(pick(r, []string{"", `{"split":0.5}`, `{"tabs":["a"]}`})),
				TabNodes: map[string]string{pick(r, sessionIDs): pick(r, []string{"n0", "n1"})},
				Deleted:  r.IntN(5) == 0,
			}
		default:
			switch r.IntN(3) {
			case 0:
				d.NoAll = breg(r.IntN(2) == 0)
			case 1:
				d.DefaultSpace = sreg(pick(r, spacePool))
			default:
				if d.WindowLayouts == nil {
					d.WindowLayouts = map[string]*protocol.LayoutEntry{}
				}
				d.WindowLayouts[pick(r, windowNames)] = &protocol.LayoutEntry{Doc: []byte(pick(r, words))}
			}
		}
	}
	return d
}

// checkedMerge merges as Merge does, and fails unless it reports a change
// exactly when there was one, and leaves src as it was.
func checkedMerge(t *testing.T, dst, src *protocol.WorkspaceState) {
	t.Helper()
	before, srcBefore := proto.CloneOf(dst), proto.CloneOf(src)
	changed := Merge(dst, src, nil, never)
	if changed == proto.Equal(before, dst) {
		t.Fatalf("Merge said changed %v:\nbefore %v\nafter %v", changed, before, dst)
	}
	if !proto.Equal(src, srcBefore) {
		t.Fatal("Merge changed src")
	}
}

// generate has nodes make n random edits, each stamped by its own clock at
// its own time, the nodes' times apart by seconds and going back now and
// then; the nodes take in each other's states now and then too, and one
// starts unanchored. It returns the stamped deltas.
func generate(t *testing.T, r *rand.Rand, nodes []*node, n int) []*protocol.WorkspaceState {
	var deltas []*protocol.WorkspaceState
	for range n {
		nd := pick(r, nodes)
		nd.phys += r.Int64N(40) - 5
		if r.IntN(8) == 0 {
			nd.pull(pick(r, nodes))
		}
		deltas = append(deltas, proto.CloneOf(nd.edit(t, randomDelta(r))))
	}
	return deltas
}

func testNodes() []*node {
	return []*node{
		newNode("n0", realTime, true),
		newNode("n1", realTime+12_000, true),
		newNode("n2", realTime-20_000, true),
		newNode("n3", realTime+3_000, false),
	}
}

func TestConvergence(t *testing.T) {
	r := seeded(t)
	for range 20 {
		nodes := testNodes()
		deltas := generate(t, r, nodes, 100)

		// The reference: every delta, merged in the order made.
		want := &protocol.WorkspaceState{}
		for _, d := range deltas {
			Merge(want, d, nil, never)
		}

		// Each node has its own edits and some of the others' states; it is
		// sent the deltas besides, out of order, some twice, some not at
		// all. Fresh replicas are sent only deltas, the same way.
		replicas := []*protocol.WorkspaceState{{}, {}, {}}
		for _, nd := range nodes {
			replicas = append(replicas, nd.st)
		}
		for _, st := range replicas {
			for _, i := range r.Perm(len(deltas)) {
				for range r.IntN(3) { // 0, 1 or 2 times
					checkedMerge(t, st, deltas[i])
				}
			}
		}
		// Then whole states go round, and every replica holds them all.
		for range 2 {
			for _, a := range replicas {
				for _, b := range replicas {
					checkedMerge(t, a, b)
				}
			}
		}
		sameState(t, append(replicas, want)...)
	}
}

func TestLattice(t *testing.T) {
	r := seeded(t)
	deltas := generate(t, r, testNodes(), 100)
	// States, each of a random part of the deltas.
	random := func() *protocol.WorkspaceState {
		st := &protocol.WorkspaceState{}
		for _, d := range deltas {
			if r.IntN(3) == 0 {
				Merge(st, d, nil, never)
			}
		}
		return st
	}
	merged := func(a, b *protocol.WorkspaceState) *protocol.WorkspaceState {
		out := proto.CloneOf(a)
		Merge(out, b, nil, never)
		return out
	}
	for range 100 {
		a, b, c := random(), random(), random()
		sameState(t, merged(a, b), merged(b, a))
		sameState(t, merged(merged(a, b), c), merged(a, merged(b, c)))
		again := proto.CloneOf(a)
		if Merge(again, a, nil, never) {
			t.Fatal("merging a state into itself changed it")
		}
		sameState(t, again, a)
		// A delta merged in again changes nothing either.
		d := pick(r, deltas)
		once := merged(a, d)
		if Merge(once, d, nil, never) {
			t.Fatal("merging a delta twice changed the state twice")
		}
	}
}

func TestMergeKeepsNothingOfSrc(t *testing.T) {
	r := seeded(t)
	deltas := generate(t, r, testNodes(), 50)
	src := &protocol.WorkspaceState{}
	for _, d := range deltas {
		Merge(src, d, nil, never)
	}
	want := proto.CloneOf(src)
	dst := &protocol.WorkspaceState{}
	Merge(dst, src, nil, never)
	// Changing every stamp of dst in place changes none of src's.
	eachClock(dst, func(h *protocol.HLC) { h.WallTime++ })
	if !proto.Equal(src, want) {
		t.Fatal("dst shares messages with src")
	}
}

func TestMergeQuarantine(t *testing.T) {
	drift := int64(crdt.MaxClockDrift.Milliseconds())
	now := realTime
	future := &protocol.HLC{WallTime: now + drift + 1, NodeId: "fast"}
	near := &protocol.HLC{WallTime: now + drift, NodeId: "fast"}
	src := &protocol.WorkspaceState{
		NoAll: &protocol.BoolReg{Value: true, Clock: future},
		Spaces: map[string]*protocol.SpaceEntry{
			"s": {Id: "s", CreatedAt: near, Name: &protocol.StringReg{Value: "far", Clock: future}},
		},
		PinnedSessions: map[string]*protocol.ElementMeta{
			"t": {AddedAt: near, Rank: "V", RankClock: future},
			"u": {AddedAt: future},
		},
		Layouts: map[string]*protocol.LayoutEntry{"all|": {Doc: []byte("x"), Clock: future}},
	}
	clock := crdt.NewClock("me", true)
	dst := &protocol.WorkspaceState{}
	if !Merge(dst, src, clock, now) {
		t.Fatal("nothing merged")
	}
	want := &protocol.WorkspaceState{
		Spaces:         map[string]*protocol.SpaceEntry{"s": {Id: "s", CreatedAt: near}},
		PinnedSessions: map[string]*protocol.ElementMeta{"t": {AddedAt: near}},
	}
	if !proto.Equal(dst, want) {
		t.Fatalf("merged %v\nwant %v", dst, want)
	}
	// The clock moved up to what it kept, and not to what it did not.
	if got := clock.Now(now); got.WallTime != now+drift {
		t.Errorf("next stamp %v, want at %d", got, now+drift)
	}
	// Once real time has caught up, the rest is merged.
	Merge(dst, src, clock, now+2)
	sameState(t, dst, src)
}
