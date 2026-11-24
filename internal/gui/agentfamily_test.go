package gui

import (
	"reflect"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func familySessions() []*protocol.SessionInfo {
	return []*protocol.SessionInfo{
		{Id: "lead", NodeId: "n"},
		{Id: "build", NodeId: "n", ParentSessionId: "lead"},
		{Id: "review", NodeId: "n", ParentSessionId: "lead"},
		{Id: "deep", NodeId: "n", ParentSessionId: "build"},
		{Id: "other", NodeId: "n"},
		{Id: "away", NodeId: "m", ParentSessionId: "lead"}, // another node's "lead" is not this one
		{Id: "remote-kid", NodeId: "m", ParentSessionId: "lead", ParentNodeId: "n"},
		{Id: "a", NodeId: "n", ParentSessionId: "b"},
		{Id: "b", NodeId: "n", ParentSessionId: "a"}, // a cycle ends
	}
}

func TestTabHostsGoToTheFurthestAncestorInThePane(t *testing.T) {
	sessions := familySessions()
	got := indexSessions(sessions).tabHosts([]string{"lead", "build", "deep", "other", "away", "remote-kid", "a", "b"})
	want := map[string]string{"build": "lead", "deep": "lead", "remote-kid": "lead"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	// Without its parent's tab, a spawned agent's shows in the bar.
	got = indexSessions(sessions).tabHosts([]string{"build", "deep", "review"})
	if want := map[string]string{"deep": "build"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts without lead = %v, want %v", got, want)
	}
}

func TestFamilyListsTheSpawningAgentFirst(t *testing.T) {
	var ids []string
	for _, s := range indexSessions(familySessions()).family("lead") {
		ids = append(ids, s.GetId())
	}
	if want := []string{"lead", "build", "review", "remote-kid", "deep"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("family = %v, want %v", ids, want)
	}
	if f := indexSessions(familySessions()).family("other"); len(f) != 1 {
		t.Fatalf("a lone session's family = %d, want 1", len(f))
	}
}

// A team set up turns no tab into a lane of agents: only a tab whose
// agent spawned others has one, and the session under it keeps the pane.
func TestTheLaneIsOnlyOfSpawnedAgents(t *testing.T) {
	m := newModel(config.Default(), "")
	m.post = func(func()) {}
	m.Prefs.Update(func(p prefs) prefs {
		p.Teams = []agentTeam{{Name: "crew", Members: []teamMember{{Name: "Builder", Agent: "claude"}}}}
		return p
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "lead", NodeId: "n", Agent: "claude", Name: "Lead", State: protocol.StateRunning},
		{Id: "kid", NodeId: "n", Agent: "codex", Name: "Kid", ParentSessionId: "lead", State: protocol.StateRunning},
		{Id: "solo", NodeId: "n", Agent: "claude", Name: "Solo", State: protocol.StateRunning},
	})
	show := func(active string) *ggui.Probe {
		root := newLayout()
		root.Group.Tabs, root.Group.Active = []string{"lead", "solo"}, active
		m.Layout.Set(root)
		p := ggui.NewProbe(paneView(m, 1), ggui.Sz(900, 300))
		for range 3 {
			p.Frame()
		}
		return p
	}
	p := show("solo")
	if _, ok := p.Find(T("Add a team")); ok {
		t.Fatal("a session that spawned none has the lane")
	}
	p = show("lead")
	team, ok := p.Find(T("Add a team"))
	if !ok {
		t.Fatal("the lead's tab has no lane")
	}
	// The lane is its own height under the tabs, not the pane's: a lane
	// as tall as the pane left the session under it none.
	if mid := team.Rect.Origin.Y + team.Rect.Size.H/2; mid > tabBarHeight+laneHeight+2 {
		t.Fatalf("the lane's button is at %v, below a lane %v tall", team.Rect, laneHeight)
	}
}
