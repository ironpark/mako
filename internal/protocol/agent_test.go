package protocol

import (
	"slices"
	"testing"
)

// A limit takes the place of the one of its window, the shortest window
// first, and leaves the limits it was copied from as they were.
func TestSetLimitReplacesItsWindowInOrder(t *testing.T) {
	cached := Usage{Limits: []RateLimit{{Minutes: FiveHours, Used: 10}, {Minutes: Week, Used: 20}}}
	u := cached
	u.Merge(Usage{Window: 200000, Limits: []RateLimit{{Minutes: Week, Used: 30}, {Minutes: 60, Used: 5}}})
	want := []RateLimit{{Minutes: 60, Used: 5}, {Minutes: FiveHours, Used: 10}, {Minutes: Week, Used: 30}}
	if !slices.Equal(u.Limits, want) || u.Window != 200000 {
		t.Fatalf("merged %+v", u)
	}
	if cached.Limits[1].Used != 20 {
		t.Fatalf("the cached limits changed: %+v", cached.Limits)
	}
	if !(Usage{Limits: []RateLimit{}}).IsZero() || u.IsZero() {
		t.Fatal("IsZero")
	}
}
