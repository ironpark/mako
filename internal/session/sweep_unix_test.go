//go:build darwin || linux

package session

import (
	"os"
	"path/filepath"
	"strings"
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

// An agent that exits leaving a process holding its terminal, one the
// sweep cannot find, still ends: it is waited for as it exits, what it
// printed last is read, and Done closes without the session being closed.
// macOS revokes the terminal as its leader exits, so there only Linux's
// case is not.
func TestAnAgentEndsThoughItLeftTheTerminalHeld(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	script := `(trap '' HUP; unset MAKO_SCOPE
if command -v setsid >/dev/null; then exec setsid sh -c "touch '` + ready + `'; exec sleep 8"; fi
touch '` + ready + `'; exec sleep 8) &
while [ ! -e '` + ready + `' ]; do sleep 0.05; done; printf bye; exit 3`
	s, err := Start(Spec{ID: "held", Command: []string{"sh", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-s.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("the session did not end")
	}
	info := s.Info()
	if info.GetExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", info.GetExitCode())
	}
	if snap, _ := s.Snapshot(false); !strings.Contains(snap.GetText(), "bye") {
		t.Errorf("screen = %q, want what the agent printed", snap.GetText())
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
