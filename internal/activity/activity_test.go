package activity

import (
	"testing"
	"time"

	"github.com/ironpark/gostty"
)

func TestProgressFollowsTheReports(t *testing.T) {
	var p Progress
	at := time.Unix(1000, 0)
	report := func(s gostty.ProgressState) {
		p.Report(gostty.Event{Kind: gostty.StreamEventProgressReport, ProgressState: s}, at)
	}
	if p.Busy(at) {
		t.Fatal("busy before any report")
	}
	report(gostty.ProgressStateIndeterminate)
	if !p.Busy(at.Add(time.Second)) {
		t.Fatal("not busy after an indeterminate report")
	}
	if p.Busy(at.Add(Stale)) {
		t.Fatal("a report never renewed still holds")
	}
	p.Report(gostty.Event{Kind: gostty.StreamEventTitleChanged}, at)
	report(gostty.ProgressStateSet)
	if !p.Busy(at) {
		t.Fatal("not busy at a percentage")
	}
	report(gostty.ProgressStateRemove)
	if p.Busy(at) {
		t.Fatal("still busy once the report is removed")
	}
}

func TestSnapshotPreservesExpiryAndInactiveReports(t *testing.T) {
	at := time.Unix(1000, 0)
	var source, restored Progress
	if source.Snapshot(at) != nil || source.Known() {
		t.Fatal("unreported state is known")
	}
	source.Report(gostty.Event{Kind: gostty.StreamEventProgressReport, ProgressState: gostty.ProgressStateSet, Progress: 42, HasProgress: true}, at)
	// The receiving machine can have an entirely different clock.
	local := at.Add(24 * time.Hour)
	report := source.Snapshot(at.Add(14 * time.Second))
	restored.Restore(report, local)
	if !restored.Known() || !restored.Busy(local) || restored.Busy(local.Add(time.Second)) || report.Value != 42 {
		t.Fatalf("restored: %+v", restored)
	}
	for _, state := range []gostty.ProgressState{gostty.ProgressStateRemove, gostty.ProgressStatePause, gostty.ProgressStateError} {
		source.Report(gostty.Event{Kind: gostty.StreamEventProgressReport, ProgressState: state}, at)
		restored.Restore(source.Snapshot(at), local)
		if !restored.Known() || restored.Busy(local) {
			t.Fatalf("inactive state %v became busy", state)
		}
	}
	restored.Restore(nil, local)
	if restored.Known() || restored.Busy(local) {
		t.Fatal("old peer snapshot did not reset progress")
	}
}
