// Package activity follows what a program says of its own work through the
// terminal's progress report, OSC 9;4, as Ghostty, iTerm2 and ConEmu show
// it: claude sends one while it thinks, as do other tools at work.
package activity

import (
	"time"

	"github.com/ironpark/gostty"
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
}

// Report records a stream event, if it is a progress report, at now.
func (p *Progress) Report(ev gostty.Event, now time.Time) {
	if ev.Kind == gostty.StreamEventProgressReport {
		p.state, p.at = ev.ProgressState, now
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
