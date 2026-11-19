package gui

import (
	"slices"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A section is in one space: the first it was moved to, else the first
// holding one of its owners, else the default one; the first space shows
// every one.
func TestAProjectIsInOneSpace(t *testing.T) {
	p := spaceSet{Default: "home", Spaces: []space{
		{ID: "work", Owners: []string{"github.com/acme"}, Projects: []string{"notes"}, Hidden: []string{"repo:github.com/acme/old"}},
		{ID: "home", Owners: []string{"github.com/acme"}, Projects: []string{"notes", "repo:github.com/acme/moved"}},
	}}
	groups := []sessionGroup{
		{Key: "repo:github.com/acme/api", Owner: "github.com/acme"},
		{Key: "repo:github.com/acme/old", Owner: "github.com/acme"},
		{Key: "repo:github.com/acme/moved", Owner: "github.com/acme"},
		{Key: "repo:github.com/me/toy", Owner: "github.com/me"},
		{Key: "notes"},
		// A project of two owners goes with either.
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
	// Left out of work by an older window, old goes on to the next space
	// of its owner.
	if got, want := keys(p.spaceGroups(2, groups)), []string{"repo:github.com/acme/old", "repo:github.com/acme/moved", "repo:github.com/me/toy"}; !slices.Equal(got, want) {
		t.Fatalf("home %v, want %v", got, want)
	}
	for key, want := range map[string]sectionReason{"notes": byProject, "repo:github.com/acme/api": shownByOwner, "repo:github.com/me/toy": byDefault} {
		if _, got := p.placeOf(key, groupOwners(groups[slices.IndexFunc(groups, func(g sessionGroup) bool { return g.Key == key })])); got != want {
			t.Errorf("%s: %v, want %v", key, got, want)
		}
	}
	// With no default chosen, or the one chosen gone, it is the first.
	p.Default = "gone"
	if at, _ := p.placeOf("repo:github.com/me/toy", []string{"github.com/me"}); at != 1 {
		t.Fatalf("with the default gone: %d", at)
	}
	if len(groups) != 6 {
		t.Fatal("filtering changed its input")
	}
}

// A project moved to a space leaves every other, whatever its owners say,
// and its notice's Undo puts it back.
func TestMovingAProjectToAnotherSpace(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.newSpace("work", "github.com/acme")
	m.newSpace("home")
	api := sessionGroup{Key: "repo:github.com/acme/api", Owner: "github.com/acme"}
	m.updateSpaces(func(s []space) []space { s[0].Hidden = []string{"x"}; s[1].Projects = []string{"y"}; return s })
	m.moveGroupsTo(1, "api", api)
	set := m.spacesNow()
	if !set.showsGroup(2, api) || set.showsGroup(1, api) {
		t.Fatalf("api not moved: %+v", set.Spaces)
	}
	m.moveToSpace(set.Spaces[0].ID, "y", "x")
	sp := m.spacesNow().Spaces
	if !slices.Equal(sp[0].Projects, []string{"y", "x"}) || len(sp[0].Hidden) != 0 || !slices.Equal(sp[1].Projects, []string{api.Key}) {
		t.Fatalf("after moving back: %+v", sp)
	}
	m.moveGroupsTo(0, "api", api)
	m.moveGroupsTo(1, "api", api)
	if !m.spacesNow().showsGroup(2, api) {
		t.Fatal("moved twice")
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
	m.toggleOwner(ggui.Peek(m.Spaces).Spaces[1].ID, "github.com/me")
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

// The settings list the projects moved to a space that no section is
// now, by their IDs.
func TestSpaceSettingsListProjectsGone(t *testing.T) {
	l := spaceList{Spaces: []space{{Projects: []string{"notes", "repo:github.com/x/gone"}, Hidden: []string{"repo:github.com/acme/old"}}}}
	got := sectionsOf([]sessionGroup{{Key: "notes", Project: "/notes"}}, l)
	if len(got) != 2 || !slices.ContainsFunc(got, func(s spaceSection) bool { return s.Key == "repo:github.com/x/gone" && s.Name == "gone" }) {
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
	order := spaceOrder(m)
	order.Set(widget.MoveItem(ggui.Peek(order), 1, 0))
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

// In the settings a project's chip dragged onto another space's card
// moves it there, and a space dragged by its handle below the next one
// goes after it.
func TestSpaceSettingsDragProjectsAndSpaces(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "1", NodeId: "a", Project: "/w/api", Owner: "github.com/acme", Remote: "github.com/acme/api"},
		{Id: "2", NodeId: "a", Project: "/w/toy"},
	})
	m.newSpace("work", "github.com/acme")
	m.newSpace("home")
	// toy, of no owner, is in home, the default.
	m.Spaces.Update(func(l spaceList) spaceList { l.Default = l.Spaces[1].ID; return l })
	p := ggui.NewProbe(spacesSettings(m), ggui.Size{W: 900, H: 1400})
	defer p.Close()
	p.Frame()
	drag := func(from, to ggui.Point) {
		p.Press(from)
		for i := 1; i <= 8; i++ {
			p.Move(ggui.Pt(from.X+(to.X-from.X)*float64(i)/8, from.Y+(to.Y-from.Y)*float64(i)/8))
			p.Frame()
		}
		p.Release(to)
		p.Frame()
		p.Frame()
	}
	api, ok := p.Find("api")
	toy, ok2 := p.Find("toy")
	if !ok || !ok2 {
		t.Fatal("no chips")
	}
	drag(api.Center(), toy.Center())
	work, home := m.spacesNow().Spaces[0], m.spacesNow().Spaces[1]
	if !slices.Equal(home.Projects, []string{"repo:github.com/acme/api"}) || len(work.Projects) != 0 {
		t.Fatalf("after dragging api to home: work %v, home %v", work.Projects, home.Projects)
	}
	handles := p.FindAll(ggui.RoleButton)
	var grips []ggui.Found
	for _, f := range handles {
		if f.Label == T("Move space") {
			grips = append(grips, f)
		}
	}
	if len(grips) != 2 {
		t.Fatalf("%d handles", len(grips))
	}
	drag(grips[0].Center(), grips[1].Center().Add(ggui.Pt(0, 200)))
	if got := m.spacesNow().Spaces; got[0].Name != "home" || got[1].Name != "work" {
		t.Fatalf("after dragging work below home: %s, %s", got[0].Name, got[1].Name)
	}
}
