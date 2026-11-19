package crdt

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

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

// randomHLC is a stamp from a small space, so that ties of time, of count
// and of hash all come up.
func randomHLC(r *rand.Rand) HLC {
	if r.IntN(10) == 0 {
		return HLC{}
	}
	nodes := []string{"", "a", "b", "c", "node-1", "node-2"}
	return HLC{
		WallTime: int64(r.IntN(3)),
		Logical:  uint32(r.IntN(3)),
		NodeID:   nodes[r.IntN(len(nodes))],
	}
}

func TestHLCTotalOrder(t *testing.T) {
	r := seeded(t)
	keys := []string{"", "space/s1/name", "x"}
	for range 20000 {
		a, b, c := randomHLC(r), randomHLC(r), randomHLC(r)
		key := keys[r.IntN(len(keys))]
		ab, ba := a.After(b, key), b.After(a, key)
		if a == b {
			if ab || ba {
				t.Fatalf("%v and itself: one is after the other", a)
			}
			continue
		}
		if ab == ba {
			t.Fatalf("%v, %v under %q: after both ways %v", a, b, key, ab)
		}
		if Compare(a, b, key) != -Compare(b, a, key) {
			t.Fatalf("Compare(%v, %v) not antisymmetric", a, b)
		}
		if ab && b.After(c, key) && !a.After(c, key) {
			t.Fatalf("%v > %v > %v under %q, but not %v > %v", a, b, c, key, a, c)
		}
	}
}

func TestHLCZeroLeast(t *testing.T) {
	for _, h := range []HLC{{NodeID: "a"}, {Logical: 1}, {WallTime: 1}} {
		if !h.After(HLC{}, "k") || (HLC{}).After(h, "k") {
			t.Errorf("%v is not after the zero stamp", h)
		}
	}
}

func TestHLCTieSpreads(t *testing.T) {
	// Of the same time and count, each node wins some keys.
	wins := map[string]int{}
	for i := range 64 {
		key := string(rune('A' + i))
		a, b := HLC{WallTime: 5, NodeID: "a"}, HLC{WallTime: 5, NodeID: "b"}
		if a.After(b, key) {
			wins["a"]++
		} else {
			wins["b"]++
		}
	}
	if wins["a"] == 0 || wins["b"] == 0 {
		t.Errorf("one node wins every tie: %v", wins)
	}
}

func TestClockMonotonic(t *testing.T) {
	r := seeded(t)
	c := NewClock("n", true)
	phys := int64(1_700_000_000_000)
	var last HLC
	for range 5000 {
		// Time mostly moves on, but stands, and goes back a little too.
		phys += int64(r.IntN(7)) - 3
		h := c.Now(phys)
		if !h.After(last, "k") || h.WallTime <= 0 {
			t.Fatalf("stamp %v after %v at %d", h, last, phys)
		}
		last = h
	}
}

func TestClockBackwardsKeepsGoing(t *testing.T) {
	c := NewClock("n", true)
	first := c.Now(10_000)
	// Back by less than the drift: the counter carries on.
	second := c.Now(9_000)
	if second.WallTime != 10_000 || second.Logical != 1 {
		t.Errorf("after going back: %v, want 10000.1", second)
	}
	if !second.After(first, "k") {
		t.Error("the later stamp is not after the first")
	}
}

func TestClockLogicalOverflow(t *testing.T) {
	c := NewClock("n", true)
	c.Observe(HLC{WallTime: 10, Logical: math.MaxUint32, NodeID: "o"}, 10)
	h := c.Now(10)
	if h.WallTime != 11 || h.Logical != 0 {
		t.Errorf("past the counter's end: %v, want 11.0", h)
	}
}

func TestClockObserve(t *testing.T) {
	now := int64(1_000_000)
	drift := int64(MaxClockDrift / time.Millisecond)
	tests := []struct {
		name   string
		remote HLC
		keep   bool
		next   HLC // the clock's next stamp at now
	}{
		{"behind", HLC{WallTime: now - 5, Logical: 3, NodeID: "o"}, true, HLC{WallTime: now, NodeID: "n"}},
		{"ahead within drift", HLC{WallTime: now + drift, Logical: 2, NodeID: "o"}, true, HLC{WallTime: now + drift, Logical: 3, NodeID: "n"}},
		{"beyond drift", HLC{WallTime: now + drift + 1, NodeID: "o"}, false, HLC{WallTime: now, NodeID: "n"}},
		{"counted", HLC{Logical: 9, NodeID: "o"}, true, HLC{WallTime: now, NodeID: "n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClock("n", true)
			if keep := c.Observe(tt.remote, now); keep != tt.keep {
				t.Errorf("Observe = %v, want %v", keep, tt.keep)
			}
			if Quarantined(tt.remote, now) == tt.keep {
				t.Errorf("Quarantined disagrees with Observe")
			}
			if got := c.Now(now); got != tt.next {
				t.Errorf("next stamp %v, want %v", got, tt.next)
			}
		})
	}
}

func TestQuarantinedNever(t *testing.T) {
	if Quarantined(HLC{WallTime: math.MaxInt64}, math.MaxInt64) {
		t.Error("a far stamp is quarantined at the end of time")
	}
}

func TestClockUnanchored(t *testing.T) {
	c := NewClock("new", false)
	a, b := c.Now(5_000), c.Now(5_000)
	if a.WallTime != 0 || b.WallTime != 0 || !b.After(a, "k") {
		t.Fatalf("counted stamps %v, %v", a, b)
	}
	// Any timed stamp, of any node, is later than every counted one.
	r := seeded(t)
	for range 1000 {
		counted := HLC{Logical: r.Uint32(), NodeID: "new"}
		timed := HLC{WallTime: 1 + r.Int64N(1<<40), Logical: r.Uint32N(4), NodeID: "old"}
		if !timed.After(counted, "k") {
			t.Fatalf("%v is not after %v", timed, counted)
		}
	}
	// Taking in a counted stamp keeps the count above it.
	c.Observe(HLC{Logical: 40, NodeID: "other"}, 5_000)
	if h := c.Now(5_000); h.Logical != 41 || h.WallTime != 0 {
		t.Errorf("after counted 40: %v", h)
	}
	// Anchored, it times its stamps, after what it took in.
	c.Observe(HLC{WallTime: 6_000, Logical: 4, NodeID: "old"}, 5_000)
	c.Anchor()
	if h := c.Now(5_000); h != (HLC{WallTime: 6_000, Logical: 5, NodeID: "new"}) {
		t.Errorf("first anchored stamp %v", h)
	}
}

func TestClockRewind(t *testing.T) {
	hour := int64(time.Hour / time.Millisecond)
	real := int64(10 * hour)
	c := NewClock("n", true)
	// Set two hours ahead, it stamps ahead.
	wrong := c.Now(real + 2*hour)
	if wrong.WallTime != real+2*hour {
		t.Fatalf("stamp %v", wrong)
	}
	if c.Ahead(real + 2*hour) {
		t.Fatal("ahead of its own time")
	}
	// It took in a peer's stamp a little ahead of real time.
	c.Observe(HLC{WallTime: real + 10, Logical: 7, NodeID: "p"}, real+2*hour)
	if !c.Ahead(real) {
		t.Fatal("not ahead of real time")
	}
	// Put right, its next stamp is near real time, after the peer's.
	h := c.Now(real)
	if h != (HLC{WallTime: real + 10, Logical: 8, NodeID: "n"}) {
		t.Errorf("after the clock was put right: %v", h)
	}
	if c.Ahead(real) {
		t.Error("still ahead after Now")
	}
}
