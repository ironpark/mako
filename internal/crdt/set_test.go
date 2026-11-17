package crdt

import (
	"slices"
	"strings"
	"testing"
)

func stampAt(wall int64, node string) HLC { return HLC{WallTime: wall, NodeID: node} }

func TestRegisterMerge(t *testing.T) {
	r := Register[string]{"old", stampAt(1, "a")}
	if r.Merge(Register[string]{"stale", stampAt(0, "b")}, "k") || r.Value != "old" {
		t.Errorf("an older write won: %v", r)
	}
	if !r.Merge(Register[string]{"new", stampAt(2, "b")}, "k") || r.Value != "new" {
		t.Errorf("a newer write lost: %v", r)
	}
	// The same stamp with another value: the greater value, either way.
	x := Register[string]{"x", stampAt(3, "a")}
	y := Register[string]{"y", stampAt(3, "a")}
	a, b := x, y
	MergeBy(&a, y, "k", strings.Compare)
	MergeBy(&b, x, "k", strings.Compare)
	if a != b || a.Value != "y" {
		t.Errorf("a tie settled differently: %v, %v", a, b)
	}
}

func TestElementMeta(t *testing.T) {
	tests := []struct {
		name   string
		meta   ElementMeta
		active bool
	}{
		{"never added", ElementMeta{}, false},
		{"added", ElementMeta{AddedAt: stampAt(1, "a")}, true},
		{"removed after", ElementMeta{AddedAt: stampAt(1, "a"), RemovedAt: stampAt(2, "b")}, false},
		{"added again", ElementMeta{AddedAt: stampAt(3, "a"), RemovedAt: stampAt(2, "b")}, true},
		{"same stamp", ElementMeta{AddedAt: stampAt(2, "a"), RemovedAt: stampAt(2, "a")}, true},
		{"only ranked", ElementMeta{Rank: "V", RankClock: stampAt(1, "a")}, false},
	}
	for _, tt := range tests {
		if got := tt.meta.Active("k"); got != tt.active {
			t.Errorf("%s: Active = %v, want %v", tt.name, got, tt.active)
		}
	}
}

func TestSetMerge(t *testing.T) {
	// Alice added on one node and Bob on another: both are in.
	a := Set{"alice": {AddedAt: stampAt(1, "a")}}
	b := Set{"bob": {AddedAt: stampAt(2, "b")}, "carol": {AddedAt: stampAt(1, "b"), RemovedAt: stampAt(3, "b")}}
	if !a.Merge(b) {
		t.Fatal("no change")
	}
	if got := a.Members(); !slices.Equal(got, []string{"alice", "bob"}) {
		t.Errorf("members %v", got)
	}
	// A later removal of alice elsewhere wins over her add.
	a.Merge(Set{"alice": {RemovedAt: stampAt(5, "b")}})
	if got := a.Members(); !slices.Equal(got, []string{"bob"}) {
		t.Errorf("members after removal %v", got)
	}
	if a.Merge(Set{"alice": {RemovedAt: stampAt(4, "b")}}) {
		t.Error("an older removal changed the set")
	}
	// Ranks order the members, their own registers.
	s := Set{"x": {AddedAt: stampAt(1, "a"), Rank: "k", RankClock: stampAt(1, "a")}, "y": {AddedAt: stampAt(1, "a"), Rank: "F", RankClock: stampAt(1, "a")}}
	s.Merge(Set{"x": {Rank: "A", RankClock: stampAt(2, "b")}})
	if got := s.Members(); !slices.Equal(got, []string{"x", "y"}) {
		t.Errorf("ordered members %v", got)
	}
}

func TestSetLattice(t *testing.T) {
	r := seeded(t)
	elems := []string{"p", "q", "r"}
	random := func() Set {
		s := Set{}
		for _, e := range elems {
			if r.IntN(3) == 0 {
				continue
			}
			s[e] = ElementMeta{AddedAt: randomHLC(r), RemovedAt: randomHLC(r), Rank: string(digits[1+r.IntN(5)]), RankClock: randomHLC(r)}
		}
		return s
	}
	clone := func(s Set) Set {
		c := Set{}
		c.Merge(s)
		return c
	}
	equal := func(a, b Set) bool {
		ca, cb := clone(a), clone(b)
		if len(ca) != len(cb) {
			return false
		}
		for k, v := range ca {
			if cb[k] != v {
				return false
			}
		}
		return true
	}
	for range 2000 {
		a, b, c := random(), random(), random()
		ab, ba := clone(a), clone(b)
		ab.Merge(b)
		ba.Merge(a)
		if !equal(ab, ba) {
			t.Fatalf("not commutative: %v, %v", ab, ba)
		}
		left, right := clone(ab), clone(b)
		left.Merge(c)
		right.Merge(c)
		right2 := clone(a)
		right2.Merge(right)
		if !equal(left, right2) {
			t.Fatalf("not associative: %v, %v", left, right2)
		}
		if again := clone(ab); again.Merge(ab) {
			t.Fatal("not idempotent")
		}
	}
}
