package gui

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/ironpark/ggui"

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
	p := ggui.ProbeBuilder(func() ggui.Widget { return ggui.Box() }, ggui.Sz(100, 100))
	p.Setup(func() { setupWorkspace(m) })
	t.Cleanup(p.Close)
	p.Frame()
	return m, p
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
	m.wsVersion = 2
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
	if m.wsVersion != newer || m.wsNode != "b" {
		t.Fatalf("version %v %q after taking one up", m.wsVersion, m.wsNode)
	}
	m.Prefs.Update(func(p prefs) prefs { p.SyncWorkspace = false; return p })
	m.takeWorkspace(&protocol.Workspace{Doc: []byte(`{"doc":{}}`), Version: newer + 1, NodeId: "c"})
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"theirs"}) {
		t.Fatalf("taken up while not following: %v", tabs)
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

// A space that keeps tabs by project shows each project's own, following
// the session picked; kept whole again, it shows the space's.
func TestTabsKeptByProject(t *testing.T) {
	m, p := workspaceModel(t)
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "a1", NodeId: "n", Project: "/w/a"},
		{Id: "a2", NodeId: "n", Project: "/w/a/sub", Repo: "/w/a"},
		{Id: "b1", NodeId: "n", Project: "/w/b"},
	})
	m.showTab("whole")
	m.Spaces.Update(func(l spaceList) spaceList { l.AllLayoutBy = layoutByProject; return l })
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
	m.Spaces.Update(func(l spaceList) spaceList { l.AllLayoutBy = layoutBySpace; return l })
	p.Frame()
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"whole"}) {
		t.Fatalf("the whole space shows %v", tabs)
	}
	if got := len(m.workspaceDocNow().Layouts); got != 3 {
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
	m.Spaces.Update(func(l spaceList) spaceList { l.AllLayoutBy = layoutByProject; return l })
	p.Frame()
	m.selectSession("b1")
	m.writeWorkspace()
	time.Sleep(100 * time.Millisecond) // the write is on its own goroutine

	restart := func(prep func(*model)) *model {
		again := newModel(config.Default(), "")
		again.post = m.post
		prep(again)
		q := ggui.ProbeBuilder(func() ggui.Widget { return ggui.Box() }, ggui.Sz(100, 100))
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
