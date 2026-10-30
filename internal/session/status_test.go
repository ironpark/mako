//go:build unix

package session

import (
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

func statusOf(s *Session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func TestStatusReportsAreOrderedAndRenewWork(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	s.ReportStatus(protocol.AgentWorking, 10)
	s.statusAt = time.Now().Add(-2 * staleWork)
	s.ReportStatus(protocol.AgentWorking, 20)
	if statusOf(s) != protocol.AgentWorking {
		t.Fatal("a repeated report did not renew work")
	}
	s.ReportStatus(protocol.AgentDone, 30)
	completedAt := s.statusAt
	for _, seq := range []int64{10, 20, 30} {
		if s.ReportStatus(protocol.AgentWorking, seq) || statusOf(s) != protocol.AgentDone {
			t.Fatal("a late hook revived completed work")
		}
	}
	s.ReportStatus(protocol.AgentDone, 35)
	if !s.statusAt.Equal(completedAt) {
		t.Fatal("duplicate completion produced another attention timestamp")
	}
	s.state = protocol.StateExited
	if s.ReportStatus(protocol.AgentWorking, 40) || statusOf(s) != "" {
		t.Fatal("an exited session accepted a hook")
	}
}

func TestStaleWorkDoesNotReviveOnRedraw(t *testing.T) {
	s := &Session{state: protocol.StateRunning, status: protocol.AgentWorking, statusAt: time.Now().Add(-2 * staleWork)}
	if statusOf(s) != protocol.AgentIdle {
		t.Fatal("stale work remained active")
	}
	s.lastOutput.Store(time.Now().UnixNano())
	if statusOf(s) != protocol.AgentIdle {
		t.Fatal("an unrelated redraw revived stale work")
	}
}

func TestProgressRemovalEndsAnInterruptedTurn(t *testing.T) {
	s, err := Start(Spec{ID: "interrupt", Command: []string{"/bin/cat"}, Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Now().UnixNano()
	s.ReportStatus(protocol.AgentWorking, at)
	s.feed([]byte("\x1b]9;4;3\x07"))
	s.feed([]byte("\x1b]9;4;0\x07"))
	if info := s.Info(); info.GetStatus() != protocol.AgentIdle || info.GetBusy() {
		t.Fatalf("interrupted: %+v", info)
	}
	if s.ReportStatus(protocol.AgentWorking, at+1) {
		t.Fatal("a delayed tool hook revived the cancelled turn")
	}
	// Completion may have been emitted just before the terminal removed its
	// progress; it must still be accepted as a successful turn's completion.
	if !s.ReportStatus(protocol.AgentDone, at+2) {
		t.Fatal("completion was lost to terminal ordering")
	}
	s.SetStatus(protocol.AgentWorking)
	if statusOf(s) != protocol.AgentWorking {
		t.Fatal("a fresh prompt did not start work")
	}
}

func TestCompletionWaitsForEveryChild(t *testing.T) {
	s := &Session{state: protocol.StateRunning, conversationID: "c"}
	base := time.Now().UnixNano()
	child := func(conversation, id string, running bool, seq int64) {
		s.ReportSubagent(conversation, id, running, base+seq)
	}
	s.ReportStatus(protocol.AgentWorking, 10)
	child("c", "a", true, 11)
	child("c", "b", true, 12)
	s.ReportStatus(protocol.AgentDone, 20)
	if statusOf(s) != protocol.AgentWorking {
		t.Fatal("parent completed while children were running")
	}
	child("c", "a", false, 21)
	if statusOf(s) != protocol.AgentWorking {
		t.Fatal("one child completed the whole session")
	}
	child("c", "b", false, 22)
	if statusOf(s) != protocol.AgentDone {
		t.Fatal("completion was not released after the final child")
	}
	child("c", "b", true, 12)
	child("other", "x", true, 30)
	s.ReportActivity("c", "", time.Time{}, time.Time{}, []*protocol.SubAgent{{Id: "a", Running: true}})
	if statusOf(s) != protocol.AgentDone {
		t.Fatal("a stale hook or log revived a stopped child")
	}
	child("c", "b", true, 31)
	s.ReportStatus(protocol.AgentWaiting, 32)
	if statusOf(s) != protocol.AgentWaiting {
		t.Fatal("children hid a request for the user")
	}
	s.ReportStatus(protocol.AgentWorking, 33)
	s.statusAt = time.Now().Add(-2 * staleWork)
	if statusOf(s) != protocol.AgentWorking {
		t.Fatal("quiet parent became idle while its child ran")
	}
	s.ReportStatus(protocol.AgentCompacting, 34)
	if statusOf(s) != protocol.AgentCompacting {
		t.Fatal("child activity hid parent compaction")
	}
	s.ReportStatus(protocol.AgentWorking, 35)
	child("c", "b", false, 34)
	if statusOf(s) != protocol.AgentWorking {
		t.Fatal("a child ending completed the parent's active turn")
	}
}

func TestAbandonedApprovalCannotOverwriteANewerTurn(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	s.ReportStatus(protocol.AgentWaiting, 10)
	changed, ok := s.BeginApproval(10, &protocol.Approval{Id: "old"})
	if !ok {
		t.Fatal("approval rejected")
	}
	s.ReportToolStatus(protocol.AgentWorking, 15)
	if s.approval.GetId() != "old" || statusOf(s) != protocol.AgentWaiting {
		t.Fatal("a concurrent tool withdrew a pending approval")
	}
	s.ReportStatus(protocol.AgentWorking, 20)
	select {
	case <-changed:
	default:
		t.Fatal("new turn did not withdraw approval")
	}
	s.ReportStatus(protocol.AgentWaiting, 30)
	s.BeginApproval(30, &protocol.Approval{Id: "new"})
	s.FinishApproval(10, "old", protocol.AgentIdle)
	if s.approval.GetId() != "new" || statusOf(s) != protocol.AgentWaiting {
		t.Fatal("old cleanup replaced a newer request")
	}
	s.FinishApproval(30, "new", protocol.AgentIdle)
	if s.approval != nil || statusOf(s) != protocol.AgentIdle {
		t.Fatal("abandoned approval stayed waiting")
	}
}

func TestLogStatusCannotReplaceANewerPromptOrConversation(t *testing.T) {
	now := time.Now()
	s := &Session{state: protocol.StateRunning, started: now, conversationID: "c"}
	s.ReportStatus(protocol.AgentWorking, now.Add(time.Second).UnixNano())
	s.ReportActivity("c", protocol.AgentIdle, now, time.Time{}, nil)
	s.ReportActivity("old", protocol.AgentDone, now.Add(2*time.Second), time.Time{}, []*protocol.SubAgent{{Id: "x", Running: true}})
	if statusOf(s) != protocol.AgentWorking || s.agentsWorking {
		t.Fatal("an old scan changed the current turn")
	}
	s.ReportActivity("c", protocol.AgentIdle, now.Add(3*time.Second), time.Time{}, nil)
	if statusOf(s) != protocol.AgentIdle {
		t.Fatal("log cancellation did not end work")
	}
}
