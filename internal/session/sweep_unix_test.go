//go:build darwin || linux

package session

import (
	"os"
	"testing"
	"time"
)

// Closing a session ends what its agent left running, however it hid from
// the hang-up.
func TestClosingEndsWhatTheAgentStarted(t *testing.T) {
	s, err := Start(Spec{ID: "sweep", Command: []string{"sh", "-c", "nohup sleep 60 >/dev/null 2>&1 & sleep 60"}})
	if err != nil {
		t.Fatal(err)
	}
	leader := s.proc.pid()
	deadline := time.Now().Add(3 * time.Second)
	for len(swept(s.scope, leader)) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	found := swept(s.scope, leader)
	if len(found) < 2 {
		_ = s.Close()
		t.Fatalf("%d of the session's processes found", len(found))
	}
	_ = s.Close()
	deadline = time.Now().Add(3 * sweepStep)
	for len(swept(s.scope, leader)) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if pids := swept(s.scope, leader); len(pids) > 0 {
		t.Fatalf("left running: %v", pids)
	}
	if swept("other", os.Getpid()) != nil {
		t.Fatal("another session's processes found")
	}
}

func TestHasEntry(t *testing.T) {
	want := []byte("MAKO_SCOPE=a\x00")
	for _, c := range []struct {
		block string
		has   bool
	}{
		{"MAKO_SCOPE=a\x00", true},
		{"x\x00MAKO_SCOPE=ab\x00MAKO_SCOPE=a\x00", true},
		{"XMAKO_SCOPE=a\x00", false},
		{"MAKO_SCOPE=ab\x00", false},
	} {
		if got := hasEntry([]byte(c.block), want); got != c.has {
			t.Errorf("%q: %v, want %v", c.block, got, c.has)
		}
	}
}
