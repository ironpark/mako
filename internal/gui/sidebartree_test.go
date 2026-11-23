package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// What is pinned shows at the top and not again below: a folder on its
// own, out of its project's tree, and a project whole, its folders under
// it, its owner's heading gone once it holds nothing else.
func TestPinnedProjectsShowOnce(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sidebarPreview(m) // ggui, a folder of XXGG/MAKO, pinned
	st := newSidebarState(m)
	p := ggui.ProbeBuilder(func() ggui.Widget { return projectTree(m, st, 0) }, ggui.Sz(280, 1200))
	defer p.Close()
	p.Frame()
	has := func(name string) bool { _, ok := p.Semantics().Find(ggui.RoleButton, name); return ok }
	if !has("XXGG/MAKO / ggui") || has("#ggui") || !has("#mako") {
		t.Fatalf("the folder pinned is not at the top alone:\n%s", p.Semantics().String())
	}
	m.togglePinScope(scopeKey{Section: "xxgg"})
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	if has("XXGG/MAKO / ggui") || !has("#ggui") || !has("#mako") || !has("XXGG/MAKO") {
		t.Fatalf("the project pinned is not at the top whole:\n%s", sem)
	}
	if strings.Count(sem, `button "#mako"`) != 1 || strings.Contains(sem, Tf("%s actions", "xxgg")) {
		t.Fatalf("the project pinned shows below too:\n%s", sem)
	}
}

func TestSidebarProjectHoverNewSession(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sidebarPreview(m)
	st := newSidebarState(m)
	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Scroll(projectTree(m, st, 0)) }, ggui.Sz(280, 1200))
	defer p.Close()
	p.Frame()

	center := func(r ggui.Rect) ggui.Point {
		return r.Origin.Add(ggui.Pt(r.Size.W/2, r.Size.H/2))
	}
	hover := func(name string) {
		t.Helper()
		n, ok := p.Semantics().Find(ggui.RoleButton, name)
		if !ok {
			t.Fatalf("button %q not found:\n%s", name, p.Semantics().String())
		}
		p.Move(center(n.Rect))
		p.Frame()
		p.Frame()
	}

	// Single-root project shows + on hover and opens a new session tab there.
	hover("acp-go")
	plus, ok := p.Semantics().Find(ggui.RoleButton, Tf("New session in %s", "acp-go"))
	if !ok {
		t.Fatalf("missing + button on hovered project:\n%s", p.Semantics().String())
	}
	p.Click(center(plus.Rect))
	p.Frame()
	tab := ggui.Peek(m.Selected)
	if !isLaunchTab(tab) || m.launches[tab] == nil || ggui.Peek(m.launches[tab].Form).Project != "/w/acp-go" {
		t.Fatalf("expected launch tab in /w/acp-go, got tab=%q", tab)
	}

	// Merged project parent row does not show +; its sub-project row does.
	hover("XXGG/MAKO")
	if _, ok := p.Semantics().Find(ggui.RoleButton, Tf("New session in %s", "XXGG/MAKO")); ok {
		t.Fatal("merged project parent row should not show a + button")
	}
	hover("#mako")
	subPlus, ok := p.Semantics().Find(ggui.RoleButton, Tf("New session in %s", "#mako"))
	if !ok {
		t.Fatalf("missing + button on hovered sub-project:\n%s", p.Semantics().String())
	}
	p.Click(center(subPlus.Rect))
	p.Frame()
	tab = ggui.Peek(m.Selected)
	if !isLaunchTab(tab) || m.launches[tab] == nil || ggui.Peek(m.launches[tab].Form).Project != "/w/mako" {
		t.Fatalf("expected launch tab in /w/mako, got tab=%q", tab)
	}
}

func TestSidebarProjectContextMenuHasOpenLocation(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sidebarPreview(m)
	st := newSidebarState(m)
	groups := m.spaceSections(st, 0)
	var single, merged sessionGroup
	for _, g := range groups {
		if g.Key == "xxgg" {
			merged = g
		}
		if g.Project == "/w/acp-go" {
			single = g
		}
	}
	if single.Key == "" || merged.Key == "" {
		t.Fatalf("expected single and merged groups, got %d groups", len(groups))
	}
	wantLabel := fileManagerLabel()

	// Single-root project menu has "Show in Finder" (or platform equivalent).
	if item := projectLocationItem(m, single); item == nil {
		t.Fatalf("expected %q item for single-root project", wantLabel)
	}
	// Merged project menu has a submenu for opening its folders.
	if item := projectLocationItem(m, merged); item == nil {
		t.Fatalf("expected %q submenu for merged project", wantLabel)
	}
	// Sub-project menu has "Show in Finder" (or platform equivalent).
	if node, path := m.rootLocation(merged, merged.Def.Roots[0]); node != "studio" || path != "/w/mako" {
		t.Fatalf("unexpected sub-project location (%q, %q)", node, path)
	}
}

// When a sub-project with no sessions is selected in the sidebar, opening a
// new session via Cmd+T / empty-state (openNew("", "")) or the pane tab bar +
// targets that sub-project rather than another sub-project or the home folder.
func TestNewSessionInEmptySubProject(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sidebarPreview(m)

	// Remove all sessions from /w/ggui so it is an empty sub-project while
	// /w/mako still has sessions.
	sessions := ggui.Peek(m.Sessions)
	var kept []*protocol.SessionInfo
	var makoSession string
	for _, s := range sessions {
		if s.GetProject() == "/w/ggui" {
			continue
		}
		if s.GetProject() == "/w/mako" && makoSession == "" {
			makoSession = s.GetId()
		}
		kept = append(kept, s)
	}
	m.Sessions.Set(kept)
	if makoSession == "" {
		t.Fatal("expected a session in /w/mako")
	}

	// Find ggui's rootKey and select it as the sidebar scope.
	var gguiRoot projectRoot
	for _, p := range m.projectsNow() {
		if p.ID == "xxgg" {
			gguiRoot = p.Roots[1]
		}
	}
	if gguiRoot.folderName() != "ggui" {
		t.Fatalf("expected ggui root, got %+v", gguiRoot)
	}

	// Open a /w/mako session tab in the project's pane, then scope to empty #ggui.
	m.showProjectTabs("xxgg")
	m.editLayout(func(root *layoutNode) int {
		g := root.groups()[0]
		g.Tabs = []string{makoSession}
		g.Active = makoSession
		return g.ID
	})
	m.setScope(scopeKey{Section: "xxgg", Root: rootKey(gguiRoot)})

	// 1) openNew("", "") (Cmd+T, empty-state button, command palette) targets /w/ggui.
	l := m.openNew("", "")
	if got := ggui.Peek(l.Form); got.NodeID != "studio" || got.Project != "/w/ggui" {
		t.Fatalf("openNew(\"\", \"\") got (%q, %q), want (\"studio\", \"/w/ggui\")", got.NodeID, got.Project)
	}

	// 2) Pane tab bar + button while /w/mako's tab is active still targets /w/ggui.
	m.editLayout(func(root *layoutNode) int {
		g := root.groups()[0]
		g.Tabs = []string{makoSession}
		g.Active = makoSession
		return g.ID
	})
	m.setScope(scopeKey{Section: "xxgg", Root: rootKey(gguiRoot)})
	group := ggui.Derived(func() tabGroup { return *m.Layout.Get().groups()[0] })
	p := ggui.ProbeBuilder(func() ggui.Widget { return paneTabBar(m, 1, group) }, ggui.Sz(800, 48))
	defer p.Close()
	p.Frame()
	plus, ok := p.Semantics().Find(ggui.RoleButton, T("New session…"))
	if !ok {
		t.Fatalf("missing pane + button:\n%s", p.Semantics().String())
	}
	p.Click(plus.Rect.Origin.Add(ggui.Pt(plus.Rect.Size.W/2, plus.Rect.Size.H/2)))
	p.Frame()
	tab := ggui.Peek(m.Selected)
	if !isLaunchTab(tab) || m.launches[tab] == nil {
		t.Fatalf("expected launch tab, got %q", tab)
	}
	if got := ggui.Peek(m.launches[tab].Form); got.NodeID != "studio" || got.Project != "/w/ggui" {
		t.Fatalf("pane + button got (%q, %q), want (\"studio\", \"/w/ggui\")", got.NodeID, got.Project)
	}
}

// Selecting a project or sub-project with no sessions still sets filesRoot so
// the right sidebar's Files & Git panel shows that project's files.
func TestPickScopePopulatesFilesRootWithoutSession(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sidebarPreview(m)
	// Clear all sessions
	m.Sessions.Set(nil)
	m.setLayout(newLayout())

	// Scope to empty #ggui
	var gguiRoot projectRoot
	for _, p := range m.projectsNow() {
		if p.ID == "xxgg" {
			gguiRoot = p.Roots[1]
		}
	}
	m.setScope(scopeKey{Section: "xxgg", Root: rootKey(gguiRoot)})

	// filesRoot() should point to /w/ggui on node studio
	loc := m.filesRoot()
	if loc.Node != "studio" || loc.Path != "/w/ggui" {
		t.Fatalf("filesRoot() = (%q, %q), want (\"studio\", \"/w/ggui\")", loc.Node, loc.Path)
	}
}

// When selecting a subproject in the sidebar, if active tab belongs to another
// subproject, switch active tab to an existing agent session of the selected subproject.
func TestPickSubProjectSwitchesActiveTab(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sidebarPreview(m)

	var makoSession, gguiSession string
	for _, s := range ggui.Peek(m.Sessions) {
		if s.GetProject() == "/w/mako" && makoSession == "" {
			makoSession = s.GetId()
		}
		if s.GetProject() == "/w/ggui" && gguiSession == "" {
			gguiSession = s.GetId()
		}
	}
	if makoSession == "" || gguiSession == "" {
		t.Fatalf("expected sessions in both /w/mako and /w/ggui, got mako=%q ggui=%q", makoSession, gguiSession)
	}

	// Layout has both tabs in pane 1, with makoSession active
	m.editLayout(func(root *layoutNode) int {
		g := root.groups()[0]
		g.Tabs = []string{makoSession, gguiSession}
		g.Active = makoSession
		return g.ID
	})
	m.selectSession(makoSession)

	// User picks ggui in the sidebar
	var gguiRoot projectRoot
	for _, p := range m.projectsNow() {
		if p.ID == "xxgg" {
			gguiRoot = p.Roots[1]
		}
	}
	k := scopeKey{Section: "xxgg", Root: rootKey(gguiRoot)}
	m.setScope(k)

	// Active tab in layout and Selected should switch to gguiSession
	if got := ggui.Peek(m.Selected); got != gguiSession {
		t.Fatalf("m.Selected = %q, want %q", got, gguiSession)
	}
	if got := m.Layout.Get().groups()[0].Active; got != gguiSession {
		t.Fatalf("active tab = %q, want %q", got, gguiSession)
	}
}

func TestProjectMachineFilterHelpers(t *testing.T) {
	nodes := []*protocol.Node{
		{Id: "box", Name: "build-box", Os: "linux", Source: "mesh"},
		{Id: "phone", Name: "Pixel", Os: "android", Source: "mesh"},
		{Id: "studio", Name: "MacStudio", Os: "darwin", Source: "self"},
		{Id: "air", Name: "Air", Os: "darwin", Source: "mesh"},
	}
	groups := []sessionGroup{
		{
			Key:    "p1",
			NodeID: "studio",
			Roots:  []fileRoot{{Node: "studio", Path: "/w/a"}, {Node: "box", Path: "/srv/a"}},
			Sessions: []*protocol.SessionInfo{
				{Id: "s1", NodeId: "studio"},
				{Id: "s2", NodeId: "air"},
			},
		},
		{
			Key:    "p2",
			NodeID: "studio",
			Roots:  []fileRoot{{Node: "studio", Path: "/w/b"}},
		},
	}

	// Self machine comes first, then alphabetical; mobile nodes without projects are excluded.
	got := projectMachines(groups, nodes, "")
	want := []string{"studio", "air", "box"}
	if len(got) != len(want) {
		t.Fatalf("projectMachines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("projectMachines = %v, want %v", got, want)
		}
	}

	// An active selection for an offline/unknown machine stays listed so it can be cleared.
	withOffline := projectMachines(groups, nodes, "gone")
	if len(withOffline) != 4 {
		t.Fatalf("expected offline selection included, got %v", withOffline)
	}

	// Filtering matches by NodeID, Roots, or Sessions without mutating groups.
	if got := filterGroupsByMachine(groups, ""); len(got) != 2 {
		t.Fatalf("empty filter returned %d groups, want 2", len(got))
	}
	if got := filterGroupsByMachine(groups, "studio"); len(got) != 2 {
		t.Fatalf("studio filter returned %d groups, want 2", len(got))
	}
	if got := filterGroupsByMachine(groups, "box"); len(got) != 1 || got[0].Key != "p1" || len(got[0].Roots) != 2 {
		t.Fatalf("box filter returned %+v", got)
	}
	if got := filterGroupsByMachine(groups, "air"); len(got) != 1 || got[0].Key != "p1" {
		t.Fatalf("air filter returned %+v", got)
	}
	if got := filterGroupsByMachine(groups, "desk"); len(got) != 0 {
		t.Fatalf("desk filter returned %d groups, want 0", len(got))
	}
}

func TestProjectsPanelMachineFilterUI(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sidebarPreview(m) // 4 machines: studio, air, box, desk
	st := newSidebarState(m)
	p := ggui.ProbeBuilder(func() ggui.Widget { return projectsPanel(m, st, 0) }, ggui.Sz(280, 1200))
	defer p.Close()
	p.Frame()

	has := func(name string) bool { _, ok := p.Semantics().Find(ggui.RoleButton, name); return ok }
	hasMenu := func(name string) bool { _, ok := p.Semantics().Find(ggui.RoleMenu, name); return ok }

	// Multi-machine setup shows the machine filter dropdown and all projects.
	if !hasMenu(T("Filter projects by machine")) {
		t.Fatalf("expected machine filter dropdown in multi-machine sidebar:\n%s", p.Semantics().String())
	}
	if !has("acp-go") || !has("XXGG/MAKO") || !has("XXGG/MAKO / ggui") || !has("#mako") || !has("#ggwd") {
		t.Fatalf("expected all projects before filtering:\n%s", p.Semantics().String())
	}

	// Filtering to "desk" shows only XXGG/MAKO and its #ggwd root (where desk has a session),
	// hides studio-only projects and non-desk sub-projects including pinned ggui.
	m.setProjectMachine("desk")
	for range 3 {
		p.Frame()
	}
	if !has("XXGG/MAKO") || !has("#ggwd") {
		t.Fatalf("expected XXGG/MAKO and #ggwd on desk:\n%s", p.Semantics().String())
	}
	if has("acp-go") || has("gostty") || has("qngg") || has("#mako") || has("#ggfx") || has("XXGG/MAKO / ggui") {
		t.Fatalf("unexpected non-desk projects shown:\n%s", p.Semantics().String())
	}

	// Agents list remains unfiltered by ProjectMachine.
	agents := m.listAgents(st, 0, agentView{Group: agentGroupNone})
	if agents.shown() != len(ggui.Peek(m.Sessions)) {
		t.Fatalf("agents list affected by project machine filter: got %d, want %d", agents.shown(), len(ggui.Peek(m.Sessions)))
	}

	// Filtering to a machine with no projects shows the reset button, and clicking it clears the filter.
	m.setProjectMachine("empty-node")
	for range 3 {
		p.Frame()
	}
	if has("XXGG/MAKO") {
		t.Fatalf("expected no projects on empty-node:\n%s", p.Semantics().String())
	}
	resetBtn, ok := p.Semantics().Find(ggui.RoleButton, T("Show all machines"))
	if !ok {
		t.Fatalf("expected reset button when machine filter is active:\n%s", p.Semantics().String())
	}
	p.Click(resetBtn.Rect.Origin.Add(ggui.Pt(resetBtn.Rect.Size.W/2, resetBtn.Rect.Size.H/2)))
	for range 3 {
		p.Frame()
	}
	if got := ggui.Peek(m.Prefs).ProjectMachine; got != "" {
		t.Fatalf("ProjectMachine = %q after reset click, want empty", got)
	}
	if !has("acp-go") || !has("XXGG/MAKO") {
		t.Fatalf("expected all projects restored after reset:\n%s", p.Semantics().String())
	}

	// Single-machine setup hides the machine filter dropdown.
	m.Nodes.Set([]*protocol.Node{{Id: "studio", Name: "MacStudio.local", Os: "darwin", Source: "self"}})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s1", NodeId: "studio", Project: "/w/acp-go", Agent: "claude", State: protocol.StateRunning}})
	m.Spaces.Set(spaceList{})
	for range 3 {
		p.Frame()
	}
	if hasMenu(T("Filter projects by machine")) {
		t.Fatalf("expected machine filter dropdown hidden on single-machine setup:\n%s", p.Semantics().String())
	}
}
