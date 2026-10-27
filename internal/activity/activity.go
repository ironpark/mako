// Package activity follows what a program says of its own work through the
// terminal's progress report, OSC 9;4, as Ghostty, iTerm2 and ConEmu show
// it: claude sends one while it thinks, as do other tools at work.
package activity

import (
	"time"

	"github.com/ironpark/gostty"
	"github.com/ironpark/mako/internal/protocol"
)

// Stale is how long a report holds without another, as Ghostty clears its
// progress bar: a program that ended without removing its report is not
// left at work.
const Stale = 15 * time.Second

// Progress is the last progress report a terminal had. The zero value is
// none. It is not safe for concurrent use; its terminal's lock guards it.
type Progress struct {
	state gostty.ProgressState
	at    time.Time
	value int16
	known bool
}

// Report records a stream event, if it is a progress report, at now.
func (p *Progress) Report(ev gostty.Event, now time.Time) {
	if ev.Kind == gostty.StreamEventProgressReport {
		p.state, p.at, p.value, p.known = ev.ProgressState, now, int16(ev.Progress), true
		if !ev.HasProgress {
			p.value = -1
		}
	}
}

// Busy reports whether the program last said it is at work, with or
// without a percentage, and said so within Stale of now. An error or a
// pause is not work.
func (p *Progress) Busy(now time.Time) bool {
	switch p.state {
	case gostty.ProgressStateSet, gostty.ProgressStateIndeterminate:
		return now.Sub(p.at) < Stale
	}
	return false
}

// Known distinguishes an explicit inactive report from no report yet.
func (p *Progress) Known() bool { return p.known }

// Snapshot preserves both inactive states and the remaining timeout.
func (p *Progress) Snapshot(now time.Time) *protocol.ProgressReport {
	if !p.known {
		return nil
	}
	remaining := max(time.Duration(0), p.at.Add(Stale).Sub(now))
	return &protocol.ProgressReport{State: uint32(p.state), Value: int32(p.value), RemainingMs: remaining.Milliseconds()}
}

// Restore replaces local progress when attaching or resynchronizing a screen.
func (p *Progress) Restore(report *protocol.ProgressReport, now time.Time) {
	*p = Progress{}
	if report == nil || report.GetState() > uint32(gostty.ProgressStatePause) {
		return
	}
	remaining := time.Duration(min(max(report.GetRemainingMs(), 0), Stale.Milliseconds())) * time.Millisecond
	p.state, p.value, p.known = gostty.ProgressState(report.GetState()), int16(report.GetValue()), true
	p.at = now.Add(remaining - Stale)
}
