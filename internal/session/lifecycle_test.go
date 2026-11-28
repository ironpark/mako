package session

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A turn's end is told once, though the Stop hook and Codex's app server
// both report it, in either order, or a hook repeats; a stale end of the
// turn before is no end of this one.
func TestATurnsEndIsToldOnce(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	s.ReportStatus(protocol.AgentWorking, 10)
	if !s.ReportTurnEnd(20) {
		t.Fatal("the turn's end was not told")
	}
	for _, seq := range []int64{15, 20, 25} { // the other reporter's, older or newer; a repeat
		if s.ReportTurnEnd(seq) {
			t.Fatalf("the turn's end was told again at %d", seq)
		}
	}
	if s.Info().GetStatus() != protocol.AgentDone {
		t.Fatal("the turn did not end")
	}

	// The next turn's end is news; a late report of the last one's is not.
	s.ReportStatus(protocol.AgentWorking, 30)
	if s.ReportTurnEnd(28) {
		t.Fatal("a report older than the turn told its end")
	}
	if !s.ReportTurnEnd(40) {
		t.Fatal("the next turn's end was not told")
	}

	// Reported first by another, as Codex's app server does when the hook
	// comes second with an older seq, it is still told once.
	s.ReportStatus(protocol.AgentWorking, 50)
	if !s.ReportTurnEnd(60) || s.ReportTurnEnd(55) {
		t.Fatal("one end, told by two, was not told once")
	}

	// Its log saying the turn ended first tells no one: the Stop hook does.
	s.ReportStatus(protocol.AgentWorking, 70)
	s.ReportStatus(protocol.AgentDone, 80)
	if !s.ReportTurnEnd(79) {
		t.Fatal("the Stop hook after the log did not tell the turn's end")
	}

	s.ReportStatus(protocol.AgentWorking, 90)
	s.state = protocol.StateExited
	if s.ReportTurnEnd(100) {
		t.Fatal("an exited session told a turn's end")
	}
}

// The end of the agent's session is told once for each conversation it
// starts, by the first report of it newer than the start; and, when the
// program exits on its own with no end told, by EndUntold, once.
func TestASessionsEndIsToldOncePerConversation(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	s.ReportConversationStart(10)
	if s.ReportSessionEnd(5) {
		t.Fatal("an end older than the conversation was told")
	}
	if !s.ReportSessionEnd(20) {
		t.Fatal("the end was not told")
	}
	if s.ReportSessionEnd(30) || s.ReportSessionEnd(20) {
		t.Fatal("a repeated end was told")
	}
	// Claude's /clear ends one conversation and starts another.
	s.ReportConversationStart(40)
	if !s.ReportSessionEnd(50) {
		t.Fatal("the next conversation's end was not told")
	}
	s.state = protocol.StateExited
	if s.EndUntold() {
		t.Fatal("an end told by the agent was told again as its program exited")
	}

	// A program that exits without its agent telling its end, as agy and
	// opencode do, tells it once.
	quiet := &Session{state: protocol.StateExited}
	if !quiet.EndUntold() || quiet.EndUntold() {
		t.Fatal("an untold end was not told once")
	}
	if quiet.ReportSessionEnd(60) {
		t.Fatal("an exited session told an end")
	}
	// One mako closed, or put to sleep, tells none.
	closed := &Session{state: protocol.StateExited, closing: true}
	if closed.EndUntold() {
		t.Fatal("a session mako closed told its end")
	}
}
