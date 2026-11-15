package gui

import (
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// product is a project of a server's repository and a web app's folder,
// which is in no repository.
func product() project {
	return project{ID: "repo:github.com/acme/server", Name: "Product", Roots: []projectRoot{
		{Remote: "github.com/acme/server", Owner: "github.com/acme", Locations: []fileRoot{{Node: "mac", Path: "/w/server"}}},
		{Label: "web", Locations: []fileRoot{{Node: "mac", Path: "/w/web"}}},
	}}
}

// A project holds the sessions in its folders, in their subfolders and
// in its repositories' worktrees, each marked with its root; the others
// group as they did.
func TestAProjectHoldsItsFolders(t *testing.T) {
	sessions := []*protocol.SessionInfo{
		{Id: "api", NodeId: "mac", Project: "/w/server", Repo: "/w/server", Remote: "github.com/acme/server"},
		{Id: "tree", NodeId: "mac", Project: "/tmp/server-fix", Repo: "/w/server", Remote: "github.com/acme/server"},
		{Id: "ui", NodeId: "mac", Project: "/w/web/src"},
		// A repository of its own inside the web folder stays apart.
		{Id: "lib", NodeId: "mac", Project: "/w/web/vendor/lib", Repo: "/w/web/vendor/lib"},
		{Id: "notes", NodeId: "mac", Project: "/w/notes"},
		// The same path on another node is not the project's.
		{Id: "box", NodeId: "box", Project: "/w/web"},
	}
	groups := groupProjects(sessions, nil, nil, []project{product()}, nil)
	got := map[string][]string{}
	var p sessionGroup
	for _, g := range groups {
		for _, s := range g.Sessions {
			got[groupName(g)] = append(got[groupName(g)], s.GetId())
		}
		if g.Key == product().ID {
			p = g
		}
	}
	if !slices.Equal(got["Product"], []string{"api", "tree", "ui"}) || len(got["lib"]) != 1 || len(got["notes"]) != 1 || len(got["web"]) != 1 {
		t.Fatalf("sections %v", got)
	}
	if !p.multiRoot() || p.RootOf["api"] != 0 || p.RootOf["ui"] != 1 || p.Def.Roots[p.RootOf["ui"]].label() != "web" {
		t.Fatalf("the project's section %+v", p)
	}
	if p.NodeID != "mac" || p.Project != "/w/server" || len(p.Roots) != 2 || p.Owner != "" {
		t.Fatalf("starts in %s:%s, roots %v, owner %q", p.NodeID, p.Project, p.Roots, p.Owner)
	}
}

// A clone of a project's repository elsewhere joins its root, and stays
// out once taken out; a repository two roots claim joins neither.
func TestClonesJoinTheirRoot(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Spaces.Set(spaceList{Projects: []project{product()}})
	clone := &protocol.SessionInfo{Id: "c", NodeId: "box", Project: "/srv/server/cmd", Repo: "/srv/server", Remote: "github.com/acme/server"}
	if pl := placeSession(m.projectsNow(), clone); pl.At != 0 || pl.Root != 0 || !pl.Join {
		t.Fatalf("placed %+v", pl)
	}
	m.joinClones([]*protocol.SessionInfo{clone})
	locs := m.projectsNow()[0].Roots[0].Locations
	if !slices.Contains(locs, fileRoot{Node: "box", Path: "/srv/server"}) {
		t.Fatalf("locations %v", locs)
	}
	if pl := placeSession(m.projectsNow(), clone); pl.Join {
		t.Fatalf("a joined clone joins again: %+v", pl)
	}

	declined := product()
	declined.Roots[0].Declined = []fileRoot{{Node: "box", Path: "/srv/server"}}
	if pl := placeSession([]project{declined}, clone); pl.At >= 0 {
		t.Fatalf("a clone taken out joined: %+v", pl)
	}
	twice := []project{product(), {ID: "other", Roots: []projectRoot{{Remote: "github.com/acme/server", Locations: []fileRoot{{Node: "pi", Path: "/x"}}}}}}
	if pl := placeSession(twice, clone); pl.At >= 0 {
		t.Fatalf("a repository two roots claim joined: %+v", pl)
	}
}

// A folder added to a project as a folder learns its repository from the
// first session there that says.
func TestARootLearnsItsRepository(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Spaces.Set(spaceList{Projects: []project{product()}})
	m.joinClones([]*protocol.SessionInfo{{Id: "w", NodeId: "mac", Project: "/w/web", Repo: "/w/web", Remote: "github.com/acme/web", Owner: "github.com/acme"}})
	if r := m.projectsNow()[0].Roots[1]; r.Remote != "github.com/acme/web" || r.Owner != "github.com/acme" {
		t.Fatalf("root %+v", r)
	}
}

// Naming a section the sidebar groups itself changes only its name: what
// is in it is still the sidebar's to say.
func TestNamingASectionKeepsItsGrouping(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sessions := []*protocol.SessionInfo{{Id: "1", NodeId: "mac", Project: "/w/notes"}}
	g := groupProjects(sessions, nil, nil, nil, nil)[0]
	m.renameProject(g, "Notes")
	groups := groupProjects(append(sessions, &protocol.SessionInfo{Id: "2", NodeId: "mac", Project: "/w/notes/sub"}), nil, nil, m.projectsNow(), ggui.Peek(m.Spaces).Names)
	if len(groups) != 2 || groupName(groups[0]) != "Notes" || groups[0].Def != nil || len(groups[0].Sessions) != 1 {
		t.Fatalf("sections %+v", groups)
	}
	m.renameProject(groups[0], "")
	if l := ggui.Peek(m.Spaces); len(l.Projects)+len(l.Names) != 0 {
		t.Fatalf("a name given back left %+v", m.projectsNow())
	}
}

// Merged, two sections are one project under the target's ID and name,
// which the spaces holding the other now hold; taking a folder out gives
// it back its own section, apart from the project of its old key.
func TestMergingProjects(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sessions := []*protocol.SessionInfo{
		{Id: "s", NodeId: "mac", Project: "/w/server", Repo: "/w/server", Remote: "github.com/acme/server", Owner: "github.com/acme"},
		{Id: "w", NodeId: "mac", Project: "/w/web"},
	}
	webKey := dirKey("mac", "/w/web")
	m.Spaces.Set(spaceList{Spaces: []space{{ID: "x", Projects: []string{webKey}}}})
	find := func(groups []sessionGroup, key string) sessionGroup {
		i := slices.IndexFunc(groups, func(g sessionGroup) bool { return g.Key == key })
		if i < 0 {
			t.Fatalf("no section %q in %+v", key, groups)
		}
		return groups[i]
	}
	groups := groupProjects(sessions, nil, nil, nil, nil)
	server := find(groups, "repo:github.com/acme/server")
	m.mergeProjects(find(groups, webKey), server)

	groups = groupProjects(sessions, nil, nil, m.projectsNow(), nil)
	if len(groups) != 1 || groups[0].Key != server.Key || groupName(groups[0]) != "server" || len(groups[0].Sessions) != 2 || !groups[0].multiRoot() {
		t.Fatalf("after merging: %+v", groups)
	}
	if sp := ggui.Peek(m.Spaces).Spaces[0]; !slices.Equal(sp.Projects, []string{server.Key}) {
		t.Fatalf("the space holds %v", sp.Projects)
	}

	m.removeRoot(groups[0], 0) // the server's own repository
	groups = groupProjects(sessions, nil, nil, m.projectsNow(), nil)
	keys := []string{}
	for _, g := range groups {
		keys = append(keys, g.Key)
	}
	slices.Sort(keys)
	if want := []string{freePrefix + server.Key, server.Key}; !slices.Equal(keys, want) {
		t.Fatalf("sections %v, want %v", keys, want)
	}
	if id := sectionID(m.projectsNow(), sessions[0]); id != freePrefix+server.Key {
		t.Fatalf("the server's session is in %q", id)
	}

	m.ungroup(find(groups, server.Key))
	if len(m.projectsNow()) != 0 || len(groupProjects(sessions, nil, nil, nil, nil)) != 2 {
		t.Fatalf("after ungrouping: %+v", m.projectsNow())
	}
}

// A repository of one clone was kept by its node and folder; it is kept
// by its repository now, its pin and its tabs with it.
func TestOldProjectKeysMove(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	old := "project:" + dirKey("mac", "/w/server")
	m.Prefs.Update(func(p prefs) prefs {
		p.Projects = []keptProject{{fileRoot{Node: "mac", Path: "/w/server"}, "github.com/acme", "github.com/acme/server"}}
		p.PinnedProjects = []scopeKey{{Section: dirKey("mac", "/w/server")}, {Section: "other"}}
		return p
	})
	m.ws.layouts = map[string]*layoutNode{layoutKey("all", old): newLayout()}
	m.ws.contexts = map[string]string{"all": old}
	m.migrateProjectKeys()
	now := "project:repo:github.com/acme/server"
	if got := ggui.Peek(m.Prefs).PinnedProjects; !slices.Equal(got, []scopeKey{{Section: "repo:github.com/acme/server"}, {Section: "other"}}) {
		t.Fatalf("pinned %v", got)
	}
	if _, ok := m.ws.layouts[layoutKey("all", now)]; !ok || m.ws.contexts["all"] != now {
		t.Fatalf("layouts %v, contexts %v", m.ws.layouts, m.ws.contexts)
	}
}

// The sidebar draws a project of two folders under its name, each marked
// # and in the project's order, and its menu; picked, the project scopes
// the agents to it, and a folder of it to that folder.
func TestSidebarShowsAProject(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Spaces.Set(spaceList{Projects: []project{product()}})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "api00000", NodeId: "mac", Project: "/w/server", Repo: "/w/server", State: protocol.StateRunning},
		{Id: "ui000000", NodeId: "mac", Project: "/w/web", State: protocol.StateRunning},
		{Id: "else0000", NodeId: "mac", Project: "/w/else", State: protocol.StateRunning},
	})
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 900))
	defer p.Close()
	p.Frame()
	page := p.Semantics().String()
	for _, label := range []string{Tf("%s actions", "Product"), Tf("Icon of %s", "Product"), "#server", "#web"} {
		if !strings.Contains(page, label) {
			t.Fatalf("the sidebar has no %q:\n%s", label, page)
		}
	}
	server, okServer := p.Semantics().Find(ggui.RoleButton, "#server")
	web, okWeb := p.Semantics().Find(ggui.RoleButton, "#web")
	if !okServer || !okWeb || server.Rect.Origin.Y >= web.Rect.Origin.Y {
		t.Fatalf("no folders in order:\n%s", page)
	}
	agents := func() []string {
		l := m.listAgents(newSidebarState(m), 0, agentView{Group: agentGroupNone})
		var ids []string
		for _, g := range l.groups {
			for _, s := range g.Sessions {
				ids = append(ids, s.GetId())
			}
		}
		slices.Sort(ids)
		return ids
	}
	if got := agents(); len(got) != 3 {
		t.Fatalf("every agent of the space: %v", got)
	}
	p.Tap("Product")
	p.Frame()
	if got := agents(); !slices.Equal(got, []string{"api00000", "ui000000"}) {
		t.Fatalf("the project's agents: %v", got)
	}
	p.Tap("#web")
	p.Frame()
	if got := agents(); !slices.Equal(got, []string{"ui000000"}) {
		t.Fatalf("the folder's agents: %v", got)
	}
	l := m.listAgents(newSidebarState(m), 0, agentView{Group: agentGroupFolder})
	if l.by != agentGroupNone {
		t.Fatalf("one folder grouped by %q", l.by)
	}
	// The project's icon opens its picker, which keeps what is picked.
	p.Tap(Tf("Icon of %s", "Product"))
	p.Frame()
	p.Tap(T("Rocket"))
	p.Frame()
	if got := ggui.Peek(m.Spaces).Looks[product().ID]; got.Icon != "rocket" {
		t.Fatalf("the look picked is %+v", got)
	}
}

// A node's daemon hears of the projects' folders on it, and of those
// that give their folders.
func TestNodeScopes(t *testing.T) {
	p := product()
	p.GiveDirs = true
	p.Roots[0].Locations = append(p.Roots[0].Locations, fileRoot{Node: "box", Path: "/srv/server"})
	defs := []project{p}
	mac := nodeScopes(defs, "mac")
	if len(mac) != 1 || mac[0].GetId() != p.ID || !slices.Equal(mac[0].GetPaths(), []string{"/w/server", "/w/web"}) || !mac[0].GetGiveDirs() {
		t.Fatalf("mac %v", mac)
	}
	if box := nodeScopes(defs, "box"); len(box) != 1 || !slices.Equal(box[0].GetPaths(), []string{"/srv/server"}) {
		t.Fatalf("box %v", box)
	}
	if pi := nodeScopes(defs, "pi"); pi != nil {
		t.Fatalf("pi %v", pi)
	}
}

// The settings save a section's draft: one the sidebar grouped itself
// stays so when only its name changed, and is a project once its ways
// change; a folder made first starts its sessions.
func TestProjectSettings(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sessions := []*protocol.SessionInfo{{Id: "1", NodeId: "mac", Project: "/w/notes"}}
	g := groupProjects(sessions, nil, nil, nil, nil)[0]
	m.openProjectSettings(g)
	m.Proj.Draft.Update(func(p project) project { p.Name = "Notes"; return p })
	m.saveProjectSettings()
	if l := ggui.Peek(m.Spaces); len(l.Projects) != 0 || l.Names[g.Key] != "Notes" {
		t.Fatalf("named: %+v", l)
	}

	m.Spaces.Set(spaceList{Projects: []project{product()}})
	g = groupProjects(nil, nil, nil, m.projectsNow(), nil)[0]
	m.openProjectSettings(g)
	m.Proj.Draft.Update(func(p project) project {
		p = p.clone()
		p.Roots = []projectRoot{p.Roots[1], p.Roots[0]}
		p.GiveDirs = true
		return p
	})
	m.saveProjectSettings()
	g = groupProjects(nil, nil, nil, m.projectsNow(), nil)[0]
	if g.Project != "/w/web" || !g.Def.GiveDirs {
		t.Fatalf("after saving, starts in %s, gives %v", g.Project, g.Def.GiveDirs)
	}
}
