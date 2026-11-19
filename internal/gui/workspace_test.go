package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// workspaceModel is a model with the workspace set up under a probe, which
// it returns for frames.
func workspaceModel(t *testing.T) (*model, *ggui.Probe) {
	t.Helper()
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.post = func(func()) {} // no daemon: what it would answer is dropped
	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Box() }, ggui.Sz(100, 100))
	p.Setup(func() { setupWorkspace(m) })
	t.Cleanup(func() { p.Close(); m.shutdown() })
	p.Frame()
	return m, p
}

// The workspace written down many times over is the last asked for, with
// no file of a write left beside it.
func TestWorkspaceWritesLandInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace.json")
	ws := newWsState()
	for i := range 200 {
		ws.writer.write(path, []byte(strconv.Itoa(i)))
	}
	ws.writer.wait()
	if got, err := os.ReadFile(path); err != nil || string(got) != "199" {
		t.Fatalf("the file holds %q, %v", got, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("files left: %v", entries)
	}
}

// Each space keeps its own tabs and panes: moving to one brings out its
// own, and back brings out the first's again.
func TestEachSpaceKeepsItsTabs(t *testing.T) {
	m, p := workspaceModel(t)
	m.showTab("s1")
	m.newSpace("work", "github.com/acme")
	p.Frame()
	if tabs := ggui.Peek(m.Tabs); len(tabs) != 0 {
		t.Fatalf("a new space shows %v", tabs)
	}
	m.showTab("s2")
	m.setSpace(0)
	p.Frame()
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"s1"}) {
		t.Fatalf("all sessions show %v", tabs)
	}
	m.setSpace(1)
	p.Frame()
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"s2"}) {
		t.Fatalf("work shows %v", tabs)
	}
	doc := m.workspaceDocNow()
	if len(doc.Layouts) != 2 || len(doc.Spaces) != 1 {
		t.Fatalf("the workspace written down: %+v", doc)
	}
}

// The daemon's workspace is taken up, spaces and panes, but for tabs that
// cannot come back; taking it up is no edit of the window's.
func TestTheDaemonsWorkspaceIsTakenUp(t *testing.T) {
	d := newFakeDaemon("me", true)
	m, p := windowOn(t, d)
	m.showTab("mine")
	p.Frame()
	m.saveWorkspace()
	// Another window's edit, later, of the same panes and the spaces.
	sp := space{ID: "w1", Name: "work", Owners: []string{"github.com/acme"}}
	d.edit(t, workspaceDoc{Spaces: []space{sp}, Layouts: map[string]*layoutNode{
		layoutKey(allSpaceID, ""): {ID: 1, Group: &tabGroup{ID: 1, Tabs: []string{"theirs", "diff\x00x"}, Active: "theirs"}},
	}})
	given := len(d.given)
	m.takeWorkspace(d.get())
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"theirs"}) {
		t.Fatalf("tabs after the daemon's workspace %v", tabs)
	}
	if got := ggui.Peek(m.Spaces).Spaces; !slicesEqual(got, []space{sp}) {
		t.Fatalf("spaces %v", got)
	}
	p.Frame()
	m.saveWorkspace()
	if len(d.given) != given {
		t.Fatalf("taking it up gave %v", d.given[given:])
	}
}

// What the window writes down leaves out diffs and empty spaces' panes.
func TestTheWorkspaceLeavesOutDiffs(t *testing.T) {
	m, _ := workspaceModel(t)
	m.showTab("s1")
	m.showTab(diffPrefix + "x")
	doc := m.workspaceDocNow()
	if got := doc.Layouts[layoutKey(allSpaceID, "")].groups()[0].Tabs; !slices.Equal(got, []string{"s1"}) {
		t.Fatalf("written down %v", got)
	}
	m.closeTab("s1") // only the diff left, which is as good as empty
	if doc := m.workspaceDocNow(); len(doc.Layouts) != 0 {
		t.Fatalf("layouts %v", doc.Layouts)
	}
}

// A space keeps tabs for each project, the one shown following the
// session picked, or the project picked in the sidebar.
func TestTabsKeptByProject(t *testing.T) {
	m, p := workspaceModel(t)
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "a1", NodeId: "n", Project: "/w/a"},
		{Id: "a2", NodeId: "n", Project: "/w/a/sub", Repo: "/w/a"},
		{Id: "b1", NodeId: "n", Project: "/w/b"},
	})
	p.Frame()
	m.selectSession("a1")
	m.selectSession("a2")
	m.selectSession("b1")
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"b1"}) {
		t.Fatalf("project b shows %v", tabs)
	}
	m.selectSession("a1")
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"a1", "a2"}) {
		t.Fatalf("project a shows %v", tabs)
	}
	m.pickScope(scopeKey{Section: dirKey("n", "/w/b")})
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"b1"}) {
		t.Fatalf("project b picked shows %v", tabs)
	}
	// A session of another project picked moves the scope to its project.
	m.selectSession("a2")
	if got := ggui.Peek(m.Scope).Section; got != dirKey("n", "/w/a") {
		t.Fatalf("the scope stayed at %q", got)
	}
	if got := len(m.workspaceDocNow().Layouts); got != 2 {
		t.Fatalf("%d layouts written down", got)
	}
}

// Layouts written down by space alone, before contexts, are the whole
// space's.
func TestOldLayoutsAreTheWholeSpaces(t *testing.T) {
	got := keyedLayouts(map[string]*layoutNode{"all": newLayout(), "w1|owner:x": newLayout()})
	for _, key := range []string{"all|", "w1|owner:x"} {
		if got[key] == nil {
			t.Fatalf("no %q in %v", key, got)
		}
	}
}

// The window remembers, across runs, whose tabs it showed in each space;
// and spaces the settings kept before are moved into the workspace.
func TestContextsAndOldSpacesCarryOver(t *testing.T) {
	m, p := workspaceModel(t)
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "b1", NodeId: "n", Project: "/w/b"}})
	p.Frame()
	m.selectSession("b1")
	m.writeWorkspace()
	m.ws.writer.wait() // all queued writes have reached disk

	restart := func(prep func(*model)) *model {
		again := newModel(config.Default(), "")
		again.post = m.post
		prep(again)
		q := ggui.ProbeBuilder(func() ggui.Widget { return widget.Box() }, ggui.Sz(100, 100))
		q.Setup(func() { setupWorkspace(again) })
		t.Cleanup(q.Close)
		q.Frame()
		return again
	}
	again := restart(func(*model) {})
	if tabs := ggui.Peek(again.Tabs); !slices.Equal(tabs, []string{"b1"}) {
		t.Fatalf("after a restart the window shows %v", tabs)
	}
	// The workspace had no spaces, so the settings' old ones come over,
	// the one shown with them.
	again = restart(func(a *model) {
		a.Prefs.Update(func(p prefs) prefs {
			p.OldSpaces, p.OldSpace = []space{{Name: "old", Owners: []string{"x"}}}, 1
			return p
		})
	})
	if got := ggui.Peek(again.Spaces).Spaces; len(got) != 1 || got[0].ID == "" || again.spacesNow().Space != 1 {
		t.Fatalf("old spaces %+v, shown %d", got, again.spacesNow().Space)
	}
	if ggui.Peek(again.Prefs).OldSpaces != nil {
		t.Fatal("the old spaces stay in the settings")
	}
}

// Pins gui.json kept move to the workspace, which shares them, once.
func TestPinsMoveToTheWorkspace(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Prefs.Update(func(p prefs) prefs {
		p.OldPinned, p.OldPinnedProjects = []string{"s1"}, []scopeKey{{Section: "x"}}
		return p
	})
	moveOldSpaces(m)
	l, p := ggui.Peek(m.Spaces), ggui.Peek(m.Prefs)
	if !slices.Equal(l.Pinned, []string{"s1"}) || !slices.Equal(l.PinnedProjects, []scopeKey{{Section: "x"}}) || p.OldPinned != nil || p.OldPinnedProjects != nil {
		t.Fatalf("spaces %+v, prefs pins %v %v", l, p.OldPinned, p.OldPinnedProjects)
	}
}

// A window on a node new to the mesh opens a tab before its node joins:
// its daemon counts the edit, and the workspace the other nodes made is
// taken up over the tab, never the other way.
func TestAWorkspaceFromNothingGivesWay(t *testing.T) {
	fresh, mesh := newFakeDaemon("fresh", false), newFakeDaemon("old", true)
	mesh.edit(t, workspaceDoc{Layouts: map[string]*layoutNode{
		layoutKey(allSpaceID, ""): {ID: 1, Group: &tabGroup{ID: 1, Tabs: []string{"theirs"}, Active: "theirs"}},
	}})
	m, p := windowOn(t, fresh)
	m.showTab("mine")
	p.Frame()
	m.saveWorkspace()
	fresh.pull(mesh)
	m.takeWorkspace(fresh.get())
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"theirs"}) {
		t.Fatalf("the window kept %v over the other nodes' workspace", tabs)
	}
	// From then on, its edits are timed, and win.
	m.showTab("next")
	p.Frame()
	m.saveWorkspace()
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"theirs", "next"}) {
		t.Fatalf("after joining the window shows %v", tabs)
	}
}

// The window's contexts are its own: they are written down in its file,
// and never given to the daemon.
func TestContextsStayInTheWindow(t *testing.T) {
	m, p := workspaceModel(t)
	p.Frame()
	m.ws.contexts[allSpaceID] = projectContext("p1")
	m.writeWorkspace()
	m.ws.writer.wait()
	path, _ := configFile("workspace.json")
	body, _ := os.ReadFile(path)
	var f workspaceFile
	if err := json.Unmarshal(body, &f); err != nil || f.Contexts[allSpaceID] != projectContext("p1") || f.Version != 0 {
		t.Fatalf("written down %s, %v", body, err)
	}
}

// A section another node's workspace takes into a project, its tabs
// with it, is followed there; one done away with leaves the space's own.
func TestContextsFollowTheirSections(t *testing.T) {
	m, p := workspaceModel(t)
	p.Frame()
	dir := keptProject{Node: "n1", Path: "/src/api"}
	m.Prefs.Update(func(p prefs) prefs { p.Projects = []keptProject{dir}; return p })
	m.ws.contexts[allSpaceID] = projectContext(dirKey("n1", "/src/api"))
	m.ws.contexts["w1"] = projectContext(dirKey("n1", "/gone"))
	merged := project{ID: "pj", Name: "both", Roots: []projectRoot{
		{Locations: []fileRoot{{Node: "n1", Path: "/src/api"}}},
		{Locations: []fileRoot{{Node: "n1", Path: "/src/web"}}},
	}}
	doc := workspaceDoc{
		Projects: []project{merged}, Spaces: []space{{ID: "w1", Name: "work"}},
		Layouts: map[string]*layoutNode{
			layoutKey(allSpaceID, projectContext("pj")): {ID: 1, Group: &tabGroup{ID: 1, Tabs: []string{"s1"}, Active: "s1"}},
		},
	}
	m.takeWorkspace(&protocol.Workspace{State: stateOf(t, doc)})
	if got := m.ws.contexts[allSpaceID]; got != projectContext("pj") {
		t.Fatalf("the context followed to %q", got)
	}
	if got := m.ws.contexts["w1"]; got != "" {
		t.Fatalf("a context of nothing is %q", got)
	}
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"s1"}) {
		t.Fatalf("the space shows %v", tabs)
	}
}
