package crdt

import (
	"cmp"
	"slices"
	"strings"
)

// ElementMeta is what a last-writer-wins element set keeps of one element:
// when it was last added and last removed, and for an ordered set its rank,
// a register of its own.
//
// It is not an observed-remove set. A removal later than an add wins over
// it, though the add was not seen; the elements are bare identifiers, with
// nothing in them an add could carry that a removal would lose.
type ElementMeta struct {
	AddedAt   HLC
	RemovedAt HLC
	Rank      string
	RankClock HLC
}

// Active reports whether the element is in the set: it was added, and not
// removed since. An add and a removal of the same stamp leave it in. key
// is the element's, as Merge is given it.
func (e ElementMeta) Active(key string) bool {
	return !e.AddedAt.IsZero() && !e.RemovedAt.After(e.AddedAt, key)
}

// Merge joins other into e, the same element of another replica: the later
// add, the later removal and the later rank, each on its own. It reports
// whether e changed.
func (e *ElementMeta) Merge(other ElementMeta, key string) (changed bool) {
	if other.AddedAt.After(e.AddedAt, key) {
		e.AddedAt, changed = other.AddedAt, true
	}
	if other.RemovedAt.After(e.RemovedAt, key) {
		e.RemovedAt, changed = other.RemovedAt, true
	}
	rank := Register[string]{e.Rank, e.RankClock}
	if MergeBy(&rank, Register[string]{other.Rank, other.RankClock}, key, strings.Compare) {
		e.Rank, e.RankClock, changed = rank.Value, rank.Clock, true
	}
	return changed
}

// Set is a last-writer-wins element set: every element ever added or
// removed, with its ElementMeta, by the element.
type Set map[string]ElementMeta

// Merge joins other into s, element by element, and reports whether s
// changed. An element other holds nothing of is not added to s.
func (s Set) Merge(other Set) (changed bool) {
	for k, o := range other {
		e := s[k]
		if e.Merge(o, k) {
			s[k] = e
			changed = true
		}
	}
	return changed
}

// Members are the elements in the set, by rank and then by themselves.
func (s Set) Members() []string {
	var out []string
	for k, e := range s {
		if e.Active(k) {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		return cmp.Or(cmp.Compare(s[a].Rank, s[b].Rank), cmp.Compare(a, b))
	})
	return out
}
