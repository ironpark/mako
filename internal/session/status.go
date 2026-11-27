package session

import (
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// A session's status is made in one place. What tells of the agent only
// records its evidence: its hooks (ReportStatus, ordered by when each ran),
// its log (ReportActivity, by when each record was written), its approvals,
// its subagents, its screen (scanScreen) and its terminal's progress
// (feed). Then recomputeLocked works out from all of it what the agent is
// doing, and publishes that, numbered, when it changed. Reading a status
// changes nothing: what time alone changes, work gone quiet or a subagent
// whose hooks fell silent, a timer recomputes, and announces as any other
// change.
//
// The hooks, the log and the daemon's own observations are ordered on one
// clock, the wall clock of the session's node, which runs them all. Of
// them, the newest report says what the agent reported; an end of work
// the terminal or the screen showed, or the quiet after a minute, refuses
// work reported from before it; then, over what was reported:
//
//  1. an ended session has no status;
//  2. an approval held for the user is waiting;
//  3. a dialog the screen shows, since the reports last said anything, is
//     waiting; work it shows outright for a second since they said a turn
//     ended, or asked, is working; an agent never reported on is what its
//     screen shows;
//  4. a turn reported done or idle while its subagents work is working;
//  5. otherwise what was reported.

// published is what recomputeLocked last made of a session's evidence.
type published struct {
	status    protocol.AgentStatus
	at        time.Time
	approval  string              // the ID of the approval held, "" for none
	authError *protocol.AuthError // active provider auth failure, nil for none

	// seq numbers the changes to status, approval, and authError; attention
	// is the seq of the last that asks for the user: a turn done, a request
	// for an answer, or a provider authentication failure. seen is the
	// attention the user has looked at, as a front-end says with See. A
	// front-end shows the session unread while attention is past seen, and
	// tells the user of each attention it had not seen before.
	seq, attention, seen uint64

	turns  uint64 // turns ended: work that went idle or done
	inTurn bool   // at work since the last turn ended
}

// timeTick is how often a running session's status is recomputed for
// what time alone changes.
const timeTick = time.Second

// watchTime recomputes the status as time passes, until the agent exits,
// for a session whose screen is not read: watchScreen, which recomputes on
// every reading, does it for the rest.
func (s *Session) watchTime() {
	t := time.NewTicker(timeTick)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case now := <-t.C:
			s.mu.Lock()
			if s.state == protocol.StateRunning {
				s.recomputeLocked(now)
			}
			s.mu.Unlock()
		}
	}
}

// recomputeLocked works out the status from the evidence, as of now, and
// publishes it, announced, when it changed.
func (s *Session) recomputeLocked(now time.Time) {
	s.updateChildrenLocked(now)
	// The ends of turns no report told of, latched, so that a redraw after
	// cannot revive them: the prompt the screen shows, and work reported,
	// then a minute without a word or a redraw.
	if s.screen != nil {
		s.screenEndsTurnLocked(now)
	}
	if !s.agentsWorking && protocol.AtWork(s.status) && now.Sub(s.LastOutput()) > staleWork && now.Sub(s.statusAt) > staleWork {
		s.endWorkLocked(now)
	}
	status, at := s.deriveLocked(now, true)
	approval := s.approval.GetId()
	authErr := s.authError
	if s.state == protocol.StateExited {
		authErr = nil
	}
	p := &s.pub
	if status == p.status && approval == p.approval && authErrorEqual(authErr, p.authError) {
		p.at = at // renewed evidence of the same: no change to tell
		return
	}
	prev, prevApproval, prevAuthErr := p.status, p.approval, p.authError
	p.status, p.at, p.approval, p.authError = status, at, approval, authErr
	p.seq++
	switch {
	case protocol.AtWork(status):
		p.inTurn = true
	case status != protocol.AgentWaiting && p.inTurn:
		p.turns++
		p.inTurn = false
	}
	authAttention := authErr != nil && !authErrorEqual(authErr, prevAuthErr)
	if asks := status == protocol.AgentWaiting && (prev != protocol.AgentWaiting || approval != prevApproval && approval != ""); asks ||
		status == protocol.AgentDone && prev != protocol.AgentDone || authAttention {
		p.attention = p.seq
	}
	s.changedLocked()
}

// authErrorEqual compares two AuthErrors by their user-visible classification
// and guidance, ignoring DetectedAt so duplicate reports of the same failure
// do not spam state_seq or attention notifications.
func authErrorEqual(a, b *protocol.AuthError) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.GetKind() == b.GetKind() &&
		a.GetProvider() == b.GetProvider() &&
		a.GetSummary() == b.GetSummary() &&
		a.GetGuidance() == b.GetGuidance() &&
		a.GetCommand() == b.GetCommand()
}

// deriveLocked is what the evidence says the agent is doing, and since
// when, by the order at the head of this file; its subagents left out
// unless subagents.
func (s *Session) deriveLocked(now time.Time, subagents bool) (protocol.AgentStatus, time.Time) {
	switch {
	case s.state == protocol.StateExited:
		return "", s.statusAt
	case s.approval != nil:
		return protocol.AgentWaiting, s.statusAt
	}
	if status, at, ok := s.screenStatusLocked(now); ok {
		return status, at
	}
	if subagents && s.agentsWorking && s.status != protocol.AgentWaiting && !protocol.AtWork(s.status) {
		return protocol.AgentWorking, s.statusAt
	}
	return s.status, s.statusAt
}

// See records that the user has looked at the session's attention up to
// seq, as a front-end showing it says, and reports whether that is news.
// A seq past the attention is taken as the attention: the user saw what
// there was.
func (s *Session) See(seq uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq = min(seq, s.pub.attention)
	if seq <= s.pub.seen {
		return false
	}
	s.pub.seen = seq
	s.changedLocked()
	return true
}
