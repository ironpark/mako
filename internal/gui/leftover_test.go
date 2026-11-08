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
	m.ws.layouts["other"] = other

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
	if tabs := m.ws.layouts["other"].Group.Tabs; len(tabs) != 1 || tabs[0] != "kept" {
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

// A tab that leaves a space not shown, or a layout taken up from another
// node, lets go of its terminal, chat and usage as a tab closed does.
func TestTabsLeavingHiddenLayoutsLetGoOfTheirTerminals(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.showTab("shown")
	other := newLayout()
	other.Group.Tabs, other.Group.Active = []string{"gone", "kept", "synced"}, "gone"
	m.ws.layouts["other"] = other
	conns := map[string]*termConn{}
	for _, id := range []string{"shown", "gone", "kept", "synced"} {
		link := newFakeLink()
		conn, err := newTermConn(id, link, 80, 24, func() {})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.close(); close(link.in) })
		conns[id], m.conns[id] = conn, conn
		m.chatState(id)
		m.usageState(id)
		m.setChatMode(id, true)
	}
	held := func(id string) bool {
		_, chat := m.chats[id]
		_, usage := m.usages[id]
		return m.conns[id] != nil || chat || usage || ggui.Peek(m.ChatModes)[id]
	}
	closed := func(id string) bool {
		conns[id].mu.Lock()
		defer conns[id].mu.Unlock()
		return conns[id].closed
	}

	live := map[string]bool{"n1": true}
	now := []*protocol.SessionInfo{{Id: "shown", NodeId: "n1"}, {Id: "kept", NodeId: "n1"}, {Id: "synced", NodeId: "n1"}}
	at := time.Unix(1000, 0)
	m.dropLeftoverTabs(now, live, at)
	m.dropLeftoverTabs(now, live, at.Add(leftoverAfter))
	if held("gone") || !closed("gone") {
		t.Fatal("a leftover tab of a space not shown kept its terminal")
	}
	if !held("kept") || !held("synced") || !held("shown") {
		t.Fatal("a tab still open was let go of")
	}

	// Another node's workspace no longer has synced.
	other = newLayout()
	other.Group.Tabs, other.Group.Active = []string{"kept"}, "kept"
	m.ws.layouts = map[string]*layoutNode{"other": other}
	m.setLayout(ggui.Peek(m.Layout))
	if held("synced") || !closed("synced") {
		t.Fatal("a tab another node's workspace dropped kept its terminal")
	}
	if !held("kept") || !held("shown") {
		t.Fatal("a tab still open was let go of")
	}
}
