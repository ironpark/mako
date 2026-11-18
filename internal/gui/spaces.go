package gui

import (
	"cmp"
	"context"
	"encoding/json"
	"image/color"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/ui/icons"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
	shared "github.com/ironpark/mako/internal/workspace"
)

// Spaces: the sidebar's sessions divided by project, as views of them. A
// project section is in one space: the one it was moved to, else the
// first holding an owner of its repositories, else the default one; see
// placeOf. The space of every session, first unless left out, shows
// every section. A space's colour washes the sidebar faintly while it
// shows. A swipe of two fingers across the sidebar moves to the space
// beside.

// space is a named set of project sections.
type space struct {
	ID    string `json:"id"` // what it is shown by, and its tabs and panes kept by
	Name  string `json:"name"`
	Color string `json:"color,omitempty"` // among lookColors, by name; none unless set
	// Owners place a section none of the spaces was moved to in the first
	// space holding one of its owners.
	Owners []string `json:"owners,omitempty"`
	// Projects are the sections moved to the space, by ID: in the first
	// space listing one, whatever its owners. Hidden are those older
	// windows left out of the space though its owners hold them, which no
	// owner of its places there.
	Projects []string `json:"projects,omitempty"`
	Hidden   []string `json:"hidden,omitempty"`
	// Layout names the window layout document the space shows; "" for
	// the default (winsync.go).
	Layout string `json:"layout,omitempty"`
}

// color is the space's colour; nil for none.
func (sp space) color() color.Color {
	if i := lookColorIndex(sp.Color); i >= 0 {
		return lookColors[i].Color
	}
	return nil
}

// spaceList is the spaces, and whether the space of every session is among
// them: part of the workspace (workspace.go), which writes them down and
// shares them with the other nodes.
type spaceList struct {
	Spaces []space `json:"spaces,omitempty"`
	// NoAll leaves the space of every session out, while there are others.
	NoAll bool `json:"no_all,omitempty"`
	// Default is the ID of the space a section goes to that nothing else
	// places; the first when it names none.
	Default string `json:"default_space,omitempty"`
	// Projects are the folders the user put together or named; see
	// projects.go.
	Projects []project `json:"projects,omitempty"`
	// Names are the names given to the sections the sidebar groups itself,
	// by their keys.
	Names map[string]string `json:"names,omitempty"`
	// Looks are the icons and colours of the project sections, by their
	// IDs; see projectlook.go.
	Looks map[string]projectLook `json:"looks,omitempty"`
	// PinnedProjects are the projects, and folders of them, and Pinned the
	// sessions, the sidebar lists first.
	PinnedProjects []scopeKey `json:"pinned_projects,omitempty"`
	Pinned         []string   `json:"pinned,omitempty"`
	// WindowLayouts are the window layout documents, by name, as their
	// JSON; see winsync.go.
	WindowLayouts map[string]json.RawMessage `json:"window_layouts,omitempty"`
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

// updateSpace changes the space of ID id, as updateSpaces does.
func (m *model) updateSpace(id string, fn func(*space)) {
	m.updateSpaces(func(spaces []space) []space {
		if i := slices.IndexFunc(spaces, func(sp space) bool { return sp.ID == id }); i >= 0 {
			fn(&spaces[i])
		}
		return spaces
	})
}

// toggleOwner adds owner to the space of ID id, or takes it out.
func (m *model) toggleOwner(id, owner string) {
	m.updateSpace(id, func(sp *space) { sp.Owners = toggled(sp.Owners, owner) })
}

// newSpace adds a space holding owners and shows it.
func (m *model) newSpace(name string, owners ...string) { m.newSpaceOf(name, owners) }

// newSpaceOf adds a space holding owners, moves groups to it and shows it.
func (m *model) newSpaceOf(name string, owners []string, groups ...sessionGroup) {
	id := newSpaceID()
	keys := make([]string, len(groups))
	for i, g := range groups {
		keys[i] = g.Key
	}
	m.updateSpaces(func(spaces []space) []space {
		return placeSections(append(spaces, space{ID: id, Name: name, Owners: owners}), id, keys...)
	})
	m.setSpace(len(ggui.Peek(m.Spaces).Spaces))
}

// spaceMark is a dot of a space's colour, as menus and its settings mark
// it with; room for one when it has none.
func spaceMark(sp space) ggui.Widget {
	c := sp.color()
	if c == nil {
		return widget.Box().Size(8, 8).Radius(4).Border(1.2, skin.ExtraMuted)
	}
	return widget.Box().Size(8, 8).Radius(4).Fill(c)
}

// spaceTint is the colour of the space shown, easing from one space's to
// the next's; clear for a space of none. It needs an owner, as a tween
// does.
func spaceTint(m *model) ggui.Readable[color.NRGBA] {
	target := ggui.Derived(func() color.NRGBA {
		p := m.spaces()
		sp, _ := p.spaceOf(p.Space)
		if c := sp.color(); c != nil {
			return color.NRGBAModel.Convert(c).(color.NRGBA)
		}
		return color.NRGBA{}
	})
	return ggui.TweenOfWith(target, motionDuration, func(a, b color.NRGBA, t float64) color.NRGBA {
		// To or from none, only the alpha moves: the hue stays the space's.
		if a.A == 0 {
			a.R, a.G, a.B = b.R, b.G, b.B
		}
		if b.A == 0 {
			b.R, b.G, b.B = a.R, a.G, a.B
		}
		at := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
		return color.NRGBA{at(a.R, b.R), at(a.G, b.G), at(a.B, b.B), at(a.A, b.A)}
	})
}

// spaceWash is the faint wash of a space's colour c down the sidebar,
// from its very top: strongest there, thinning along a curve rather than
// a line, so it has no edge where it fades; none for a clear c.
func spaceWash(c color.NRGBA) ggui.Gradient {
	if c.A == 0 {
		return ggui.Gradient{}
	}
	const steps = 8
	stops := make([]ggui.GradientStop, steps+1)
	for i := range stops {
		t := float64(i) / steps
		a := float64(c.A) / 255 * 0.13 * (0.12 + 0.88*(1-t)*(1-t))
		stops[i] = ggui.GradientStop{Offset: t, Color: color.NRGBA{c.R, c.G, c.B, uint8(math.Round(a * 255))}}
	}
	return ggui.LinearGradient(math.Pi/2, stops...)
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

// spaceSwitcher is the sidebar heading's title: the space shown, and a
// menu of the others.
func spaceSwitcher(m *model) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		p := m.spaces()
		current := p.Space
		items := make([]ggui.Widget, 0, len(p.Spaces)+3)
		for i := p.firstSpace(); i <= len(p.Spaces); i++ {
			item := ui.MenuItem(p.spaceName(i), func() { m.setSpace(i) }).Checked(i == current)
			if sp, ok := p.spaceOf(i); ok {
				item = item.Icon(spaceMark(sp))
			}
			items = append(items, item)
		}
		items = append(items, ui.MenuDivider(), ui.MenuItem(T("Edit spaces…"), func() { m.openSettings("spaces") }))
		parts := []ggui.Widget{
			widget.Text(p.spaceName(current)).Size(14).Weight(ggui.WeightSemibold).NoWrap().Ellipsis(),
			icon("chevron-down").Size(13).Color(skin.Muted),
		}
		if sp, _ := p.spaceOf(current); sp.color() != nil {
			parts = append([]ggui.Widget{widget.Box(spaceMark(sp)).Pad(0, 2, 0, 0)}, parts...)
		}
		title := widget.Row(parts...).Gap(4).Align(widget.AlignCenter)
		return iconMenu(ui.MenuOf(title, items...).Name(T("Spaces")), 2)
	})
}

// spaceDots float at the foot of the sidebar's agents, outside the swipe
// that moves them, while there is more than one space: a dot for each, the one shown drawn long and bright.
func spaceDots(m *model) ggui.Widget {
	// The dots are made again as spaces come, go or are renamed, not as
	// the current one changes, which each dot follows itself.
	type dotList struct {
		First  int
		Names  []string
		Colors []color.Color
	}
	list := ggui.Derived(func() dotList {
		p := m.spaces()
		l := dotList{First: p.firstSpace()}
		for i := l.First; i <= len(p.Spaces); i++ {
			sp, _ := p.spaceOf(i)
			l.Names = append(l.Names, p.spaceName(i))
			l.Colors = append(l.Colors, sp.color())
		}
		return l
	}).WithEqual(deepEqual)
	current := ggui.Derived(func() int { return m.spaces().Space })
	return ggui.View(list, func(l dotList) ggui.Widget {
		if len(l.Names) <= 1 {
			return widget.Box()
		}
		var dots []ggui.Widget
		for j, name := range l.Names {
			i := l.First + j
			dots = append(dots, ui.ButtonOf(spaceDot(current, i, l.Colors[j]), func() { m.setSpace(i) }).Ghost().Pad(0).Radius(6).Name(name))
		}
		return widget.Row(dots...).Gap(2).Align(widget.AlignCenter)
	})
}

// spaceDot is space i's dot: a pill while it is the current space, which
// grows from a dot, and brightens, as the space comes in, and shrinks back
// as it goes; in the space's colour c while it is, unless it has none.
func spaceDot(current ggui.Readable[int], i int, c color.Color) ggui.Widget {
	var lit color.Color = skin.Muted
	if c != nil {
		lit = c
	}
	return ggui.Component(func() ggui.Widget {
		on := ggui.TweenOf(ggui.Map(current, func(c int) float64 { return pick(c == i, 1.0, 0.0) }), motionDuration).Easing(ggui.EaseOut)
		return ggui.View(on, func(t float64) ggui.Widget {
			w := 6 + 10*t
			pill := widget.Stack(
				widget.Box().Size(w, 6).Radius(3).Fill(skin.Surface3),
				widget.Opacity(widget.Box().Size(w, 6).Radius(3).Fill(lit), t),
			)
			return widget.Box(widget.Center(pill)).Size(22, 22)
		})
	})
}

// The trackpad swipe that moves between spaces. The list follows the
// fingers, pixel for pixel, or swipePixels to a wheel's unit; when they lift past swipeCommit of
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
func (s *spaceSwipe) scroll(d ggui.Point, phase ggui.ScrollPhase, pixels bool) bool {
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
	step := d.X
	if !pixels {
		step *= swipePixels
	}
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
	return ev.Kind == ggui.PointerScroll && a.s.scroll(ev.Scroll, ev.ScrollPhase, ev.ScrollPixels)
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

// ownerSpaceItems are an owner heading's menu entries for the spaces:
// moving its projects to one, and a new space of the owner, which they
// move to and which new projects of the owner go to.
func ownerSpaceItems(m *model, o ownerSection, name string) []ggui.Widget {
	items := []ggui.Widget{ui.MenuDivider()}
	if to := moveItems(m, name, o.Groups...); len(to) > 0 {
		items = append(items, ui.MenuSub(T("Move projects to space"), to...))
	}
	return append(items, ui.MenuItem(T("New space with this owner"), func() { m.newSpaceOf(name, []string{o.Owner}, o.Groups...) }))
}

// spacesSettings edits the spaces: which one is the default, each one's
// name and colour, their order, dragged by their handles, the owners each
// places new projects by, and the projects in each, dragged from one to
// another.
func spacesSettings(m *model) ggui.Widget {
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
		row(T("All sessions"), T("A space of every session, before the others. Left out, the sidebar shows only the spaces you add."),
			ui.Switch(showAll, "").Name(T("All sessions"))),
		row(T("Default space"), T("Where a new project goes that the owners of no space place."), defaultSpaceMenu(m)),
		row(T("Copy from another node"), T("Take the spaces, projects, names, looks and pins of a node's windows in place of these. A node shows its own only to one it lets in."),
			copySpacesMenu(m)),
	)
	list := widget.Reorderable(spaceOrder(m), func(sp space) string { return sp.ID }, func(it widget.EachItem[space]) ggui.Widget {
		return spaceCard(m, it.Item.ID, owners, sections)
	}).Handles().Gap(sectionGap).Stretch()
	none := ggui.Map(m.Spaces, func(l spaceList) bool { return len(l.Spaces) == 0 })
	return page(T("Spaces"), T("Spaces divide the sidebar's projects, each project in one. A new project goes to the first space holding an owner of its repository, else to the default space; move it to another from its menu, or by dragging it here. Swipe across the sidebar with two fingers, or pick a space from its title, to move between them."),
		section(T("Every space"), general),
		section(T("Spaces"),
			ggui.View(none, func(none bool) ggui.Widget {
				if none {
					return muted(widget.Text(T("No spaces yet. Add one, or pick New space with this owner from an owner's menu in the sidebar.")).Size(13))
				}
				return list
			}),
			widget.Align(add).Left(),
		),
		section(T("On a phone"), compactSection(m)),
	)
}

// spaceOrder binds the spaces in order, for the list that reorders them.
func spaceOrder(m *model) ggui.Binding[[]space] {
	return ggui.Bind(func() []space { return m.Spaces.Get().Spaces }, func(v []space) {
		m.updateSpaces(func([]space) []space { return slices.Clone(v) })
	})
}

// defaultSpaceMenu picks the default space.
func defaultSpaceMenu(m *model) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		set := m.spaces()
		if len(set.Spaces) == 0 {
			return muted(widget.Text(T("No spaces yet")).Size(12.5))
		}
		at := set.defaultSpace()
		var items []ggui.Widget
		for i, sp := range set.Spaces {
			items = append(items, ui.MenuItem(spaceLabel(sp, i+1), func() {
				m.Spaces.Update(func(l spaceList) spaceList { l.Default = sp.ID; return l })
			}).Icon(spaceMark(sp)).Checked(i+1 == at))
		}
		sp, _ := set.spaceOf(at)
		face := widget.Row(spaceMark(sp), widget.Text(spaceLabel(sp, at)).Size(13).NoWrap(), icon("chevron-down").Size(14).Color(skin.Muted)).Gap(8).Align(widget.AlignCenter)
		return ui.MenuOf(face, items...).Name(T("Default space"))
	})
}

// spaceSection is a project section as the space settings list it: its
// ID, its name, and the owners of its repositories.
type spaceSection struct {
	Key, Name string
	Owners    []string
}

// sectionsOf are the sections of groups, by name, and those moved to the
// spaces of l that no group is now, as their IDs name them.
func sectionsOf(groups []sessionGroup, l spaceList) []spaceSection {
	out := make([]spaceSection, 0, len(groups))
	seen := map[string]bool{}
	for _, g := range groups {
		out = append(out, spaceSection{Key: g.Key, Name: groupName(g), Owners: groupOwners(g)})
		seen[g.Key] = true
	}
	for _, sp := range l.Spaces {
		for _, id := range sp.Projects {
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
// with a notice whose Undo brings it back, as the func returned does. Its
// projects go where the others place them, or to the default space.
func (m *model) deleteSpace(i int) (undo func()) {
	before := slices.Clone(ggui.Peek(m.Spaces).Spaces)
	if i < 0 || i >= len(before) {
		return func() {}
	}
	name := spaceLabel(before[i], i+1)
	m.updateSpaces(func(spaces []space) []space { return slices.Delete(spaces, i, i+1) })
	undo = func() { m.updateSpaces(func([]space) []space { return slices.Clone(before) }) }
	m.toaster.Push(ui.Toast(Tf("Deleted %s", name), "").Action(T("Undo"), undo))
	return undo
}

// spaceIndex is the index of the space of ID id among the spaces after
// the first, -1 for none.
func spaceIndex(spaces []space, id string) int {
	return slices.IndexFunc(spaces, func(sp space) bool { return sp.ID == id })
}

// spaceCard is the space of ID id: its handle, colour, name, how many
// projects are in it, its owners and its projects.
func spaceCard(m *model, id string, owners ggui.Readable[[]string], sections ggui.Readable[[]spaceSection]) ggui.Widget {
	name := spaceField(m, id, func(sp *space) *string { return &sp.Name })
	grip := widget.ReorderHandle(widget.Box(widget.Center(ui.Icon(icons.Grip).Size(15).Color(skin.Muted))).Size(20, 30)).Name(T("Move space"))
	trash := ui.Tooltip(ui.ButtonOf(icon("trash-2").Size(14), func() { m.deleteSpace(spaceIndex(ggui.Peek(m.Spaces).Spaces, id)) }).
		Ghost().Pad(6).Radius(6).Name(T("Delete space")), T("Delete space"))
	status := ggui.Reactive(func() ggui.Widget {
		set := m.spaces()
		i := spaceIndex(set.Spaces, id)
		if i < 0 {
			return widget.Box()
		}
		in := 0
		for _, sec := range sections.Get() {
			if at, _ := set.placeOf(sec.Key, sec.Owners); at == i+1 {
				in++
			}
		}
		count := muted(widget.Text(pick(in == 1, T("1 project"), Tf("%d projects", in))).Size(12).NoWrap())
		if set.defaultSpace() != i+1 {
			return count
		}
		return widget.Row(ui.Badge(T("Default")).Secondary(), count).Gap(8).Align(widget.AlignCenter)
	})
	head := widget.Row(
		grip,
		spaceColorPicker(m, id),
		widget.Expanded(ui.TextField(name).Placeholder(T("Space name")).Name(T("Space name"))),
		status,
		trash,
	).Gap(6).Align(widget.AlignCenter)
	return card(
		widget.Box(head).Pad(10, 12, 10, 8),
		widget.Box(spaceOwners(m, id, owners)).Pad(12, 16),
		widget.Box(spaceProjects(m, id, sections)).Pad(12, 16),
	)
}

// spaceColorPicker is a swatch of the space's colour, which opens a choice
// of the others, and of none.
func spaceColorPicker(m *model, id string) ggui.Widget {
	set := func(c string) func() { return func() { m.updateSpace(id, func(sp *space) { sp.Color = c }) } }
	swatches := ggui.Reactive(func() ggui.Widget {
		l := m.Spaces.Get()
		i := spaceIndex(l.Spaces, id)
		if i < 0 {
			return widget.Box()
		}
		now := l.Spaces[i].Color
		swatch := func(inner ggui.Widget, on bool, label string, fn func()) ggui.Widget {
			face := widget.Box(widget.Center(inner)).Size(24, 24).Radius(12)
			if on {
				face = face.Border(1.5, skin.Fg)
			}
			return ui.ButtonOf(face, fn).Ghost().Pad(0).Radius(12).Name(label)
		}
		w := []ggui.Widget{swatch(icon("circle-slash").Size(14).Color(skin.Muted), lookColorIndex(now) < 0, T("No colour"), set(""))}
		for _, c := range lookColors {
			w = append(w, swatch(widget.Box().Size(14, 14).Radius(7).Fill(c.Color), c.Name == now, T(c.Label), set(c.Name)))
		}
		return widget.Column(muted(widget.Text(T("Colour")).Size(11.5)), widget.Row(w...).Gap(6)).Gap(8)
	})
	face := ggui.Reactive(func() ggui.Widget {
		l := m.Spaces.Get()
		var sp space
		if i := spaceIndex(l.Spaces, id); i >= 0 {
			sp = l.Spaces[i]
		}
		if c := sp.color(); c != nil {
			return widget.Box(widget.Center(widget.Box().Size(14, 14).Radius(7).Fill(c))).Size(18, 18)
		}
		return widget.Box(widget.Center(icon("palette").Size(15).Color(skin.Muted))).Size(18, 18)
	})
	picker := ui.PopoverOf(face, swatches).Name(T("Space colour")).Pad(10).Align(widget.PopupStart)
	picker.Trigger().Ghost().Pad(5).Radius(6)
	return borderless(picker)
}

// subhead is a part of a space's card: its title and what it does, over
// its content.
func subhead(title, hint string, content ...ggui.Widget) ggui.Widget {
	return widget.Column(append([]ggui.Widget{
		widget.Column(widget.Text(title).Size(13).Weight(ggui.WeightMedium), muted(widget.Text(hint).Size(12))).Gap(2).Stretch(),
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

// spaceOwners are the owners the space of ID id holds, each a chip that
// lets it go, and a menu of the others the sidebar knows to add.
func spaceOwners(m *model, id string, owners ggui.Readable[[]string]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		l := m.Spaces.Get()
		i := spaceIndex(l.Spaces, id)
		if i < 0 {
			return widget.Box()
		}
		in := slices.Clone(l.Spaces[i].Owners)
		slices.SortFunc(in, compareOwners)
		var w []ggui.Widget
		for _, o := range in {
			label := ownerLabel(o)
			w = append(w, removableChip(label, Tf("Take %s out", label), func() { m.toggleOwner(id, o) }))
		}
		var rest []ggui.Widget
		for _, o := range owners.Get() {
			if !slices.Contains(in, o) {
				rest = append(rest, ui.MenuItem(ownerLabel(o), func() { m.toggleOwner(id, o) }))
			}
		}
		if len(rest) > 0 {
			w = append(w, addMenu(T("Add owner"), rest))
		}
		hint := T("A project of theirs goes here unless it was moved, or an earlier space holds its owner.")
		switch {
		case len(in) == 0 && len(rest) > 0:
			hint = T("Add an owner to put its projects here unless they were moved.")
		case len(in) == 0:
			hint = T("No owners yet: they come from the origin remotes of the sessions' repositories.")
		}
		return subhead(T("Owners"), hint, widget.Wrap(w...).Gap(6))
	})
}

// spaceDrag is a project dragged from a space's card: its section's ID,
// and the space it is in.
type spaceDrag struct{ Key, From string }

// spaceProjects are the projects in the space of ID id, each a chip that
// drags to another space's card or moves there from its menu, in a well
// the others' drop into.
func spaceProjects(m *model, id string, sections ggui.Readable[[]spaceSection]) ggui.Widget {
	chips := ggui.Reactive(func() ggui.Widget {
		set := m.spaces()
		i := spaceIndex(set.Spaces, id)
		if i < 0 {
			return widget.Box()
		}
		var w []ggui.Widget
		for _, sec := range sections.Get() {
			if at, why := set.placeOf(sec.Key, sec.Owners); at == i+1 {
				w = append(w, spaceChip(m, set, i, sec, why))
			}
		}
		if len(w) == 0 {
			return muted(widget.Text(T("No projects. Drag one here from another space.")).Size(12))
		}
		return widget.Wrap(w...).Gap(6)
	})
	over := ggui.State(false)
	zone := widget.DropZone(chips, func(d widget.Dropped[spaceDrag]) { m.moveToSpace(id, d.Value.Key) }).
		Accept(func(d spaceDrag) bool { return d.From != id }).OnHover(over.Set)
	well := ggui.View(over, func(on bool) ggui.Widget {
		box := widget.Box(zone).Pad(10).Radius(8).Border(1, skin.Border)
		if on {
			box = box.Fill(skin.Surface3).Border(1.5, skin.Muted)
		}
		return box
	})
	return subhead(T("Projects"), T("Drag a project to another space, or move it from its menu."), well)
}

// spaceChip is a project in space i, which drags to another space and
// whose menu says why it is here and moves it to another.
func spaceChip(m *model, set spaceSet, i int, sec spaceSection, why sectionReason) ggui.Widget {
	sp, _ := set.spaceOf(i + 1)
	var reason string
	switch why {
	case byProject:
		reason = T("Moved here")
	case shownByOwner:
		var by []string
		for _, o := range sec.Owners {
			if slices.Contains(sp.Owners, o) {
				name, _ := ownerName(o)
				by = append(by, name)
			}
		}
		reason = Tf("Through %s", strings.Join(by, ", "))
	default:
		reason = T("The default space")
	}
	items := []ggui.Widget{ui.MenuLabel(reason), ui.MenuDivider()}
	for j, to := range set.Spaces {
		if j != i {
			items = append(items, ui.MenuItem(Tf("Move to %s", spaceLabel(to, j+1)), func() { m.moveToSpace(to.ID, sec.Key) }).Icon(spaceMark(to)))
		}
	}
	face := widget.Row(lookOf(set.Looks, sec.Key).glyph(13), widget.Text(sec.Name).Size(12.5).NoWrap().Ellipsis()).Gap(6).Align(widget.AlignCenter)
	chip := ui.MenuOf(widget.Box(face).MaxWidth(220), items...).Name(sec.Name)
	return widget.DragSource(chip, spaceDrag{Key: sec.Key, From: sp.ID})
}

// removableChip is a chip of label with a cross that runs remove.
func removableChip(label, name string, remove func()) ggui.Widget {
	return ui.ButtonOf(widget.Row(
		widget.Text(label).Size(12.5).NoWrap(),
		icon("x").Size(12).Color(skin.Muted),
	).Gap(6).Align(widget.AlignCenter), remove).Outline().Name(name)
}

// addMenu is a button of label with a plus that opens items.
func addMenu(label string, items []ggui.Widget) ggui.Widget {
	return ui.MenuOf(widget.Row(icon("plus").Size(13), widget.Text(label).Size(12.5).NoWrap()).Gap(6).Align(widget.AlignCenter), items...).Name(label)
}

// spaceField binds a field of the space of ID id, as prefField does a
// setting's.
func spaceField(m *model, id string, field func(*space) *string) ggui.Binding[string] {
	return ggui.Bind(func() string {
		l := m.Spaces.Get()
		if i := spaceIndex(l.Spaces, id); i >= 0 {
			return *field(&l.Spaces[i])
		}
		return ""
	}, func(v string) { m.updateSpace(id, func(sp *space) { *field(sp) = v }) })
}

// copySpacesMenu lists the nodes whose spaces can be copied: those of the
// mesh new enough to be asked for them.
func copySpacesMenu(m *model) ggui.Widget {
	return ggui.View(m.Nodes, func(nodes []*protocol.Node) ggui.Widget {
		var items []ggui.Widget
		for _, n := range nodes {
			if n.GetSource() == "self" || n.GetRevision() < protocol.RevisionWorkspaceCRDT {
				continue
			}
			id, name := n.GetId(), n.GetName()
			items = append(items, ui.MenuItem(name, func() {
				m.askConfirm(confirmation{
					Title:  Tf("Copy the spaces of %s?", name),
					Body:   T("These spaces, projects, names, looks and pins are replaced, here and on the nodes that follow this one."),
					Action: T("Copy"),
					Run:    func() { m.copySpaces(id, name) },
				})
			}))
		}
		if len(items) == 0 {
			return muted(widget.Text(T("No other node to copy from")).Size(12.5))
		}
		return ui.MenuOf(widget.Row(widget.Text(T("Choose a node")).Size(13), icon("chevron-down").Size(14).Color(skin.Muted)).Gap(8).Align(widget.AlignCenter),
			items...).Name(T("Copy from another node"))
	})
}

// copySpaces takes the spaces of the workspace node holds in place of
// these, as an edit of the window's, which the nodes that follow it take
// up in turn.
func (m *model) copySpaces(node, name string) {
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Workspace, error) {
		return c.WorkspaceOf(ctx, node)
	}, func(ws *protocol.Workspace, err error) {
		var doc workspaceDoc
		switch {
		case err != nil:
			m.toaster.Push(ui.Toast(Tf("Could not copy the spaces of %s", name), err.Error()).Destructive())
			return
		case shared.Empty(ws.GetState()) || json.Unmarshal(shared.View(ws.GetState()), &doc) != nil:
			m.toaster.Push(ui.Toast(Tf("Could not copy the spaces of %s", name), T("Its windows have kept no spaces.")).Destructive())
			return
		}
		m.Spaces.Set(doc.spaceList)
		m.migrateProjectKeys()
		m.toaster.Push(ui.Toast(Tf("Copied the spaces of %s", name), ""))
	})
}
