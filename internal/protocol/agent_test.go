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

func TestUsageApplyReplacesUpdatedFieldsAndKeepsOmitted(t *testing.T) {
	u := &Usage{
		Window:         1048576,
		Input:          100,
		Output:         50,
		Context:        100,
		Model:          "Gemini 4 Neon",
		Effort:         "high",
		PermissionMode: "auto",
		Limits:         []*RateLimit{{Minutes: FiveHours, Used: 10}},
	}
	// A later live report changes model, effort, mode, and token counts while
	// omitting window and limits: reported fields update, omitted fields stay.
	u.Apply(&Usage{
		Input:          300,
		Output:         120,
		Context:        250,
		Model:          "Gemini 4 Hydrogen",
		Effort:         "medium",
		PermissionMode: "plan",
	})
	if u.GetWindow() != 1048576 || len(u.GetLimits()) != 1 || u.GetLimits()[0].GetUsed() != 10 {
		t.Fatalf("omitted fields were lost: %+v", u)
	}
	if u.GetInput() != 300 || u.GetOutput() != 120 || u.GetContext() != 250 ||
		u.GetModel() != "Gemini 4 Hydrogen" || u.GetEffort() != "medium" || u.GetPermissionMode() != "plan" {
		t.Fatalf("updated fields did not replace prior values: %+v", u)
	}
	// A partial report (e.g. only rate limits) preserves existing mode/model/effort.
	u.Apply(&Usage{Limits: []*RateLimit{{Minutes: FiveHours, Used: 25}}})
	if u.GetModel() != "Gemini 4 Hydrogen" || u.GetEffort() != "medium" || u.GetPermissionMode() != "plan" || u.GetLimits()[0].GetUsed() != 25 {
		t.Fatalf("partial Apply clobbered fields: %+v", u)
	}
}
