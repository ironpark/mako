package gui

import (
	"context"
	"slices"
	"time"

	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// With the setting on, an agent out of view that has rested a while is
// put to sleep: its node stops its program, freeing what it holds, and
// keeps the session, which starts again on its conversation when it is
// next shown. Resting is sitting at its prompt with nothing at work, its
// subagents, background tasks and the sessions it started included, and
// nothing asked of the user; the node checks again before it stops it,
// and refuses one another window is attached to.

// sleepCheck is how often the window looks for agents to put to sleep.
const sleepCheck = 30 * time.Second

// sleepRetry is how long the window waits to ask again of a session its
// node would not put to sleep.
const sleepRetry = 5 * time.Minute

// sleeps is what the window keeps of putting agents to sleep.
type sleeps struct {
	checked time.Time
	asking  map[string]bool      // asked, the answer not back
	retry   map[string]time.Time // refused, and when to ask again
}

// sleepIdle puts the agents to sleep that have rested out of view for as
// long as the settings say, once each sleepCheck.
func (m *model) sleepIdle(now time.Time) {
	p := ggui.Peek(m.Prefs)
	if !p.SleepIdle || now.Sub(m.sleeps.checked) < sleepCheck {
		return
	}
	m.sleeps.checked = now
	if m.sleeps.asking == nil {
		m.sleeps.asking, m.sleeps.retry = map[string]bool{}, map[string]time.Time{}
	}
	shown := map[string]bool{}
	for _, g := range ggui.Peek(m.Layout).groups() {
		shown[g.Active] = true
	}
	after := time.Duration(p.SleepAfter) * time.Minute
	for _, s := range ggui.Peek(m.Sessions) {
		id := s.GetId()
		if shown[id] || m.sleeps.asking[id] || now.Before(m.sleeps.retry[id]) || !restedFor(s, now, after) || !m.nodeHas(id, protocol.RevisionSleep) {
			continue
		}
		// The node refuses a session attached to, so the window lets go
		// of the terminal of a tab out of view first, and asks the next
		// time; showing the tab dials it again.
		if m.dropConn(id) {
			m.Attached.Set(slices.DeleteFunc(slices.Clone(ggui.Peek(m.Attached)), func(t string) bool { return t == id }))
			continue
		}
		m.sleeps.asking[id] = true
		req := &protocol.SleepSession{SessionId: id, NodeId: m.remoteID(s.GetNodeId())}
		fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
			return c.Sleep(ctx, req)
		}, func(_ *protocol.SessionInfo, err error) {
			delete(m.sleeps.asking, id)
			if err != nil {
				m.sleeps.retry[id] = time.Now().Add(sleepRetry)
			}
		})
	}
}

// restedFor reports whether the agent of session s has rested for after:
// running, with a conversation to resume, at its prompt with nothing at
// work and nothing asked of the user, since a status, message or output
// that long ago.
func restedFor(s *protocol.SessionInfo, now time.Time, after time.Duration) bool {
	switch {
	case s.GetState() != protocol.StateRunning, s.GetAgent() == "shell", s.GetConversationId() == "",
		protocol.AtWork(s.GetStatus()), s.GetBusy(), s.GetStatus() == protocol.AgentWaiting,
		s.GetApproval() != nil, s.GetDialog() != nil,
		slices.ContainsFunc(s.GetTasks(), (*protocol.BackgroundTask).GetRunning):
		return false
	}
	// An agent's screen may redraw at rest: its status and its messages
	// say when it last did anything, its output only for one with neither.
	var last time.Time
	for _, t := range []*timestamppb.Timestamp{s.GetStatusAt(), s.GetLastMessage()} {
		if t != nil && t.AsTime().After(last) {
			last = t.AsTime()
		}
	}
	if last.IsZero() {
		if s.GetLastOutput() == nil {
			return false
		}
		last = s.GetLastOutput().AsTime()
	}
	return now.Sub(last) >= after
}
