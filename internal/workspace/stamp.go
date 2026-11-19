package workspace

import (
	"cmp"
	"slices"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/crdt"
)

// Stamp stamps a front-end's delta with one fresh stamp of the daemon's
// clock, in place, and returns it. Whatever clocks the front-end put in are
// ignored: it has no clock of its own, and many front-ends edit through one
// daemon, which alone keeps that node's stamps rising.
//
// Each register the delta holds is written, and takes the stamp. A
// creation, deletion, add or removal is asked for by a stamp that is not
// zero, whatever it is, and takes the stamp; one that is zero is no edit,
// and is cleared. An element's rank is written when the delta gives it one
// or a clock, so that a rank is cleared by an empty one with a clock. An
// entity's ID is set to the key it is held by.
//
// An add and a removal of one element in one delta leave it in, as their
// stamps are the same; a creation and a deletion of one entity leave it
// deleted, as it shows only while created later than deleted.
func Stamp(delta *protocol.WorkspaceState, clock *crdt.Clock, physNow int64) crdt.HLC {
	s := stamper(clock.Now(physNow))
	s.boolean(delta.GetNoAll())
	s.str(delta.GetDefaultSpace())
	for id, sp := range delta.GetSpaces() {
		if sp == nil {
			continue
		}
		sp.Id = id
		s.intent(&sp.CreatedAt)
		s.intent(&sp.DeletedAt)
		s.str(sp.Name, sp.Rank, sp.Color, sp.Layout)
		s.set(sp.Owners, sp.Projects, sp.Hidden)
	}
	for id, p := range delta.GetProjects() {
		if p == nil {
			continue
		}
		p.Id = id
		s.intent(&p.CreatedAt)
		s.intent(&p.DeletedAt)
		s.str(p.Name, p.Rank)
		s.boolean(p.NoJoin, p.GiveDirs)
		for rid, r := range p.GetRoots() {
			if r == nil {
				continue
			}
			r.Id = rid
			s.intent(&r.CreatedAt)
			s.intent(&r.DeletedAt)
			s.str(r.Rank, r.Label, r.Remote, r.Owner)
			s.set(r.Locations, r.Declined)
		}
	}
	for _, n := range delta.GetNames() {
		s.str(n)
	}
	for _, l := range delta.GetLooks() {
		if l != nil {
			s.str(l.Icon, l.Color)
		}
	}
	s.set(delta.GetPinnedProjects(), delta.GetPinnedSessions())
	for _, layouts := range []map[string]*protocol.LayoutEntry{delta.GetLayouts(), delta.GetWindowLayouts()} {
		for _, l := range layouts {
			if l != nil {
				l.Clock = pbHLC(crdt.HLC(s))
			}
		}
	}
	return crdt.HLC(s)
}

// stamper puts one stamp into a delta.
type stamper crdt.HLC

// boolean stamps bool registers, those the delta holds.
func (s stamper) boolean(regs ...*protocol.BoolReg) {
	for _, r := range regs {
		if r != nil {
			r.Clock = pbHLC(crdt.HLC(s))
		}
	}
}

// str stamps string registers, those the delta holds.
func (s stamper) str(regs ...*protocol.StringReg) {
	for _, r := range regs {
		if r != nil {
			r.Clock = pbHLC(crdt.HLC(s))
		}
	}
}

// intent stamps a stamp that asks for an edit, and clears one that does
// not.
func (s stamper) intent(h **protocol.HLC) {
	if hlc(*h).IsZero() {
		*h = nil
		return
	}
	*h = pbHLC(crdt.HLC(s))
}

// set stamps the elements of sets.
func (s stamper) set(sets ...map[string]*protocol.ElementMeta) {
	for _, set := range sets {
		for _, e := range set {
			if e == nil {
				continue
			}
			s.intent(&e.AddedAt)
			s.intent(&e.RemovedAt)
			if e.Rank != "" || !hlc(e.RankClock).IsZero() {
				e.RankClock = pbHLC(crdt.HLC(s))
			} else {
				e.RankClock = nil
			}
		}
	}
}

// Observe brings clock up to date with every stamp st holds, at physNow:
// what a daemon does with the state it kept, before it stamps anything.
// Stamps further ahead than crdt.MaxClockDrift move it nowhere.
func Observe(st *protocol.WorkspaceState, clock *crdt.Clock, physNow int64) {
	eachClock(st, func(h *protocol.HLC) { clock.Observe(hlc(h), physNow) })
}

// Heal stamps again, in place, the entries of st that clock's own node
// stamped further ahead of physNow than crdt.MaxClockDrift, and reports
// whether there were any. They were stamped while the node's clock was
// wrong, and every node with a right one has left them out, quarantined;
// stamped again, at the time it is now, they keep their values and reach
// the others. Stamps of other nodes are not touched.
//
// The new stamps keep the order the old ones had among themselves, and
// come after every other stamp st holds, so that each comparison within st
// comes out as it did: an element added and then removed stays removed.
// The node had timed its stamps, so clock is anchored if it was not.
func Heal(st *protocol.WorkspaceState, clock *crdt.Clock, physNow int64) bool {
	type ahead struct {
		h   *protocol.HLC
		old crdt.HLC
	}
	var own []ahead
	seen := map[*protocol.HLC]bool{}
	eachClock(st, func(h *protocol.HLC) {
		c := hlc(h)
		if c.NodeID == clock.NodeID() && crdt.Quarantined(c, physNow) {
			if !seen[h] {
				seen[h] = true
				own = append(own, ahead{h, c})
			}
			return
		}
		clock.Observe(c, physNow)
	})
	if len(own) == 0 {
		return false
	}
	clock.Anchor()
	slices.SortStableFunc(own, func(a, b ahead) int {
		return cmp.Or(cmp.Compare(a.old.WallTime, b.old.WallTime), cmp.Compare(a.old.Logical, b.old.Logical))
	})
	var tick crdt.HLC
	for i, a := range own {
		if i == 0 || a.old != own[i-1].old {
			tick = clock.Now(physNow)
		}
		a.h.WallTime, a.h.Logical = tick.WallTime, tick.Logical
	}
	return true
}

// eachClock calls fn with every stamp st holds.
func eachClock(st *protocol.WorkspaceState, fn func(*protocol.HLC)) {
	visit := func(hs ...*protocol.HLC) {
		for _, h := range hs {
			if h != nil {
				fn(h)
			}
		}
	}
	set := func(set map[string]*protocol.ElementMeta) {
		for _, e := range set {
			visit(e.GetAddedAt(), e.GetRemovedAt(), e.GetRankClock())
		}
	}
	visit(st.GetNoAll().GetClock(), st.GetDefaultSpace().GetClock())
	for _, sp := range st.GetSpaces() {
		visit(sp.GetCreatedAt(), sp.GetDeletedAt(), sp.GetName().GetClock(), sp.GetRank().GetClock(),
			sp.GetColor().GetClock(), sp.GetLayout().GetClock())
		set(sp.GetOwners())
		set(sp.GetProjects())
		set(sp.GetHidden())
	}
	for _, p := range st.GetProjects() {
		visit(p.GetCreatedAt(), p.GetDeletedAt(), p.GetName().GetClock(), p.GetNoJoin().GetClock(),
			p.GetGiveDirs().GetClock(), p.GetRank().GetClock())
		for _, r := range p.GetRoots() {
			visit(r.GetCreatedAt(), r.GetDeletedAt(), r.GetRank().GetClock(), r.GetLabel().GetClock(),
				r.GetRemote().GetClock(), r.GetOwner().GetClock())
			set(r.GetLocations())
			set(r.GetDeclined())
		}
	}
	for _, n := range st.GetNames() {
		visit(n.GetClock())
	}
	for _, l := range st.GetLooks() {
		visit(l.GetIcon().GetClock(), l.GetColor().GetClock())
	}
	set(st.GetPinnedProjects())
	set(st.GetPinnedSessions())
	for _, l := range st.GetLayouts() {
		visit(l.GetClock())
	}
	for _, l := range st.GetWindowLayouts() {
		visit(l.GetClock())
	}
}

// Anchored reports whether the clock of node, keeping st, is to time its
// stamps: whether st holds a timed stamp, or one of another node. A
// node's clock times them from the first state of a peer it merges, or the
// first timed workspace it takes up, and either leaves in its state a
// timed stamp or another node's; a node that has only counted, never
// having joined another, holds neither. So the state itself keeps whether
// its clock is anchored. Another node's counted stamp counts too, as two
// nodes that join while both still count hold no timed stamp until one of
// them edits again. A stamp of no node, as a window's workspace given back
// whole has, is none of another node's.
func Anchored(st *protocol.WorkspaceState, node string) bool {
	anchored := false
	eachClock(st, func(h *protocol.HLC) {
		anchored = anchored || h.GetWallTime() > 0 || h.GetNodeId() != "" && h.GetNodeId() != node
	})
	return anchored
}
