package session

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// Statuses set in a row hold in order however close they come: the wall
// clock may read the same for both.
func TestStatusesSetInARowHoldInOrder(t *testing.T) {
	s := &Session{state: protocol.StateRunning}
	for range 1000 {
		s.SetStatus(protocol.AgentWorking)
		s.SetStatus(protocol.AgentDone)
		if at, _ := s.AtPrompt(); !at {
			t.Fatal("a status set right after another was dropped")
		}
	}
}
