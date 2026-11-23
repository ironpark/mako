package gui

import (
	"slices"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// Each New session opens a tab of its own, with what it picks of its own,
// and a tab closed forgets its own alone.
func TestEachNewSessionOpensItsOwnTab(t *testing.T) {
	m := newModel(config.Default(), "")
	a := m.openNew("n", "/work/a")
	b := m.openNew("n", "/work/b")
	tabs := slices.DeleteFunc(slices.Clone(ggui.Peek(m.Tabs)), func(id string) bool { return !isLaunchTab(id) })
	if len(tabs) != 2 || a == b {
		t.Fatalf("new sessions' tabs = %q", tabs)
	}
	if m.launchShown() != b {
		t.Fatal("the tab opened last is not the one shown")
	}
	a.Form.Update(func(f newSession) newSession { f.Prompt = "first"; return f })
	if ggui.Peek(b.Form).Prompt != "" || ggui.Peek(b.Form).Project != "/work/b" {
		t.Fatalf("the second tab took the first's: %+v", ggui.Peek(b.Form))
	}
	m.closeLaunch(tabs[1])
	if m.launches[tabs[1]] != nil || m.launches[tabs[0]] != a || ggui.Peek(a.Form).Prompt != "first" {
		t.Fatal("closing one new session's tab lost the other's")
	}
}

// Creating a new session from a pane tab bar or project scope prefers this
// machine's clone when available, falls back to the remote node when the
// project exists only there, and still honors explicit remote openNew calls.
func TestNewSessionPrefersLocalWhenAvailable(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	self := &protocol.Node{Id: "studio", Name: "MacStudio", Os: "darwin", Source: "self"}
	box := &protocol.Node{Id: "box", Name: "build-box", Os: "linux", Source: "mesh"}
	m.Conn.Set(connState{OK: true, Self: self})
	m.Nodes.Set([]*protocol.Node{self, box})

	// Defined project: root 0 exists on both box and studio; root 1 only on box.
	// A second multi-root project has root 0 only on box and root 1 on studio.
	m.Spaces.Set(spaceList{
		Projects: []project{
			{
				ID:   "p-both",
				Name: "Both",
				Roots: []projectRoot{
					{
						Remote: "github.com/acme/shared",
						Locations: []fileRoot{
							{Node: "box", Path: "/srv/shared"},
							{Node: "studio", Path: "/w/shared"},
						},
					},
					{
						Remote:    "github.com/acme/linux-only",
						Locations: []fileRoot{{Node: "box", Path: "/srv/linux-only"}},
					},
				},
			},
			{
				ID:   "p-second-local",
				Name: "SecondLocal",
				Roots: []projectRoot{
					{Remote: "github.com/acme/remote-first", Locations: []fileRoot{{Node: "box", Path: "/srv/remote-first"}}},
					{Remote: "github.com/acme/local-second", Locations: []fileRoot{{Node: "studio", Path: "/w/local-second"}}},
				},
			},
		},
	})
	m.Prefs.Update(func(p prefs) prefs {
		p.Projects = []keptProject{
			{Node: "studio", Path: "/w/auto-repo", Remote: "github.com/acme/auto"},
		}
		return p
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-shared-box", NodeId: "box", Project: "/srv/shared/cmd", Repo: "/srv/shared", Remote: "github.com/acme/shared"},
		{Id: "s-linux-only", NodeId: "box", Project: "/srv/linux-only", Repo: "/srv/linux-only", Remote: "github.com/acme/linux-only"},
		{Id: "s-auto-box", NodeId: "box", Project: "/srv/auto-repo", Repo: "/srv/auto-repo", Remote: "github.com/acme/auto"},
	})

	clickPanePlus := func(activeTab string) newSession {
		t.Helper()
		m.editLayout(func(root *layoutNode) int {
			g := root.groups()[0]
			g.Tabs = []string{activeTab}
			g.Active = activeTab
			return g.ID
		})
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
		return ggui.Peek(m.launches[tab].Form)
	}

	// 1) Active remote session in a defined project root that also exists locally -> studio.
	if got := clickPanePlus("s-shared-box"); got.NodeID != "studio" || got.Project != "/w/shared" {
		t.Fatalf("shared defined root: got (%q, %q), want (\"studio\", \"/w/shared\")", got.NodeID, got.Project)
	}

	// 2) Active remote session in an auto-grouped repo that has a local kept clone -> studio.
	if got := clickPanePlus("s-auto-box"); got.NodeID != "studio" || got.Project != "/w/auto-repo" {
		t.Fatalf("auto-grouped repo: got (%q, %q), want (\"studio\", \"/w/auto-repo\")", got.NodeID, got.Project)
	}

	// 3) Active remote doc tab in a shared repo -> studio.
	docID := docTabID("box", "/srv/shared/main.go")
	m.Docs.Set(map[string]docInfo{
		docID: {Node: "box", Path: "/srv/shared/main.go", Root: fileRoot{Node: "box", Path: "/srv/shared"}},
	})
	if got := clickPanePlus(docID); got.NodeID != "studio" || got.Project != "/w/shared" {
		t.Fatalf("remote doc tab: got (%q, %q), want (\"studio\", \"/w/shared\")", got.NodeID, got.Project)
	}

	// 4) Active remote-only session with no local clone -> stays on box.
	if got := clickPanePlus("s-linux-only"); got.NodeID != "box" || got.Project != "/srv/linux-only" {
		t.Fatalf("remote-only session: got (%q, %q), want (\"box\", \"/srv/linux-only\")", got.NodeID, got.Project)
	}

	// 5) Multi-root project whose first root is remote-only and second root is local -> scope picks local.
	m.setScope(scopeKey{Section: "p-second-local"})
	l := m.openNew("", "")
	if got := ggui.Peek(l.Form); got.NodeID != "studio" || got.Project != "/w/local-second" {
		t.Fatalf("project scope with second local root: got (%q, %q), want (\"studio\", \"/w/local-second\")", got.NodeID, got.Project)
	}

	// 6) Explicit remote openNew (e.g. "New session on build-box") is preserved.
	explicit := m.openNew("box", "/srv/shared")
	if got := ggui.Peek(explicit.Form); got.NodeID != "box" || got.Project != "/srv/shared" {
		t.Fatalf("explicit remote openNew: got (%q, %q), want (\"box\", \"/srv/shared\")", got.NodeID, got.Project)
	}
}
