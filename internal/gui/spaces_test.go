package gui

import (
	"slices"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A space shows the sessions and kept folders of its owners; the first
// shows every one, those of no owner too.
func TestSpacesDivideSessionsByOwner(t *testing.T) {
	p := prefs{Spaces: []space{{Name: "work", Owners: []string{"github.com/acme"}}}}
	sessions := []*protocol.SessionInfo{{Id: "1", Owner: "github.com/acme"}, {Id: "2", Owner: "github.com/me"}, {Id: "3"}}
	ids := func(ss []*protocol.SessionInfo) (out []string) {
		for _, s := range ss {
			out = append(out, s.GetId())
		}
		return out
	}
	if got := ids(p.spaceSessions(0, sessions)); !slices.Equal(got, []string{"1", "2", "3"}) {
		t.Fatalf("all sessions %v", got)
	}
	if got := ids(p.spaceSessions(1, sessions)); !slices.Equal(got, []string{"1"}) {
		t.Fatalf("work %v", got)
	}
	kept := []keptProject{{fileRoot{"n", "/a"}, "github.com/acme"}, {fileRoot{"n", "/b"}, ""}}
	if got := p.spaceProjects(1, kept); len(got) != 1 || got[0].Path != "/a" {
		t.Fatalf("work folders %v", got)
	}
	if len(sessions) != 3 || len(kept) != 2 {
		t.Fatal("filtering changed its input")
	}
}

// Spaces are moved between as far as there are; deleting the one shown
// shows one still there.
func TestMovingBetweenSpaces(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.moveSpace(-1)
	if got := ggui.Peek(m.Prefs).Space; got != 0 {
		t.Fatalf("before the first: %d", got)
	}
	m.newSpace("work", "github.com/acme")
	m.newSpace("home")
	if got := ggui.Peek(m.Prefs).Space; got != 2 {
		t.Fatalf("a new space is not shown: %d", got)
	}
	m.moveSpace(1)
	if got := ggui.Peek(m.Prefs).Space; got != 2 {
		t.Fatalf("past the last: %d", got)
	}
	m.toggleOwner(1, "github.com/me")
	if got := ggui.Peek(m.Prefs).Spaces[1].Owners; !slices.Equal(got, []string{"github.com/me"}) {
		t.Fatalf("owners %v", got)
	}
	m.updateSpaces(func(s []space) []space { return s[:1] })
	if got := ggui.Peek(m.Prefs).Space; got != 1 {
		t.Fatalf("after deleting the space shown: %d", got)
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
			s.scroll(d, pick(i == 0, ggui.ScrollPhaseBegan, ggui.ScrollPhaseChanged))
		}
	}
	gesture(ggui.Pt(-1, 0.1), ggui.Pt(-6, 0), ggui.Pt(-6, 0))
	if s.drag != -13*swipePixels || len(moves) != 0 {
		t.Fatalf("drag %v moves %v: the list does not follow, or moved before the lift", s.drag, moves)
	}
	if !s.scroll(ggui.Point{}, ggui.ScrollPhaseEnded) || !slices.Equal(moves, []int{1}) {
		t.Fatalf("moves %v after the lift", moves)
	}
	if s.offset(time.Now()) <= 0 {
		t.Fatal("the next space does not come in from the right")
	}
	if s.scroll(ggui.Pt(-3, 0), ggui.ScrollPhaseMomentum) {
		t.Fatal("momentum was taken")
	}

	// A short swipe goes back.
	gesture(ggui.Pt(1, 0), ggui.Pt(1, 0))
	s.scroll(ggui.Point{}, ggui.ScrollPhaseEnded)
	if len(moves) != 1 {
		t.Fatalf("a short swipe moved: %v", moves)
	}
	// Past the last space the list gives way, and goes back.
	at = 2
	gesture(ggui.Pt(-2, 0), ggui.Pt(-2, 0), ggui.Pt(-2, 0))
	if s.drag != -6*swipePixels*swipeEdge {
		t.Fatalf("past the edge drag %v", s.drag)
	}
	s.scroll(ggui.Point{}, ggui.ScrollPhaseEnded)
	if len(moves) != 1 {
		t.Fatalf("moved past the last: %v", moves)
	}
	// Begun down the list, it scrolls the list to its end.
	at = 0
	gesture(ggui.Pt(0.2, 2), ggui.Pt(-5, 0))
	if s.active || s.scroll(ggui.Point{}, ggui.ScrollPhaseEnded) {
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
