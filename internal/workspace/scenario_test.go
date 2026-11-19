package workspace

import (
	"slices"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/crdt"
)

// The scenarios of docs/workspace-sync-plan.md §4: two nodes edit apart,
// then take in each other's states.

// pair is two anchored nodes holding the same state, which setup made.
func pair(t *testing.T, setup ...*protocol.WorkspaceState) (a, b *node) {
	a, b = newNode("a", realTime, true), newNode("b", realTime+500, true)
	for _, d := range setup {
		a.edit(t, d)
	}
	b.pull(a)
	return a, b
}

func newSpace(id, name, rank string) *protocol.WorkspaceState {
	d := &protocol.WorkspaceState{}
	sp := space(d, id)
	sp.CreatedAt, sp.Name, sp.Rank = mark(), sreg(name), sreg(rank)
	return d
}

// Scenario 1: a layout changed on one node and a space added on the other
// are different entries, and both stay.
func TestScenarioLayoutAndSpace(t *testing.T) {
	a, b := pair(t, newSpace("s1", "One", crdt.Initial(0, 1)))
	d := &protocol.WorkspaceState{}
	layout(d, "s1|", `{"split":0.3}`)
	a.edit(t, d)
	b.edit(t, newSpace("work", "Work", crdt.Between(crdt.Initial(0, 1), "")))
	exchange(a, b)
	sameState(t, a.st, b.st)
	if got := string(Layout(a.st, "s1|").GetDoc()); got != `{"split":0.3}` {
		t.Errorf("layout %q", got)
	}
	if got := spaceIDs(a.st); !slices.Equal(got, []string{"s1", "work"}) {
		t.Errorf("spaces %v", got)
	}
}

// Scenario 2: one node renames a space while the other adds a project to
// it; the name and the set are registers of their own, and both stay.
func TestScenarioSpaceFields(t *testing.T) {
	a, b := pair(t, newSpace("work", "Work", "V"))
	rename := &protocol.WorkspaceState{}
	space(rename, "work").Name = sreg("Backend")
	a.edit(t, rename)
	addProject := &protocol.WorkspaceState{}
	add(&space(addProject, "work").Projects, "mako")
	b.edit(t, addProject)
	exchange(a, b)
	sameState(t, a.st, b.st)
	sp := a.st.GetSpaces()["work"]
	if sp.GetName().GetValue() != "Backend" {
		t.Errorf("name %q", sp.GetName().GetValue())
	}
	if got := SpaceProjects(a.st, sp); !slices.Equal(got, []string{"mako"}) {
		t.Errorf("projects %v", got)
	}
}

// Scenario 3: one node moves the first space to the end while the other
// adds a space at the front; each rewrites one rank, and both moves land.
func TestScenarioOrder(t *testing.T) {
	var setup []*protocol.WorkspaceState
	for i, id := range []string{"s1", "s2", "s3"} {
		setup = append(setup, newSpace(id, id, crdt.Initial(i, 3)))
	}
	a, b := pair(t, setup...)
	rank := func(n *node, id string) string { return n.st.GetSpaces()[id].GetRank().GetValue() }

	move := &protocol.WorkspaceState{}
	space(move, "s1").Rank = sreg(crdt.Between(rank(a, "s3"), ""))
	a.edit(t, move)
	b.edit(t, newSpace("s4", "s4", crdt.Between("", rank(b, "s1"))))

	exchange(a, b)
	sameState(t, a.st, b.st)
	if got := spaceIDs(a.st); !slices.Equal(got, []string{"s4", "s2", "s3", "s1"}) {
		t.Errorf("order %v", got)
	}
}

// Scenario 4: a node whose clock is two hours ahead pins a session while
// one with a right clock creates three projects. The right node leaves the
// pin out and its clock where it was, so its projects are not lost; the
// pin reaches it once the wrong clock is put right, or real time catches
// up.
func TestScenarioFastClock(t *testing.T) {
	hour := time.Hour.Milliseconds()
	for _, fix := range []string{"clock put right", "time catches up"} {
		t.Run(fix, func(t *testing.T) {
			a, b := pair(t, newSpace("s1", "One", "V"))
			a.phys = realTime + 2*hour
			pin := &protocol.WorkspaceState{}
			add(&pin.PinnedSessions, "t1")
			a.edit(t, pin)
			for _, id := range []string{"p1", "p2", "p3"} {
				d := &protocol.WorkspaceState{}
				p := project(d, id)
				p.CreatedAt, p.Name = mark(), sreg(id)
				b.edit(t, d)
			}

			exchange(a, b)
			if got := PinnedSessions(b.st); len(got) != 0 {
				t.Errorf("the right node took the pin in: %v", got)
			}
			if next := b.clock.Now(b.phys); next.WallTime > b.phys {
				t.Errorf("the right node's clock was dragged ahead: %v", next)
			}
			for _, n := range []*node{a, b} {
				if got := len(Projects(n.st)); got != 3 {
					t.Errorf("node %s holds %d projects", n.id, got)
				}
			}

			if fix == "clock put right" {
				a.phys = b.phys
				if !Heal(a.st, a.clock, a.phys) {
					t.Fatal("nothing stamped again")
				}
				if Heal(a.st, a.clock, a.phys) {
					t.Error("stamped again twice")
				}
			} else {
				b.phys += 2 * hour
			}
			exchange(a, b)
			sameState(t, a.st, b.st)
			if got := PinnedSessions(b.st); !slices.Equal(got, []string{"t1"}) {
				t.Errorf("pins %v", got)
			}
		})
	}
}

// Heal keeps the order of what it stamps again: an element added and then
// removed while the clock was wrong stays removed, and entries of other
// nodes are not touched.
func TestHealKeepsOrder(t *testing.T) {
	hour := time.Hour.Milliseconds()
	a := newNode("a", realTime+hour, true)
	d := &protocol.WorkspaceState{}
	add(&d.PinnedSessions, "gone")
	add(&d.PinnedSessions, "kept")
	a.edit(t, d)
	d = &protocol.WorkspaceState{}
	remove(&d.PinnedSessions, "gone")
	a.edit(t, d)
	other := &protocol.HLC{WallTime: realTime + hour, NodeId: "b"}
	a.st.Names = map[string]*protocol.StringReg{"x": {Value: "theirs", Clock: other}}

	if !Heal(a.st, a.clock, realTime) {
		t.Fatal("nothing stamped again")
	}
	if got := PinnedSessions(a.st); !slices.Equal(got, []string{"kept"}) {
		t.Errorf("pins %v", got)
	}
	eachClock(a.st, func(h *protocol.HLC) {
		if h.GetNodeId() == "a" && crdt.Quarantined(hlc(h), realTime) {
			t.Errorf("own stamp %v still ahead", h)
		}
	})
	if a.st.GetNames()["x"].GetClock().GetWallTime() != realTime+hour {
		t.Error("another node's stamp was changed")
	}
}

// Scenario 5: one node deletes a space while the other renames it; the
// deletion wins, whichever came later, and the rename comes back with the
// space when the deletion is undone.
func TestScenarioDeleteAndEdit(t *testing.T) {
	for _, order := range []string{"rename first", "delete first"} {
		t.Run(order, func(t *testing.T) {
			a, b := pair(t, newSpace("x", "X", "V"))
			del := &protocol.WorkspaceState{}
			space(del, "x").DeletedAt = mark()
			rename := &protocol.WorkspaceState{}
			space(rename, "x").Name = sreg("Renamed")
			if order == "rename first" {
				b.edit(t, rename)
				a.phys = b.phys + 10
				a.edit(t, del)
			} else {
				a.edit(t, del)
				b.phys = a.phys + 10
				b.edit(t, rename)
			}
			exchange(a, b)
			sameState(t, a.st, b.st)
			if got := spaceIDs(a.st); len(got) != 0 {
				t.Errorf("deleted space shows: %v", got)
			}

			undo := &protocol.WorkspaceState{}
			space(undo, "x").CreatedAt = mark()
			a.phys += 100
			a.edit(t, undo)
			exchange(a, b)
			sameState(t, a.st, b.st)
			if got := Spaces(b.st); len(got) != 1 || got[0].GetName().GetValue() != "Renamed" {
				t.Errorf("after undo: %v", got)
			}
		})
	}
}

// Scenario 6: a node new to the mesh opens two tabs before it joins. Its
// stamps are counted, so where it wrote the same layout as the mesh the
// mesh's stays; what only it had is kept.
func TestScenarioNewNode(t *testing.T) {
	old := newNode("old", realTime, true)
	old.edit(t, newSpace("s1", "Work", "V"))
	d := &protocol.WorkspaceState{}
	layout(d, "all|", "months of tabs")
	old.edit(t, d)

	fresh := newNode("fresh", realTime+1_000, false)
	for _, doc := range []string{"one tab", "two tabs"} {
		d := &protocol.WorkspaceState{}
		layout(d, "all|", doc)
		fresh.edit(t, d)
	}
	d = &protocol.WorkspaceState{}
	layout(d, "fresh|", "mine")
	fresh.edit(t, d)
	if got := fresh.st.GetLayouts()["all|"].GetClock().GetWallTime(); got != 0 {
		t.Fatalf("an unanchored stamp is timed: %d", got)
	}

	exchange(fresh, old)
	sameState(t, fresh.st, old.st)
	if got := string(Layout(fresh.st, "all|").GetDoc()); got != "months of tabs" {
		t.Errorf("all| is %q", got)
	}
	if got := string(Layout(old.st, "fresh|").GetDoc()); got != "mine" {
		t.Errorf("fresh| is %q", got)
	}

	// Joined, the new node's edits are timed, and win as any other's.
	if !fresh.clock.Anchored() {
		t.Fatal("not anchored after joining")
	}
	d = &protocol.WorkspaceState{}
	layout(d, "all|", "after joining")
	fresh.edit(t, d)
	exchange(fresh, old)
	if got := string(Layout(old.st, "all|").GetDoc()); got != "after joining" {
		t.Errorf("all| after joining is %q", got)
	}
}
