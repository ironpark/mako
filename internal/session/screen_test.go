//go:build unix

package session

import (
	"testing"
	"time"

	"github.com/ironpark/mako/internal/detect"
	"github.com/ironpark/mako/internal/protocol"
)

// A turn's end the screen only suggests is confirmed before it is taken;
// one it shows outright, or work, is taken at once.
func TestTheScreensTurnEndIsConfirmed(t *testing.T) {
	now := time.Now()
	w := &screenWatch{state: detect.Working, at: now}
	gone := detect.Rule{}
	for i := range screenConfirms {
		w.observe(gone, now.Add(time.Duration(i)*screenRecheck))
		if w.state != detect.Working {
			t.Fatalf("reading %d took the spinner's end at once", i)
		}
	}
	if w.observe(gone, now.Add(screenConfirms*screenRecheck)); w.state != detect.Unknown || !w.pendingAt.IsZero() {
		t.Fatalf("confirmed end not taken: %+v", w)
	}

	w = &screenWatch{state: detect.Working, at: now}
	w.observe(gone, now)
	w.observe(detect.Rule{State: detect.Working}, now.Add(screenRecheck))
	if w.state != detect.Working || !w.pendingAt.IsZero() || !w.at.Equal(now) {
		t.Fatalf("a spinner back at work: %+v", w)
	}
	w.observe(detect.Rule{State: detect.Idle}, now.Add(2*screenRecheck))
	if w.state != detect.Idle || !w.visible {
		t.Fatalf("a prompt shown is not taken at once: %+v", w)
	}
	w.observe(detect.Rule{Hold: true}, now.Add(3*screenRecheck))
	if w.state != detect.Idle {
		t.Fatal("a screen that hides the state replaced it")
	}
}

// A dialog the screen shows since the hooks last reported makes the agent
// waiting, and keeps messages from being typed into it; one older than
// them does not.
func TestADialogOnScreenWaits(t *testing.T) {
	now := time.Now()
	s := &Session{state: protocol.StateRunning, status: protocol.AgentWorking, statusAt: now}
	s.screen = &screenWatch{scanned: true, state: detect.Blocked, visible: true, at: now.Add(time.Second)}
	if statusOf(s) != protocol.AgentWaiting {
		t.Fatal("a dialog newer than the hooks is not waiting")
	}
	s.status, s.statusAt = protocol.AgentIdle, now.Add(time.Second)
	if ok, _ := s.AtPrompt(); ok {
		t.Fatal("a dialog on screen is taken for a prompt")
	}
	s.statusAt = now.Add(2 * time.Second)
	if statusOf(s) != protocol.AgentIdle {
		t.Fatal("a dialog older than the hooks outweighed them")
	}
	if ok, _ := s.AtPrompt(); !ok {
		t.Fatal("the hooks' prompt is not one")
	}
	s.screen.visible = false
	s.statusAt = now
	if statusOf(s) != protocol.AgentIdle {
		t.Fatal("a dialog the screen only suggests outweighed the hooks")
	}
}

// An agent no hook reported on is what its screen shows, and at its
// prompt when that shows one.
func TestAnUnheardAgentIsWhatItsScreenShows(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	s.screen = &screenWatch{scanned: true, state: detect.Working, at: time.Now()}
	if ok, _ := s.AtPrompt(); statusOf(s) != protocol.AgentWorking || ok {
		t.Fatal("working on screen")
	}
	s.screen.state = detect.Idle
	if ok, _ := s.AtPrompt(); statusOf(s) != protocol.AgentIdle || !ok {
		t.Fatal("idle on screen")
	}
	s.screen.state = detect.Unknown
	if statusOf(s) != "" {
		t.Fatal("nothing on screen")
	}
}

// The screen ends a turn the hooks left at work once it has shown the
// prompt, since they last said work, for screenSettle.
func TestThePromptOnScreenEndsAnUnreportedTurn(t *testing.T) {
	now := time.Now()
	s := &Session{state: protocol.StateRunning, status: protocol.AgentWorking, statusAt: now}
	s.screen = &screenWatch{scanned: true, state: detect.Idle, visible: true, at: now.Add(time.Second)}
	s.screenEndsTurnLocked(now.Add(time.Second + screenSettle/2))
	if s.status != protocol.AgentWorking {
		t.Fatal("ended before the prompt settled")
	}
	s.screenEndsTurnLocked(now.Add(time.Second + screenSettle))
	if s.status != protocol.AgentIdle {
		t.Fatal("a settled prompt did not end the turn")
	}
	if s.ReportStatus(protocol.AgentWorking, s.workEndedAt) {
		t.Fatal("a hook from before the end revived the turn")
	}

	// A prompt shown before the hooks said work is the one the turn began at.
	s = &Session{state: protocol.StateRunning, status: protocol.AgentWorking, statusAt: now}
	s.screen = &screenWatch{scanned: true, state: detect.Idle, visible: true, at: now.Add(-time.Second)}
	if s.screenEndsTurnLocked(now.Add(time.Minute)); s.status != protocol.AgentWorking {
		t.Fatal("a prompt older than the turn ended it")
	}
}
