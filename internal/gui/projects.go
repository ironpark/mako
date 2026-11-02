package gui

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/protocol"
)

// Projects: the sidebar's sections, by the folders they hold. Left alone,
// the sidebar groups a session itself: by its repository, the clones of
// one on every node together, or outside a repository by its own folder
// (groupSessions). A project the user puts together holds folders of its
// own, a repository or not, on any node: a server's and a web app's
// repositories, or a folder of notes. Projects are part of the workspace
// (spaceList), shared with the other nodes as the spaces are, and so are
// the names given to the sections the sidebar groups itself (Names).
//
// Every section has an ID that stays put: a project's own, and a section
// the sidebar groups by itself the key it groups by, "repo:" and the
// repository's remote, or the node and folder (implicitKey). A project
// made of such a section keeps its ID, so the tabs it kept, whether it
// was folded, and the spaces showing it carry on.

// project is a set of folders the user put together.
type project struct {
	ID    string        `json:"id"`
	Name  string        `json:"name,omitempty"`
	Roots []projectRoot `json:"roots,omitempty"`
	// NoJoin keeps new clones of its roots' repositories, on other nodes or
	// in other folders, out until they are added.
	NoJoin bool `json:"no_join,omitempty"`
	// GiveDirs gives an agent started in one of its folders the others on
	// its node too, as an agent that takes more folders than its own does.
	GiveDirs bool `json:"give_dirs,omitempty"`
}

// projectRoot is one of a project's folders: one repository, or one
// folder outside any, where it is on each node it is on.
type projectRoot struct {
	Label string `json:"label,omitempty"` // as its sessions are marked, the folder's name when empty
	// Remote and Owner are the repository's, host/path and host/name, as
	// its sessions last said; "" outside a repository.
	Remote    string     `json:"remote,omitempty"`
	Owner     string     `json:"owner,omitempty"`
	Locations []fileRoot `json:"locations,omitempty"`
	// Declined are the clones taken out after they joined by their
	// remote, which do not join again.
	Declined []fileRoot `json:"declined,omitempty"`
}

// label is how the root's sessions and its start entries name it.
func (r projectRoot) label() string { return cmp.Or(r.Label, r.folderName()) }

// folderName is the root's name when it has no label: its folder's.
func (r projectRoot) folderName() string {
	if len(r.Locations) > 0 {
		return projectName(r.Locations[0].Path)
	}
	if r.Remote != "" {
		return projectName(r.Remote)
	}
	return "~"
}

// clone is p with nothing shared with p.
func (p project) clone() project {
	p.Roots = slices.Clone(p.Roots)
	for i := range p.Roots {
		p.Roots[i].Locations = slices.Clone(p.Roots[i].Locations)
		p.Roots[i].Declined = slices.Clone(p.Roots[i].Declined)
	}
	return p
}

// locations are the folders of every root, in order.
func (p project) locations() []fileRoot {
	var out []fileRoot
	for _, r := range p.Roots {
		out = append(out, r.Locations...)
	}
	return out
}

// implicitKey is the key the sidebar groups a folder by on its own: its
// repository's, by the remote, which the clones on every node share, or
// else the node's folder.
func implicitKey(node, root, remote string) string {
	if remote != "" {
		return "repo:" + remote
	}
	return dirKey(node, root)
}

// freePrefix sets a section the sidebar grouped itself apart from a
// project that has the key as its ID, as one made of the section and then
// left by the folder has.
const freePrefix = "free:"

// sectionKey is the ID of the section the sidebar groups itself by key:
// the key, set apart from a project's of the same ID.
func sectionKey(defs []project, key string) string {
	if projectByID(defs, key) >= 0 {
		return freePrefix + key
	}
	return key
}

// placement is where a session or folder goes among the projects: the
// project whose root holds it, At its index in the projects, -1 when the
// sidebar groups it itself.
type placement struct {
	At   int
	Root int // an index into the project's Roots
	// Join says the root holds it by its repository's remote only: a new
	// clone, which joinClones adds to the root's locations.
	Join bool
}

// placeIn finds the project a session or folder on node is in: the one with
// a folder that holds it, the deepest when several do, else the one root
// whose repository it is a clone of. A clone that two roots claim, or that
// was taken out, the sidebar groups itself.
func placeIn(defs []project, node, dir, repo, remote string) placement {
	best, depth := placement{At: -1}, -1
	for i, p := range defs {
		for j, r := range p.Roots {
			for _, l := range r.Locations {
				if l.Node == node && pathscope.Covers(l.Path, dir, repo) && len(l.Path) > depth {
					best, depth = placement{At: i, Root: j}, len(l.Path)
				}
			}
		}
	}
	if depth >= 0 || remote == "" {
		return best
	}
	at := fileRoot{Node: node, Path: cmp.Or(repo, dir)}
	claims := 0
	for i, p := range defs {
		for j, r := range p.Roots {
			if r.Remote != remote {
				continue
			}
			claims++
			if !p.NoJoin && !slices.Contains(r.Declined, at) {
				best = placement{At: i, Root: j, Join: true}
			}
		}
	}
	if claims == 1 {
		return best
	}
	return placement{At: -1}
}

// placeSession is placeIn for a session.
func placeSession(defs []project, s *protocol.SessionInfo) placement {
	return placeIn(defs, s.GetNodeId(), s.GetProject(), s.GetRepo(), s.GetRemote())
}

// placeKept is placeIn for a folder the sidebar keeps, which it knows is a
// repository's when it knows the repository's owner or remote.
func placeKept(defs []project, r keptProject) placement {
	repo := ""
	if r.Owner != "" || r.Remote != "" {
		repo = r.Path
	}
	return placeIn(defs, r.Node, r.Path, repo, r.Remote)
}

// sectionID is the ID of the section a session is in: its project's, or
// the key the sidebar groups it by.
func sectionID(defs []project, s *protocol.SessionInfo) string {
	if pl := placeSession(defs, s); pl.At >= 0 {
		return defs[pl.At].ID
	}
	return sectionKey(defs, implicitKey(s.GetNodeId(), projectOf(s), s.GetRemote()))
}

// projectByID is the index of the project with id, -1 for none.
func projectByID(defs []project, id string) int {
	return slices.IndexFunc(defs, func(p project) bool { return p.ID == id })
}

// groupProjects is the sidebar's project sections: the projects', and
// those it groups itself of the sessions and kept folders no project
// holds, named as names says, by name.
func groupProjects(sessions []*protocol.SessionInfo, nodes []*protocol.Node, kept []keptProject, defs []project, names map[string]string) []sessionGroup {
	own := make([]sessionGroup, len(defs))
	for i, p := range defs {
		own[i] = projectGroup(p, nodes)
	}
	var rest []*protocol.SessionInfo
	for _, s := range sessions {
		pl := placeSession(defs, s)
		if pl.At < 0 {
			rest = append(rest, s)
			continue
		}
		own[pl.At].Sessions = append(own[pl.At].Sessions, s)
		own[pl.At].RootOf[s.GetId()] = pl.Root
	}
	kept = slices.DeleteFunc(slices.Clone(kept), func(r keptProject) bool { return placeKept(defs, r).At >= 0 })
	groups := groupSessions(rest, groupProject, nodes, kept)
	for i := range groups {
		groups[i].Key = sectionKey(defs, groups[i].Key)
		groups[i].Name = names[groups[i].Key]
	}
	groups = append(groups, own...)
	sort.SliceStable(groups, func(a, b int) bool { return groupName(groups[a]) < groupName(groups[b]) })
	return groups
}

// projectGroup is the section of project p, before its sessions: its
// folders, the one a new session starts in by default the first root's
// on this machine, else its first, and the owner its roots share.
func projectGroup(p project, nodes []*protocol.Node) sessionGroup {
	g := sessionGroup{Key: p.ID, Name: p.Name, Def: &p, RootOf: map[string]int{}, Roots: p.locations()}
	if len(p.Roots) > 0 && len(p.Roots[0].Locations) > 0 {
		first := p.Roots[0].Locations
		at := first[0]
		self := selfNode(nodes)
		if i := slices.IndexFunc(first, func(l fileRoot) bool { return l.Node == self }); i >= 0 {
			at = first[i]
		}
		g.NodeID, g.Project = at.Node, at.Path
	} else if len(g.Roots) > 0 {
		g.NodeID, g.Project = g.Roots[0].Node, g.Roots[0].Path
	}
	for i, r := range p.Roots {
		if i == 0 {
			g.Owner = r.Owner
		} else if r.Owner != g.Owner {
			g.Owner = "" // a project of several owners is no one's
			break
		}
	}
	if len(p.Roots) == 1 {
		g.Remote = p.Roots[0].Remote
	}
	return g
}

// selfNode is this machine's node's ID among nodes.
func selfNode(nodes []*protocol.Node) string {
	for _, n := range nodes {
		if n.GetSource() == "self" {
			return n.GetId()
		}
	}
	return ""
}

// groupName is how a section is named and sorted: its project's name, or
// its folder's.
func groupName(g sessionGroup) string { return cmp.Or(g.Name, projectName(g.Project)) }

// multiRoot reports whether a section is a project of several folders,
// whose sessions say which they are in.
func (g sessionGroup) multiRoot() bool { return g.Def != nil && len(g.Def.Roots) > 1 }

// rootCount is how many folders a section is: a project's roots, or the
// one the sidebar grouped itself.
func (g sessionGroup) rootCount() int {
	if g.Def == nil {
		return 1
	}
	return len(g.Def.Roots)
}

// rootOf is the project root a session of the section is in, nil outside
// a project.
func (g sessionGroup) rootOf(s *protocol.SessionInfo) *projectRoot {
	if r, ok := g.RootOf[s.GetId()]; ok && g.Def != nil {
		return &g.Def.Roots[r]
	}
	return nil
}

// groupOwners are the owners of a section's repositories: its own, its
// sessions' and its roots'.
func groupOwners(g sessionGroup) []string {
	owners := []string{g.Owner}
	for _, s := range g.Sessions {
		owners = append(owners, s.GetOwner())
	}
	if g.Def != nil {
		for _, r := range g.Def.Roots {
			owners = append(owners, r.Owner)
		}
	}
	owners = slices.DeleteFunc(owners, func(o string) bool { return o == "" })
	slices.Sort(owners)
	return slices.Compact(owners)
}

// asProject is the project section g is, to change: its project, or one
// made of the section the sidebar grouped itself, of one root holding its
// folders, under its key, so what was kept by the key carries on. A key
// set apart from a project's cannot be another's ID, and the project
// takes a new one.
func asProject(g sessionGroup) project {
	if g.Def != nil {
		return g.Def.clone()
	}
	id := g.Key
	if strings.HasPrefix(id, freePrefix) {
		id = "p" + newSpaceID()
	}
	root := projectRoot{Remote: g.Remote, Owner: g.Owner, Locations: slices.Clone(g.Roots)}
	if len(root.Locations) == 0 && g.Project != "" {
		root.Locations = []fileRoot{{Node: g.NodeID, Path: g.Project}}
	}
	return project{ID: id, Name: g.Name, Roots: []projectRoot{root}}
}

// setProject puts p among the projects in place of the one of its ID; a
// project of no folders is gone.
func setProject(defs []project, p project) []project {
	out := slices.DeleteFunc(slices.Clone(defs), func(o project) bool { return o.ID == p.ID })
	if len(p.Roots) == 0 {
		return out
	}
	return append(out, p)
}

// Changes ---------------------------------------------------------------------

// projectsNow are the projects, as the workspace holds them.
func (m *model) projectsNow() []project { return ggui.Peek(m.Spaces).Projects }

// changeProjects changes the projects, the sections' names and the spaces
// showing them, and moves what was kept by each section's ID in moved to
// the ID it maps to: the tabs a space kept for it, and its fold. A notice,
// unless it is "", offers to put the projects, names and spaces back as
// they were, and moves back what moved.
func (m *model) changeProjects(notice string, moved map[string]string, fn func(l spaceList) spaceList) {
	before := ggui.Peek(m.Spaces)
	m.Spaces.Update(func(l spaceList) spaceList {
		l.Projects = slices.Clone(l.Projects)
		l.Spaces = slices.Clone(l.Spaces)
		l.Names = maps.Clone(l.Names)
		l = fn(l)
		for from, to := range moved {
			for i := range l.Spaces {
				l.Spaces[i].Projects = replaceID(l.Spaces[i].Projects, from, to)
				l.Spaces[i].Hidden = slices.DeleteFunc(slices.Clone(l.Spaces[i].Hidden), func(id string) bool { return id == from })
			}
		}
		return l
	})
	m.moveContexts(moved)
	if notice == "" || m.toaster == nil {
		return
	}
	m.toaster.Push(ui.Toast(notice, "").Action(T("Undo"), func() {
		m.Spaces.Update(func(now spaceList) spaceList {
			// The projects, names and spaces' projects as they were; the
			// rest as it is now.
			now.Projects, now.Names = before.Projects, before.Names
			spaces := slices.Clone(now.Spaces)
			for i := range spaces {
				if j := slices.IndexFunc(before.Spaces, func(sp space) bool { return sp.ID == spaces[i].ID }); j >= 0 {
					spaces[i].Projects, spaces[i].Hidden = before.Spaces[j].Projects, before.Spaces[j].Hidden
				}
			}
			now.Spaces = spaces
			return now
		})
		back := map[string]string{}
		for from, to := range moved {
			back[to] = from
		}
		m.moveContexts(back)
	}))
}

// renameProject names a section, or gives it back its folder's name. A
// section the sidebar grouped itself keeps grouping so.
func (m *model) renameProject(g sessionGroup, name string) {
	name = strings.TrimSpace(name)
	m.changeProjects("", nil, func(l spaceList) spaceList {
		if g.Def != nil {
			p := g.Def.clone()
			p.Name = name
			l.Projects = setProject(l.Projects, p)
			return l
		}
		if l.Names == nil {
			l.Names = map[string]string{}
		}
		l.Names[g.Key] = name
		if name == "" {
			delete(l.Names, g.Key)
		}
		return l
	})
}

// saveProject keeps p, made of section g, as a project: what was kept by
// g's key moves to p's ID, and g's name, if the sidebar named it, is p's.
func (m *model) saveProject(notice string, g sessionGroup, p project) {
	m.changeProjects(notice, map[string]string{g.Key: p.ID}, func(l spaceList) spaceList {
		delete(l.Names, g.Key)
		l.Projects = setProject(l.Projects, p)
		return l
	})
}

// addFolder adds a folder on node to section g, as a root of its own,
// making the section a project if it was not one.
func (m *model) addFolder(g sessionGroup, node, dir string) {
	at := fileRoot{Node: node, Path: dir}
	if slices.Contains(g.Roots, at) {
		return
	}
	p := asProject(g)
	p.Roots = append(p.Roots, projectRoot{Locations: []fileRoot{at}})
	m.saveProject(Tf("Added %s to %s", projectName(dir), groupName(g)), g, p)
}

// mergeProjects puts the folders of section from into section into, which
// keeps its ID and name; the spaces that showed from show into, and the
// tabs from kept are into's where into kept none.
func (m *model) mergeProjects(from, into sessionGroup) {
	dst, src := asProject(into), asProject(from)
	dst.Roots = append(dst.Roots, src.Roots...)
	m.changeProjects(Tf("Merged %s into %s", groupName(from), groupName(into)), map[string]string{from.Key: dst.ID, into.Key: dst.ID}, func(l spaceList) spaceList {
		delete(l.Names, from.Key)
		delete(l.Names, into.Key)
		l.Projects = setProject(slices.DeleteFunc(l.Projects, func(p project) bool { return p.ID == from.Key }), dst)
		return l
	})
}

// replaceID is ids with old as new, once.
func replaceID(ids []string, old, new string) []string {
	if old == new || !slices.Contains(ids, old) {
		return ids
	}
	out := slices.Clone(ids)
	for i := range out {
		if out[i] == old {
			out[i] = new
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// removeRoot takes root i out of project section g; its folders go back
// to the sidebar's own grouping. A project left with no folders is gone.
func (m *model) removeRoot(g sessionGroup, i int) {
	if g.Def == nil || i < 0 || i >= len(g.Def.Roots) {
		return
	}
	p := g.Def.clone()
	label := p.Roots[i].label()
	p.Roots = slices.Delete(p.Roots, i, i+1)
	m.changeProjects(Tf("Took %s out of %s", label, groupName(g)), nil, func(l spaceList) spaceList {
		l.Projects = setProject(l.Projects, p)
		return l
	})
}

// ungroup ends project section g: its folders go back to the sidebar's
// own grouping.
func (m *model) ungroup(g sessionGroup) {
	if g.Def == nil {
		return
	}
	id := g.Def.ID
	m.changeProjects(Tf("Ungrouped %s", groupName(g)), nil, func(l spaceList) spaceList {
		l.Projects = slices.DeleteFunc(l.Projects, func(p project) bool { return p.ID == id })
		return l
	})
}

// moveContexts gives the tabs and panes a space kept for each section in
// moved to the one it maps to, where that one kept none, shows the new
// one's where the old one's showed, and drops the old one's fold.
func (m *model) moveContexts(moved map[string]string) {
	contexts := map[string]string{}
	for from, to := range moved {
		if from != to {
			contexts["project:"+from] = "project:" + to
		}
	}
	if len(contexts) == 0 {
		return
	}
	m.rekeyContexts(contexts)
	m.Prefs.Update(func(p prefs) prefs {
		p.Collapsed = slices.DeleteFunc(slices.Clone(p.Collapsed), func(k string) bool { _, ok := contexts[k]; return ok })
		return p
	})
	m.workspaceChanged()
}

// rekeyContexts moves the tabs and panes the spaces kept under each layout
// context in moved to the context it maps to, where that one has none, and
// the spaces showing the one to the other. The panes shown move too, or
// give way to the new context's own.
func (m *model) rekeyContexts(moved map[string]string) {
	for key, root := range maps.Clone(m.layouts) {
		space, ctx, _ := strings.Cut(key, "|")
		to, ok := moved[ctx]
		if !ok {
			continue
		}
		delete(m.layouts, key)
		if _, taken := m.layouts[layoutKey(space, to)]; !taken && m.layoutKey != layoutKey(space, to) {
			m.layouts[layoutKey(space, to)] = root
		}
	}
	for space, ctx := range m.contexts {
		if to, ok := moved[ctx]; ok {
			m.contexts[space] = to
			m.contextsMoved = true
		}
	}
	if space, ctx, _ := strings.Cut(m.layoutKey, "|"); m.layoutKey != "" {
		if to, ok := moved[ctx]; ok {
			if _, own := m.layouts[layoutKey(space, to)]; own {
				m.showLayout(layoutKey(space, to))
				delete(m.layouts, layoutKey(space, ctx))
			} else {
				m.layoutKey = layoutKey(space, to)
			}
		}
	}
}

// joinClones adds the new clones of the projects' repositories to their
// roots, each with a notice that takes it out again, and fills in the
// repository of a root added as a folder once a session in it says.
func (m *model) joinClones(sessions []*protocol.SessionInfo) {
	defs := m.projectsNow()
	if len(defs) == 0 {
		return
	}
	type edit struct {
		id   string
		root int
		fn   func(*projectRoot)
	}
	var edits []edit
	type join struct {
		undo edit
		at   fileRoot
		into string
	}
	var joins []join
	for _, s := range sessions {
		pl := placeSession(defs, s)
		if pl.At < 0 {
			continue
		}
		p := defs[pl.At]
		r := p.Roots[pl.Root]
		at := fileRoot{Node: s.GetNodeId(), Path: projectOf(s)}
		switch {
		case pl.Join && !slices.ContainsFunc(joins, func(j join) bool { return j.at == at }):
			e := edit{p.ID, pl.Root, func(r *projectRoot) { r.Locations = append(r.Locations, at) }}
			edits = append(edits, e)
			joins = append(joins, join{edit{p.ID, pl.Root, func(r *projectRoot) {
				r.Locations = slices.DeleteFunc(r.Locations, func(l fileRoot) bool { return l == at })
				r.Declined = append(r.Declined, at)
			}}, at, cmp.Or(p.Name, r.label())})
		case !pl.Join && r.Remote == "" && s.GetRemote() != "" && slices.Contains(r.Locations, at):
			remote, owner := s.GetRemote(), s.GetOwner()
			edits = append(edits, edit{p.ID, pl.Root, func(r *projectRoot) { r.Remote, r.Owner = remote, owner }})
		}
	}
	apply := func(edits ...edit) {
		m.Spaces.Update(func(l spaceList) spaceList {
			for _, e := range edits {
				i := projectByID(l.Projects, e.id)
				if i < 0 || e.root >= len(l.Projects[i].Roots) {
					continue
				}
				p := l.Projects[i].clone()
				e.fn(&p.Roots[e.root])
				l.Projects = setProject(l.Projects, p)
			}
			return l
		})
	}
	if len(edits) == 0 {
		return
	}
	apply(edits...)
	if m.toaster == nil {
		return
	}
	nodes := ggui.Peek(m.Nodes)
	for _, j := range joins {
		body := Tf("%s on %s joined %s, as a clone of its repository.", projectName(j.at.Path), nodeName(nodes, j.at.Node), j.into)
		m.toaster.Push(ui.Toast(T("A new place for a project's folder"), body).Action(T("Take out"), func() { apply(j.undo) }))
	}
}

// migrateProjectKeys moves what was kept by the key a section of one
// clone of a repository had, its node and folder, to the key it has now,
// its repository's, which the kept folders remember: whether it was
// folded, and the tabs a space kept for it.
func (m *model) migrateProjectKeys() {
	moved := map[string]string{}
	for _, r := range ggui.Peek(m.Prefs).Projects {
		if r.Remote != "" {
			moved["project:"+dirKey(r.Node, r.Path)] = "project:repo:" + r.Remote
		}
	}
	if len(moved) == 0 {
		return
	}
	if slices.ContainsFunc(ggui.Peek(m.Prefs).Collapsed, func(k string) bool { _, ok := moved[k]; return ok }) {
		m.Prefs.Update(func(p prefs) prefs {
			out := make([]string, 0, len(p.Collapsed))
			for _, k := range p.Collapsed {
				out = append(out, cmp.Or(moved[k], k))
			}
			slices.Sort(out)
			p.Collapsed = slices.Compact(out)
			return p
		})
	}
	m.rekeyContexts(moved)
}

// Daemons --------------------------------------------------------------------

// nodeScopes are the projects as node's daemon is told of them: the
// folders each has there, for those with any.
func nodeScopes(defs []project, node string) []*protocol.ProjectScope {
	var out []*protocol.ProjectScope
	for _, p := range defs {
		s := &protocol.ProjectScope{Id: p.ID, Name: p.Name, GiveDirs: p.GiveDirs}
		for _, l := range p.locations() {
			if l.Node == node && !slices.Contains(s.Paths, l.Path) {
				s.Paths = append(s.Paths, l.Path)
			}
		}
		if len(s.Paths) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// pushProjects tells each reachable node's daemon that knows of projects
// the ones with folders there, when they are not as it was last told: its
// agents in one project's folders are then one project's. UI goroutine.
func (m *model) pushProjects(nodes []*protocol.Node) {
	if m.post == nil {
		return
	}
	defs := m.projectsNow()
	for _, n := range nodes {
		if !reachable(n) || n.GetRevision() < protocol.RevisionProjects {
			continue
		}
		node := n.GetId()
		scopes := nodeScopes(defs, node)
		if sent, ok := m.projectsSent[node]; ok && slices.EqualFunc(sent, scopes, func(a, b *protocol.ProjectScope) bool { return proto.Equal(a, b) }) {
			continue
		}
		m.projectsSent[node] = scopes
		req := &protocol.SetProjects{NodeId: m.remoteID(node), Projects: scopes}
		fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Projects, error) {
			return c.SetProjects(ctx, req)
		}, func(_ *protocol.Projects, err error) {
			if err != nil {
				delete(m.projectsSent, node) // told again at the next poll
			}
		})
	}
}
