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
