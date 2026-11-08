package gui

import (
	"slices"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A space shows the project sections of its owners, whole, and those it
// holds by ID, and leaves out those it hides; the first shows every one,
// those of no owner too.
func TestSpacesShowProjectsByOwnerAndByID(t *testing.T) {
	p := spaceSet{spaceList: spaceList{Spaces: []space{{Name: "work", Owners: []string{"github.com/acme"}, Projects: []string{"notes"}, Hidden: []string{"repo:github.com/acme/old"}}}}}
	groups := []sessionGroup{
		{Key: "repo:github.com/acme/api", Owner: "github.com/acme"},
		{Key: "repo:github.com/acme/old", Owner: "github.com/acme"},
		{Key: "repo:github.com/me/toy", Owner: "github.com/me"},
		{Key: "notes"},
		// A project of two owners shows where either is held.
		{Key: "product", Def: &project{Roots: []projectRoot{{Owner: "github.com/me"}, {Owner: "github.com/acme"}}}},
	}
	keys := func(gs []sessionGroup) (out []string) {
		for _, g := range gs {
			out = append(out, g.Key)
		}
		return out
	}
	if got := keys(p.spaceGroups(0, groups)); len(got) != len(groups) {
		t.Fatalf("all sessions %v", got)
	}
	if got, want := keys(p.spaceGroups(1, groups)), []string{"repo:github.com/acme/api", "notes", "product"}; !slices.Equal(got, want) {
		t.Fatalf("work %v, want %v", got, want)
	}
	if len(groups) != 5 {
		t.Fatal("filtering changed its input")
	}
}

// A project put in a space from its menu stays, and one left out of it
// stays out, whatever its owners say.
func TestTogglingAProjectInASpace(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.newSpace("work", "github.com/acme")
	api := sessionGroup{Key: "repo:github.com/acme/api", Owner: "github.com/acme"}
	notes := sessionGroup{Key: "notes"}
	m.toggleProject(0, api)
	m.toggleProject(0, notes)
	sp := ggui.Peek(m.Spaces).Spaces[0]
	if !slices.Equal(sp.Hidden, []string{api.Key}) || !slices.Equal(sp.Projects, []string{notes.Key}) {
		t.Fatalf("held %v, hidden %v", sp.Projects, sp.Hidden)
	}
	m.toggleProject(0, api)
	m.toggleProject(0, notes)
	if sp := ggui.Peek(m.Spaces).Spaces[0]; len(sp.Projects)+len(sp.Hidden) != 0 {
		t.Fatalf("after toggling back: held %v, hidden %v", sp.Projects, sp.Hidden)
	}
}

// Spaces are moved between as far as there are; deleting the one shown
// shows one still there.
func TestMovingBetweenSpaces(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.moveSpace(-1)
	if got := m.spacesNow().Space; got != 0 {
		t.Fatalf("before the first: %d", got)
	}
	m.newSpace("work", "github.com/acme")
	m.newSpace("home")
	if got := m.spacesNow().Space; got != 2 {
		t.Fatalf("a new space is not shown: %d", got)
	}
	m.moveSpace(1)
	if got := m.spacesNow().Space; got != 2 {
		t.Fatalf("past the last: %d", got)
	}
	m.toggleOwner(1, "github.com/me")
	if got := ggui.Peek(m.Spaces).Spaces[1].Owners; !slices.Equal(got, []string{"github.com/me"}) {
		t.Fatalf("owners %v", got)
	}
	m.updateSpaces(func(s []space) []space { return s[:1] })
	if got := m.spacesNow().Space; got != 0 {
		t.Fatalf("after deleting the space shown: %d, not every session's", got)
	}
	// Kept by its ID, the space shown stays shown as others come before it.
	m.setSpace(1)
	m.updateSpaces(func(s []space) []space { return append([]space{{ID: "first"}}, s...) })
	if got := m.spacesNow().Space; got != 2 {
		t.Fatalf("after a space came before the one shown: %d", got)
	}
}

// With every session's space left out, the first space is shown in its
// place and nothing moves before it; with no space, it stays.
func TestTheSpaceOfEverySessionLeftOut(t *testing.T) {
	list := spaceList{Spaces: []space{{ID: "w"}, {ID: "h"}}, NoAll: true}
	p := spaceSetOf(list, allSpaceID)
	if p.Space != 1 || p.hasSpace(0) || !p.hasSpace(2) {
		t.Fatalf("shown %d", p.Space)
	}
	if p := spaceSetOf(spaceList{NoAll: true}, allSpaceID); p.Space != 0 || !p.hasSpace(0) {
		t.Fatalf("with no spaces shown %d", p.Space)
	}
}

// Each space's way of keeping tabs, from before one way held for all,
// becomes the one: every session's space's first.
func TestOneWayOfKeepingTabs(t *testing.T) {
	got := spaceList{Spaces: []space{{ID: "w", OldLayoutBy: layoutByProject}}}.upgraded()
	if got.LayoutBy != layoutByProject || got.Spaces[0].OldLayoutBy != "" {
		t.Fatalf("%+v", got)
	}
	got = spaceList{Spaces: []space{{ID: "w", OldLayoutBy: layoutByProject}}, OldAllLayoutBy: layoutByOwner}.upgraded()
	if got.LayoutBy != layoutByOwner || got.OldAllLayoutBy != "" {
		t.Fatalf("%+v", got)
	}
}

// The list follows the fingers across, and moves to the space that way
// only when they lift far enough along; short of it, or with no space
// there, it goes back. A gesture begun down the list scrolls it.
func TestSwipeMovesWhenTheFingersLift(t *testing.T) {
	var moves []int
	at := 0
	s := &spaceSwipe{
		can:  func(by int) bool { return at+by >= 0 && at+by <= 2 },
		move: func(by int) { at += by; moves = append(moves, by) },
		post: func(fn func()) {}, moving: ggui.State(false),
	}
	s.width = 200
	gesture := func(steps ...ggui.Point) {
		s.settle, s.drag = ggui.Motion{}, 0
		for i, d := range steps {
			s.scroll(d, pick(i == 0, ggui.ScrollPhaseBegan, ggui.ScrollPhaseChanged), false)
		}
	}
	gesture(ggui.Pt(-1, 0.1), ggui.Pt(-6, 0), ggui.Pt(-6, 0))
	if s.drag != -13*swipePixels || len(moves) != 0 {
		t.Fatalf("drag %v moves %v: the list does not follow, or moved before the lift", s.drag, moves)
	}
	if !s.scroll(ggui.Point{}, ggui.ScrollPhaseEnded, false) || !slices.Equal(moves, []int{1}) {
		t.Fatalf("moves %v after the lift", moves)
	}
	if s.offset(time.Now()) <= 0 {
		t.Fatal("the next space does not come in from the right")
	}
	if s.scroll(ggui.Pt(-3, 0), ggui.ScrollPhaseMomentum, false) {
		t.Fatal("momentum was taken")
	}

	// A trackpad's scroll, in pixels, moves it pixel for pixel.
	s.settle, s.drag = ggui.Motion{}, 0
	s.scroll(ggui.Pt(-12, 0), ggui.ScrollPhaseBegan, true)
	s.scroll(ggui.Pt(-8, 0), ggui.ScrollPhaseChanged, true)
	if s.drag != -20 {
		t.Fatalf("drag %v for 20 pixels", s.drag)
	}
	s.settle, s.drag, s.active = ggui.Motion{}, 0, false

	// A short swipe goes back.
	gesture(ggui.Pt(1, 0), ggui.Pt(1, 0))
	s.scroll(ggui.Point{}, ggui.ScrollPhaseEnded, false)
	if len(moves) != 1 {
		t.Fatalf("a short swipe moved: %v", moves)
	}
	// Past the last space the list gives way, and goes back.
	at = 2
	gesture(ggui.Pt(-2, 0), ggui.Pt(-2, 0), ggui.Pt(-2, 0))
	if s.drag != -6*swipePixels*swipeEdge {
		t.Fatalf("past the edge drag %v", s.drag)
	}
	s.scroll(ggui.Point{}, ggui.ScrollPhaseEnded, false)
	if len(moves) != 1 {
		t.Fatalf("moved past the last: %v", moves)
	}
	// Begun down the list, it scrolls the list to its end.
	at = 0
	gesture(ggui.Pt(0.2, 2), ggui.Pt(-5, 0))
	if s.active || s.scroll(ggui.Point{}, ggui.ScrollPhaseEnded, false) {
		t.Fatal("a scroll down turned into a swipe")
	}
}

func TestSidebarAndSettingsWithSpaces(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "1", NodeId: "a", Project: "/w/x", Owner: "github.com/acme"}})
	m.newSpace("work", "github.com/acme")
	for _, w := range []ggui.Widget{sidebar(m), spacesSettings(m)} {
		p := ggui.NewProbe(w, ggui.Size{W: 700, H: 700})
		p.Frame()
		p.Close()
	}
}

// The settings say why a space shows each project: by an owner it holds,
// held itself, or left out; they list projects a space holds that no
// section is now, by their IDs.
func TestSpaceSettingsSayWhyAProjectShows(t *testing.T) {
	l := spaceList{Spaces: []space{{Owners: []string{"github.com/acme"}, Projects: []string{"notes", "repo:github.com/x/gone"}, Hidden: []string{"repo:github.com/acme/old"}}}}
	set := spaceSet{spaceList: l}
	for key, want := range map[string]sectionReason{
		"repo:github.com/acme/api": shownByOwner, "notes": byProject, "repo:github.com/acme/old": leftOut, "repo:github.com/me/toy": notShown,
	} {
		owners := []string{"github.com/acme"}
		if key == "notes" || key == "repo:github.com/me/toy" {
			owners = nil
		}
		if got := set.sectionIn(1, key, owners); got != want {
			t.Errorf("%s: %v, want %v", key, got, want)
		}
	}
	got := sectionsOf([]sessionGroup{{Key: "notes", Project: "/notes"}}, l)
	if len(got) != 3 || !slices.ContainsFunc(got, func(s spaceSection) bool { return s.Key == "repo:github.com/x/gone" && s.Name == "gone" }) {
		t.Fatalf("sections %+v", got)
	}
}

// A deleted space comes back with its notice's Undo; spaces move up and
// down among themselves, as far as they go.
func TestDeletingAndReorderingSpaces(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.newSpace("a")
	m.newSpace("b")
	names := func() (out []string) {
		for _, sp := range ggui.Peek(m.Spaces).Spaces {
			out = append(out, sp.Name)
		}
		return out
	}
	m.reorderSpace(1, -1)
	m.reorderSpace(0, -1)
	if got := names(); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("reordered %v", got)
	}
	undo := m.deleteSpace(0)
	if got := names(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("deleted %v", got)
	}
	undo()
	if got := names(); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("undone %v", got)
	}
}
