package gui

import (
	"cmp"
	"fmt"
	"image/color"
	"slices"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// The sidebar's projects: the project sections of the space shown, under
// their owners, each with the icon the user gave it, and the folders of a
// project of several hanging from it on a dotted line, marked #. Nothing
// folds, and no sessions show here: picking a project or one of its
// folders scopes the agents below to it (sidebaragents.go), and shows the
// project's tabs. Projects and folders pinned show first.

// scopeKey names what the sidebar's projects pick for its agents: a
// project section, by its ID, or a folder of one, by rootKey; zero for
// every section of the space.
type scopeKey struct {
	Section string `json:"section"`
	Root    string `json:"root,omitempty"`
}

// sidebarScope is the scope picked, in the space it was picked in, by ID.
type sidebarScope struct {
	Space string
	scopeKey
}

// rootKey names a project's folder: its repository, else where it first
// is.
func rootKey(r projectRoot) string {
	switch {
	case r.Remote != "":
		return r.Remote
	case len(r.Locations) > 0:
		return dirKey(r.Locations[0].Node, r.Locations[0].Path)
	}
	return r.Label
}

// sessionScope is the scope a session is in: its section, and its folder
// when the section is a project of several.
func sessionScope(defs []project, s *protocol.SessionInfo) scopeKey {
	if pl := placeSession(defs, s); pl.At >= 0 {
		k := scopeKey{Section: defs[pl.At].ID}
		if len(defs[pl.At].Roots) > 1 {
			k.Root = rootKey(defs[pl.At].Roots[pl.Root])
		}
		return k
	}
	return scopeKey{Section: sectionID(defs, s)}
}

// holds reports whether scope k takes in what is in scope in.
func (k scopeKey) holds(in scopeKey) bool {
	return k.Section == "" || k.Section == in.Section && (k.Root == "" || k.Root == in.Root)
}

// scopeIn is the scope picked for space i, zero when none was picked
// there.
func (m *model) scopeIn(i int) scopeKey {
	s := m.Scope.Get()
	if s.Space != m.spaces().spaceID(i) {
		return scopeKey{}
	}
	return s.scopeKey
}

// pickScope scopes the agents to k in the space shown, and shows the
// tabs of its project; picking the scope shown again shows every agent of
// the space.
func (m *model) pickScope(k scopeKey) {
	p := m.spacesNow()
	id := p.spaceID(p.Space)
	if cur := ggui.Peek(m.Scope); cur.Space == id && cur.scopeKey == k {
		k = scopeKey{}
	}
	m.setScope(k)
}

// setScope scopes the agents to k in the space shown, and shows the tabs
// of its project.
func (m *model) setScope(k scopeKey) {
	p := m.spacesNow()
	m.Scope.Set(sidebarScope{Space: p.spaceID(p.Space), scopeKey: k})
	if k.Section != "" {
		m.showProjectTabs(k.Section)
	}
}

// followScope moves a scope picked in the space shown to the project of
// session s, picked elsewhere, so the agents and the tabs shown are of one
// project. Every agent of the space stays so.
func (m *model) followScope(s *protocol.SessionInfo) {
	cur := ggui.Peek(m.Scope)
	p := m.spacesNow()
	if cur.Section == "" || cur.Space != p.spaceID(p.Space) {
		return
	}
	if in := sessionScope(p.Projects, s); !cur.holds(in) {
		m.Scope.Set(sidebarScope{Space: cur.Space, scopeKey: scopeKey{Section: in.Section}})
	}
}

// pinnedScope reports whether the sidebar lists a project or folder first.
func (m *model) pinnedScope(k scopeKey) bool {
	return slices.Contains(ggui.Peek(m.Prefs).PinnedProjects, k)
}

// togglePinScope pins a project or folder to the top of the sidebar, or
// unpins it.
func (m *model) togglePinScope(k scopeKey) {
	m.Prefs.Update(func(p prefs) prefs {
		if i := slices.Index(p.PinnedProjects, k); i >= 0 {
			p.PinnedProjects = slices.Delete(slices.Clone(p.PinnedProjects), i, i+1)
		} else {
			p.PinnedProjects = append(slices.Clone(p.PinnedProjects), k)
		}
		return p
	})
}

// spaceSections are the project sections of space i, by name.
func (m *model) spaceSections(st sidebarState, i int) []sessionGroup {
	p := m.spaces()
	return p.spaceGroups(i, groupProjects(m.Sessions.Get(), m.Nodes.Get(), st.kept.Get(), p.Projects, p.Names))
}

// projectTree is the sidebar's projects in space i: those pinned, then
// every one under its owner.
func projectTree(m *model, st sidebarState, i int) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		groups := m.spaceSections(st, i)
		nodes := m.Nodes.Get()
		scope := m.scopeIn(i)
		pins := st.pinnedProjects.Get()
		looks := m.spaces().Looks
		if len(groups) == 0 {
			return ggui.Box(muted(ggui.Text(T("No projects yet. Add a folder with +.")).Size(12))).Pad(8)
		}
		labels := projectLabels(groups, nodes)
		tree := treeRows{m: m, scope: scope, pins: pins, looks: looks, labels: labels}
		var rows []ggui.Widget
		if pinned := tree.pinnedRows(groups); len(pinned) > 0 {
			rows = append(rows, ggui.Column(append([]ggui.Widget{treeHeading(icon("pin").Size(13), T("Pinned"), nil)}, pinned...)...).Gap(1).Stretch(),
				ggui.Box(hline()).Pad(6, 0, 8, 0))
		}
		sections := byOwner(groups)
		if sections == nil {
			sections = []ownerSection{{Groups: groups}}
		}
		for _, o := range sections {
			if o.Owner != "" {
				rows = append(rows, ownerHeading(m, o))
			}
			for _, g := range o.Groups {
				rows = append(rows, tree.project(g)...)
			}
		}
		return ggui.Column(rows...).Gap(1).Stretch()
	})
}

// treeRows draws the rows of the sidebar's projects.
type treeRows struct {
	m      *model
	scope  scopeKey
	pins   []scopeKey
	looks  map[string]projectLook
	labels map[string]string
}

// name is how section g is named in the tree.
func (t treeRows) name(g sessionGroup) string { return cmp.Or(g.Name, t.labels[g.Key], groupName(g)) }

// treeHeading is a quiet heading in the tree: an owner's, or the pins'.
func treeHeading(lead ggui.Widget, name string, end ggui.Widget) ggui.Widget {
	row := []ggui.Widget{lead, ggui.Expanded(ggui.Align(ggui.Text(name).Size(12).Weight(ggui.WeightMedium).Color(skin.Muted).NoWrap().Ellipsis()).Left())}
	if end != nil {
		row = append(row, end)
	}
	return ggui.Box(ggui.Row(row...).Gap(8).Align(ggui.AlignCenter)).Pad(6, 8, 6, 8)
}

// ownerHeading names the owner of the projects under it: the name, and
// quietly the service it is on. Its menu takes the owner's folders off the
// sidebar, or puts them in a space.
func ownerHeading(m *model, o ownerSection) ggui.Widget {
	name, host := ownerName(o.Owner)
	var end ggui.Widget
	if host != "" {
		end = ggui.Text(host).Size(11.5).Color(skin.ExtraMuted).NoWrap()
	}
	head := treeHeading(icon(ownerGlyph(host)).Size(14), name, end)
	return ui.ContextMenuFunc(head, func() []ggui.Widget {
		return append(ownerMenu(m, o, name), ownerSpaceItems(m, o.Owner, name)...)
	}).Name(Tf("%s actions", name))
}

// project is section g's row, and under it, for a project of several
// folders, a row for each.
func (t treeRows) project(g sessionGroup) []ggui.Widget {
	m := t.m
	name := t.name(g)
	key := scopeKey{Section: g.Key}
	look := lookOf(t.looks, g.Key)
	hover := ggui.State(false)
	title := []ggui.Widget{ggui.Expanded(ggui.Align(ggui.Text(name).Size(14).
		Weight(pick(t.scope.Section == g.Key, ggui.WeightSemibold, ggui.WeightMedium)).NoWrap().Ellipsis()).Left())}
	if working(g.Sessions) {
		title = append(title, workingDot())
	}
	if len(g.Sessions) > 0 {
		title = append(title, kindBadges(g.Sessions))
	}
	btn := ui.ButtonOf(ggui.Row(title...).Gap(6).Align(ggui.AlignCenter), func() { m.pickScope(key) }).
		Ghost().Pad(6, 8, 6, 4).Radius(7).Name(name)
	row := ggui.Row(
		lookButton(m, g.Key, name, look),
		ggui.Expanded(btn),
		pinButton(m, key, name, hover),
	).Gap(0).Align(ggui.AlignCenter)
	var w ggui.Widget = selectedRow(row, t.scope == key)
	w = ui.ContextMenuFunc(w, func() []ggui.Widget { return projectRowMenu(m, g, name, key) }).Name(Tf("%s actions", name))
	// A session dropped on the row starts again there; a project dropped
	// on it merges there.
	w = projectDragged(m, sectionDrop(m, w, g.NodeID, g.Project), g)
	rows := []ggui.Widget{ggui.Pointer(w).OnHover(hover.Set)}
	if !g.multiRoot() {
		return rows
	}
	for j, r := range g.Def.Roots {
		rows = append(rows, t.folder(g, j, r, j == len(g.Def.Roots)-1))
	}
	return rows
}

// folder is the row of root j of project section g, hanging from the
// project's on a dotted line, the last's ending there.
func (t treeRows) folder(g sessionGroup, j int, r projectRoot, last bool) ggui.Widget {
	m := t.m
	key := scopeKey{Section: g.Key, Root: rootKey(r)}
	var in []*protocol.SessionInfo
	for _, s := range g.Sessions {
		if at, ok := g.RootOf[s.GetId()]; ok && at == j {
			in = append(in, s)
		}
	}
	label := r.label()
	hover := ggui.State(false)
	selected := t.scope == key
	title := []ggui.Widget{
		hashMark(14),
		ggui.Expanded(ggui.Align(ggui.Text(label).Size(13.5).Color(pick[color.Color](selected, skin.Fg, skin.Muted)).NoWrap().Ellipsis()).Left()),
	}
	if working(in) {
		title = append(title, workingDot())
	}
	title = append(title, ggui.Text(fmt.Sprint(len(in))).Size(12).Color(skin.ExtraMuted).NoWrap())
	btn := ui.ButtonOf(ggui.Row(title...).Gap(6).Align(ggui.AlignCenter), func() { m.pickScope(key) }).
		Ghost().Pad(5, 8, 5, 6).Radius(7).Name("#" + label)
	row := selectedRow(ggui.Row(ggui.Expanded(btn), pinButton(m, key, "#"+label, hover)).Align(ggui.AlignCenter), selected)
	var w ggui.Widget = &treeBranch{child: row, last: last}
	w = ui.ContextMenuFunc(w, func() []ggui.Widget {
		items := []ggui.Widget{pinItem(m, key)}
		for _, l := range r.Locations {
			items = append(items, ui.MenuItem(Tf("New session on %s", nodeName(ggui.Peek(m.Nodes), l.Node)), func() { m.openNew(l.Node, l.Path) }))
		}
		return append(items, ui.MenuDivider(), ui.MenuItem(Tf("Take %s out of the project", label), func() { m.removeRoot(g, j) }))
	}).Name(Tf("%s actions", "#"+label))
	return ggui.Pointer(w).OnHover(hover.Set)
}

// pinnedRows are the projects and folders pinned, in the order pinned, of
// those in groups.
func (t treeRows) pinnedRows(groups []sessionGroup) []ggui.Widget {
	m := t.m
	var rows []ggui.Widget
	for _, k := range t.pins {
		i := slices.IndexFunc(groups, func(g sessionGroup) bool { return g.Key == k.Section })
		if i < 0 {
			continue
		}
		g := groups[i]
		name, parent, sessions := t.name(g), "", g.Sessions
		lead := lookOf(t.looks, g.Key).glyph(16)
		if k.Root != "" {
			j := -1
			if g.Def != nil {
				j = slices.IndexFunc(g.Def.Roots, func(r projectRoot) bool { return rootKey(r) == k.Root })
			}
			if j < 0 {
				continue
			}
			sessions = nil
			for _, s := range g.Sessions {
				if at, ok := g.RootOf[s.GetId()]; ok && at == j {
					sessions = append(sessions, s)
				}
			}
			name, parent, lead = g.Def.Roots[j].label(), name, hashMark(16)
		}
		row := []ggui.Widget{
			lead,
			ggui.Text(name).Size(14).NoWrap(),
			ggui.Expanded(ggui.Align(ggui.Text(parent).Size(12).Color(skin.ExtraMuted).NoWrap().Ellipsis()).Left()),
		}
		if working(sessions) {
			row = append(row, workingDot())
		}
		if k.Root != "" {
			row = append(row, ggui.Text(fmt.Sprint(len(sessions))).Size(12).Color(skin.ExtraMuted).NoWrap())
		}
		label := cmp.Or(parent+" / ", "") + name
		btn := ui.ButtonOf(ggui.Row(row...).Gap(8).Align(ggui.AlignCenter), func() { m.pickScope(k) }).
			Ghost().Pad(6, 8).Radius(7).Name(label)
		rows = append(rows, selectedRow(ggui.Row(ggui.Expanded(btn), pinButton(m, k, label, ggui.Const(true))).Align(ggui.AlignCenter), t.scope == k))
	}
	return rows
}

// selectedRow marks a row of the tree as the scope picked.
func selectedRow(row ggui.Widget, selected bool) ggui.Widget {
	if selected {
		return ggui.Box(row).Fill(skin.SidebarSel).Radius(7)
	}
	return ggui.Box(row).Radius(7)
}

// projectRowMenu is a project row's menu: starting a session in it, its
// pin, and the project's own entries.
func projectRowMenu(m *model, g sessionGroup, name string, key scopeKey) []ggui.Widget {
	items := []ggui.Widget{ui.MenuItem(T("New session here"), func() { m.openNew(g.NodeID, g.Project) })}
	if len(g.Roots) > 1 {
		items = append(items, ui.MenuSub(T("New session in…"), startItems(m, g, ggui.Peek(m.Nodes))...))
	}
	items = append(items, pinItem(m, key), ui.MenuDivider())
	return append(items, projectMenu(m, g, name)...)
}

// pinItem pins or unpins a project or folder, from its menu.
func pinItem(m *model, k scopeKey) ggui.Widget {
	return ui.MenuItem(pick(m.pinnedScope(k), T("Unpin"), T("Pin to top")), func() { m.togglePinScope(k) })
}

// pinButton pins what a row is to the top, or unpins it: shown while the
// pointer is over the row, and always, in amber, once pinned.
func pinButton(m *model, k scopeKey, name string, hover ggui.Readable[bool]) ggui.Widget {
	on := m.pinnedScope(k)
	return ggui.View(hover, func(h bool) ggui.Widget {
		if !h && !on {
			return ggui.Box().Size(23, 23)
		}
		label := pick(on, Tf("Unpin %s", name), Tf("Pin %s to the top", name))
		c := pick[color.Color](on, skin.DotWaiting, skin.Muted)
		return ui.Tooltip(ui.ButtonOf(icon("pin").Size(13).Color(c), func() { m.togglePinScope(k) }).
			Ghost().Pad(5).Radius(6).Name(label), label)
	})
}

// working reports whether any of sessions has its agent at work.
func working(sessions []*protocol.SessionInfo) bool {
	return slices.ContainsFunc(sessions, func(s *protocol.SessionInfo) bool { return protocol.AtWork(s.GetStatus()) })
}

// workingDot marks a project or folder with an agent at work.
func workingDot() ggui.Widget { return ggui.Box().Size(6, 6).Radius(3).Fill(skin.DotRunning) }

// hashMark is the # a project's folder is marked with.
func hashMark(size float64) ggui.Widget {
	return ggui.Text("#").Size(size).Weight(ggui.WeightSemibold).Color(skin.ExtraMuted).NoWrap()
}

// treeBranch is a folder's row hanging from its project's on a dotted
// line: down the row, or to its middle for the last, and across to it.
type treeBranch struct {
	child ggui.Widget
	last  bool
	size  ggui.Size
}

// The line runs treeTrunk in from the row's start, and the row starts
// treeIndent in; treeDash is a dash of the line, and the gap after it.
const (
	treeTrunk  = 15
	treeIndent = treeTrunk + 14
	treeDash   = 3
)

func (t *treeBranch) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	inner := c
	inner.MaxW = max(c.MaxW-treeIndent, 0)
	inner.MinW = max(c.MinW-treeIndent, 0)
	t.size = t.child.Layout(inner, env)
	return c.Constrain(ggui.Sz(t.size.W+treeIndent, t.size.H))
}

func (t *treeBranch) Paint(dst *ggui.Canvas, r ggui.Rect) {
	x, mid := r.Origin.X+treeTrunk, r.Origin.Y+r.Size.H/2
	end := r.Origin.Y + r.Size.H
	if t.last {
		end = mid
	}
	for y := r.Origin.Y; y < end; y += 2 * treeDash {
		dst.FillRect(ggui.Rct(ggui.Pt(x, y), ggui.Sz(1, min(treeDash, end-y))), skin.Surface3)
	}
	for dx := 0.0; dx < 10; dx += 2 * treeDash {
		dst.FillRect(ggui.Rct(ggui.Pt(x+dx, mid), ggui.Sz(min(treeDash, 10-dx), 1)), skin.Surface3)
	}
	dst.Paint(t.child, ggui.Rct(ggui.Pt(r.Origin.X+treeIndent, r.Origin.Y), t.size))
}
