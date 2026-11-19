package crdt

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// checkBetween fails unless r lies strictly between prev and next, "" being
// the ends, and is a rank Between could be given again.
func checkBetween(t *testing.T, prev, next, r string) {
	t.Helper()
	if r == "" || strings.HasSuffix(r, "0") || strings.Trim(r, digits) != "" {
		t.Fatalf("Between(%q, %q) = %q: not a rank", prev, next, r)
	}
	if r <= prev || (next != "" && r >= next) {
		t.Fatalf("Between(%q, %q) = %q: not between", prev, next, r)
	}
}

func TestBetweenTable(t *testing.T) {
	tests := []struct{ prev, next, want string }{
		{"", "", "V"},
		{"", "V", "F"},
		{"V", "", "k"},
		{"", "1", "0V"},
		{"a", "b", "aV"},
		{"a", "aV", "aF"},
		{"z", "", "zV"},
		{"a0", "", "n"}, // a trailing zero is dropped
	}
	for _, tt := range tests {
		if got := Between(tt.prev, tt.next); got != tt.want {
			t.Errorf("Between(%q, %q) = %q, want %q", tt.prev, tt.next, got, tt.want)
		}
	}
}

func TestBetweenRandom(t *testing.T) {
	r := seeded(t)
	// A list grown by inserting at random places, ranks kept in order.
	list := []string{Between("", "")}
	for range 3000 {
		i := r.IntN(len(list) + 1)
		var prev, next string
		if i > 0 {
			prev = list[i-1]
		}
		if i < len(list) {
			next = list[i]
		}
		got := Between(prev, next)
		checkBetween(t, prev, next, got)
		list = slices.Insert(list, i, got)
	}
	if !slices.IsSorted(list) {
		t.Fatal("the list is out of order")
	}
}

func TestBetweenSameSpot(t *testing.T) {
	// Inserting again and again at one spot keeps finding room: just after
	// prev, just before next, at the front and at the back.
	tests := []struct {
		name string
		step func(prev, next, got string) (string, string)
	}{
		{"after prev", func(prev, _, got string) (string, string) { return prev, got }},
		{"before next", func(_, next, got string) (string, string) { return got, next }},
		{"front", func(_, _, got string) (string, string) { return "", got }},
		{"back", func(_, _, got string) (string, string) { return got, "" }},
	}
	for _, tt := range tests {
		prev, next := "a", "b"
		for range 500 {
			got := Between(prev, next)
			checkBetween(t, prev, next, got)
			prev, next = tt.step(prev, next, got)
		}
	}
}

func TestBetweenOutOfOrder(t *testing.T) {
	// Ranks the wrong way round, or equal, still give a rank after prev.
	for _, pair := range [][2]string{{"b", "a"}, {"a", "a"}, {"", "000"}} {
		got := Between(pair[0], pair[1])
		if got <= pair[0] {
			t.Errorf("Between(%q, %q) = %q", pair[0], pair[1], got)
		}
	}
}

func TestInitial(t *testing.T) {
	for _, n := range []int{1, 2, 3, 10, 61, 62, 63, 500, 4000} {
		var prev string
		for i := range n {
			got := Initial(i, n)
			if got != Initial(i, n) {
				t.Fatalf("Initial(%d, %d) differs between calls", i, n)
			}
			checkBetween(t, prev, "", got)
			prev = got
		}
	}
	if got := Initial(0, 1); got != "V" {
		t.Errorf("Initial(0, 1) = %q, want V", got)
	}
}

func TestRerank(t *testing.T) {
	ranks := map[string]string{"a": "F", "b": "V", "c": "k", "d": "s"}
	tests := []struct {
		name    string
		order   []string
		changed []string
	}{
		{"as it is", []string{"a", "b", "c", "d"}, nil},
		{"one moved to the end", []string{"b", "c", "d", "a"}, []string{"a"}},
		{"one moved to the front", []string{"d", "a", "b", "c"}, []string{"d"}},
		{"a new one in the middle", []string{"a", "b", "new", "c", "d"}, []string{"new"}},
		{"reversed", []string{"d", "c", "b", "a"}, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		got := Rerank(tt.order, ranks)
		if changed := slices.Sorted(maps.Keys(got)); !slices.Equal(changed, tt.changed) {
			t.Errorf("%s: changed %v, want %v", tt.name, changed, tt.changed)
		}
		checkOrder(t, tt.order, ranks, got)
	}
	// Equal ranks, as two nodes inserting at one spot make, are set apart.
	same := map[string]string{"x": "V", "y": "V"}
	checkOrder(t, []string{"x", "y"}, same, Rerank([]string{"x", "y"}, same))
}

func TestRerankRandom(t *testing.T) {
	r := seeded(t)
	for range 300 {
		n := 1 + r.IntN(12)
		ranks := map[string]string{}
		var order []string
		for i := range n {
			id := string(rune('a' + i))
			order = append(order, id)
			if r.IntN(4) > 0 {
				ranks[id] = Initial(r.IntN(6), 6) // collisions too
			}
		}
		r.Shuffle(n, func(i, j int) { order[i], order[j] = order[j], order[i] })
		checkOrder(t, order, ranks, Rerank(order, ranks))
	}
}

// checkOrder fails unless the ranks, as changed, rise along order.
func checkOrder(t *testing.T, order []string, ranks, changed map[string]string) {
	t.Helper()
	prev := ""
	for _, id := range order {
		rank := ranks[id]
		if c, ok := changed[id]; ok {
			rank = c
		}
		if rank <= prev {
			t.Fatalf("order %v, ranks %v, changed %v: %q not after %q", order, ranks, changed, rank, prev)
		}
		prev = rank
	}
}
