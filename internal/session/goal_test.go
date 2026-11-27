package session

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

func goalOf(s *Session) *protocol.GoalStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.goalLocked()
}

func TestSessionGoalLifecycleAndDeduplication(t *testing.T) {
	var changes atomic.Int32
	s := &Session{
		spec:  Spec{ID: "s1", Agent: "claude", OnChange: func() { changes.Add(1) }},
		state: protocol.StateRunning,
	}
	s.SetConversation("conv-1", "/tmp/conv-1.jsonl")

	// Mismatched conversation ID is ignored.
	s.ReportGoal("conv-other", &protocol.GoalStatus{
		Source:          "claude",
		Status:          protocol.GoalActive,
		Text:            "wrong conv",
		SourceSessionId: "conv-other",
		SourceRevision:  "r0",
	})
	if goalOf(s) != nil || changes.Load() != 0 {
		t.Fatalf("mismatched conversation goal should be ignored: %v changes=%d", goalOf(s), changes.Load())
	}

	// Matching conversation goal updates and notifies once.
	g1 := &protocol.GoalStatus{
		Source:          "claude",
		Status:          protocol.GoalActive,
		Text:            "ship feature",
		ObservedAt:      protocol.Time(time.Unix(1000, 0)),
		SourceSessionId: "conv-1",
		SourceRevision:  "r1",
		CanEdit:         true,
		CanClear:        true,
	}
	s.ReportGoal("conv-1", g1)
	if got := goalOf(s); got == nil || got.GetText() != "ship feature" || changes.Load() != 1 {
		t.Fatalf("expected goal set and 1 change, got %v changes=%d", got, changes.Load())
	}

	// Reporting the same goal with a newer ObservedAt does not re-trigger OnChange.
	g1Dup := &protocol.GoalStatus{
		Source:          "claude",
		Status:          protocol.GoalActive,
		Text:            "ship feature",
		ObservedAt:      protocol.Time(time.Unix(2000, 0)),
		SourceSessionId: "conv-1",
		SourceRevision:  "r1",
		CanEdit:         true,
		CanClear:        true,
	}
	s.ReportGoal("conv-1", g1Dup)
	if changes.Load() != 1 {
		t.Fatalf("duplicate semantic goal fired OnChange: changes=%d", changes.Load())
	}

	// Switching conversation clears goal and notifies.
	s.SetConversation("conv-2", "/tmp/conv-2.jsonl")
	if goalOf(s) != nil || changes.Load() != 2 {
		t.Fatalf("SetConversation did not clear goal: %v changes=%d", goalOf(s), changes.Load())
	}

	// Bridge goal updates and clears cleanly.
	s.ReportBridgeGoal(&protocol.GoalStatus{
		Source:          "codex",
		Status:          protocol.GoalActive,
		Text:            "codex objective",
		ProviderStatus:  "active",
		SourceSessionId: "th-1",
		SourceRevision:  "42",
	})
	if goalOf(s).GetText() != "codex objective" || changes.Load() != 3 {
		t.Fatalf("ReportBridgeGoal failed: %v changes=%d", goalOf(s), changes.Load())
	}
	s.ClearGoal()
	if goalOf(s) != nil || changes.Load() != 4 {
		t.Fatalf("ClearGoal failed: %v changes=%d", goalOf(s), changes.Load())
	}

	// Exited session ignores new goals and reports nil.
	s.ReportBridgeGoal(&protocol.GoalStatus{Source: "codex", Status: protocol.GoalActive, Text: "before exit"})
	s.mu.Lock()
	s.state = protocol.StateExited
	s.mu.Unlock()
	if goalOf(s) != nil {
		t.Fatalf("exited session reported goal: %v", goalOf(s))
	}
	before := changes.Load()
	s.ReportBridgeGoal(&protocol.GoalStatus{Source: "codex", Status: protocol.GoalActive, Text: "after exit"})
	if changes.Load() != before {
		t.Fatal("exited session accepted ReportBridgeGoal")
	}
}

func TestSetConversationPreservesGoalForTargetConversation(t *testing.T) {
	var changes atomic.Int32
	s := &Session{
		spec:  Spec{ID: "codex-1", Agent: "codex", OnChange: func() { changes.Add(1) }},
		state: protocol.StateRunning,
	}

	// 1. First-time "" -> thread-1 binding preserves a goal already observed for thread-1.
	s.ReportBridgeGoal(&protocol.GoalStatus{
		Source:          "codex",
		Status:          protocol.GoalActive,
		Text:            "pre-bind goal",
		SourceSessionId: "thread-1",
		SourceRevision:  "10",
	})
	if changes.Load() != 1 {
		t.Fatalf("expected 1 change, got %d", changes.Load())
	}
	s.SetConversation("thread-1", "/tmp/rollout-thread-1.jsonl")
	if got := goalOf(s); got == nil || got.GetText() != "pre-bind goal" || changes.Load() != 1 {
		t.Fatalf("first-time bind wiped goal for thread-1: got=%v changes=%d", got, changes.Load())
	}

	// 2. Switching thread-1 -> thread-2 preserves a goal already reported for thread-2.
	s.ReportBridgeGoal(&protocol.GoalStatus{
		Source:          "codex",
		Status:          protocol.GoalActive,
		Text:            "thread-2 goal",
		SourceSessionId: "thread-2",
		SourceRevision:  "20",
	})
	s.SetConversation("thread-2", "/tmp/rollout-thread-2.jsonl")
	if got := goalOf(s); got == nil || got.GetText() != "thread-2 goal" || changes.Load() != 2 {
		t.Fatalf("thread switch bind wiped goal for thread-2: got=%v changes=%d", got, changes.Load())
	}

	// 3. Switching thread-2 -> thread-3 clears a goal belonging to thread-2.
	s.SetConversation("thread-3", "/tmp/rollout-thread-3.jsonl")
	if got := goalOf(s); got != nil || changes.Load() != 3 {
		t.Fatalf("expected old thread-2 goal to clear on switch to thread-3: got=%v changes=%d", got, changes.Load())
	}

	// 4. A goal with an empty SourceSessionId is cleared when conversation changes.
	s.ReportBridgeGoal(&protocol.GoalStatus{
		Source: "codex",
		Status: protocol.GoalActive,
		Text:   "unscoped goal",
	})
	s.SetConversation("thread-4", "/tmp/rollout-thread-4.jsonl")
	if got := goalOf(s); got != nil {
		t.Fatalf("expected unscoped goal to clear on conversation change, got %v", got)
	}
}

func TestSetConversationCombinedAuthErrorAndGoalGuards(t *testing.T) {
	s := &Session{
		spec:  Spec{ID: "combo-1", Agent: "codex"},
		state: protocol.StateRunning,
	}

	// 1. Initial bind "" -> "conv-1" preserves BOTH pre-bind authError and matching goal.
	s.ReportAuthError(&protocol.AuthError{
		Kind:     protocol.AuthErrorOAuthExpired,
		Provider: "codex",
		Summary:  "Codex OAuth expired",
	}, 100)
	s.ReportBridgeGoal(&protocol.GoalStatus{
		Source:          "codex",
		Status:          protocol.GoalActive,
		Text:            "initial objective",
		SourceSessionId: "conv-1",
		SourceRevision:  "1",
	})
	s.SetConversation("conv-1", "/tmp/conv-1.jsonl")
	if ae := s.Info().GetAuthError(); ae == nil || ae.GetKind() != protocol.AuthErrorOAuthExpired {
		t.Fatalf("initial bind cleared pre-bind authError: %v", ae)
	}
	if g := s.Info().GetGoal(); g == nil || g.GetText() != "initial objective" {
		t.Fatalf("initial bind cleared matching pre-bind goal: %v", g)
	}

	// 2. Switching "conv-1" -> "conv-2" clears stale authError while preserving
	// a goal already tagged for "conv-2".
	s.ReportBridgeGoal(&protocol.GoalStatus{
		Source:          "codex",
		Status:          protocol.GoalActive,
		Text:            "conv-2 objective",
		SourceSessionId: "conv-2",
		SourceRevision:  "2",
	})
	s.SetConversation("conv-2", "/tmp/conv-2.jsonl")
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("conversation switch did not clear stale authError: %v", ae)
	}
	if g := s.Info().GetGoal(); g == nil || g.GetText() != "conv-2 objective" {
		t.Fatalf("conversation switch wiped target conv-2 goal: %v", g)
	}

	// 3. Switching "conv-2" -> "conv-3" clears both authError and conv-2 goal.
	s.ReportAuthError(&protocol.AuthError{
		Kind:     protocol.AuthErrorProviderAuth,
		Provider: "codex",
		Summary:  "Invalid API key",
	}, time.Now().UnixNano()+int64(time.Second))
	s.SetConversation("conv-3", "/tmp/conv-3.jsonl")
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("second conversation switch did not clear authError: %v", ae)
	}
	if g := s.Info().GetGoal(); g != nil {
		t.Fatalf("second conversation switch did not clear stale conv-2 goal: %v", g)
	}
}

func TestSessionRestartFailureClearsGoalAndAuthError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	s, err := Start(Spec{
		ID:      "restart-fail-clear",
		Agent:   "codex",
		Project: t.TempDir(),
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Cols:    40,
		Rows:    10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.ReportBridgeGoal(&protocol.GoalStatus{
		Source:          "codex",
		Status:          protocol.GoalActive,
		Text:            "active before restart failure",
		SourceSessionId: "th-1",
		SourceRevision:  "1",
	})
	s.ReportAuthError(&protocol.AuthError{
		Kind:     protocol.AuthErrorOAuthExpired,
		Provider: "codex",
		Summary:  "Codex OAuth expired",
	}, time.Now().UnixNano())

	if s.Info().GetGoal() == nil || s.Info().GetAuthError() == nil {
		t.Fatalf("expected goal and authError active before restart failure")
	}

	restartErr := s.Restart(func(ctx context.Context) ([]string, map[string]string, error) {
		return nil, nil, errors.New("restart backend failed")
	})
	if restartErr == nil {
		t.Fatal("expected Restart error")
	}
	<-s.Done()

	info := s.Info()
	if info.GetState() != protocol.StateExited {
		t.Fatalf("state = %q, want exited", info.GetState())
	}
	if info.GetGoal() != nil {
		t.Fatalf("goal survived failed restart exit: %v", info.GetGoal())
	}
	if info.GetAuthError() != nil {
		t.Fatalf("authError survived failed restart exit: %v", info.GetAuthError())
	}
	s.mu.Lock()
	rawGoal := s.goal
	s.mu.Unlock()
	if rawGoal != nil {
		t.Fatalf("internal s.goal was not cleared on restart-failure exit: %v", rawGoal)
	}
}
