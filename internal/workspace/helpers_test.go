package workspace

import (
	"math/rand/v2"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/crdt"
)

// never is a time no stamp is too far ahead of.
const never = int64(^uint64(0) >> 1)

// realTime is a moment to start the tests' clocks from, in Unix ms.
const realTime = int64(1_763_000_000_000)

// seeded is a random source for a test, whose seed a failure prints.
func seeded(t *testing.T) *rand.Rand {
	t.Helper()
	seed := uint64(time.Now().UnixNano())
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("seed %d", seed)
		}
	})
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}

func sreg(v string) *protocol.StringReg { return &protocol.StringReg{Value: v} }
func breg(v bool) *protocol.BoolReg     { return &protocol.BoolReg{Value: v} }

// node is a daemon, as far as the workspace goes: its clock, its time and
// its state.
type node struct {
	id    string
	clock *crdt.Clock
	phys  int64
	st    *protocol.WorkspaceState
}

func newNode(id string, phys int64, anchored bool) *node {
	return &node{id: id, clock: crdt.NewClock(id, anchored), phys: phys, st: &protocol.WorkspaceState{}}
}

// edit takes a front-end's delta as the daemon does: stamped, then merged.
// It returns the delta as stamped.
func (n *node) edit(t *testing.T, d *protocol.WorkspaceState) *protocol.WorkspaceState {
	t.Helper()
	n.phys++
	Stamp(d, n.clock, n.phys)
	Merge(n.st, d, n.clock, n.phys)
	return d
}

// pull merges another node's whole state into n's, as its NodeState
// brings it, and anchors n's clock.
func (n *node) pull(o *node) bool {
	changed := Merge(n.st, proto.CloneOf(o.st), n.clock, n.phys)
	n.clock.Anchor()
	return changed
}

// exchange has each node pull from each other, twice round, at their own
// times.
func exchange(nodes ...*node) {
	for range 2 {
		for _, a := range nodes {
			for _, b := range nodes {
				if a != b {
					a.pull(b)
				}
			}
		}
	}
}

// sameState fails unless the states are equal, as messages and by digest.
func sameState(t *testing.T, states ...*protocol.WorkspaceState) {
	t.Helper()
	for i := 1; i < len(states); i++ {
		if !proto.Equal(states[0], states[i]) {
			t.Fatalf("states 0 and %d differ:\n%v\n%v", i, states[0], states[i])
		}
		if string(Digest(states[0])) != string(Digest(states[i])) {
			t.Fatalf("states 0 and %d are equal but their digests differ", i)
		}
	}
}

// space is the entry of a space in a delta, made if need be.
func space(d *protocol.WorkspaceState, id string) *protocol.SpaceEntry {
	if d.Spaces == nil {
		d.Spaces = map[string]*protocol.SpaceEntry{}
	}
	if d.Spaces[id] == nil {
		d.Spaces[id] = &protocol.SpaceEntry{}
	}
	return d.Spaces[id]
}

// project is the entry of a project in a delta, made if need be.
func project(d *protocol.WorkspaceState, id string) *protocol.ProjectEntry {
	if d.Projects == nil {
		d.Projects = map[string]*protocol.ProjectEntry{}
	}
	if d.Projects[id] == nil {
		d.Projects[id] = &protocol.ProjectEntry{}
	}
	return d.Projects[id]
}

// root is the entry of a root of a project in a delta, made if need be.
func root(p *protocol.ProjectEntry, id string) *protocol.ProjectRootEntry {
	if p.Roots == nil {
		p.Roots = map[string]*protocol.ProjectRootEntry{}
	}
	if p.Roots[id] == nil {
		p.Roots[id] = &protocol.ProjectRootEntry{}
	}
	return p.Roots[id]
}

// add and remove put an element's add or removal into a delta's set.
func add(set *map[string]*protocol.ElementMeta, key string) *protocol.ElementMeta {
	return element(set, key, &protocol.ElementMeta{AddedAt: mark()})
}

func remove(set *map[string]*protocol.ElementMeta, key string) *protocol.ElementMeta {
	return element(set, key, &protocol.ElementMeta{RemovedAt: mark()})
}

func element(set *map[string]*protocol.ElementMeta, key string, e *protocol.ElementMeta) *protocol.ElementMeta {
	if *set == nil {
		*set = map[string]*protocol.ElementMeta{}
	}
	(*set)[key] = e
	return e
}

// layout puts a pane tree into a delta.
func layout(d *protocol.WorkspaceState, key, doc string) {
	if d.Layouts == nil {
		d.Layouts = map[string]*protocol.LayoutEntry{}
	}
	d.Layouts[key] = &protocol.LayoutEntry{Doc: []byte(doc)}
}

// spaceIDs lists the spaces that show, in order.
func spaceIDs(st *protocol.WorkspaceState) []string {
	var out []string
	for _, sp := range Spaces(st) {
		out = append(out, sp.GetId())
	}
	return out
}
