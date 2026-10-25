package session

import (
	"strings"
	"testing"
	"time"
)

// An exited session keeps the screen its agent left, so it can still be
// attached to and read, and resizing it is harmless.
func TestAnExitedSessionKeepsItsScreen(t *testing.T) {
	s, err := Start(Spec{ID: "s", Command: []string{"/bin/sh", "-c", "echo goodbye"}, Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not exit")
	}
	for _, styled := range []bool{false, true} {
		snap, err := s.Snapshot(styled)
		if err != nil || !strings.Contains(snap.Text, "goodbye") {
			t.Fatalf("snapshot (styled %v) = %q, %v", styled, snap.Text, err)
		}
	}
	if err := s.Resize(80, 24); err != nil {
		t.Fatalf("resizing an exited session: %v", err)
	}
}

// A client attaching after the initial OSC must receive the program's title,
// including after the process has exited and its terminal has been released.
func TestSnapshotAndInfoKeepTerminalTitle(t *testing.T) {
	s, err := Start(Spec{ID: "title", Command: []string{"/bin/sh", "-c", "printf '\033]2;first\007\033]2;작업 제목\007body'"}, Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	s.SetName("custom name")
	if info := s.Info(); info.Title != "작업 제목" || info.Name != "custom name" {
		t.Fatalf("info: %+v", info)
	}
	for _, styled := range []bool{false, true} {
		snap, err := s.Snapshot(styled)
		if err != nil || snap.Title != "작업 제목" || !strings.Contains(snap.Text, "body") {
			t.Fatalf("snapshot styled=%v title=%q, text=%q, err=%v", styled, snap.Title, snap.Text, err)
		}
	}
}

func TestPTYProgressIsIncludedInReplayAndClearedOnExit(t *testing.T) {
	s, err := Start(Spec{ID: "progress", Command: []string{"/bin/sh", "-c", "printf '\033]9;4;3\007'; read answer"}, Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !s.Info().Busy {
		if time.Now().After(deadline) {
			t.Fatal("PTY progress was not received")
		}
		time.Sleep(time.Millisecond)
	}
	snap, err := s.Snapshot(true)
	if err != nil || snap.Progress == nil || snap.Progress.State != 3 || snap.Progress.RemainingMS <= 0 {
		t.Fatalf("snapshot progress=%+v err=%v", snap.Progress, err)
	}
	if _, err := s.Write([]byte("done\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit")
	}
	if s.Info().Busy {
		t.Fatal("exited process still busy")
	}
	snap, err = s.Snapshot(true)
	if err != nil || snap.Progress != nil {
		t.Fatalf("exited progress=%+v err=%v", snap.Progress, err)
	}
}
