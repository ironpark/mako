//go:build unix

package session

import (
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// A turn ends where work goes idle or done: by the hooks, an approval
// answered, or the screen.
func TestTurnsAreCounted(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	s.SetStatus(protocol.AgentIdle)
	s.SetStatus(protocol.AgentWorking)
	s.SetStatus(protocol.AgentWaiting)
	if s.Turns() != 0 {
		t.Fatal("a turn ended on asking the user")
	}
	s.SetStatus(protocol.AgentWorking)
	s.SetStatus(protocol.AgentDone)
	s.SetStatus(protocol.AgentDone)
	if s.Turns() != 1 {
		t.Fatalf("turns %d after one", s.Turns())
	}
	s.SetStatus(protocol.AgentCompacting)
	s.mu.Lock()
	s.endWorkLocked(time.Now())
	s.mu.Unlock()
	if s.Turns() != 2 {
		t.Fatalf("turns %d after a turn ended unreported", s.Turns())
	}
}

// Keys sent after a paste come a moment after it, not in its burst.
func TestKeysWaitOnAPaste(t *testing.T) {
	s, err := Start(Spec{ID: "send", Command: []string{"sh", "-c", "stty raw -echo; od -c"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := s.Send("text", []byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < pasteGap {
		t.Fatal("the keys went with the paste")
	}
}
