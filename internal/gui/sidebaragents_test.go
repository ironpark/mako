package gui

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// listed is each group of l by its key, with its sessions' IDs.
func listed(l agentList) [][]string {
	var out [][]string
	for _, g := range l.groups {
		ids := []string{pick(g.Pinned, "pinned", g.Key)}
		for _, s := range g.Sessions {
			ids = append(ids, s.GetId())
		}
		out = append(out, ids)
	}
	return out
}

// The agents are those of the scope picked, filtered by status, which is
// counted over the scope, and grouped; pinned ones come first wherever
// they are.
func TestAgentsListedByScopeStatusAndGroup(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Nodes.Set([]*protocol.Node{{Id: "here", Name: "studio", Source: "self"}, {Id: "box", Name: "box"}})
	run := func(id, node, dir, status string) *protocol.SessionInfo {
		return &protocol.SessionInfo{Id: id, NodeId: node, Project: dir, Agent: "claude", State: protocol.StateRunning, Status: status}
	}
	m.Sessions.Set([]*protocol.SessionInfo{
		run("a1", "box", "/w/a", protocol.AgentWorking),
		run("a2", "here", "/w/a", protocol.AgentWaiting),
		run("a3", "here", "/w/a", ""),
		run("b1", "here", "/w/b", protocol.AgentWorking),
	})
	m.Spaces.Update(func(l spaceList) spaceList { l.Pinned = []string{"b1"}; return l })
	st := newSidebarState(m)
	m.setScope(scopeKey{Section: dirKey("box", "/w/a")})
	// The clones of a folder with no remote are sections of their own:
	// the scope is box's.
	l := m.listAgents(st, 0, agentView{Group: agentGroupNone})
	if got := listed(l); !slices.EqualFunc(got, [][]string{{"pinned", "b1"}, {"", "a1"}}, slices.Equal) {
		t.Fatalf("box's /w/a lists %v", got)
	}
	m.setScope(scopeKey{Section: dirKey("here", "/w/a")})
	l = m.listAgents(st, 0, agentView{Group: agentGroupStatus})
	if got := listed(l); !slices.EqualFunc(got, [][]string{{"pinned", "b1"}, {"0", "a2"}, {"3", "a3"}}, slices.Equal) {
		t.Fatalf("by status %v", got)
	}
	if l.counts[""] != 2 || l.counts["waiting"] != 1 || l.counts["working"] != 0 {
		t.Fatalf("counts %v", l.counts)
	}
	l = m.listAgents(st, 0, agentView{Group: agentGroupNone, Filter: "working"})
	if got := listed(l); !slices.EqualFunc(got, [][]string{{"pinned", "b1"}}, slices.Equal) {
		t.Fatalf("working %v", got)
	}
	// With nothing picked, every agent of the space, this machine's
	// first, each saying its project.
	m.setScope(scopeKey{})
	l = m.listAgents(st, 0, agentView{Group: agentGroupMachine})
	if got := listed(l); !slices.EqualFunc(got, [][]string{{"pinned", "b1"}, {"here", "a2", "a3"}, {"box", "a1"}}, slices.Equal) {
		t.Fatalf("by machine %v", got)
	}
	if l.where["a1"] != "a" {
		t.Fatalf("a1 says %q", l.where["a1"])
	}
}

// A section's look is drawn from its ID, one of the shapes in a colour,
// the same each time, until one is picked; one picked that is not offered
// falls back to the drawn one.
func TestProjectLooks(t *testing.T) {
	drawn := lookOf(nil, "repo:github.com/x/y")
	if drawn != lookOf(nil, "repo:github.com/x/y") || !slices.Contains(lookShapes, drawn.Icon) || drawn.Color == "gray" {
		t.Fatalf("drawn %+v", drawn)
	}
	own := lookOf(map[string]projectLook{"k": {Icon: "rocket", Color: "gray"}}, "k")
	if own.Icon != "rocket" || own.Color != "gray" {
		t.Fatalf("picked %+v", own)
	}
	if bad := lookOf(map[string]projectLook{"k": {Icon: "nope", Color: "nope"}}, "k"); bad != lookOf(nil, "k") {
		t.Fatalf("unknown look %+v", bad)
	}
	for range 50 {
		if l := randomLook(); !slices.Contains(lookShapes, l.Icon) || l.Color == "gray" {
			t.Fatalf("random %+v", l)
		}
	}
	for _, ic := range lookIcons {
		if !hasIcon(ic.Icon) {
			t.Fatalf("no icon %s", ic.Icon)
		}
	}
}
