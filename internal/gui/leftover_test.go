package gui

import (
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestLeftoverTabsLeaveOnlyWhenTheirNodeIsLive(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	for _, id := range []string{"alive", "away", "gone", "unknown"} {
		m.showTab(id)
	}
	// Another space's layout holds a gone tab too.
	other := newLayout()
	other.Group.Tabs, other.Group.Active = []string{"gone", "kept"}, "gone"
	m.layouts["other"] = other

	live := map[string]bool{"n1": true}
	at := time.Unix(1000, 0)
	m.dropLeftoverTabs([]*protocol.SessionInfo{
		{Id: "alive", NodeId: "n1"}, {Id: "away", NodeId: "n2"}, {Id: "gone", NodeId: "n1"}, {Id: "kept", NodeId: "n1"},
	}, live, at)
	if m.tabNodes["away"] != "n2" || m.tabNodes["gone"] != "n1" {
		t.Fatalf("nodes not noted: %v", m.tabNodes)
	}

	// n2 is out of reach and gone has been killed; unknown was never seen.
	now := []*protocol.SessionInfo{{Id: "alive", NodeId: "n1"}, {Id: "kept", NodeId: "n1"}}
	m.dropLeftoverTabs(now, live, at.Add(time.Second))
	m.dropLeftoverTabs(now, live, at.Add(time.Second+leftoverAfter/2))
	if got := paneTabs(m); !samePanes(got, []string{"alive", "away", "gone", "unknown"}) {
		t.Fatalf("tabs left before their sessions were missing long: %q", got)
	}
	m.dropLeftoverTabs(now, live, at.Add(time.Second+leftoverAfter))
	if got := paneTabs(m); !samePanes(got, []string{"alive", "away"}) {
		t.Fatalf("panes %q, want alive and away", got)
	}
	if tabs := m.layouts["other"].Group.Tabs; len(tabs) != 1 || tabs[0] != "kept" {
		t.Fatalf("other space's tabs %q", tabs)
	}

	// Once n2 is live and still lacks away, its tab leaves as well.
	live["n2"] = true
	m.dropLeftoverTabs(now, live, at.Add(2*leftoverAfter))
	m.dropLeftoverTabs(now, live, at.Add(3*leftoverAfter))
	if got := ggui.Peek(m.Tabs); len(got) != 1 || got[0] != "alive" {
		t.Fatalf("tabs %q, want alive", got)
	}
}

func TestLiveNodes(t *testing.T) {
	now := time.Unix(1000, 0)
	seen := func(ago time.Duration) *protocol.Node {
		return &protocol.Node{LastSeen: timestamppb.New(now.Add(-ago))}
	}
	recent, stale, untrusted := seen(time.Second), seen(time.Minute), seen(time.Second)
	recent.Id, recent.TrustsUs = "recent", true
	stale.Id, stale.TrustsUs = "stale", true
	untrusted.Id = "untrusted"
	live := liveNodes(&protocol.Node{Id: "self"}, []*protocol.Node{recent, stale, untrusted}, now)
	if !live["self"] || !live["recent"] || live["stale"] || live["untrusted"] {
		t.Fatalf("live %v", live)
	}
}
