package gui

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/protocol"
)

// An agent has rested once it has sat at its prompt, with nothing at work
// and nothing asked of the user, for as long as the settings say, its
// status and messages, not its screen's redraws, saying when it last did
// anything.
func TestRestedFor(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) *timestamppb.Timestamp { return timestamppb.New(now.Add(-d)) }
	rested := func() *protocol.SessionInfo {
		return &protocol.SessionInfo{Id: "a", Agent: "claude", ConversationId: "c", State: protocol.StateRunning,
			Status: protocol.AgentDone, StatusAt: ago(6 * time.Minute), LastOutput: ago(time.Second)}
	}
	if !restedFor(rested(), now, 5*time.Minute) {
		t.Fatal("an agent done six minutes ago has not rested five")
	}
	for name, change := range map[string]func(*protocol.SessionInfo){
		"recent":          func(s *protocol.SessionInfo) { s.StatusAt = ago(time.Minute) },
		"a recent reply":  func(s *protocol.SessionInfo) { s.LastMessage = ago(time.Minute) },
		"working":         func(s *protocol.SessionInfo) { s.Status = protocol.AgentWorking },
		"waiting":         func(s *protocol.SessionInfo) { s.Status = protocol.AgentWaiting },
		"an approval":     func(s *protocol.SessionInfo) { s.Approval = &protocol.Approval{} },
		"a task":          func(s *protocol.SessionInfo) { s.Tasks = []*protocol.BackgroundTask{{Running: true}} },
		"a shell":         func(s *protocol.SessionInfo) { s.Agent = "shell" },
		"no conversation": func(s *protocol.SessionInfo) { s.ConversationId = "" },
		"asleep":          func(s *protocol.SessionInfo) { s.State = protocol.StateStopped },
	} {
		s := rested()
		change(s)
		if restedFor(s, now, 5*time.Minute) {
			t.Errorf("%s: taken as rested", name)
		}
	}
	// With no status or message, its output says.
	s := rested()
	s.Status, s.StatusAt, s.LastOutput = "", nil, ago(10*time.Minute)
	if !restedFor(s, now, 5*time.Minute) {
		t.Fatal("quiet ten minutes with no status, not rested")
	}
}
