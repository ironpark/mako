package session

import (
	"time"

	"github.com/ironpark/mako/internal/detect"
	"github.com/ironpark/mako/internal/protocol"
)

// An agent's hooks say what it did when it ran them; its screen says what
// it shows now. The screen is read for what no hook tells: a dialog it
// asks in, which shows it waiting while the screen is newer than the
// hooks, and the prompt it waits at once a turn ended unreported, as an
// interrupted one does. An agent no hook has reported on is what its
// screen shows.

const (
	// screenTick is how often a screen that changed is read again.
	screenTick = 300 * time.Millisecond
	// screenRecheck is how soon a turn's end is read again to be sure of.
	screenRecheck = 100 * time.Millisecond
	// A turn's end the screen only suggests, a spinner gone with no prompt
	// shown, holds for screenConfirms readings or screenConfirmCap, as a
	// redraw between two frames shows none.
	screenConfirms   = 3
	screenConfirmCap = 700 * time.Millisecond
	// screenGrace is how long a starting agent's screen is not read: what
	// it draws first is the shell's and its splash.
	screenGrace = 3 * time.Second
	// screenSettle is how long the screen shows a prompt, read after the
	// hooks last said work, before the turn is taken to have ended.
	screenSettle = 1500 * time.Millisecond
)

// screenWatch is what an agent's screen last showed; guarded by its
// session's mu.
type screenWatch struct {
	rules *detect.Rules

	scanned bool
	seen    uint64 // the session's outputs when last read
	title   string // and its title

	state   detect.State
	visible bool      // shown outright, as a dialog or a prompt is
	at      time.Time // when the screen came to show state

	pendingAt time.Time // when a turn's end began to be confirmed
	confirms  int

	dialog *protocol.ScreenDialog // the dialog of options it waits on, if any
}

// watchScreen reads the screen as it changes until the agent exits.
func (s *Session) watchScreen() {
	t := time.NewTimer(screenTick)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		t.Reset(s.scanScreen(time.Now()))
	}
}

// scanScreen reads the screen, if it changed or a turn's end is being
// confirmed, and ends a turn it shows over; it is how long until the next.
func (s *Session) scanScreen(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.screen
	if s.state != protocol.StateRunning || now.Sub(s.started) < screenGrace {
		return screenTick
	}
	pending := !w.pendingAt.IsZero()
	if n := s.outputs.Load(); pending || !w.scanned || n != w.seen || s.title != w.title {
		text, err := s.plainTextLocked()
		if err != nil {
			return screenTick
		}
		w.scanned, w.seen, w.title = true, n, s.title
		was, wasVisible := w.state, w.visible
		w.observe(w.rules.Detect(detect.Input{Screen: text, Title: s.title}), now)
		if s.status == "" && was == detect.Working && w.state == detect.Idle {
			s.turns++ // a turn no hook told of, as the screen shows it
		}
		if dialog := s.readDialogLocked(); dialog || w.state != was || w.visible != wasVisible {
			s.changedLocked()
		}
	}
	s.screenEndsTurnLocked(now)
	if !w.pendingAt.IsZero() {
		return screenRecheck
	}
	return screenTick
}

// observe takes what the screen shows: at once when it shows it outright
// or the agent at work, after confirming when a turn's end is only
// suggested; not at all when the screen hides the agent's state.
func (w *screenWatch) observe(r detect.Rule, now time.Time) {
	if r.Hold {
		return
	}
	if w.state == detect.Working && r.State != detect.Working && (r.Suggests || r.State == detect.Unknown) {
		if w.pendingAt.IsZero() {
			w.pendingAt, w.confirms = now, 0
			return
		}
		if w.confirms++; w.confirms < screenConfirms && now.Sub(w.pendingAt) < screenConfirmCap {
			return
		}
	}
	w.pendingAt, w.confirms = time.Time{}, 0
	if r.State != w.state {
		w.state, w.at = r.State, now
	}
	w.visible = !r.Suggests && r.State != detect.Unknown
}

// screenEndsTurnLocked ends a turn the hooks left at work once the screen
// has shown the agent at its prompt, since after they last said work, for
// screenSettle: an interrupted turn runs no Stop hook.
func (s *Session) screenEndsTurnLocked(now time.Time) {
	w := s.screen
	if w.state != detect.Idle || !w.visible || !protocol.AtWork(s.status) || s.approval != nil {
		return
	}
	if w.at.After(s.statusAt) && now.Sub(w.at) >= screenSettle {
		s.endWorkLocked(now)
	}
}

// screenStatusLocked is what the screen says over the hooks, and since
// when: waiting at a dialog shown since they last reported; for an agent
// they never reported on, whatever it shows. ok is false where the hooks
// say.
func (s *Session) screenStatusLocked() (status protocol.AgentStatus, at time.Time, ok bool) {
	w := s.screen
	if w == nil {
		return "", time.Time{}, false
	}
	unheard := s.status == ""
	switch {
	case w.state == detect.Blocked && (w.visible || unheard) && !w.at.Before(s.statusAt):
		return protocol.AgentWaiting, w.at, true
	case unheard && w.state == detect.Working:
		return protocol.AgentWorking, w.at, true
	case unheard && w.state == detect.Idle:
		return protocol.AgentIdle, w.at, true
	}
	return "", time.Time{}, false
}
