package gui

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestGroupSessions(t *testing.T) {
	s := func(id, node, project string) *protocol.SessionInfo {
		return &protocol.SessionInfo{Id: id, NodeId: node, Project: project, Repo: project}
	}
	sessions := []*protocol.SessionInfo{
		s("a", "n1", "/src/mako/internal"),
		s("b", "n1", "/src/mako"),
		s("c", "n2", "/src/mako"),
		s("d", "n1", "/src/makox"),
	}
	sessions[0].Repo = "/src/mako" // a folder of the repository
	nodes := []*protocol.Node{{Id: "n1", Name: "mac"}, {Id: "n2", Name: "box"}}

	byProject := groupSessions(sessions, groupProject, nodes, nil)
	got := map[string][]string{}
	for _, g := range byProject {
		for _, x := range g.Sessions {
			got[g.NodeID+":"+g.Project] = append(got[g.NodeID+":"+g.Project], x.GetId())
		}
	}
	// A folder of a repository joins its section, on its node only, and a
	// sibling sharing a prefix does not.
	if len(byProject) != 3 || len(got["n1:/src/mako"]) != 2 || len(got["n2:/src/mako"]) != 1 || len(got["n1:/src/makox"]) != 1 {
		t.Fatalf("by project: %v", got)
	}
	if rel := subfolder("/src/mako/internal", "/src/mako"); rel != "internal" {
		t.Fatalf("subfolder %q", rel)
	}

	byHost := groupSessions(sessions, groupHost, nodes, nil)
	if len(byHost) != 2 || nodeName(nodes, byHost[0].NodeID) != "box" || len(byHost[1].Sessions) != 3 {
		t.Fatalf("by host: %+v", byHost)
	}
	if flat := groupSessions(sessions, groupFlat, nodes, nil); len(flat) != 1 || len(flat[0].Sessions) != 4 {
		t.Fatalf("flat: %+v", flat)
	}
}
