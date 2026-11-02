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

// Spaces: the sidebar's sessions divided by project, as views of them. A
// space holds owners, host/name as the daemon gives them, and projects by
// ID; the sidebar shows the project sections of the space chosen, whole,
// or every section in the first, which is always there. A space holds a
// section it names, leaves out one it hides, and else holds one of an
// owner of its; see showsGroup. A swipe of two
// fingers across the sidebar moves to the space beside it.

// space is a named set of repository owners.
type space struct {
	ID     string   `json:"id"` // what it is shown by, and its tabs and panes kept by
	Name   string   `json:"name"`
	Owners []string `json:"owners,omitempty"`
	// Projects are the sections the space shows whatever their owners,
	// and Hidden those it leaves out though their owners are its: by ID.
	Projects []string `json:"projects,omitempty"`
	Hidden   []string `json:"hidden,omitempty"`
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
	// Projects are the folders the user put together or named; see
	// projects.go.
	Projects []project `json:"projects,omitempty"`
	// Names are the names given to the sections the sidebar groups itself,
	// by their keys.
	Names map[string]string `json:"names,omitempty"`
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
			spaces[i].Projects = slices.Clone(spaces[i].Projects)
			spaces[i].Hidden = slices.Clone(spaces[i].Hidden)
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

// spacesSettings edits the spaces: each one's name, its place among them,
// the owners it holds and the projects it shows.
func spacesSettings(m *model) ggui.Widget {
	spaces := ggui.Map(m.Spaces, func(l spaceList) int { return len(l.Spaces) })
	owners := ggui.Map(m.Sessions, func(s []*protocol.SessionInfo) []string {
		return knownOwners(s, ggui.Peek(m.Prefs).Projects)
	}).WithEqual(slices.Equal)
	sections := ggui.Derived(func() []spaceSection {
		l := m.Spaces.Get()
		return sectionsOf(groupProjects(m.Sessions.Get(), m.Nodes.Get(), m.Prefs.Get().Projects, l.Projects, l.Names), l)
	}).WithEqual(deepEqual)
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
	return page(T("Spaces"), T("Spaces divide the sidebar's projects: those of the owners a space holds, and those you put in it from a project's menu. Swipe across the sidebar with two fingers, or pick one from its title, to move between them."),
		section(T("Every space"), general),
		ggui.View(spaces, func(n int) ggui.Widget {
			if n == 0 {
				return section(T("Spaces"), muted(ggui.Text(T("No spaces yet. Add one, or pick New space with this owner from an owner's menu in the sidebar.")).Size(13)), ggui.Align(add).Left())
			}
			cards := make([]ggui.Widget, n)
			for i := range n {
				cards[i] = spaceCard(m, i, n, owners, sections)
			}
			return section(T("Spaces"), append(cards, ggui.Align(add).Left())...)
		}),
	)
}

// spaceSection is a project section as the space settings list it: its
// ID, its name, and the owners of its repositories.
type spaceSection struct {
	Key, Name string
	Owners    []string
}

// sectionsOf are the sections of groups, by name, and those the spaces of
// l hold or hide that no group is now, as their IDs name them.
func sectionsOf(groups []sessionGroup, l spaceList) []spaceSection {
	out := make([]spaceSection, 0, len(groups))
	seen := map[string]bool{}
	for _, g := range groups {
		out = append(out, spaceSection{Key: g.Key, Name: groupName(g), Owners: groupOwners(g)})
		seen[g.Key] = true
	}
	for _, sp := range l.Spaces {
		for _, id := range append(slices.Clone(sp.Projects), sp.Hidden...) {
			if !seen[id] {
				out = append(out, spaceSection{Key: id, Name: projectLabel(l.Projects, id)})
				seen[id] = true
			}
		}
	}
	slices.SortStableFunc(out, func(a, b spaceSection) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

// deleteSpace deletes space i, an index into the spaces after the first,
// with a notice whose Undo brings it back, as the func returned does.
func (m *model) deleteSpace(i int) (undo func()) {
	before := slices.Clone(ggui.Peek(m.Spaces).Spaces)
	if i < 0 || i >= len(before) {
		return func() {}
	}
	name := cmp.Or(before[i].Name, Tf("Space %d", i+1))
	m.updateSpaces(func(spaces []space) []space { return slices.Delete(spaces, i, i+1) })
	undo = func() { m.updateSpaces(func([]space) []space { return slices.Clone(before) }) }
	m.toaster.Push(ui.Toast(Tf("Deleted %s", name), "").Action(T("Undo"), undo))
	return undo
}

// reorderSpace moves space i by places among the spaces after the first:
// -1 before the one before it, 1 after the one after.
func (m *model) reorderSpace(i, by int) {
	m.updateSpaces(func(spaces []space) []space {
		if j := i + by; i >= 0 && i < len(spaces) && j >= 0 && j < len(spaces) {
			spaces[i], spaces[j] = spaces[j], spaces[i]
		}
		return spaces
	})
}

// spaceCard is one space of n: its name, its place, how many projects it
// shows, its owners and its projects.
func spaceCard(m *model, i, n int, owners ggui.Readable[[]string], sections ggui.Readable[[]spaceSection]) ggui.Widget {
	name := spaceField(m, i, func(sp *space) *string { return &sp.Name })
	button := func(glyph, label string, enabled bool, fn func()) ggui.Widget {
		face := icon(glyph).Size(14)
		if !enabled {
			face = face.Color(tokExtraMuted)
		}
		return ui.Tooltip(ui.ButtonOf(face, fn).Ghost().Pad(6).Radius(6).Disabled(!enabled).Name(label), label)
	}
	count := ggui.Reactive(func() ggui.Widget {
		set := m.spaces()
		shown := 0
		for _, sec := range sections.Get() {
			if set.showsSection(i+1, sec.Key, sec.Owners) {
				shown++
			}
		}
		return muted(ggui.Text(Tf("%d projects", shown)).Size(12).NoWrap())
	})
	head := ggui.Row(
		ggui.Expanded(ui.TextField(name).Placeholder(Tf("Space %d", i+1)).Name(T("Space name"))),
		count,
		button("arrow-up", T("Move up"), i > 0, func() { m.reorderSpace(i, -1) }),
		button("arrow-down", T("Move down"), i < n-1, func() { m.reorderSpace(i, 1) }),
		button("trash-2", T("Delete space"), true, func() { m.deleteSpace(i) }),
	).Gap(4).Align(ggui.AlignCenter)
	return card(
		ggui.Box(head).Pad(12, 12, 12, 16),
		ggui.Box(spaceOwners(m, i, owners)).Pad(12, 16),
		ggui.Box(spaceProjects(m, i, sections)).Pad(12, 16),
	)
}

// subhead is a part of a space's card: its title and what it does, over
// its content.
func subhead(title, hint string, content ...ggui.Widget) ggui.Widget {
	return ggui.Column(append([]ggui.Widget{
		ggui.Column(ggui.Text(title).Size(13).Weight(ggui.WeightMedium), muted(ggui.Text(hint).Size(12))).Gap(2).Stretch(),
	}, content...)...).Gap(10).Stretch()
}

// ownerLabel names an owner, with its host when it has one.
func ownerLabel(o string) string {
	name, host := ownerName(o)
	if host != "" {
		return name + " · " + host
	}
	return name
}

// spaceOwners are the owners space i holds, each a chip that lets it go,
// and a menu of the others the sidebar knows to add.
func spaceOwners(m *model, i int, owners ggui.Readable[[]string]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		l := m.Spaces.Get()
		if i >= len(l.Spaces) {
			return ggui.Box()
		}
		in := slices.Clone(l.Spaces[i].Owners)
		slices.SortFunc(in, compareOwners)
		var w []ggui.Widget
		for _, o := range in {
			label := ownerLabel(o)
			w = append(w, removableChip(label, Tf("Take %s out", label), func() { m.toggleOwner(i, o) }))
		}
		var rest []ggui.Widget
		for _, o := range owners.Get() {
			if !slices.Contains(in, o) {
				rest = append(rest, ui.MenuItem(ownerLabel(o), func() { m.toggleOwner(i, o) }))
			}
		}
		if len(rest) > 0 {
			w = append(w, addMenu(T("Add owner"), rest))
		}
		hint := T("Every project whose repository one of them owns shows here.")
		switch {
		case len(in) == 0 && len(rest) > 0:
			hint = T("Add an owner to show every project whose repository it owns.")
		case len(in) == 0:
			hint = T("No owners yet: they come from the origin remotes of the sessions' repositories.")
		}
		return subhead(T("Owners"), hint, ggui.Wrap(w...).Gap(6))
	})
}

// spaceProjects are the projects space i shows, or leaves out, each with
// why and a switch, and a menu of the others to add.
func spaceProjects(m *model, i int, sections ggui.Readable[[]spaceSection]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		set := m.spaces()
		if _, ok := set.spaceOf(i + 1); !ok {
			return ggui.Box()
		}
		var rows, rest []ggui.Widget
		for _, sec := range sections.Get() {
			reason := set.sectionIn(i+1, sec.Key, sec.Owners)
			if reason == notShown {
				rest = append(rest, ui.MenuItem(sec.Name, func() { m.toggleSection(i, sec.Key, sec.Owners) }))
				continue
			}
			rows = append(rows, spaceProjectRow(m, i, sec, reason, set))
		}
		var content []ggui.Widget
		if len(rows) == 0 {
			content = append(content, muted(ggui.Text(T("None yet: add its owners, or projects one by one.")).Size(12)))
		} else {
			content = append(content, ggui.Column(rows...).Gap(10).Stretch())
		}
		if len(rest) > 0 {
			content = append(content, ggui.Align(addMenu(T("Add project"), rest)).Left())
		}
		return subhead(T("Projects"), T("Those its owners hold, and those put in or left out here or from a project's menu."), content...)
	})
}

// spaceProjectRow is a project space i shows or leaves out, said why, with
// a switch to show it or leave it out.
func spaceProjectRow(m *model, i int, sec spaceSection, reason sectionReason, set spaceSet) ggui.Widget {
	var why string
	switch reason {
	case shownByOwner:
		sp, _ := set.spaceOf(i + 1)
		var by []string
		for _, o := range sec.Owners {
			if slices.Contains(sp.Owners, o) {
				name, _ := ownerName(o)
				by = append(by, name)
			}
		}
		why = Tf("Through %s", strings.Join(by, ", "))
	case byProject:
		why = T("Added to this space")
	case leftOut:
		why = T("Left out of this space")
	}
	shown := ggui.Bind(func() bool { return m.spaces().showsSection(i+1, sec.Key, sec.Owners) }, func(on bool) {
		if on != m.spacesNow().showsSection(i+1, sec.Key, sec.Owners) {
			m.toggleSection(i, sec.Key, sec.Owners)
		}
	})
	text := ggui.Text(sec.Name).Size(13).NoWrap().Ellipsis()
	if reason == leftOut {
		text = text.Color(tokMuted)
	}
	return ggui.Row(
		ggui.Expanded(ggui.Column(text, muted(ggui.Text(why).Size(11.5).NoWrap().Ellipsis())).Gap(1).Stretch()),
		ui.Switch(shown, "").Name(Tf("Show %s", sec.Name)),
	).Gap(10).Align(ggui.AlignCenter)
}

// removableChip is a chip of label with a cross that runs remove.
func removableChip(label, name string, remove func()) ggui.Widget {
	return ui.ButtonOf(ggui.Row(
		ggui.Text(label).Size(12.5).NoWrap(),
		icon("x").Size(12).Color(tokMuted),
	).Gap(6).Align(ggui.AlignCenter), remove).Outline().Name(name)
}

// addMenu is a button of label with a plus that opens items.
func addMenu(label string, items []ggui.Widget) ggui.Widget {
	return ui.MenuOf(ggui.Row(icon("plus").Size(13), ggui.Text(label).Size(12.5).NoWrap()).Gap(6).Align(ggui.AlignCenter), items...).Name(label)
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
