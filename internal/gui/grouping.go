package gui

import (
	"cmp"
	"slices"
	"sort"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/protocol"
)

// How the sidebar groups sessions, and the projects it keeps.

// The ways the sidebar groups its sessions.
const (
	groupProject = "project" // by node and working directory
	groupHost    = "host"    // by node
	groupFlat    = "flat"    // one list
)

// sessionGroup is one sidebar section: the sessions sharing a node and a
// project, the way Paseo groups workspaces under a repository, or only a
// node. Project is empty for a node's section.
type sessionGroup struct {
	Key      string
	NodeID   string
	Project  string
	Owner    string // the owner of the project's repository, host/name
	Remote   string // the repository its origin is of, host/path
	Sessions []*protocol.SessionInfo
	// Roots are the folders of the section, on their nodes: more than one
	// for the clones of one repository on several nodes, or a project's
	// folders, NodeID and Project being the one a new session starts in
	// by default.
	Roots []fileRoot
	// Name is the section's name, as a project gives it; "" for its
	// folder's.
	Name string
	// Def is the project the section is, nil for one the sidebar groups
	// itself; RootOf is the root of it each session is in, by ID.
	Def    *project
	RootOf map[string]int
}

// keptProject is a folder the sidebar keeps, with the owner of its
// repository and the repository its origin is of, as its sessions last
// said, so the folder stays in its owner's group, and with the other
// clones of its repository, once they are gone.
type keptProject struct {
	fileRoot
	Owner  string `json:"owner,omitempty"`
	Remote string `json:"remote,omitempty"`
}

// keepProjects remembers the folders the sidebar groups sessions into,
// and whose repositories they are. A folder with nothing in it says
// nothing here, so the list only grows.
func (m *model) keepProjects(sessions []*protocol.SessionInfo) {
	projects := ggui.Peek(m.Prefs).Projects
	index := make(map[fileRoot]int, len(projects))
	for i, r := range projects {
		index[r.fileRoot] = i
	}
	var changed []keptProject // a copy, made at the first change
	for _, s := range sessions {
		r := keptProject{fileRoot{Node: s.GetNodeId(), Path: projectOf(s)}, s.GetOwner(), s.GetRemote()}
		if r.Path == "" {
			continue
		}
		i, ok := index[r.fileRoot]
		if ok && (changed == nil && projects[i] == r || changed != nil && changed[i] == r) {
			continue
		}
		if changed == nil {
			changed = slices.Clone(projects)
		}
		if ok {
			changed[i] = r
		} else {
			index[r.fileRoot] = len(changed)
			changed = append(changed, r)
		}
	}
	if changed != nil {
		m.Prefs.Update(func(p prefs) prefs { p.Projects = changed; return p })
	}
}

// forgetProject drops a folder from the sidebar. It is offered for a folder
// with no sessions, whose groups would not bring it back.
func (m *model) forgetProject(r fileRoot) { m.forgetProjects([]fileRoot{r}) }

// forgetProjects drops folders from the sidebar.
func (m *model) forgetProjects(roots []fileRoot) {
	m.Prefs.Update(func(p prefs) prefs {
		p.Projects = slices.DeleteFunc(slices.Clone(p.Projects), func(o keptProject) bool { return slices.Contains(roots, o.fileRoot) })
		return p
	})
}

// projectOf is the folder a session's section is for: the main work tree
// of its repository, so a repository's subfolders and worktrees share one,
// and outside a repository its own folder. One folder inside another is
// not otherwise the other's: a home folder holds every project.
func projectOf(s *protocol.SessionInfo) string {
	if s.GetRepo() != "" {
		return s.GetRepo()
	}
	return s.GetProject()
}

// groupSessions sorts sessions into the sidebar's sections for a grouping.
// By project, a session goes in its repository's section, as projectOf
// says, which the clones of one repository on several nodes share.
// Remembered projects keep a section when they have no sessions.
func groupSessions(sessions []*protocol.SessionInfo, by string, nodes []*protocol.Node, projects []keptProject) []sessionGroup {
	switch by {
	case groupFlat:
		if len(sessions) == 0 {
			return nil
		}
		return []sessionGroup{{Key: "", Sessions: sessions}}
	case groupHost:
		groups := bucket(sessions, func(s *protocol.SessionInfo) (string, string) { return s.GetNodeId(), "" })
		sort.SliceStable(groups, func(a, b int) bool {
			return nodeName(nodes, groups[a].NodeID) < nodeName(nodes, groups[b].NodeID)
		})
		return groups
	}
	groups := bucket(sessions, func(s *protocol.SessionInfo) (string, string) { return s.GetNodeId(), projectOf(s) })
	present := make(map[string]bool, len(groups))
	for i, g := range groups {
		present[g.Key] = true
		groups[i].Owner, groups[i].Remote = g.Sessions[0].GetOwner(), g.Sessions[0].GetRemote()
	}
	for _, r := range projects {
		if r.Path == "" || present[dirKey(r.Node, r.Path)] {
			continue
		}
		present[dirKey(r.Node, r.Path)] = true
		groups = append(groups, sessionGroup{Key: dirKey(r.Node, r.Path), NodeID: r.Node, Project: r.Path, Owner: r.Owner, Remote: r.Remote})
	}
	groups = byRemote(groups, nodes)
	sort.SliceStable(groups, func(a, b int) bool {
		return projectName(groups[a].Project) < projectName(groups[b].Project)
	})
	return groups
}

// byRemote gathers the sections of one repository's clones on several
// nodes into one, keyed by the repository. Its folder by default is this
// machine's clone, else the first. Every section says its folders in
// Roots.
func byRemote(groups []sessionGroup, nodes []*protocol.Node) []sessionGroup {
	self := selfNode(nodes)
	var out []sessionGroup
	index := map[string]int{}
	for _, g := range groups {
		root := fileRoot{Node: g.NodeID, Path: g.Project}
		i, ok := index[g.Remote]
		if g.Remote == "" || !ok {
			g.Roots = []fileRoot{root}
			if g.Remote != "" {
				// A repository's section is keyed by it, so its key stays
				// as its clones come and go.
				g.Key = implicitKey(g.NodeID, g.Project, g.Remote)
				index[g.Remote] = len(out)
			}
			out = append(out, g)
			continue
		}
		m := &out[i]
		m.Roots = append(m.Roots, root)
		m.Sessions = append(m.Sessions, g.Sessions...)
		if g.NodeID == self && m.NodeID != self {
			m.NodeID, m.Project = g.NodeID, g.Project
		}
	}
	return out
}

// withHosts adds a section for each node that has no sessions, so the
// sidebar grouped by host lists every host there is to start one on.
func withHosts(groups []sessionGroup, nodes []*protocol.Node) []sessionGroup {
	present := make(map[string]bool, len(groups))
	for _, g := range groups {
		present[g.NodeID] = true
	}
	for _, n := range nodes {
		if !present[n.GetId()] {
			present[n.GetId()] = true
			groups = append(groups, sessionGroup{Key: dirKey(n.GetId(), ""), NodeID: n.GetId()})
		}
	}
	sort.SliceStable(groups, func(a, b int) bool {
		return nodeName(nodes, groups[a].NodeID) < nodeName(nodes, groups[b].NodeID)
	})
	return groups
}

// ownerSection is the project sections whose repositories one user or
// organization owns on one service, Owner being host/name as the daemon
// gives it; it is "" for those that no one does.
type ownerSection struct {
	Owner  string
	Groups []sessionGroup
}

// byOwner gathers project sections under their repositories' owners.
// Folders no one owns come last, outside any owner's. It is nil when no
// folder has an owner, so the sidebar groups only by project.
func byOwner(groups []sessionGroup) []ownerSection {
	var out []ownerSection
	index := map[string]int{}
	for _, g := range groups {
		i, ok := index[g.Owner]
		if !ok {
			i = len(out)
			index[g.Owner] = i
			out = append(out, ownerSection{Owner: g.Owner})
		}
		out[i].Groups = append(out[i].Groups, g)
	}
	if len(out) == 0 || len(out) == 1 && out[0].Owner == "" {
		return nil
	}
	sort.SliceStable(out, func(a, b int) bool {
		if (out[a].Owner == "") != (out[b].Owner == "") {
			return out[b].Owner == ""
		}
		// By name, and one name's services by host.
		na, ha := ownerName(out[a].Owner)
		nb, hb := ownerName(out[b].Owner)
		return cmp.Or(cmp.Compare(strings.ToLower(na), strings.ToLower(nb)), cmp.Compare(ha, hb)) < 0
	})
	return out
}

// bucket groups sessions by the node and project key gives them, in the
// order each first appears.
func bucket(sessions []*protocol.SessionInfo, key func(*protocol.SessionInfo) (string, string)) []sessionGroup {
	var groups []sessionGroup
	index := map[string]int{}
	for _, s := range sessions {
		node, project := key(s)
		k := dirKey(node, project)
		i, ok := index[k]
		if !ok {
			i = len(groups)
			index[k] = i
			groups = append(groups, sessionGroup{Key: k, NodeID: node, Project: project})
		}
		groups[i].Sessions = append(groups[i].Sessions, s)
	}
	return groups
}
