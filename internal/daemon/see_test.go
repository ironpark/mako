package daemon

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A front-end on one node says the user has seen a session of another,
// which that node keeps: every front-end, on either node, then lists it
// seen.
func TestASessionSeenIsSeenOnEveryNode(t *testing.T) {
	a := meshDaemon(t, "see-a")
	b := meshDaemon(t, "see-b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	addSession(t, b, "far")
	s, _ := b.sessions.get("far")
	s.SetStatus(protocol.AgentWorking)
	s.SetStatus(protocol.AgentDone)
	attention := s.Info().GetAttentionSeq()
	if attention == 0 || s.Info().GetSeenSeq() != 0 {
		t.Fatalf("a turn done: %+v", s.Info())
	}

	c := dialDaemon(t, a)
	listed := func() *protocol.SessionInfo {
		list, err := c.Sessions(t.Context())
		if err != nil {
			return nil
		}
		if i := slices.IndexFunc(list, func(s *protocol.SessionInfo) bool { return s.GetId() == "far" }); i >= 0 {
			return list[i]
		}
		return nil
	}
	waitFor(t, "the other node's session unread", func() bool {
		info := listed()
		return info.GetAttentionSeq() == attention && info.GetSeenSeq() == 0
	})
	info, err := c.SeeSession(t.Context(), &protocol.SeeSession{NodeId: b.cfg.NodeID, SessionId: "far", Seq: attention})
	if err != nil || info.GetSeenSeq() != attention {
		t.Fatalf("seeing it: %+v %v", info, err)
	}
	waitFor(t, "the session seen as the other node shares it", func() bool {
		return listed().GetSeenSeq() == attention
	})
}
