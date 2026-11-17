package workspace

import (
	"bytes"
	"cmp"
	"maps"
	"slices"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/crdt"
	"github.com/ironpark/mako/internal/protocol"
)

// A window keeps the workspace as its document (document.go) and edits
// that, as it always has; Diff turns the edit from one of its documents to
// the next into the delta it is, so that only what the window changed
// reaches the state: what it changed, created, deleted, added, removed or
// moved, and nothing of what other nodes changed meanwhile.

// Diff is the delta that makes of the edit from the window's document prev
// to next an edit of cur, the state the window last had: unstamped, for
// Stamp. A prev that is no document counts as an empty one. A list whose
// order changed is ranked anew against cur's ranks, rewriting as few as it
// takes (crdt.Rerank): a move rewrites the moved item's rank, and two
// items that two nodes ranked alike are set apart.
func Diff(cur *protocol.WorkspaceState, prev, next []byte) (*protocol.WorkspaceState, error) {
	n, err := fromDocument(next)
	if err != nil {
		return nil, err
	}
	p, err := fromDocument(prev)
	if err != nil {
		p = &protocol.WorkspaceState{}
	}
	return diff(cur, p, n), nil
}

// Empty reports whether a state, or a delta, holds nothing.
func Empty(st *protocol.WorkspaceState) bool { return proto.Size(st) == 0 }

// diff is the delta from p to n, both converted documents, whose ranks
// are cur's.
func diff(cur, p, n *protocol.WorkspaceState) *protocol.WorkspaceState {
	d := &protocol.WorkspaceState{
		NoAll:        diffBool(p.GetNoAll(), n.GetNoAll()),
		DefaultSpace: diffStr(p.GetDefaultSpace(), n.GetDefaultSpace()),
	}
	d.Spaces = diffEntities(p.GetSpaces(), n.GetSpaces(), diffSpace, func() *protocol.SpaceEntry { return &protocol.SpaceEntry{DeletedAt: mark()} })
	rankEntities(p.GetSpaces(), n.GetSpaces(), cur.GetSpaces(), (*protocol.SpaceEntry).GetRank, func(id, rank string) {
		entry(&d.Spaces, id, func() *protocol.SpaceEntry { return &protocol.SpaceEntry{} }).Rank = &protocol.StringReg{Value: rank}
	})
	for id, ns := range n.GetSpaces() {
		ps, cs := p.GetSpaces()[id], cur.GetSpaces()[id]
		get := func() *protocol.SpaceEntry {
			return entry(&d.Spaces, id, func() *protocol.SpaceEntry { return &protocol.SpaceEntry{} })
		}
		rankSet(ps.GetOwners(), ns.GetOwners(), cs.GetOwners(), func() *map[string]*protocol.ElementMeta { return &get().Owners })
		rankSet(ps.GetProjects(), ns.GetProjects(), cs.GetProjects(), func() *map[string]*protocol.ElementMeta { return &get().Projects })
		rankSet(ps.GetHidden(), ns.GetHidden(), cs.GetHidden(), func() *map[string]*protocol.ElementMeta { return &get().Hidden })
	}

	d.Projects = diffEntities(p.GetProjects(), n.GetProjects(), diffProject, func() *protocol.ProjectEntry { return &protocol.ProjectEntry{DeletedAt: mark()} })
	rankEntities(p.GetProjects(), n.GetProjects(), cur.GetProjects(), (*protocol.ProjectEntry).GetRank, func(id, rank string) {
		entry(&d.Projects, id, func() *protocol.ProjectEntry { return &protocol.ProjectEntry{} }).Rank = &protocol.StringReg{Value: rank}
	})
	for id, np := range n.GetProjects() {
		pp, cp := p.GetProjects()[id], cur.GetProjects()[id]
		project := func() *protocol.ProjectEntry {
			return entry(&d.Projects, id, func() *protocol.ProjectEntry { return &protocol.ProjectEntry{} })
		}
		rankEntities(pp.GetRoots(), np.GetRoots(), cp.GetRoots(), (*protocol.ProjectRootEntry).GetRank, func(rid, rank string) {
			entry(&project().Roots, rid, func() *protocol.ProjectRootEntry { return &protocol.ProjectRootEntry{} }).Rank = &protocol.StringReg{Value: rank}
		})
		for rid, nr := range np.GetRoots() {
			pr, cr := pp.GetRoots()[rid], cp.GetRoots()[rid]
			get := func() *protocol.ProjectRootEntry {
				return entry(&project().Roots, rid, func() *protocol.ProjectRootEntry { return &protocol.ProjectRootEntry{} })
			}
			rankSet(pr.GetLocations(), nr.GetLocations(), cr.GetLocations(), func() *map[string]*protocol.ElementMeta { return &get().Locations })
			rankSet(pr.GetDeclined(), nr.GetDeclined(), cr.GetDeclined(), func() *map[string]*protocol.ElementMeta { return &get().Declined })
		}
	}

	for key, nn := range n.GetNames() {
		if r := diffStr(p.GetNames()[key], nn); r != nil {
			put(&d.Names, key, r)
		}
	}
	for key := range p.GetNames() {
		if _, ok := n.GetNames()[key]; !ok {
			put(&d.Names, key, &protocol.StringReg{})
		}
	}
	for key, nl := range n.GetLooks() {
		pl := p.GetLooks()[key]
		l := &protocol.LookEntry{Icon: diffStr(pl.GetIcon(), nl.GetIcon()), Color: diffStr(pl.GetColor(), nl.GetColor())}
		if l.Icon != nil || l.Color != nil {
			put(&d.Looks, key, l)
		}
	}
	for key := range p.GetLooks() {
		if _, ok := n.GetLooks()[key]; !ok {
			put(&d.Looks, key, &protocol.LookEntry{Icon: &protocol.StringReg{}, Color: &protocol.StringReg{}})
		}
	}
	d.PinnedProjects = diffSet(p.GetPinnedProjects(), n.GetPinnedProjects())
	rankSet(p.GetPinnedProjects(), n.GetPinnedProjects(), cur.GetPinnedProjects(), func() *map[string]*protocol.ElementMeta { return &d.PinnedProjects })
	d.PinnedSessions = diffSet(p.GetPinnedSessions(), n.GetPinnedSessions())
	rankSet(p.GetPinnedSessions(), n.GetPinnedSessions(), cur.GetPinnedSessions(), func() *map[string]*protocol.ElementMeta { return &d.PinnedSessions })

	for key, nl := range n.GetLayouts() {
		if pl := p.GetLayouts()[key]; pl == nil || !bytes.Equal(pl.GetDoc(), nl.GetDoc()) || !maps.Equal(pl.GetTabNodes(), nl.GetTabNodes()) {
			put(&d.Layouts, key, &protocol.LayoutEntry{Doc: nl.GetDoc(), TabNodes: nl.GetTabNodes()})
		}
	}
	for key := range p.GetLayouts() {
		if _, ok := n.GetLayouts()[key]; !ok {
			put(&d.Layouts, key, &protocol.LayoutEntry{Deleted: true})
		}
	}
	return d
}

// diffEntities are the entities created, changed or deleted from p to n:
// diffOne gives what changed of one, nil for nothing, and deleted the
// deletion of one n no longer has.
func diffEntities[E any](p, n map[string]E, diffOne func(p, n E) (E, bool), deleted func() E) map[string]E {
	var out map[string]E
	for id, ne := range n {
		if e, ok := diffOne(p[id], ne); ok {
			put(&out, id, e)
		}
	}
	for id := range p {
		if _, ok := n[id]; !ok {
			put(&out, id, deleted())
		}
	}
	return out
}

func diffSpace(p, n *protocol.SpaceEntry) (*protocol.SpaceEntry, bool) {
	e := &protocol.SpaceEntry{
		Name:     diffStr(p.GetName(), n.GetName()),
		Color:    diffStr(p.GetColor(), n.GetColor()),
		Layout:   diffStr(p.GetLayout(), n.GetLayout()),
		Owners:   diffSet(p.GetOwners(), n.GetOwners()),
		Projects: diffSet(p.GetProjects(), n.GetProjects()),
		Hidden:   diffSet(p.GetHidden(), n.GetHidden()),
	}
	if p == nil {
		e.CreatedAt = mark()
	}
	return e, proto.Size(e) > 0
}

func diffProject(p, n *protocol.ProjectEntry) (*protocol.ProjectEntry, bool) {
	e := &protocol.ProjectEntry{
		Name:     diffStr(p.GetName(), n.GetName()),
		NoJoin:   diffBool(p.GetNoJoin(), n.GetNoJoin()),
		GiveDirs: diffBool(p.GetGiveDirs(), n.GetGiveDirs()),
		Roots: diffEntities(p.GetRoots(), n.GetRoots(), diffRoot, func() *protocol.ProjectRootEntry {
			return &protocol.ProjectRootEntry{DeletedAt: mark()}
		}),
	}
	if p == nil {
		e.CreatedAt = mark()
	}
	return e, proto.Size(e) > 0
}

func diffRoot(p, n *protocol.ProjectRootEntry) (*protocol.ProjectRootEntry, bool) {
	e := &protocol.ProjectRootEntry{
		Label:     diffStr(p.GetLabel(), n.GetLabel()),
		Remote:    diffStr(p.GetRemote(), n.GetRemote()),
		Owner:     diffStr(p.GetOwner(), n.GetOwner()),
		Locations: diffSet(p.GetLocations(), n.GetLocations()),
		Declined:  diffSet(p.GetDeclined(), n.GetDeclined()),
	}
	if p == nil {
		e.CreatedAt = mark()
	}
	return e, proto.Size(e) > 0
}

// diffStr is n's value as a write, if it differs from p's; nil if not.
func diffStr(p, n *protocol.StringReg) *protocol.StringReg {
	if p != nil && p.GetValue() == n.GetValue() || p == nil && n == nil {
		return nil
	}
	return &protocol.StringReg{Value: n.GetValue()}
}

func diffBool(p, n *protocol.BoolReg) *protocol.BoolReg {
	if p != nil && p.GetValue() == n.GetValue() || p == nil && n == nil {
		return nil
	}
	return &protocol.BoolReg{Value: n.GetValue()}
}

// diffSet is the adds of the elements n has and p does not, and the
// removals of those p has and n does not.
func diffSet(p, n map[string]*protocol.ElementMeta) map[string]*protocol.ElementMeta {
	var out map[string]*protocol.ElementMeta
	for k := range n {
		if _, ok := p[k]; !ok {
			put(&out, k, &protocol.ElementMeta{AddedAt: mark()})
		}
	}
	for k := range p {
		if _, ok := n[k]; !ok {
			put(&out, k, &protocol.ElementMeta{RemovedAt: mark()})
		}
	}
	return out
}

// rankEntities ranks n's entities in their order against cur's ranks, when
// their order is not p's, through write.
func rankEntities[E proto.Message](p, n, cur map[string]E, rank func(E) *protocol.StringReg, write func(id, rank string)) {
	order := func(m map[string]E) []string {
		return slices.SortedFunc(maps.Keys(m), func(a, b string) int {
			return cmp.Or(cmp.Compare(rank(m[a]).GetValue(), rank(m[b]).GetValue()), cmp.Compare(a, b))
		})
	}
	next := order(n)
	if slices.Equal(order(p), next) {
		return
	}
	ranks := map[string]string{}
	for id, e := range cur {
		ranks[id] = rank(e).GetValue()
	}
	for id, r := range crdt.Rerank(next, ranks) {
		write(id, r)
	}
}

// rankSet ranks n's elements in their order against cur's ranks, when
// their order is not p's, into the set dst gives.
func rankSet(p, n, cur map[string]*protocol.ElementMeta, dst func() *map[string]*protocol.ElementMeta) {
	next := Members(n)
	if slices.Equal(Members(p), next) {
		return
	}
	ranks := map[string]string{}
	for k, e := range cur {
		ranks[k] = e.GetRank()
	}
	for k, r := range crdt.Rerank(next, ranks) {
		e := entry(dst(), k, func() *protocol.ElementMeta { return &protocol.ElementMeta{} })
		e.Rank, e.RankClock = r, mark()
	}
}

// entry is the entry of m under key, made with make if there is none.
func entry[V any](m *map[string]V, key string, make func() V) V {
	if v, ok := (*m)[key]; ok {
		return v
	}
	v := make()
	put(m, key, v)
	return v
}

func put[V any](m *map[string]V, key string, v V) {
	if *m == nil {
		*m = map[string]V{}
	}
	(*m)[key] = v
}

// mark asks for a creation, deletion, add or removal, for Stamp to stamp.
func mark() *protocol.HLC { return &protocol.HLC{Logical: 1} }
