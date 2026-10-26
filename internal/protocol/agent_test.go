package protocol

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// A limit takes the place of the one of its window, the shortest window
// first, and leaves the list it was merged into as it was.
func TestSetLimitReplacesItsWindowInOrder(t *testing.T) {
	cached := &Usage{Limits: []*RateLimit{{Minutes: FiveHours, Used: 10}, {Minutes: Week, Used: 20}}}
	u := proto.CloneOf(cached)
	u.Limits = cached.Limits // shared, as a cached read's
	u.Merge(&Usage{Window: 200000, Limits: []*RateLimit{{Minutes: Week, Used: 30}, {Minutes: 60, Used: 5}}})
	want := &Usage{Window: 200000, Limits: []*RateLimit{{Minutes: 60, Used: 5}, {Minutes: FiveHours, Used: 10}, {Minutes: Week, Used: 30}}}
	if !proto.Equal(u, want) {
		t.Fatalf("merged %v", u)
	}
	if cached.Limits[1].Used != 20 || len(cached.Limits) != 2 {
		t.Fatalf("the cached limits changed: %v", cached.Limits)
	}
	if !(&Usage{Limits: []*RateLimit{}}).IsZero() || !(*Usage)(nil).IsZero() || u.IsZero() {
		t.Fatal("IsZero")
	}
}
