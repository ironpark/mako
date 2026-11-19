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
