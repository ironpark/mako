//go:build unix

package session

import (
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// pubOf is what the session last published.
func pubOf(s *Session) published {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pub
}

// statusOf is what the session makes of its evidence now, as its timer
// or the next report would.
func statusOf(s *Session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recomputeLocked(time.Now())
	status := s.pub.status
	return status
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

// A background task the log says still runs, but which started before
// this process did, was the last process's: it is stopped. The tasks go
// with the session's end.
func TestBackgroundTasksOfAnEarlierProcessAreStopped(t *testing.T) {
	started := time.Unix(1000, 0)
	s := &Session{state: protocol.StateRunning, conversationID: "c", started: started}
	s.ReportTasks("c", []*protocol.BackgroundTask{{Id: "old", Running: true, Started: 900}, {Id: "new", Running: true, Started: 1100}})
	s.ReportTasks("other", nil)
	tasks := s.tasksLocked()
	if len(tasks) != 2 || tasks[0].GetRunning() || tasks[0].GetStatus() != protocol.TaskStopped || !tasks[1].GetRunning() {
		t.Fatalf("tasks %v", tasks)
	}
	s.state = protocol.StateExited
	if s.tasksLocked() != nil {
		t.Fatal("an ended session kept its tasks")
	}
}

// Work gone quiet ends as time passes, announced as any change is, and
// numbered: not only once something reads the status.
func TestTimeAloneAnnouncesItsChanges(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	s.ReportStatus(protocol.AgentWorking, 10)
	s.mu.Lock()
	s.statusAt = time.Now().Add(-2 * staleWork)
	seq := s.pub.seq
	status := s.pub.status
	s.mu.Unlock()
	if status != protocol.AgentWorking {
		t.Fatal("reading the status changed it")
	}
	changed := s.Changed()
	s.mu.Lock()
	s.recomputeLocked(time.Now())
	s.mu.Unlock()
	select {
	case <-changed:
	default:
		t.Fatal("work gone quiet ended unannounced")
	}
	if p := pubOf(s); p.status != protocol.AgentIdle || p.seq != seq+1 || p.turns != 1 {
		t.Fatalf("after quiet: %+v", p)
	}
}

// A turn done and a request for the user ask for attention, numbered;
// saying a status again, or work, does not. What the user saw is the
// session's, whichever front-end said it.
func TestAttentionIsNumberedAndSeen(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	attention := func() (uint64, uint64) {
		p := pubOf(s)
		return p.attention, p.seen
	}
	s.ReportStatus(protocol.AgentWorking, 10)
	if a, _ := attention(); a != 0 {
		t.Fatal("work asked for attention")
	}
	s.ReportStatus(protocol.AgentDone, 20)
	done, _ := attention()
	if done == 0 {
		t.Fatal("a turn done asked for no attention")
	}
	s.ReportStatus(protocol.AgentDone, 25)
	if a, _ := attention(); a != done {
		t.Fatal("a completion said again asked again")
	}
	if !s.See(done+100) || s.See(done) {
		t.Fatal("seeing it was not news once")
	}
	if a, seen := attention(); seen != done || a != done {
		t.Fatalf("seen %d of %d, want all of it", seen, a)
	}
	s.ReportStatus(protocol.AgentWaiting, 30)
	s.BeginApproval(30, &protocol.Approval{Id: "a"})
	asked, seen := attention()
	if asked <= done || seen != done {
		t.Fatal("a request did not ask anew")
	}
	s.HoldApproval(&protocol.Approval{Id: "b"})
	if a, _ := attention(); a <= asked {
		t.Fatal("another request did not ask anew")
	}
}

// A turn ends once its subagents have: a parent done while one still
// works has not ended its turn.
func TestATurnEndsWithItsLastChild(t *testing.T) {
	s := &Session{state: protocol.StateRunning, conversationID: "c"}
	base := time.Now().UnixNano()
	s.ReportStatus(protocol.AgentWorking, base)
	s.ReportSubagent("c", "a", true, base+1)
	s.ReportStatus(protocol.AgentDone, base+2)
	if s.Turns() != 0 || pubOf(s).attention != 0 {
		t.Fatal("the turn ended while a child worked")
	}
	s.ReportSubagent("c", "a", false, base+3)
	if p := pubOf(s); p.turns != 1 || p.status != protocol.AgentDone || p.attention == 0 {
		t.Fatalf("the last child's end did not end the turn: %+v", p)
	}
}

// A tool's end says work goes on, or resumes from a request answered in
// the terminal, but never revives a turn that ended: a tool an
// interruption left running ends after the turn.
func TestAToolsEndNeverRevivesAnEndedTurn(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	s.ReportStatus(protocol.AgentWorking, 10)
	if !s.ReportToolDone(20) {
		t.Fatal("a tool's end did not renew work")
	}
	s.ReportStatus(protocol.AgentWaiting, 30)
	if !s.ReportToolDone(40) || statusOf(s) != protocol.AgentWorking {
		t.Fatal("a request answered in the terminal did not resume work")
	}
	s.ReportStatus(protocol.AgentIdle, 50) // interrupted
	if s.ReportToolDone(60) || statusOf(s) != protocol.AgentIdle {
		t.Fatal("a tool ending after the interruption revived the turn")
	}
	s.ReportStatus(protocol.AgentDone, 70)
	if s.ReportToolDone(80) || statusOf(s) != protocol.AgentDone {
		t.Fatal("a tool's end revived a turn done")
	}
}
