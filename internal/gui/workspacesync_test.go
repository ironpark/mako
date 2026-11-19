package gui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	shared "github.com/ironpark/mako/internal/workspace"
	"github.com/ironpark/mako/pkg/crdt"
)

// fakeDaemon keeps a workspace as a daemon does: it stamps what a window
// gives with its own clock and merges it, and answers with the workspace
// after; pull merges another's, as its peers' states come. With hold, its
// answers wait for release, as on their way.
type fakeDaemon struct {
	fail  bool // answers every Workspace with an error, as a daemon gone
	st    *protocol.WorkspaceState
	clock *crdt.Clock
	phys  int64
	given []*protocol.Workspace // what windows gave it, as they gave it
	hold  bool
	held  []func()
}

func newFakeDaemon(node string, anchored bool) *fakeDaemon {
	return &fakeDaemon{st: &protocol.WorkspaceState{}, clock: crdt.NewClock(node, anchored), phys: time.Now().UnixMilli()}
}

func (d *fakeDaemon) give(ws *protocol.Workspace, done func(*protocol.Workspace, error)) {
	if d.fail {
		done(nil, errors.New("the daemon is gone"))
		return
	}
	d.given = append(d.given, proto.CloneOf(ws))
	d.phys++
	switch {
	case ws.GetState() != nil:
		delta := proto.CloneOf(ws.GetState())
		shared.Heal(d.st, d.clock, d.phys)
		shared.Stamp(delta, d.clock, d.phys)
		shared.Merge(d.st, delta, d.clock, d.phys)
	case len(ws.GetDoc()) > 0:
		if st, err := shared.FromLegacy(ws.GetDoc(), d.phys); err == nil {
			shared.Merge(d.st, st, d.clock, d.phys)
			if shared.Anchored(d.st, d.clock.NodeID()) {
				d.clock.Anchor()
			}
		}
	}
	answer := d.get()
	if d.hold {
		d.held = append(d.held, func() { done(answer, nil) })
		return
	}
	done(answer, nil)
}

// get is the workspace it holds, as a front-end asking is answered.
func (d *fakeDaemon) get() *protocol.Workspace {
	return &protocol.Workspace{State: proto.CloneOf(d.st)}
}

// pull merges o's workspace in, as a peer's state brings it.
func (d *fakeDaemon) pull(o *fakeDaemon) {
	d.phys++
	shared.Merge(d.st, proto.CloneOf(o.st), d.clock, d.phys)
	d.clock.Anchor()
}

// release sends the answers held.
func (d *fakeDaemon) release() {
	held := d.held
	d.hold, d.held = false, nil
	for _, f := range held {
		f()
	}
}

// edit has the daemon take doc whole as another window's edit, given
// through a window of its own.
func (d *fakeDaemon) edit(t *testing.T, doc workspaceDoc) {
	t.Helper()
	before, _ := json.Marshal(workspaceDoc{})
	if view := shared.View(d.st); len(d.st.GetSpaces())+len(d.st.GetLayouts())+len(d.st.GetProjects()) > 0 {
		before = view
	}
	after, _ := json.Marshal(doc)
	delta, err := shared.Diff(d.st, before, after)
	if err != nil {
		t.Fatal(err)
	}
	d.give(&protocol.Workspace{State: delta}, func(*protocol.Workspace, error) {})
}

// lastDelta is the last edit a window gave d.
func (d *fakeDaemon) lastDelta(t *testing.T) *protocol.WorkspaceState {
	t.Helper()
	if len(d.given) == 0 {
		t.Fatal("nothing given")
	}
	return d.given[len(d.given)-1].GetState()
}

// windowOn is a window whose daemon is d, set up, and prep run on the
// model before it is.
func windowOn(t *testing.T, d *fakeDaemon, prep ...func(*model)) (*model, *ggui.Probe) {
	t.Helper()
	if os.Getenv("MAKO_HOME") == "" || !filepath.IsAbs(os.Getenv("MAKO_HOME")) {
		t.Setenv("MAKO_HOME", t.TempDir())
	}
	m := newModel(config.Default(), "")
	m.post = func(func()) {} // no daemon but d: what the link would answer is dropped
	m.ws.give = d.give
	for _, f := range prep {
		f(m)
	}
	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Box() }, ggui.Sz(100, 100))
	p.Setup(func() { setupWorkspace(m) })
	t.Cleanup(func() { p.Close(); m.shutdown() })
	p.Frame()
	m.takeWorkspace(d.get())
	p.Frame()
	return m, p
}

// stateOf is a state holding doc, as another node made it.
func stateOf(t *testing.T, doc workspaceDoc) *protocol.WorkspaceState {
	t.Helper()
	body, _ := json.Marshal(workspaceFile{Version: time.Now().UnixNano(), NodeID: "b", Doc: doc})
	st, err := shared.FromLegacy(body, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// spaceNames are the names of the spaces a window shows, in order.
func spaceNames(m *model) []string {
	var out []string
	for _, sp := range ggui.Peek(m.Spaces).Spaces {
		out = append(out, sp.Name)
	}
	return out
}

// Two windows on two nodes edit different things while apart; once the
// nodes meet, both show both's edits.
func TestWindowsApartComeTogether(t *testing.T) {
	da, db := newFakeDaemon("a", true), newFakeDaemon("b", true)
	wa, pa := windowOn(t, da)
	wb, pb := windowOn(t, db)
	wa.Spaces.Update(func(l spaceList) spaceList {
		l.Spaces = append(l.Spaces, space{ID: "sa", Name: "A's"})
		l.Names = map[string]string{"free:x": "X"}
		return l
	})
	pa.Frame()
	wa.saveWorkspace()
	wb.Spaces.Update(func(l spaceList) spaceList {
		l.Projects = append(l.Projects, project{ID: "pb", Name: "B's", Roots: []projectRoot{{Locations: []fileRoot{{Node: "b", Path: "/src/pb"}}}}})
		l.Pinned = []string{"t1"}
		return l
	})
	pb.Frame()
	wb.saveWorkspace()

	da.pull(db)
	db.pull(da)
	wa.takeWorkspace(da.get())
	wb.takeWorkspace(db.get())
	la, lb := ggui.Peek(wa.Spaces), ggui.Peek(wb.Spaces)
	if !reflect.DeepEqual(la, lb) {
		t.Fatalf("the windows differ:\n%+v\n%+v", la, lb)
	}
	if !slices.Equal(spaceNames(wa), []string{"A's"}) || len(la.Projects) != 1 || la.Projects[0].Name != "B's" ||
		!slices.Equal(la.Pinned, []string{"t1"}) || la.Names["free:x"] != "X" {
		t.Fatalf("the windows show %+v", la)
	}
}

// A tab moved in one window while a space is added in the other: both
// stay, the panes in each.
func TestATabMovedWhileASpaceIsAdded(t *testing.T) {
	da, db := newFakeDaemon("a", true), newFakeDaemon("b", true)
	wa, pa := windowOn(t, da)
	for _, id := range []string{"s1", "s2", "s3"} {
		wa.showTab(id)
	}
	pa.Frame()
	wa.saveWorkspace()
	db.pull(da)
	wb, pb := windowOn(t, db)
	if tabs := ggui.Peek(wb.Tabs); !slices.Equal(tabs, []string{"s1", "s2", "s3"}) {
		t.Fatalf("b shows %v", tabs)
	}

	wa.moveTab("s3", ggui.Peek(wa.Layout).groups()[0].ID, 0)
	pa.Frame()
	wa.saveWorkspace()
	wb.newSpace("work")
	pb.Frame()
	wb.saveWorkspace()

	da.pull(db)
	db.pull(da)
	wa.takeWorkspace(da.get())
	wb.takeWorkspace(db.get())
	pa.Frame()
	pb.Frame()
	for _, m := range []*model{wa, wb} {
		if got := spaceNames(m); !slices.Equal(got, []string{"work"}) {
			t.Errorf("spaces %v", got)
		}
	}
	if tabs := ggui.Peek(wa.Tabs); !slices.Equal(tabs, []string{"s3", "s1", "s2"}) {
		t.Errorf("a shows %v", tabs)
	}
	if tabs := wb.ws.layouts[layoutKey(allSpaceID, "")].groups()[0].Tabs; !slices.Equal(tabs, []string{"s3", "s1", "s2"}) {
		t.Errorf("b keeps %v for every session", tabs)
	}
}

// An edit of the window's not yet given when the daemon's workspace
// changes is given first, and the answer, holding both, taken up: it is
// neither undone by the daemon's, nor given as an undoing of it.
func TestAnEditNotYetGivenSurvivesTheDaemons(t *testing.T) {
	da, db := newFakeDaemon("a", true), newFakeDaemon("b", true)
	wa, pa := windowOn(t, da)
	wa.newSpace("x")
	pa.Frame()
	wa.saveWorkspace()
	db.pull(da)
	wb, pb := windowOn(t, db)
	id := ggui.Peek(wb.Spaces).Spaces[0].ID
	wb.updateSpace(id, func(sp *space) { sp.Name = "B's" })
	pb.Frame()
	wb.saveWorkspace()
	da.pull(db)

	wa.newSpace("mine") // in the pause before it is given
	pa.Frame()
	wa.takeWorkspace(da.get())
	pa.Frame()
	if got := spaceNames(wa); !slices.Equal(got, []string{"B's", "mine"}) {
		t.Fatalf("a shows %v", got)
	}
	delta := da.lastDelta(t)
	if len(delta.GetSpaces()) != 1 || delta.GetSpaces()[id] != nil {
		t.Fatalf("a gave %v", delta)
	}

	// An edit on its way: a state from before its answer is left; the
	// answer is taken up.
	old := da.get()
	da.hold = true
	wa.newSpace("more")
	pa.Frame()
	wa.saveWorkspace()
	wa.takeWorkspace(old)
	pa.Frame()
	if got := spaceNames(wa); !slices.Equal(got, []string{"B's", "mine", "more"}) {
		t.Fatalf("a state from before the answer undid the edit: %v", got)
	}
	da.release()
	pa.Frame()
	if got := spaceNames(wa); !slices.Equal(got, []string{"B's", "mine", "more"}) {
		t.Fatalf("after the answer a shows %v", got)
	}
}

// A move rewrites the moved space's rank alone.
func TestAMoveGivesOneRank(t *testing.T) {
	d := newFakeDaemon("a", true)
	m, p := windowOn(t, d)
	for _, name := range []string{"a", "b", "c"} {
		m.newSpace(name)
	}
	p.Frame()
	m.saveWorkspace()
	moved := ggui.Peek(m.Spaces).Spaces[2].ID
	m.Spaces.Update(func(l spaceList) spaceList {
		l.Spaces = append([]space{l.Spaces[2]}, l.Spaces[:2]...)
		return l
	})
	p.Frame()
	m.saveWorkspace()
	want := &protocol.WorkspaceState{Spaces: map[string]*protocol.SpaceEntry{moved: {}}}
	got := proto.CloneOf(d.lastDelta(t))
	if got.GetSpaces()[moved].GetRank() == nil {
		t.Fatalf("gave %v", got)
	}
	got.Spaces[moved].Rank = nil
	if !proto.Equal(got, want) {
		t.Fatalf("gave %v besides the rank", got)
	}
	if names := spaceNames(m); !slices.Equal(names, []string{"c", "a", "b"}) {
		t.Fatalf("spaces %v", names)
	}
}

// A clone joining its project's root is one location added.
func TestAJoinedCloneGivesOneLocation(t *testing.T) {
	d := newFakeDaemon("a", true)
	m, p := windowOn(t, d)
	m.Spaces.Update(func(l spaceList) spaceList { l.Projects = []project{product()}; return l })
	p.Frame()
	m.saveWorkspace()
	m.joinClones([]*protocol.SessionInfo{{Id: "c", NodeId: "box", Project: "/srv/server/cmd", Repo: "/srv/server", Remote: "github.com/acme/server"}})
	p.Frame()
	m.saveWorkspace()
	delta := d.lastDelta(t)
	roots := delta.GetProjects()[product().ID].GetRoots()
	if len(delta.GetProjects()) != 1 || len(roots) != 1 {
		t.Fatalf("gave %v", delta)
	}
	for _, r := range roots {
		locs := r.GetLocations()
		e := locs[shared.LocationKey("box", "/srv/server")]
		if len(locs) != 1 || e.GetAddedAt() == nil || r.GetLabel() != nil || r.GetCreatedAt() != nil {
			t.Fatalf("gave %v", delta)
		}
	}
}

// A window first run after the workspace became a CRDT gives its daemon
// what it kept before, whole, once: nothing of it is lost.
func TestAnOldWorkspaceIsTakenIn(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	old := workspaceFile{
		Version: time.Now().Add(-time.Hour).UnixNano(),
		NodeID:  "old",
		Doc: workspaceDoc{
			spaceList: spaceList{
				Spaces:         []space{{ID: "w", Name: "Work", Color: "blue", Owners: []string{"github.com/acme"}, Projects: []string{product().ID}}, {ID: "h", Name: "Home"}},
				NoAll:          true,
				Default:        "h",
				Projects:       []project{product()},
				Names:          map[string]string{"free:x": "X"},
				Looks:          map[string]projectLook{product().ID: {Icon: "star", Color: "red"}},
				PinnedProjects: []scopeKey{{Section: product().ID}},
				Pinned:         []string{"s2", "s1"},
			},
			Layouts: map[string]*layoutNode{
				"w":                       {ID: 1, Group: &tabGroup{ID: 1, Tabs: []string{"s1"}, Active: "s1"}},
				layoutKey(allSpaceID, ""): {ID: 1, Group: &tabGroup{ID: 1, Tabs: []string{"s2", "s1"}, Active: "s1"}},
			},
			TabNodes: map[string]string{"s1": "mac", "s2": "mac"},
		},
		Contexts: map[string]string{"w": ""},
	}
	body, _ := json.Marshal(old)
	path, _ := configFile("workspace.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	d := newFakeDaemon("me", false)
	m, p := windowOn(t, d)
	if len(d.given) == 0 || len(d.given[0].GetDoc()) == 0 {
		t.Fatalf("the old workspace was not given: %v", d.given)
	}
	l := ggui.Peek(m.Spaces)
	want := old.Doc.spaceList
	// Roots now carry their IDs in the daemon's workspace.
	for i := range l.Projects {
		for j := range l.Projects[i].Roots {
			l.Projects[i].Roots[j].ID = ""
		}
	}
	if !reflect.DeepEqual(l, want) {
		t.Fatalf("the window shows\n%+v\nnot\n%+v", l, want)
	}
	// Without the space of every session, the first space shows.
	if tabs := ggui.Peek(m.Tabs); !slices.Equal(tabs, []string{"s1"}) {
		t.Fatalf("tabs %v", tabs)
	}
	if got := m.ws.layouts[layoutKey(allSpaceID, "")]; got == nil || !slices.Equal(got.groups()[0].Tabs, []string{"s2", "s1"}) {
		t.Fatalf("every session's panes %v", got)
	}
	// Once: the file no longer says it is from before, and the window's
	// next edit is a delta.
	m.writeWorkspace()
	m.ws.writer.wait()
	var f workspaceFile
	written, _ := os.ReadFile(path)
	if json.Unmarshal(written, &f) != nil || f.Version != 0 || f.Contexts["w"] != "" {
		t.Fatalf("written %s", written)
	}
	m.showTab("s3")
	p.Frame()
	m.saveWorkspace()
	if d.lastDelta(t) == nil {
		t.Fatal("the next edit was not a delta")
	}
}

// A daemon that holds nothing, as one whose workspace was lost, is given
// the window's whole, counted, so that the other nodes' wins over it.
func TestALostWorkspaceIsGivenBack(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	d := newFakeDaemon("me", true)
	m, p := windowOn(t, d)
	m.newSpace("kept")
	p.Frame()
	m.saveWorkspace()
	m.writeWorkspace()
	m.ws.writer.wait()

	lost := newFakeDaemon("me", true)
	again, _ := windowOn(t, lost)
	if got := spaceNames(again); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("the window shows %v", got)
	}
	if got := len(lost.st.GetSpaces()); got != 1 {
		t.Fatalf("the daemon holds %d spaces", got)
	}
}

// A node that does not share its workspace says so in its configuration,
// and its window still gives its daemon its edits and takes its daemon's.
func TestAWindowNotFollowingStillHasItsDaemon(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	d := newFakeDaemon("me", true)
	m, p := windowOn(t, d)
	m.setWorkspaceSync(false)
	cfg, err := config.Load()
	if err != nil || cfg.Workspace.Sync == nil || *cfg.Workspace.Sync || ggui.Peek(m.WorkspaceSync) {
		t.Fatalf("config %+v, %v", cfg.Workspace, err)
	}
	m.newSpace("mine")
	p.Frame()
	m.saveWorkspace()
	if len(d.st.GetSpaces()) != 1 {
		t.Fatal("the edit did not reach the daemon")
	}
	d.edit(t, workspaceDoc{spaceList: spaceList{Spaces: append(ggui.Peek(m.Spaces).Spaces, space{ID: "other", Name: "other"})}})
	m.takeWorkspace(d.get())
	if got := spaceNames(m); !slices.Equal(got, []string{"mine", "other"}) {
		t.Fatalf("the window shows %v", got)
	}
}

// A state the poll asked for before the answer to an edit, and brings
// after it, does not undo the edit.
func TestALateStateUndoesNothing(t *testing.T) {
	d := newFakeDaemon("a", true)
	m, p := windowOn(t, d)
	before := d.get()
	m.newSpace("mine")
	p.Frame()
	m.saveWorkspace()
	m.takeWorkspace(before)
	if got := spaceNames(m); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("a late state undid the edit: %v", got)
	}
}

// An edit the daemon did not take is written down with the document it is
// of, and given by the window started again.
func TestAnEditNotGivenOutlivesTheWindow(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	d := newFakeDaemon("a", true)
	m, p := windowOn(t, d)
	m.newSpace("given")
	p.Frame()
	m.saveWorkspace()
	d.fail = true
	m.newSpace("missed")
	p.Frame()
	m.saveWorkspace()
	m.ws.writer.wait()

	d.fail = false
	again, _ := windowOn(t, d)
	if got := spaceNames(again); !slices.Equal(got, []string{"given", "missed"}) {
		t.Fatalf("the window started again shows %v", got)
	}
	if got := len(d.st.GetSpaces()); got != 2 {
		t.Fatalf("the daemon holds %d spaces", got)
	}
}

// The last edit, made in the pause before it would be given, is written
// down as the window closes, and given at its next start.
func TestTheLastEditOutlivesTheWindow(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	d := newFakeDaemon("a", true)
	m, p := windowOn(t, d)
	m.newSpace("given")
	p.Frame()
	m.saveWorkspace()
	m.newSpace("late")
	p.Frame()
	m.shutdown()

	again, _ := windowOn(t, d)
	if got := spaceNames(again); !slices.Equal(got, []string{"given", "late"}) {
		t.Fatalf("the window started again shows %v", got)
	}
}

// A window pointed at another daemon brings its workspace to none: it
// takes up the new daemon's, an answer of the one it left is dropped,
// and a daemon that holds nothing is given the window's.
func TestAWindowLeavesItsDaemonsWorkspace(t *testing.T) {
	da, db := newFakeDaemon("a", true), newFakeDaemon("b", true)
	db.edit(t, workspaceDoc{spaceList: spaceList{Spaces: []space{{ID: "b", Name: "B's"}}}})
	m, p := windowOn(t, da)
	m.newSpace("A's")
	p.Frame()
	m.saveWorkspace()
	da.hold = true
	m.newSpace("on its way")
	p.Frame()
	m.saveWorkspace()

	m.setLink("")
	m.ws.give = db.give
	da.release() // the answer of the daemon left
	m.takeWorkspace(db.get())
	p.Frame()
	if got := spaceNames(m); !slices.Equal(got, []string{"B's"}) {
		t.Fatalf("on the new daemon the window shows %v", got)
	}
	m.newSpace("mine")
	p.Frame()
	m.saveWorkspace()
	if spaces := db.lastDelta(t).GetSpaces(); len(spaces) != 1 {
		t.Fatalf("the new daemon was given %v", spaces)
	}

	dc := newFakeDaemon("c", true)
	m.setLink("")
	m.ws.give = dc.give
	m.takeWorkspace(dc.get())
	if got := len(dc.st.GetSpaces()); got != 2 {
		t.Fatalf("a daemon holding nothing was given %d spaces", got)
	}
}
