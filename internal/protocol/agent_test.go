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

func TestUsageFastModeMergeAndApply(t *testing.T) {
	// Merge fills nil FastMode from r, including explicit false, without clobbering existing values.
	u := &Usage{}
	u.Merge(&Usage{FastMode: proto.Bool(true)})
	if u.FastMode == nil || !u.GetFastMode() {
		t.Fatalf("Merge did not copy FastMode=true: %+v", u)
	}
	u.Merge(&Usage{FastMode: proto.Bool(false)})
	if !u.GetFastMode() {
		t.Fatalf("Merge overwrote existing FastMode=true: %+v", u)
	}

	uFalse := &Usage{}
	src := proto.Bool(false)
	uFalse.Merge(&Usage{FastMode: src})
	*src = true
	if uFalse.FastMode == nil || uFalse.GetFastMode() {
		t.Fatalf("Merge aliased or lost FastMode=false: %+v", uFalse)
	}

	// Apply overrides FastMode whenever r.FastMode != nil and preserves it when nil.
	u.Apply(&Usage{FastMode: proto.Bool(false)})
	if u.FastMode == nil || u.GetFastMode() {
		t.Fatalf("Apply did not override FastMode to false: %+v", u)
	}
	u.Apply(&Usage{Model: "gpt-5.4"})
	if u.FastMode == nil || u.GetFastMode() {
		t.Fatalf("Apply with nil FastMode clobbered existing false: %+v", u)
	}
	u.Apply(&Usage{FastMode: proto.Bool(true)})
	if u.FastMode == nil || !u.GetFastMode() {
		t.Fatalf("Apply did not override FastMode to true: %+v", u)
	}
	u.Apply(&Usage{Effort: "high"})
	if u.FastMode == nil || !u.GetFastMode() {
		t.Fatalf("Apply with nil FastMode clobbered existing true: %+v", u)
	}
}
