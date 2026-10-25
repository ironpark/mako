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
