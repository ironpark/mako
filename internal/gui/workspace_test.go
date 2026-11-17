package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

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

// A newer workspace from the daemon is taken up, spaces and all; an older
// one is not, nor one at all while the window does not follow.
func TestANewerWorkspaceIsTakenUp(t *testing.T) {
	m, p := workspaceModel(t)
	m.showTab("mine")
	p.Frame()
	sp := space{ID: "w1", Name: "work", Owners: []string{"github.com/acme"}}
	other := workspaceDoc{spaceList: spaceList{Spaces: []space{sp}}, Layouts: map[string]*layoutNode{
		allSpaceID: {ID: 1, Group: &tabGroup{ID: 1, Tabs: []string{"theirs", "diff\x00x"}, Active: "theirs"}},
	}}
	body, _ := json.Marshal(workspaceFile{Version: 1, NodeID: "b", Doc: other})
	m.ws.version = 2
	m.takeWorkspace(&protocol.Workspace{Doc: body, Version: 1, NodeId: "b"})
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"mine"}) {
		t.Fatalf("an older workspace was taken up: %v", tabs)
	}
	newer := time.Now().UnixNano()
	m.takeWorkspace(&protocol.Workspace{Doc: body, Version: newer, NodeId: "b"})
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"theirs"}) {
		t.Fatalf("tabs after a newer workspace %v", tabs)
	}
	if got := ggui.Peek(m.Spaces).Spaces; !slicesEqual(got, []space{sp}) {
		t.Fatalf("spaces %v", got)
	}
	// Taken up, it is no edit of the window's to send back.
	p.Frame()
	if m.ws.version != newer || m.ws.node != "b" {
		t.Fatalf("version %v %q after taking one up", m.ws.version, m.ws.node)
	}
	m.Prefs.Update(func(p prefs) prefs { p.SyncWorkspace = false; return p })
	m.takeWorkspace(&protocol.Workspace{Doc: []byte(`{"doc":{}}`), Version: newer + 1, NodeId: "c"})
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"theirs"}) {
		t.Fatalf("taken up while not following: %v", tabs)
	}
}

// An edit of a workspace from a node whose clock runs ahead is newer still
// than it: the nodes take it up, rather than undo it with the one it
// changed.
func TestAnEditOutdatesWhatItEdited(t *testing.T) {
	m, p := workspaceModel(t)
	p.Frame()
	ahead := time.Now().Add(time.Hour).UnixNano()
	body, _ := json.Marshal(workspaceFile{Version: ahead, NodeID: "b", Doc: workspaceDoc{}})
	m.takeWorkspace(&protocol.Workspace{Doc: body, Version: ahead, NodeId: "b"})
	m.Spaces.Update(func(l spaceList) spaceList { l.Spaces = []space{{ID: "w", Name: "work"}}; return l })
	m.saveWorkspace()
	if m.ws.version <= ahead {
		t.Fatalf("an edit of %d is %d", ahead, m.ws.version)
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

// A window that begins its workspace from nothing, as a new node's does,
// counts its edits: the workspace the other nodes made is newer, and is
// taken up over the tab it opened meanwhile, never the other way.
func TestAWorkspaceFromNothingGivesWay(t *testing.T) {
	m, p := workspaceModel(t)
	m.showTab("mine")
	p.Frame()
	m.saveWorkspace()
	if m.ws.version != 1 {
		t.Fatalf("the first edit from nothing is version %d", m.ws.version)
	}
	theirs := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	doc := workspaceDoc{Layouts: map[string]*layoutNode{
		layoutKey(allSpaceID, ""): {ID: 1, Group: &tabGroup{ID: 1, Tabs: []string{"theirs"}, Active: "theirs"}},
	}}
	body, _ := json.Marshal(workspaceFile{Version: theirs, NodeID: "b", Doc: doc})
	m.takeWorkspace(&protocol.Workspace{Doc: body, Version: theirs, NodeId: "b"})
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"theirs"}) {
		t.Fatalf("the window kept %v over the other nodes' workspace", tabs)
	}
	// From then on, its edits are timed, and newer than what they edit.
	m.showTab("next")
	m.saveWorkspace()
	if m.ws.version <= theirs {
		t.Fatalf("an edit of %d is %d", theirs, m.ws.version)
	}
}

// The workspace the nodes share leaves out the window's own contexts,
// which only its file keeps.
func TestContextsStayInTheWindow(t *testing.T) {
	m, p := workspaceModel(t)
	p.Frame()
	m.ws.contexts[allSpaceID] = projectContext("p1")
	var f workspaceFile
	if err := json.Unmarshal(m.writeWorkspace(), &f); err != nil || f.Contexts != nil {
		t.Fatalf("shared %+v, %v", f, err)
	}
	m.ws.writer.wait()
	path, _ := configFile("workspace.json")
	body, _ := os.ReadFile(path)
	f = workspaceFile{}
	if err := json.Unmarshal(body, &f); err != nil || f.Contexts[allSpaceID] != projectContext("p1") {
		t.Fatalf("written down %s, %v", body, err)
	}
}

// A section another node's workspace takes into a project, its tabs
// with it, is followed there; one done away with leaves the space's own.
func TestContextsFollowTheirSections(t *testing.T) {
	m, p := workspaceModel(t)
	p.Frame()
	dir := keptProject{fileRoot: fileRoot{Node: "n1", Path: "/src/api"}}
	m.Prefs.Update(func(p prefs) prefs { p.Projects = []keptProject{dir}; return p })
	m.ws.contexts[allSpaceID] = projectContext(dirKey("n1", "/src/api"))
	m.ws.contexts["w1"] = projectContext(dirKey("n1", "/gone"))
	merged := project{ID: "pj", Name: "both", Roots: []projectRoot{
		{Locations: []fileRoot{{Node: "n1", Path: "/src/api"}}},
		{Locations: []fileRoot{{Node: "n1", Path: "/src/web"}}},
	}}
	doc := workspaceDoc{
		spaceList: spaceList{Projects: []project{merged}, Spaces: []space{{ID: "w1", Name: "work"}}},
		Layouts: map[string]*layoutNode{
			layoutKey(allSpaceID, projectContext("pj")): {ID: 1, Group: &tabGroup{ID: 1, Tabs: []string{"s1"}, Active: "s1"}},
		},
	}
	version := time.Now().UnixNano()
	body, _ := json.Marshal(workspaceFile{Version: version, NodeID: "b", Doc: doc})
	m.takeWorkspace(&protocol.Workspace{Doc: body, Version: version, NodeId: "b"})
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
