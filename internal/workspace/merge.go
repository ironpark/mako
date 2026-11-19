// Package workspace is the workspace shared by one person's nodes, as the
// CRDT state protocol.WorkspaceState holds: its spaces, projects, names,
// looks, pins and layouts. Every field is a register, a set or a map of
// entries of its own, joined with another node's field by field (Merge),
// so that edits made apart, in any order, come together without one
// node's edit wiping out another's.
//
// Only a daemon stamps edits (Stamp), with its own clock; front-ends give
// it deltas, states holding only what they changed. Daemons give each
// other their whole state.
package workspace

import (
	"bytes"
	"cmp"
	"maps"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/crdt"
)

// Merge joins src into dst and reports whether dst changed. Each register
// takes the later write, each set element the later add and the later
// removal, each entity the later creation and the later deletion; an entry
// src holds and dst does not is added. Merging is commutative, associative
// and idempotent, so nodes that merged the same states in any order, some
// of them more than once, hold the same.
//
// physNow is this node's time in Unix milliseconds. A stamp in src further
// ahead of it than crdt.MaxClockDrift comes from a node whose clock is
// wrong, and what it stamps is left out, quarantined; the rest of src is
// merged still. src is sent again and again, so the entry is merged once
// real time catches up with it. clock, when not nil, takes in every stamp
// that is kept, so that this node's next edits come after them; it is not
// anchored here, which is the caller's to do on first merging a peer's
// state. Merge(dst, src, nil, math.MaxInt64) quarantines nothing.
//
// src is not changed, and dst shares nothing with it after.
func Merge(dst, src *protocol.WorkspaceState, clock *crdt.Clock, physNow int64) (changed bool) {
	if src == nil {
		return false
	}
	m := merger{clock: clock, physNow: physNow}
	changed = m.boolean(&dst.NoAll, src.GetNoAll(), "no_all") || changed
	changed = m.str(&dst.DefaultSpace, src.GetDefaultSpace(), "default_space") || changed
	changed = mergeMap(&dst.Spaces, src.GetSpaces(), m.space) || changed
	changed = mergeMap(&dst.Projects, src.GetProjects(), m.project) || changed
	changed = mergeMap(&dst.Names, src.GetNames(), func(d **protocol.StringReg, s *protocol.StringReg, k string) bool {
		return m.str(d, s, "name\x00"+k)
	}) || changed
	changed = mergeMap(&dst.Looks, src.GetLooks(), m.look) || changed
	changed = m.set(&dst.PinnedProjects, src.GetPinnedProjects()) || changed
	changed = m.set(&dst.PinnedSessions, src.GetPinnedSessions()) || changed
	changed = mergeMap(&dst.Layouts, src.GetLayouts(), func(d **protocol.LayoutEntry, s *protocol.LayoutEntry, k string) bool {
		return m.layout(d, s, "layout\x00"+k)
	}) || changed
	changed = mergeMap(&dst.WindowLayouts, src.GetWindowLayouts(), func(d **protocol.LayoutEntry, s *protocol.LayoutEntry, k string) bool {
		return m.layout(d, s, "window_layout\x00"+k)
	}) || changed
	return changed
}

// merger merges one src into one dst.
type merger struct {
	clock   *crdt.Clock
	physNow int64
}

// keep reports whether to merge what h stamps, taking it into the clock.
func (m merger) keep(h *protocol.HLC) bool {
	if m.clock != nil {
		return m.clock.Observe(hlc(h), m.physNow)
	}
	return !crdt.Quarantined(hlc(h), m.physNow)
}

// mergeMap merges each entry of src into the entry of dst under the same
// key with merge, which is given nil for an entry dst does not hold and
// reports whether it changed it. An entry is added to dst only when there
// is something of it to keep, so that merging an empty entry adds nothing.
func mergeMap[V any](dst *map[string]V, src map[string]V, merge func(dst *V, src V, key string) bool) (changed bool) {
	for k, s := range src {
		d := (*dst)[k]
		if !merge(&d, s, k) {
			continue
		}
		if *dst == nil {
			*dst = make(map[string]V)
		}
		(*dst)[k] = d
		changed = true
	}
	return changed
}

// later sets *dst to src if src is the later stamp: a creation's or a
// deletion's, which only rise.
func (m merger) later(dst **protocol.HLC, src *protocol.HLC, key string) bool {
	if src == nil || !m.keep(src) || !hlc(src).After(hlc(*dst), key) {
		return false
	}
	*dst = pbHLC(hlc(src))
	return true
}

// str merges a string register.
func (m merger) str(dst **protocol.StringReg, src *protocol.StringReg, key string) bool {
	if src == nil || !m.keep(src.GetClock()) {
		return false
	}
	r := crdt.Register[string]{Value: (*dst).GetValue(), Clock: hlc((*dst).GetClock())}
	if !crdt.MergeBy(&r, crdt.Register[string]{Value: src.GetValue(), Clock: hlc(src.GetClock())}, key, strings.Compare) {
		return false
	}
	*dst = &protocol.StringReg{Value: r.Value, Clock: pbHLC(r.Clock)}
	return true
}

// boolean merges a bool register.
func (m merger) boolean(dst **protocol.BoolReg, src *protocol.BoolReg, key string) bool {
	if src == nil || !m.keep(src.GetClock()) {
		return false
	}
	r := crdt.Register[bool]{Value: (*dst).GetValue(), Clock: hlc((*dst).GetClock())}
	if !crdt.MergeBy(&r, crdt.Register[bool]{Value: src.GetValue(), Clock: hlc(src.GetClock())}, key, compareBool) {
		return false
	}
	*dst = &protocol.BoolReg{Value: r.Value, Clock: pbHLC(r.Clock)}
	return true
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case b:
		return -1
	}
	return 1
}

// meta merges one element of a set, each of its stamps on its own.
func (m merger) meta(dst **protocol.ElementMeta, src *protocol.ElementMeta, key string) bool {
	if src == nil {
		return false
	}
	var o crdt.ElementMeta
	if m.keep(src.GetAddedAt()) {
		o.AddedAt = hlc(src.GetAddedAt())
	}
	if m.keep(src.GetRemovedAt()) {
		o.RemovedAt = hlc(src.GetRemovedAt())
	}
	if m.keep(src.GetRankClock()) {
		o.Rank, o.RankClock = src.GetRank(), hlc(src.GetRankClock())
	}
	e := metaOf(*dst)
	if !e.Merge(o, key) {
		return false
	}
	*dst = pbMeta(e)
	return true
}

// set merges a set: its elements are their own keys.
func (m merger) set(dst *map[string]*protocol.ElementMeta, src map[string]*protocol.ElementMeta) bool {
	return mergeMap(dst, src, m.meta)
}

// space merges a space; it keeps the ID it is held by.
func (m merger) space(dst **protocol.SpaceEntry, src *protocol.SpaceEntry, id string) (changed bool) {
	if src == nil {
		return false
	}
	d := *dst
	if d == nil {
		d = &protocol.SpaceEntry{Id: id}
	} else if d.Id != id {
		d.Id, changed = id, true
	}
	key := spaceKey(id)
	changed = m.later(&d.CreatedAt, src.GetCreatedAt(), key) || changed
	changed = m.later(&d.DeletedAt, src.GetDeletedAt(), key) || changed
	changed = m.str(&d.Name, src.GetName(), key+"\x00name") || changed
	changed = m.str(&d.Rank, src.GetRank(), key+"\x00rank") || changed
	changed = m.set(&d.Owners, src.GetOwners()) || changed
	changed = m.set(&d.Projects, src.GetProjects()) || changed
	changed = m.set(&d.Hidden, src.GetHidden()) || changed
	changed = m.str(&d.Color, src.GetColor(), key+"\x00color") || changed
	changed = m.str(&d.Layout, src.GetLayout(), key+"\x00layout") || changed
	if changed {
		*dst = d
	}
	return changed
}

// project merges a project and its roots.
func (m merger) project(dst **protocol.ProjectEntry, src *protocol.ProjectEntry, id string) (changed bool) {
	if src == nil {
		return false
	}
	d := *dst
	if d == nil {
		d = &protocol.ProjectEntry{Id: id}
	} else if d.Id != id {
		d.Id, changed = id, true
	}
	key := projectKey(id)
	changed = m.later(&d.CreatedAt, src.GetCreatedAt(), key) || changed
	changed = m.later(&d.DeletedAt, src.GetDeletedAt(), key) || changed
	changed = m.str(&d.Name, src.GetName(), key+"\x00name") || changed
	changed = m.boolean(&d.NoJoin, src.GetNoJoin(), key+"\x00no_join") || changed
	changed = m.boolean(&d.GiveDirs, src.GetGiveDirs(), key+"\x00give_dirs") || changed
	changed = mergeMap(&d.Roots, src.GetRoots(), func(dr **protocol.ProjectRootEntry, sr *protocol.ProjectRootEntry, rid string) bool {
		return m.root(dr, sr, key, rid)
	}) || changed
	changed = m.str(&d.Rank, src.GetRank(), key+"\x00rank") || changed
	if changed {
		*dst = d
	}
	return changed
}

// root merges one root of the project whose key is project.
func (m merger) root(dst **protocol.ProjectRootEntry, src *protocol.ProjectRootEntry, project, id string) (changed bool) {
	if src == nil {
		return false
	}
	d := *dst
	if d == nil {
		d = &protocol.ProjectRootEntry{Id: id}
	} else if d.Id != id {
		d.Id, changed = id, true
	}
	key := rootKey(project, id)
	changed = m.later(&d.CreatedAt, src.GetCreatedAt(), key) || changed
	changed = m.later(&d.DeletedAt, src.GetDeletedAt(), key) || changed
	changed = m.str(&d.Rank, src.GetRank(), key+"\x00rank") || changed
	changed = m.str(&d.Label, src.GetLabel(), key+"\x00label") || changed
	changed = m.str(&d.Remote, src.GetRemote(), key+"\x00remote") || changed
	changed = m.str(&d.Owner, src.GetOwner(), key+"\x00owner") || changed
	changed = m.set(&d.Locations, src.GetLocations()) || changed
	changed = m.set(&d.Declined, src.GetDeclined()) || changed
	if changed {
		*dst = d
	}
	return changed
}

// look merges a section's look, its icon and colour each on its own.
func (m merger) look(dst **protocol.LookEntry, src *protocol.LookEntry, section string) (changed bool) {
	if src == nil {
		return false
	}
	d := *dst
	if d == nil {
		d = &protocol.LookEntry{}
	}
	key := "look\x00" + section
	changed = m.str(&d.Icon, src.GetIcon(), key+"\x00icon") || changed
	changed = m.str(&d.Color, src.GetColor(), key+"\x00color") || changed
	if changed {
		*dst = d
	}
	return changed
}

// layout merges a layout, one register of a whole document. A tie of
// stamps, which one edit never makes, goes to the greater document.
func (m merger) layout(dst **protocol.LayoutEntry, src *protocol.LayoutEntry, key string) bool {
	if src == nil || !m.keep(src.GetClock()) {
		return false
	}
	c := crdt.Compare(hlc(src.GetClock()), hlc((*dst).GetClock()), key)
	if c < 0 || c == 0 && compareLayout(src, *dst) <= 0 {
		return false
	}
	l := proto.CloneOf(src)
	l.Clock = pbHLC(hlc(src.GetClock()))
	*dst = l
	return true
}

// compareLayout orders two layouts of the same stamp by what they hold.
func compareLayout(a, b *protocol.LayoutEntry) int {
	if c := compareBool(a.GetDeleted(), b.GetDeleted()); c != 0 {
		return c
	}
	if c := bytes.Compare(a.GetDoc(), b.GetDoc()); c != 0 {
		return c
	}
	ta, tb := a.GetTabNodes(), b.GetTabNodes()
	ka, kb := slices.Sorted(maps.Keys(ta)), slices.Sorted(maps.Keys(tb))
	if c := slices.Compare(ka, kb); c != 0 {
		return c
	}
	for _, k := range ka {
		if c := cmp.Compare(ta[k], tb[k]); c != 0 {
			return c
		}
	}
	return 0
}

// The keys an entry's stamps are compared by, which spread the nodes'
// wins of ties over the entries. Any keys would do, the same everywhere.
// A set's element is compared by itself.

func spaceKey(id string) string         { return "space\x00" + id }
func projectKey(id string) string       { return "project\x00" + id }
func rootKey(project, id string) string { return project + "\x00root\x00" + id }

// hlc is a message's stamp; nil is the zero stamp.
func hlc(h *protocol.HLC) crdt.HLC {
	return crdt.HLC{WallTime: h.GetWallTime(), Logical: h.GetLogical(), NodeID: h.GetNodeId()}
}

// pbHLC is h as a message; nil for the zero stamp, so that a state holds
// one form of it however it came to.
func pbHLC(h crdt.HLC) *protocol.HLC {
	if h.IsZero() {
		return nil
	}
	return &protocol.HLC{WallTime: h.WallTime, Logical: h.Logical, NodeId: h.NodeID}
}

// metaOf is a set element's message as crdt has it, and pbMeta back.
func metaOf(e *protocol.ElementMeta) crdt.ElementMeta {
	return crdt.ElementMeta{AddedAt: hlc(e.GetAddedAt()), RemovedAt: hlc(e.GetRemovedAt()), Rank: e.GetRank(), RankClock: hlc(e.GetRankClock())}
}

func pbMeta(e crdt.ElementMeta) *protocol.ElementMeta {
	return &protocol.ElementMeta{AddedAt: pbHLC(e.AddedAt), RemovedAt: pbHLC(e.RemovedAt), Rank: e.Rank, RankClock: pbHLC(e.RankClock)}
}
