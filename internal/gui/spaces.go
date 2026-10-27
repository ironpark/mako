package gui

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/protocol"
)

// Spaces: the sidebar's sessions divided by whose repositories they are
// in. A space is a set of owners, host/name as the daemon gives them; the
// sidebar shows the sessions and kept folders of the space chosen, or of
// every owner and none in the first, which is always there. A swipe of two
// fingers across the sidebar moves to the space beside it.

// space is a named set of repository owners.
type space struct {
	Name   string   `json:"name"`
	Owners []string `json:"owners,omitempty"`
}

// allSpace is the index of the space of every session.
const allSpace = 0

// spaceOf is the space at index i, 0 for every session; ok is false for
// that one, which holds no owners of its own.
func (p prefs) spaceOf(i int) (sp space, ok bool) {
	if i <= allSpace || i > len(p.Spaces) {
		return space{}, false
	}
	return p.Spaces[i-1], true
}

// spaceName is the name the sidebar shows for space i.
func (p prefs) spaceName(i int) string {
	sp, ok := p.spaceOf(i)
	if !ok {
		return T("All sessions")
	}
	return cmp.Or(sp.Name, Tf("Space %d", i))
}

// inSpace reports whether a session or folder of owner shows in space i.
func (p prefs) inSpace(i int, owner string) bool {
	sp, ok := p.spaceOf(i)
	return !ok || slices.Contains(sp.Owners, owner)
}

// spaceSessions keeps the sessions space i shows.
func (p prefs) spaceSessions(i int, sessions []*protocol.SessionInfo) []*protocol.SessionInfo {
	if _, ok := p.spaceOf(i); !ok {
		return sessions
	}
	return slices.DeleteFunc(slices.Clone(sessions), func(s *protocol.SessionInfo) bool { return !p.inSpace(i, s.GetOwner()) })
}

// spaceProjects keeps the kept folders space i shows.
func (p prefs) spaceProjects(i int, projects []keptProject) []keptProject {
	if _, ok := p.spaceOf(i); !ok {
		return projects
	}
	return slices.DeleteFunc(slices.Clone(projects), func(r keptProject) bool { return !p.inSpace(i, r.Owner) })
}

// setSpace shows space i in the sidebar.
func (m *model) setSpace(i int) {
	m.Prefs.Update(func(p prefs) prefs { p.Space = min(max(i, allSpace), len(p.Spaces)); return p })
}

// moveSpace shows the space by beside the one shown, as far as there is
// one: 1 the next, -1 the one before.
func (m *model) moveSpace(by int) { m.setSpace(ggui.Peek(m.Prefs).Space + by) }

// updateSpaces changes the spaces, on a copy so the setting they were
// stays as it was, and keeps the one shown in range.
func (m *model) updateSpaces(fn func([]space) []space) {
	m.Prefs.Update(func(p prefs) prefs {
		spaces := make([]space, len(p.Spaces))
		for i, sp := range p.Spaces {
			spaces[i] = space{Name: sp.Name, Owners: slices.Clone(sp.Owners)}
		}
		p.Spaces = fn(spaces)
		p.Space = min(p.Space, len(p.Spaces))
		return p
	})
}

// toggleOwner adds owner to space i, an index into the spaces after the
// first, or takes it out.
func (m *model) toggleOwner(i int, owner string) {
	m.updateSpaces(func(spaces []space) []space {
		spaces[i].Owners = toggled(spaces[i].Owners, owner)
		return spaces
	})
}

// newSpace adds a space holding owners and shows it.
func (m *model) newSpace(name string, owners ...string) {
	m.updateSpaces(func(spaces []space) []space { return append(spaces, space{Name: name, Owners: owners}) })
	m.setSpace(len(ggui.Peek(m.Prefs).Spaces))
}

// knownOwners are the owners the sidebar's sessions and kept folders say
// their repositories have, by name and then host.
func knownOwners(sessions []*protocol.SessionInfo, projects []keptProject) []string {
	var owners []string
	for _, s := range sessions {
		owners = append(owners, s.GetOwner())
	}
	for _, r := range projects {
		owners = append(owners, r.Owner)
	}
	owners = slices.DeleteFunc(owners, func(o string) bool { return o == "" })
	slices.SortFunc(owners, compareOwners)
	return slices.Compact(owners)
}

// compareOwners orders owners by name, and one name's services by host.
func compareOwners(a, b string) int {
	na, ha := ownerName(a)
	nb, hb := ownerName(b)
	return cmp.Or(cmp.Compare(strings.ToLower(na), strings.ToLower(nb)), cmp.Compare(ha, hb))
}

// spaceSwitcher is the sidebar heading's title: the space shown, a menu
// of the others, and a dot for each space when there is more than one.
func spaceSwitcher(m *model) ggui.Widget {
	return ggui.View(ggui.Map(m.Prefs, func(p prefs) [2]any { return [2]any{p.Space, slices.Clone(p.Spaces)} }).WithEqual(func(a, b [2]any) bool {
		return a[0] == b[0] && slices.EqualFunc(a[1].([]space), b[1].([]space), func(x, y space) bool { return x.Name == y.Name && slices.Equal(x.Owners, y.Owners) })
	}), func(v [2]any) ggui.Widget {
		p := ggui.Peek(m.Prefs)
		current := v[0].(int)
		items := make([]ggui.Widget, 0, len(p.Spaces)+3)
		for i := range len(p.Spaces) + 1 {
			items = append(items, ui.MenuItem(p.spaceName(i), func() { m.setSpace(i) }).Checked(i == current))
		}
		items = append(items, ui.MenuDivider(), ui.MenuItem(T("Edit spaces…"), func() { m.openSettings("spaces") }))
		title := ggui.Row(
			ggui.Text(p.spaceName(current)).Size(14).Weight(ggui.WeightMedium).NoWrap().Ellipsis(),
			icon("chevron-down").Size(13).Color(tokMuted),
		).Gap(4).Align(ggui.AlignCenter)
		w := []ggui.Widget{iconMenu(ui.MenuOf(title, items...).Name(T("Spaces")), 2)}
		if len(p.Spaces) > 0 {
			w = append(w, spaceDots(m, len(p.Spaces)+1, current))
		}
		return ggui.Row(w...).Gap(6).Align(ggui.AlignCenter)
	})
}

// spaceDots marks the space shown among n, each dot showing its own.
func spaceDots(m *model, n, current int) ggui.Widget {
	dots := make([]ggui.Widget, n)
	for i := range n {
		c := tokSurface3
		if i == current {
			c = tokMuted
		}
		dots[i] = ui.ButtonOf(ggui.Box().Size(6, 6).Radius(3).Fill(c), func() { m.setSpace(i) }).
			Ghost().Pad(2).Radius(4).Name(ggui.Peek(m.Prefs).spaceName(i))
	}
	return ggui.Row(dots...).Gap(0).Align(ggui.AlignCenter)
}

// The trackpad swipe that moves between spaces. The list follows the
// fingers, swipePixels to a wheel unit; when they lift past swipeCommit of
// its width it moves to the space beside, and short of that springs back.
// Past the first or last space the list gives only swipeEdge of the way. A
// scroll that says nothing of the fingers, a wheel's, ends at a pause of
// swipeGap.
const (
	swipePixels = 10
	swipeCommit = 0.15
	swipeEdge   = 0.3
	swipeGap    = 200 * time.Millisecond
	swipeSettle = 220 * time.Millisecond
)

// spaceSwipe is a two-finger swipe across the sidebar, moving its list
// between spaces. Its area takes the swipe over the whole sidebar and
// leaves it every other input; its slide is the list that moves.
type spaceSwipe struct {
	can  func(by int) bool // whether there is a space that way
	move func(by int)      // shows the space that way
	post func(func())      // runs work on the UI goroutine

	drag     float64   // where the fingers have the list, while active
	active   bool      // a gesture is moving the list
	vertical bool      // the gesture began down the list, and scrolls it
	last     time.Time // the latest scroll
	width    float64   // the list's, as last painted
	commit   int       // a move made, for the list to come in at the next paint
	settle   ggui.Motion
}

func newSpaceSwipe(m *model) *spaceSwipe {
	return &spaceSwipe{
		can: func(by int) bool {
			p := ggui.Peek(m.Prefs)
			i := p.Space + by
			return i >= allSpace && i <= len(p.Spaces)
		},
		move: m.moveSpace,
		post: m.post,
	}
}

// area lays the swipe's input over child.
func (s *spaceSwipe) area(child ggui.Widget) ggui.Widget { return &swipeArea{s, child} }

// slide is child moved as the swipe has it.
func (s *spaceSwipe) slide(child ggui.Widget) ggui.Widget { return &swipeSlide{s, child} }

// scroll takes a scroll of the fingers across, until they lift, and
// leaves the rest.
func (s *spaceSwipe) scroll(d ggui.Point, phase ggui.ScrollPhase) bool {
	now := time.Now()
	gap := now.Sub(s.last) > swipeGap
	s.last = now
	switch phase {
	case ggui.ScrollPhaseEnded:
		if s.active {
			s.release()
			return true
		}
		return false
	case ggui.ScrollPhaseMomentum, ggui.ScrollPhaseMomentumEnded:
		// What the fingers threw goes on down the list, not across it.
		return false
	case ggui.ScrollPhaseBegan:
		s.vertical = false
	case ggui.ScrollPhaseNone:
		if gap {
			s.vertical = false
		}
	}
	if !s.active && (s.vertical || math.Abs(d.X) <= math.Abs(d.Y)) {
		s.vertical = s.vertical || d != (ggui.Point{})
		return false // scrolling the list
	}
	if !s.active {
		s.active = true
		s.drag = s.settle.Value(ggui.Now())
		if phase == ggui.ScrollPhaseNone {
			s.watchEnd()
		}
	}
	step := d.X * swipePixels
	// Fingers to the left, d.X below 0, bring the next space in.
	if !s.can(pick(s.drag+step < 0, 1, -1)) {
		step *= swipeEdge
	}
	s.drag += step
	return true
}

// watchEnd releases a gesture whose scrolls say nothing of the fingers
// once they pause, which brings no event of its own.
func (s *spaceSwipe) watchEnd() {
	time.AfterFunc(swipeGap, func() {
		s.post(func() {
			switch {
			case !s.active:
			case time.Since(s.last) >= swipeGap:
				s.release()
			default:
				s.watchEnd()
			}
		})
	})
}

// release ends the gesture: on to the space it was dragged toward if it
// went far enough, or back. The list settles at the next paint.
func (s *spaceSwipe) release() {
	s.active = false
	by := pick(s.drag < 0, 1, -1)
	if s.width > 0 && math.Abs(s.drag) >= s.width*swipeCommit && s.can(by) {
		s.commit = by
		s.move(by)
	}
}

// offset is where the list is drawn at now, across.
func (s *spaceSwipe) offset(now time.Time) float64 {
	switch {
	case s.active:
		return s.drag
	case s.commit != 0:
		// The next space's list comes in from the side the fingers left,
		// from as far as the old one had gone.
		s.settle.MoveTo(s.drag+float64(s.commit)*s.width, now, 0)
		s.settle.MoveTo(0, now, swipeSettle)
		s.commit = 0
	case s.drag != 0:
		s.settle.MoveTo(s.drag, now, 0)
		s.settle.MoveTo(0, now, swipeSettle)
	}
	s.drag = 0
	return s.settle.Value(now)
}

// swipeArea is a spaceSwipe's input over its child.
type swipeArea struct {
	s     *spaceSwipe
	child ggui.Widget
}

func (a *swipeArea) Layout(c ggui.Constraints, env ggui.Env) ggui.Size { return a.child.Layout(c, env) }

// Paint paints the child, then the region over it, which sees a scroll
// first and passes on all it does not take.
func (a *swipeArea) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(a.child, r)
	dst.HitPointer(r, a)
}

func (a *swipeArea) HandlePointer(ev ggui.PointerEvent) bool {
	return ev.Kind == ggui.PointerScroll && a.s.scroll(ev.Scroll, ev.ScrollPhase)
}

// swipeSlide is the list a spaceSwipe moves, fading as it goes.
type swipeSlide struct {
	s     *spaceSwipe
	child ggui.Widget
}

func (l *swipeSlide) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	return l.child.Layout(c, env)
}

func (l *swipeSlide) Paint(dst *ggui.Canvas, r ggui.Rect) {
	l.s.width = r.Size.W
	x := l.s.offset(ggui.Now())
	if x == 0 || r.Size.W <= 0 {
		dst.Paint(l.child, r)
		return
	}
	fade := min(math.Abs(x)/r.Size.W, 1) * 0.6
	dst.Clip(r).Layer(ggui.LayerOptions{Fade: fade}, func(layer *ggui.Canvas) {
		layer.Transform(ggui.Affine{}.Translate(x, 0), func(c *ggui.Canvas) { c.Paint(l.child, r) })
	})
}

// ownerSpaceItems are an owner heading's menu entries for the spaces: one
// to put it in each space or take it out, and one for a new space of it.
func ownerSpaceItems(m *model, owner, name string) []ggui.Widget {
	spaces := ggui.Peek(m.Prefs).Spaces
	items := []ggui.Widget{ui.MenuDivider()}
	for i, sp := range spaces {
		label := cmp.Or(sp.Name, Tf("Space %d", i+1))
		items = append(items, ui.MenuItem(Tf("In %s", label), func() { m.toggleOwner(i, owner) }).Checked(slices.Contains(sp.Owners, owner)))
	}
	return append(items, ui.MenuItem(T("New space with this owner"), func() { m.newSpace(name, owner) }))
}

// spacesSettings edits the spaces: each one's name and owners, picked
// from the owners the sidebar knows.
func spacesSettings(m *model) ggui.Widget {
	spaces := ggui.Map(m.Prefs, func(p prefs) int { return len(p.Spaces) })
	owners := ggui.Map(m.Sessions, func(s []*protocol.SessionInfo) []string {
		return knownOwners(s, ggui.Peek(m.Prefs).Projects)
	}).WithEqual(slices.Equal)
	add := ui.Button(T("Add space"), func() { m.newSpace("") }).Outline()
	return page(T("Spaces"), T("Spaces divide the sidebar's sessions by whose repositories they are in. Swipe across the sidebar with two fingers, or pick one from its title, to move between them."),
		ggui.View(spaces, func(n int) ggui.Widget {
			if n == 0 {
				return section(T("Spaces"), muted(ggui.Text(T("No spaces yet. Add one, or pick New space with this owner from an owner's menu in the sidebar.")).Size(13)), ggui.Align(add).Left())
			}
			cards := make([]ggui.Widget, n)
			for i := range n {
				cards[i] = spaceCard(m, i, owners)
			}
			return section(T("Spaces"), append(cards, ggui.Align(add).Left())...)
		}),
	)
}

// spaceCard is one space's name, owners and delete button.
func spaceCard(m *model, i int, owners ggui.Readable[[]string]) ggui.Widget {
	name := ggui.Bind(func() string {
		p := m.Prefs.Get()
		if i < len(p.Spaces) {
			return p.Spaces[i].Name
		}
		return ""
	}, func(v string) {
		m.updateSpaces(func(spaces []space) []space {
			if i < len(spaces) {
				spaces[i].Name = v
			}
			return spaces
		})
	})
	remove := ui.Tooltip(ui.ButtonOf(icon("trash-2").Size(14), func() {
		m.updateSpaces(func(spaces []space) []space { return slices.Delete(spaces, i, i+1) })
	}).Ghost().Pad(6).Radius(6).Name(T("Delete space")), T("Delete space"))
	chips := ggui.Reactive(func() ggui.Widget {
		p := m.Prefs.Get()
		if i >= len(p.Spaces) {
			return ggui.Box()
		}
		in := p.Spaces[i].Owners
		// The owners it holds that no session names now stay offered too.
		all := append(slices.Clone(owners.Get()), in...)
		slices.SortFunc(all, compareOwners)
		all = slices.Compact(all)
		if len(all) == 0 {
			return muted(ggui.Text(T("No owners yet: they come from the origin remotes of the sessions' repositories.")).Size(12))
		}
		var w []ggui.Widget
		for _, o := range all {
			name, host := ownerName(o)
			label := name
			if host != "" {
				label += " · " + host
			}
			b := ui.Button(label, func() { m.toggleOwner(i, o) }).Name(label)
			if !slices.Contains(in, o) {
				b = b.Outline()
			}
			w = append(w, b)
		}
		return ggui.Wrap(w...).Gap(6)
	})
	head := ggui.Row(
		ggui.Expanded(ui.TextField(name).Placeholder(Tf("Space %d", i+1)).Name(T("Space name"))),
		remove,
	).Gap(8).Align(ggui.AlignCenter)
	return card(ggui.Box(ggui.Column(head, chips).Gap(12).Stretch()).Pad(14, 16))
}
