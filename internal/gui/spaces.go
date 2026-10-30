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
	ID     string   `json:"id"` // what it is shown by, and its tabs and panes kept by
	Name   string   `json:"name"`
	Owners []string `json:"owners,omitempty"`
	// OldLayoutBy is how the space kept its tabs and panes before one way
	// held for every space, read once into spaceList's LayoutBy.
	OldLayoutBy string `json:"layout_by,omitempty"`
}

// spaceList is the spaces, how they keep their tabs and panes, and whether
// the space of every session is among them: part of the workspace
// (workspace.go), which writes them down and shares them with the other
// nodes.
type spaceList struct {
	Spaces   []space `json:"spaces,omitempty"`
	LayoutBy string  `json:"layout_by,omitempty"` // whole space, by owner or by project; see layoutBys
	// NoAll leaves the space of every session out, while there are others.
	NoAll bool `json:"no_all,omitempty"`
	// OldAllLayoutBy is how the space of every session kept its tabs and
	// panes, as OldLayoutBy is a space's.
	OldAllLayoutBy string `json:"all_layout_by,omitempty"`
}

// upgraded is l with the ways its spaces each kept their tabs, from before
// one way held for all, made the one way: the space of every session's,
// or else the first space's that had one.
func (l spaceList) upgraded() spaceList {
	by := l.OldAllLayoutBy
	l.Spaces = slices.Clone(l.Spaces)
	for i := range l.Spaces {
		by = cmp.Or(by, l.Spaces[i].OldLayoutBy)
		l.Spaces[i].OldLayoutBy = ""
	}
	l.LayoutBy, l.OldAllLayoutBy = cmp.Or(l.LayoutBy, by), ""
	return l
}

// spaceSet is the spaces with the one the window shows, by index: 0 for
// every session, then the spaces in order. The window keeps the one shown
// by its ID, so the spaces changing on another node shows the same space
// still, or every session once it is gone.
type spaceSet struct {
	spaceList
	Space int
}

// spaceSetOf is list with the space whose ID is shown, or the first there
// is.
func spaceSetOf(list spaceList, shown string) spaceSet {
	p := spaceSet{spaceList: list}
	i := slices.IndexFunc(list.Spaces, func(sp space) bool { return sp.ID == shown })
	p.Space = max(i+1, p.firstSpace())
	return p
}

// spacesNow is the spaces, and the one shown, as they are.
func (m *model) spacesNow() spaceSet { return ggui.Peek(m.spaceSet) }

// spaces is spacesNow, followed by the reactive context reading it.
func (m *model) spaces() spaceSet { return m.spaceSet.Get() }

// allSpace is the index of the space of every session.
const allSpace = 0

// spaceOf is the space at index i, 0 for every session; ok is false for
// that one, which holds no owners of its own.
func (p spaceSet) spaceOf(i int) (sp space, ok bool) {
	if i <= allSpace || i > len(p.Spaces) {
		return space{}, false
	}
	return p.Spaces[i-1], true
}

// firstSpace is the index of the first space shown: that of every
// session, unless it is left out for the others.
func (p spaceSet) firstSpace() int {
	if p.NoAll && len(p.Spaces) > 0 {
		return allSpace + 1
	}
	return allSpace
}

// hasSpace reports whether there is a space at index i: the first shown
// through the last.
func (p spaceSet) hasSpace(i int) bool { return i >= p.firstSpace() && i <= len(p.Spaces) }

// spaceName is the name the sidebar shows for space i.
func (p spaceSet) spaceName(i int) string {
	sp, ok := p.spaceOf(i)
	if !ok {
		return T("All sessions")
	}
	return cmp.Or(sp.Name, Tf("Space %d", i))
}

// inSpace reports whether a session or folder of owner shows in space i.
func (p spaceSet) inSpace(i int, owner string) bool {
	sp, ok := p.spaceOf(i)
	return !ok || slices.Contains(sp.Owners, owner)
}

// spaceSessions keeps the sessions space i shows.
func (p spaceSet) spaceSessions(i int, sessions []*protocol.SessionInfo) []*protocol.SessionInfo {
	if _, ok := p.spaceOf(i); !ok {
		return sessions
	}
	return slices.DeleteFunc(slices.Clone(sessions), func(s *protocol.SessionInfo) bool { return !p.inSpace(i, s.GetOwner()) })
}

// spaceProjects keeps the kept folders space i shows.
func (p spaceSet) spaceProjects(i int, projects []keptProject) []keptProject {
	if _, ok := p.spaceOf(i); !ok {
		return projects
	}
	return slices.DeleteFunc(slices.Clone(projects), func(r keptProject) bool { return !p.inSpace(i, r.Owner) })
}

// setSpace shows space i in the sidebar, as far as there is one.
func (m *model) setSpace(i int) {
	set := m.spacesNow()
	id := set.spaceID(min(max(i, set.firstSpace()), len(set.Spaces)))
	m.Prefs.Update(func(p prefs) prefs { p.ShownSpace = id; return p })
}

// moveSpace shows the space by beside the one shown, as far as there is
// one: 1 the next, -1 the one before.
func (m *model) moveSpace(by int) { m.setSpace(m.spacesNow().Space + by) }

// updateSpaces changes the spaces, on a copy so the list they were stays
// as it was.
func (m *model) updateSpaces(fn func([]space) []space) {
	m.Spaces.Update(func(l spaceList) spaceList {
		spaces := slices.Clone(l.Spaces)
		for i := range spaces {
			spaces[i].Owners = slices.Clone(spaces[i].Owners)
		}
		l.Spaces = fn(spaces)
		return l
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
	m.updateSpaces(func(spaces []space) []space {
		return append(spaces, space{ID: newSpaceID(), Name: name, Owners: owners})
	})
	m.setSpace(len(ggui.Peek(m.Spaces).Spaces))
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
	return ggui.Reactive(func() ggui.Widget {
		p := m.spaces()
		current := p.Space
		items := make([]ggui.Widget, 0, len(p.Spaces)+3)
		for i := p.firstSpace(); i <= len(p.Spaces); i++ {
			items = append(items, ui.MenuItem(p.spaceName(i), func() { m.setSpace(i) }).Checked(i == current))
		}
		items = append(items, ui.MenuDivider(), ui.MenuItem(T("Edit spaces…"), func() { m.openSettings("spaces") }))
		title := ggui.Row(
			ggui.Text(p.spaceName(current)).Size(14).Weight(ggui.WeightMedium).NoWrap().Ellipsis(),
			icon("chevron-down").Size(13).Color(tokMuted),
		).Gap(4).Align(ggui.AlignCenter)
		w := []ggui.Widget{iconMenu(ui.MenuOf(title, items...).Name(T("Spaces")), 2)}
		if first := p.firstSpace(); len(p.Spaces) > first {
			w = append(w, spaceDots(m, first, len(p.Spaces), current))
		}
		return ggui.Row(w...).Gap(6).Align(ggui.AlignCenter)
	})
}

// spaceDots marks the space shown among first through last, each dot
// showing its own.
func spaceDots(m *model, first, last, current int) ggui.Widget {
	var dots []ggui.Widget
	for i := first; i <= last; i++ {
		c := tokSurface3
		if i == current {
			c = tokMuted
		}
		dots = append(dots, ui.ButtonOf(ggui.Box().Size(6, 6).Radius(3).Fill(c), func() { m.setSpace(i) }).
			Ghost().Pad(2).Radius(4).Name(m.spacesNow().spaceName(i)))
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
	// moving is set while the list is off its place, so the lists beside
	// it are made only then.
	moving *ggui.StateValue[bool]
}

func newSpaceSwipe(m *model) *spaceSwipe {
	return &spaceSwipe{
		can: func(by int) bool {
			p := m.spacesNow()
			return p.hasSpace(p.Space + by)
		},
		move:   m.moveSpace,
		post:   m.post,
		moving: ggui.State(false),
	}
}

// area lays the swipe's input over child.
func (s *spaceSwipe) area(child ggui.Widget) ggui.Widget { return &swipeArea{s, child} }

// slide is the list of the space shown, moved as the swipe has it, with
// the list of the space it moves toward, prev or next, beside it.
func (s *spaceSwipe) slide(prev, cur, next ggui.Widget) ggui.Widget {
	return &swipeSlide{s: s, prev: prev, cur: cur, next: next}
}

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
		s.moving.Set(true)
		s.drag = s.settle.Value(ggui.FrameTime())
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
	x := s.settle.Value(now)
	if x == 0 && ggui.Peek(s.moving) {
		s.post(func() {
			if !s.active {
				s.moving.Set(false)
			}
		})
	}
	return x
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

// swipeSlide is the lists a spaceSwipe moves: the space shown, and the
// one beside it the fingers bring in, edge to edge.
type swipeSlide struct {
	s               *spaceSwipe
	prev, cur, next ggui.Widget
}

// Layout lays out the lists beside too, which are there only while the
// list moves.
func (l *swipeSlide) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	size := l.cur.Layout(c, env)
	l.prev.Layout(c, env)
	l.next.Layout(c, env)
	return size
}

func (l *swipeSlide) Paint(dst *ggui.Canvas, r ggui.Rect) {
	l.s.width = r.Size.W
	// FrameTime, not Now: the settle asks for frames itself while it
	// moves, and the list at rest asks for none. Now kept the window
	// painting every frame for as long as the sidebar showed.
	x := l.s.offset(ggui.FrameTime())
	if x == 0 || r.Size.W <= 0 {
		dst.Paint(l.cur, r)
		return
	}
	// Moved left, the next space follows on its right; moved right, the
	// one before comes in on its left.
	beside, at := l.next, x+r.Size.W
	if x > 0 {
		beside, at = l.prev, x-r.Size.W
	}
	clip := dst.Clip(r)
	clip.Transform(ggui.Affine{}.Translate(x, 0), func(c *ggui.Canvas) { c.Paint(l.cur, r) })
	clip.Transform(ggui.Affine{}.Translate(at, 0), func(c *ggui.Canvas) { c.Paint(beside, r) })
}

// ownerSpaceItems are an owner heading's menu entries for the spaces: one
// to put it in each space or take it out, and one for a new space of it.
func ownerSpaceItems(m *model, owner, name string) []ggui.Widget {
	spaces := ggui.Peek(m.Spaces).Spaces
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
	spaces := ggui.Map(m.Spaces, func(l spaceList) int { return len(l.Spaces) })
	owners := ggui.Map(m.Sessions, func(s []*protocol.SessionInfo) []string {
		return knownOwners(s, ggui.Peek(m.Prefs).Projects)
	}).WithEqual(slices.Equal)
	add := ui.Button(T("Add space"), func() { m.newSpace("") }).Outline()
	showAll := ggui.Bind(func() bool { return !m.Spaces.Get().NoAll }, func(on bool) {
		m.Spaces.Update(func(l spaceList) spaceList { l.NoAll = !on; return l })
	})
	general := card(
		row(T("Tabs and panes"), T("Each space keeps tabs and panes of its own: one set for the whole space, or one for each owner or project, shown as you pick one of its sessions in the sidebar."),
			layoutByToggle(m.Spaces.Field(func(l *spaceList) *string { return &l.LayoutBy }))),
		row(T("All sessions"), T("A space of every session, before the others. Left out, the sidebar shows only the spaces you add."),
			ui.Switch(showAll, "").Name(T("All sessions"))),
	)
	return page(T("Spaces"), T("Spaces divide the sidebar's sessions by whose repositories they are in. Swipe across the sidebar with two fingers, or pick one from its title, to move between them."),
		section(T("Every space"), general),
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
	name := spaceField(m, i, func(sp *space) *string { return &sp.Name })
	remove := ui.Tooltip(ui.ButtonOf(icon("trash-2").Size(14), func() {
		m.updateSpaces(func(spaces []space) []space { return slices.Delete(spaces, i, i+1) })
	}).Ghost().Pad(6).Radius(6).Name(T("Delete space")), T("Delete space"))
	chips := ggui.Reactive(func() ggui.Widget {
		p := m.Spaces.Get()
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

// spaceField binds a field of space i, as prefField does a setting's.
func spaceField(m *model, i int, field func(*space) *string) ggui.Binding[string] {
	return ggui.Bind(func() string {
		p := m.Spaces.Get()
		if i < len(p.Spaces) {
			return *field(&p.Spaces[i])
		}
		return ""
	}, func(v string) {
		m.updateSpaces(func(spaces []space) []space {
			if i < len(spaces) {
				*field(&spaces[i]) = v
			}
			return spaces
		})
	})
}

// layoutByToggle picks how a space keeps its tabs and panes.
func layoutByToggle(b ggui.Binding[string]) ggui.Widget {
	return ui.ToggleGroup(b).Options(layoutBys).Format(func(s string) string { return T(layoutByLabels[s]) }).Name(T("Tabs and panes"))
}
