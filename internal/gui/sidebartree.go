package gui

import (
	"cmp"
	"fmt"
	"image/color"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/pathscope"
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

// scopeSync records the last (space, tab) synced to the sidebar's active project.
type scopeSync struct {
	space string
	tab   string
}

// projectTreeScrollKey identifies a space's project list scroll view across rebuilds.
type projectTreeScrollKey struct{ space int }

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

// rootScope is the scope a file root is in: its section, and its folder
// when the section is a project of several.
func rootScope(defs []project, r fileRoot) scopeKey {
	if pl := placeIn(defs, r.Node, r.Path, "", ""); pl.At >= 0 {
		k := scopeKey{Section: defs[pl.At].ID}
		if len(defs[pl.At].Roots) > 1 {
			k.Root = rootKey(defs[pl.At].Roots[pl.Root])
		}
		return k
	}
	return scopeKey{Section: sectionKey(defs, implicitKey(r.Node, r.Path, ""))}
}

// scopeLocation is the node and folder of the sidebar's selected project or
// sub-project in the space shown, or ("", "", false) when none is picked.
func (m *model) scopeLocation() (node, dir string, ok bool) {
	p := m.spaces()
	cur := m.Scope.Get()
	if cur.Section == "" || cur.Space != p.spaceID(p.Space) {
		return "", "", false
	}
	nodes := m.Nodes.Get()
	if i := projectByID(p.Projects, cur.Section); i >= 0 {
		g := projectGroup(p.Projects[i], nodes)
		if cur.Root != "" {
			if j := slices.IndexFunc(g.Def.Roots, func(r projectRoot) bool { return rootKey(r) == cur.Root }); j >= 0 {
				node, dir = m.rootLocation(g, g.Def.Roots[j])
				return node, dir, node != "" || dir != ""
			}
		}
		node, dir = m.groupLocation(g)
		return node, dir, node != "" || dir != ""
	}
	groups := groupProjects(m.Sessions.Get(), nodes, m.Prefs.Get().Projects, p.Projects, p.Names)
	if i := slices.IndexFunc(groups, func(g sessionGroup) bool { return g.Key == cur.Section }); i >= 0 {
		node, dir = m.groupLocation(groups[i])
		return node, dir, node != "" || dir != ""
	}
	return "", "", false
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
// of its project. If a sub-project (or project) is picked and the currently
// active tab is not in it, it activates an agent of that project if available.
func (m *model) setScope(k scopeKey) {
	m.clearSidebarPage()
	m.pickingScope = true
	defer func() {
		m.pickingScope = false
		p := m.spacesNow()
		m.lastScopeSync = scopeSync{space: p.spaceID(p.Space), tab: ggui.Peek(m.Selected)}
	}()
	p := m.spacesNow()
	m.Scope.Set(sidebarScope{Space: p.spaceID(p.Space), scopeKey: k})
	if k.Section != "" {
		m.showProjectTabs(k.Section)
		m.activateScopedSession(k)
	}
}

// activateScopedSession switches the active tab in the layout to an agent
// of scope k, if the active tab is not already in k and an agent of k exists.
func (m *model) activateScopedSession(k scopeKey) {
	if k.Section == "" {
		return
	}
	defs := m.spacesNow().Projects
	curTab := ggui.Peek(m.Selected)
	if s, ok := m.session(curTab); ok && k.holds(sessionScope(defs, s)) {
		return // already active in k
	}
	// 1. Look for a tab already in the layout that belongs to k.
	for _, g := range ggui.Peek(m.Layout).groups() {
		for _, id := range g.Tabs {
			if s, ok := m.session(id); ok && k.holds(sessionScope(defs, s)) {
				m.showTab(id)
				return
			}
		}
	}
	// 2. Look for any existing session of k.
	for _, s := range ggui.Peek(m.Sessions) {
		if k.holds(sessionScope(defs, s)) {
			m.showTab(s.GetId())
			return
		}
	}
}

// locationScope finds the deepest project section (and sub-project root for a
// multi-root project) in groups that holds from.
func locationScope(from fileRoot, groups []sessionGroup) (scopeKey, bool) {
	if from.Path == "" {
		return scopeKey{}, false
	}
	section, root, depth := -1, -1, -1
	holds := func(r fileRoot) bool {
		return r.Node == from.Node && pathscope.Within(from.Path, r.Path) && len(r.Path) > depth
	}
	for i, g := range groups {
		if g.multiRoot() {
			for j, pr := range g.Def.Roots {
				for _, l := range pr.Locations {
					if holds(l) {
						section, root, depth = i, j, len(l.Path)
					}
				}
			}
			continue
		}
		for _, r := range g.Roots {
			if holds(r) {
				section, root, depth = i, -1, len(r.Path)
			}
		}
	}
	if section < 0 {
		return scopeKey{}, false
	}
	g := groups[section]
	k := scopeKey{Section: g.Key}
	if root >= 0 && g.Def != nil && root < len(g.Def.Roots) {
		k.Root = rootKey(g.Def.Roots[root])
	}
	return k, true
}

// tabScopeInGroups resolves tab id (session, file, diff, or new-session tab) to
// its project or sub-project scope among groups.
func (m *model) tabScopeInGroups(id string, defs []project, groups []sessionGroup) (scopeKey, bool) {
	if id == "" || len(groups) == 0 {
		return scopeKey{}, false
	}
	switch {
	case isLaunchTab(id):
		l := m.launches[id]
		if l == nil {
			return scopeKey{}, false
		}
		f := ggui.Peek(l.Form)
		return locationScope(fileRoot{Node: f.NodeID, Path: f.Project}, groups)
	case isDocTab(id), isDiffTab(id), isPluginTab(id):
		docs, diffs, ptabs := ggui.Peek(m.Docs), ggui.Peek(m.Diffs), ggui.Peek(m.PluginTabs)
		if sec, root := tabPlace(id, groups, docs, diffs, ptabs); sec >= 0 {
			g := groups[sec]
			k := scopeKey{Section: g.Key}
			if root >= 0 && g.Def != nil && root < len(g.Def.Roots) {
				k.Root = rootKey(g.Def.Roots[root])
			}
			return k, true
		}
		if isDocTab(id) {
			if d, ok := docs[id]; ok {
				return locationScope(d.Root, groups)
			}
		}
		return scopeKey{}, false
	default:
		s, ok := m.session(id)
		if !ok {
			return scopeKey{}, false
		}
		k := sessionScope(defs, s)
		if !slices.ContainsFunc(groups, func(g sessionGroup) bool { return g.Key == k.Section }) {
			return scopeKey{}, false
		}
		return k, true
	}
}

// tabScope resolves tab id to its project or sub-project scope in spaceIdx.
func (m *model) tabScope(id string, spaceIdx int) (scopeKey, bool) {
	if id == "" {
		return scopeKey{}, false
	}
	p := m.spacesNow()
	groups := p.spaceGroups(spaceIdx, groupProjects(ggui.Peek(m.Sessions), ggui.Peek(m.Nodes), ggui.Peek(m.Prefs).Projects, p.Projects, p.Names))
	return m.tabScopeInGroups(id, p.Projects, groups)
}

// syncScopeToTab moves the left sidebar's active project (or sub-project) to
// match tab id in the space shown. When force is false, it skips work if
// (space, id) was already synced so background polls do not undo an explicit
// unscope or parent-project click in the sidebar.
func (m *model) syncScopeToTab(id string, force bool) {
	if m.pickingScope || id == "" {
		return
	}
	p := m.spacesNow()
	spaceID := p.spaceID(p.Space)
	syncKey := scopeSync{space: spaceID, tab: id}
	if !force && m.lastScopeSync == syncKey {
		return
	}
	k, ok := m.tabScope(id, p.Space)
	if !ok {
		return
	}
	m.lastScopeSync = syncKey
	want := sidebarScope{Space: spaceID, scopeKey: k}
	if ggui.Peek(m.Scope) != want {
		m.Scope.Set(want)
	}
}

// followScope moves the sidebar's active project in the space shown to the
// project (or sub-project) of session s.
func (m *model) followScope(s *protocol.SessionInfo) {
	if s == nil {
		return
	}
	m.syncScopeToTab(s.GetId(), true)
}

// pinnedScope reports whether the sidebar lists a project or folder first.
func (m *model) pinnedScope(k scopeKey) bool {
	return slices.Contains(ggui.Peek(m.Spaces).PinnedProjects, k)
}

// togglePinScope pins a project or folder to the top of the sidebar, or
// unpins it.
func (m *model) togglePinScope(k scopeKey) {
	m.Spaces.Update(func(l spaceList) spaceList {
		l.PinnedProjects = toggled(l.PinnedProjects, k)
		return l
	})
}

// spaceSections are the project sections of space i, by name.
func (m *model) spaceSections(st sidebarState, i int) []sessionGroup {
	p := m.spaces()
	return p.spaceGroups(i, groupProjects(m.Sessions.Get(), m.Nodes.Get(), st.kept.Get(), p.Projects, p.Names))
}

// setProjectMachine sets the machine filter for the sidebar's project list
// ("" for all machines).
func (m *model) setProjectMachine(nodeID string) {
	m.Prefs.Update(func(p prefs) prefs { p.ProjectMachine = nodeID; return p })
}

// groupHasMachine reports whether project section g has a folder or session on
// machine ("" matches every section).
func groupHasMachine(g sessionGroup, machine string) bool {
	if machine == "" || g.NodeID == machine {
		return true
	}
	for _, r := range g.Roots {
		if r.Node == machine {
			return true
		}
	}
	for _, s := range g.Sessions {
		if s.GetNodeId() == machine {
			return true
		}
	}
	return false
}

// rootHasMachine reports whether root j of project section g has a location or
// session on machine ("" matches every root).
func rootHasMachine(g sessionGroup, j int, r projectRoot, machine string) bool {
	if machine == "" {
		return true
	}
	for _, l := range r.Locations {
		if l.Node == machine {
			return true
		}
	}
	for _, s := range g.Sessions {
		if s.GetNodeId() == machine {
			if at, ok := g.RootOf[s.GetId()]; ok && at == j {
				return true
			}
		}
	}
	return false
}

// filterGroupsByMachine returns the project sections that belong to machine,
// preserving order without modifying the sections.
func filterGroupsByMachine(groups []sessionGroup, machine string) []sessionGroup {
	if machine == "" {
		return groups
	}
	var out []sessionGroup
	for _, g := range groups {
		if groupHasMachine(g, machine) {
			out = append(out, g)
		}
	}
	return out
}

// projectMachines returns the machine IDs offered in the project list's machine
// filter: those referenced by groups, known non-mobile nodes, and any active
// selection, with this machine first and the rest by name.
func projectMachines(groups []sessionGroup, nodes []*protocol.Node, selected string) []string {
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" {
			seen[id] = true
		}
	}
	for _, g := range groups {
		add(g.NodeID)
		for _, r := range g.Roots {
			add(r.Node)
		}
		for _, s := range g.Sessions {
			add(s.GetNodeId())
		}
	}
	for _, n := range nodes {
		if n.GetOs() == "android" || n.GetOs() == "ios" {
			continue
		}
		add(n.GetId())
	}
	add(selected)
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	slices.SortStableFunc(out, func(a, b string) int {
		na, _ := nodeIn(nodes, a)
		nb, _ := nodeIn(nodes, b)
		return cmp.Or(
			cmp.Compare(pick(na.GetSource() == "self", 0, 1), pick(nb.GetSource() == "self", 0, 1)),
			cmp.Compare(strings.ToLower(nodeName(nodes, a)), strings.ToLower(nodeName(nodes, b))),
			cmp.Compare(a, b),
		)
	})
	return out
}

// projectsPanel is the sidebar's project list in space i: the machine filter
// heading when several machines are known (or a filter is active), over the
// scrollable project tree.
func projectsPanel(m *model, st sidebarState, i int) ggui.Widget {
	head := ggui.Reactive(func() ggui.Widget {
		return projectMachineBar(m, m.spaceSections(st, i), m.Nodes.Get(), st.projectMachine.Get())
	})
	tree := widget.Scroll(widget.Padding(projectTree(m, st, i), 6, 8, 10, 8)).Bar(skin.Surface3).Key(projectTreeScrollKey{i})
	return widget.Column(head, widget.Expanded(tree)).Stretch()
}

// projectMachineBar is the heading above the project tree when more than one
// machine is available or a machine filter is active.
func projectMachineBar(m *model, allGroups []sessionGroup, nodes []*protocol.Node, machine string) ggui.Widget {
	machines := projectMachines(allGroups, nodes, machine)
	if len(machines) <= 1 && machine == "" {
		return widget.Box().Height(4)
	}
	shownCount := len(filterGroupsByMachine(allGroups, machine))
	items := make([]ggui.Widget, 0, len(machines)+2)
	items = append(items,
		ui.MenuItem(T("All machines"), func() { m.setProjectMachine("") }).
			Icon(icon("monitor").Size(13)).
			Checked(machine == "").
			Shortcut(fmt.Sprint(len(allGroups))),
		ui.MenuDivider(),
	)
	for _, id := range machines {
		nodeID := id
		n, _ := nodeIn(nodes, nodeID)
		count := len(filterGroupsByMachine(allGroups, nodeID))
		items = append(items, ui.MenuItem(nodeName(nodes, nodeID), func() { m.setProjectMachine(nodeID) }).
			Icon(icon(osIcon(n.GetOs())).Size(13)).
			Checked(machine == nodeID).
			Shortcut(fmt.Sprint(count)))
	}
	glyph, label := "monitor", T("All machines")
	glyphColor, textColor := color.Color(skin.Muted), color.Color(skin.Muted)
	if machine != "" {
		n, _ := nodeIn(nodes, machine)
		glyph, label = osIcon(n.GetOs()), nodeName(nodes, machine)
		glyphColor, textColor = skin.AccentBright, skin.Fg
	}
	labelText := widget.Text(label).Size(12).Color(textColor).NoWrap().Ellipsis()
	if machine != "" {
		labelText.Weight(ggui.WeightMedium)
	}
	face := widget.Box(widget.Row(
		icon(glyph).Size(12.5).Color(glyphColor),
		widget.Box(labelText).MaxWidth(120),
		icon("chevron-down").Size(10).Color(pick[color.Color](machine != "", skin.Muted, skin.ExtraMuted)),
	).Gap(4).Align(widget.AlignCenter)).Pad(0, 6).Height(24)
	if machine != "" {
		face = face.Fill(skin.Surface2).Radius(6)
	}
	menu := iconMenu(ui.MenuOf(face, items...).Name(T("Filter projects by machine")), 0)
	count := widget.Box(widget.Text(fmt.Sprint(shownCount)).Size(11).Color(skin.Muted).NoWrap()).Pad(1, 6).Radius(9).Fill(skin.Surface2)
	row := []ggui.Widget{
		widget.Text(T("Projects")).Size(13).Weight(ggui.WeightSemibold).NoWrap(),
		count,
		widget.Spacer(),
		menu,
	}
	if machine != "" {
		row = append(row, ui.Tooltip(
			ui.ButtonOf(icon("x").Size(12).Color(skin.Muted), func() { m.setProjectMachine("") }).
				Ghost().Pad(4).Radius(6).Name(T("Show all machines")),
			T("Show all machines"),
		))
	}
	return widget.Box(widget.Row(row...).Gap(4).Align(widget.AlignCenter)).Pad(8, 8, 2, 16)
}

// projectTree is the sidebar's projects in space i: those pinned, then
// every other one under its owner. A project pinned shows at the top as
// it would below, its folders under it; a folder pinned shows there on
// its own, and neither shows again below.
func projectTree(m *model, st sidebarState, i int) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		allGroups := m.spaceSections(st, i)
		nodes := m.Nodes.Get()
		machine := st.projectMachine.Get()
		groups := filterGroupsByMachine(allGroups, machine)
		scope := m.scopeIn(i)
		pins := st.pinnedProjects.Get()
		looks := m.spaces().Looks
		if len(allGroups) == 0 {
			empty := T("No projects yet. Add a folder with +.")
			if _, ok := m.spaces().spaceOf(i); ok {
				empty = T("No projects in this space. Add a folder with +, or move a project here from its menu.")
			}
			return widget.Box(muted(widget.Text(empty).Size(12))).Pad(8)
		}
		if len(groups) == 0 {
			return widget.Box(widget.Column(
				muted(widget.Text(Tf("No projects on %s.", nodeName(nodes, machine))).Size(12)),
				widget.Align(ui.ButtonOf(widget.Text(T("Show all machines")).Size(12).Color(skin.Fg).NoWrap(), func() { m.setProjectMachine("") }).Outline().Pad(4, 8).Radius(6).Name(T("Show all machines"))).Left(),
			).Gap(6)).Pad(8)
		}
		labels := projectLabels(groups, nodes)
		var spColor color.Color
		if sp, ok := m.spaces().spaceOf(i); ok {
			spColor = sp.color()
		}
		tree := treeRows{m: m, space: i, scope: scope, pins: pins, looks: looks, labels: labels, spaceColor: spColor, machine: machine}
		var rows []ggui.Widget
		if pinned := tree.pinnedRows(groups); len(pinned) > 0 {
			rows = append(rows, widget.Column(append([]ggui.Widget{treeHeading(icon("pin").Size(13), T("Pinned"), nil)}, pinned...)...).Gap(1).Stretch(),
				widget.Box(hline()).Pad(6, 0, 8, 0))
		}
		sections := byOwner(groups)
		if sections == nil {
			sections = []ownerSection{{Groups: groups}}
		}
		for _, o := range sections {
			var under []ggui.Widget
			for _, g := range o.Groups {
				if !tree.pinned(scopeKey{Section: g.Key}) {
					under = append(under, tree.project(g)...)
				}
			}
			if o.Owner != "" && len(under) > 0 {
				rows = append(rows, ownerHeading(m, o))
			}
			rows = append(rows, under...)
		}
		return widget.Column(rows...).Gap(1).Stretch()
	})
}

// treeRows draws the rows of the sidebar's projects.
type treeRows struct {
	m          *model
	space      int
	scope      scopeKey
	pins       []scopeKey
	looks      map[string]projectLook
	labels     map[string]string
	spaceColor color.Color
	machine    string
}

// pinned reports whether k is pinned.
func (t treeRows) pinned(k scopeKey) bool { return slices.Contains(t.pins, k) }

// name is how section g is named in the tree.
func (t treeRows) name(g sessionGroup) string { return cmp.Or(g.Name, t.labels[g.Key], groupName(g)) }

// treeHeading is a quiet heading in the tree: an owner's, or the pins'.
func treeHeading(lead ggui.Widget, name string, end ggui.Widget) ggui.Widget {
	row := []ggui.Widget{lead, widget.Expanded(widget.Align(widget.Text(name).Size(12).Weight(ggui.WeightMedium).Color(skin.Muted).NoWrap().Ellipsis()).Left())}
	if end != nil {
		row = append(row, end)
	}
	return widget.Box(widget.Row(row...).Gap(8).Align(widget.AlignCenter)).Pad(6, 8, 6, 8)
}

// ownerHeading names the owner of the projects under it: the name, and
// quietly the service it is on. Its menu takes the owner's folders off the
// sidebar, or puts them in a space.
func ownerHeading(m *model, o ownerSection) ggui.Widget {
	name, host := ownerName(o.Owner)
	var end ggui.Widget
	if host != "" {
		end = widget.Text(host).Size(11.5).Color(skin.ExtraMuted).NoWrap()
	}
	head := treeHeading(icon(ownerGlyph(host)).Size(14), name, end)
	return ui.ContextMenuFunc(head, func() []ggui.Widget {
		return append(ownerMenu(m, o, name), ownerSpaceItems(m, o, name)...)
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
	title := []ggui.Widget{widget.Expanded(widget.Align(widget.Text(name).Size(14).
		Weight(pick(t.scope.Section == g.Key, ggui.WeightSemibold, ggui.WeightMedium)).
		Color(pick[color.Color](t.scope.Section == g.Key, skin.Fg, skin.Muted)).NoWrap().Ellipsis()).Left())}
	if working(g.Sessions) {
		title = append(title, workingDot())
	}
	if len(g.Sessions) > 0 {
		title = append(title, kindBadges(g.Sessions))
	}
	btn := ui.ButtonOf(widget.Row(title...).Gap(6).Align(widget.AlignCenter), func() { m.pickScope(key) }).
		Ghost().Pad(6, 8, 6, 4).Radius(7).Name(name)
	cells := []ggui.Widget{
		lookButton(m, g.Key, name, look),
		widget.Expanded(btn),
		pinButton(m, key, name, hover),
	}
	if !g.multiRoot() {
		cells = append(cells, newSessionButton(name, hover, func() { m.openNew(m.groupLocation(g)) }))
	}
	row := widget.Row(cells...).Gap(0).Align(widget.AlignCenter)
	var w ggui.Widget = t.selectedRow(row, t.scope == key)
	w = ui.ContextMenuFunc(w, func() []ggui.Widget { return projectRowMenu(m, g, name, key) }).Name(Tf("%s actions", name))
	// A session dropped on the row starts again there; a project dropped
	// on it merges there.
	w = projectDragged(m, sectionDrop(m, w, g.NodeID, g.Project), g)
	rows := []ggui.Widget{widget.Pointer(w).OnHover(hover.Set)}
	if !g.multiRoot() {
		return rows
	}
	// A folder pinned shows with the pins, unless the project is pinned
	// too and shows there whole.
	var matching []int
	for j, r := range g.Def.Roots {
		if rootHasMachine(g, j, r, t.machine) {
			matching = append(matching, j)
		}
	}
	if len(matching) == 0 {
		for j := range g.Def.Roots {
			matching = append(matching, j)
		}
	}
	whole := t.pinned(key)
	var shown []int
	for _, j := range matching {
		if whole || !t.pinned(scopeKey{Section: g.Key, Root: rootKey(g.Def.Roots[j])}) {
			shown = append(shown, j)
		}
	}
	for n, j := range shown {
		rows = append(rows, t.folder(g, j, g.Def.Roots[j], n == len(shown)-1))
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
		widget.Expanded(widget.Align(widget.Text(label).Size(13.5).Color(pick[color.Color](selected, skin.Fg, skin.Muted)).NoWrap().Ellipsis()).Left()),
	}
	if working(in) {
		title = append(title, workingDot())
	}
	title = append(title, widget.Text(fmt.Sprint(len(in))).Size(12).Color(skin.ExtraMuted).NoWrap())
	btn := ui.ButtonOf(widget.Row(title...).Gap(6).Align(widget.AlignCenter), func() { m.pickScope(key) }).
		Ghost().Pad(5, 8, 5, 6).Radius(7).Name("#" + label)
	row := t.selectedRow(widget.Row(
		widget.Expanded(btn),
		pinButton(m, key, "#"+label, hover),
		newSessionButton("#"+label, hover, func() { m.openNewInRoot(g, r) }),
	).Align(widget.AlignCenter), selected)
	var w ggui.Widget = &treeBranch{child: row, last: last}
	w = ui.ContextMenuFunc(w, func() []ggui.Widget {
		return folderRowMenu(m, g, j, r)
	}).Name(Tf("%s actions", "#"+label))
	return widget.Pointer(w).OnHover(hover.Set)
}

// pinnedRows are the projects and folders pinned, in the order pinned, of
// those in groups: a project as the tree shows it, its folders under it,
// and a folder on its own, unless its project is pinned and shows it.
func (t treeRows) pinnedRows(groups []sessionGroup) []ggui.Widget {
	m := t.m
	var rows []ggui.Widget
	for _, k := range t.pins {
		i := slices.IndexFunc(groups, func(g sessionGroup) bool { return g.Key == k.Section })
		if i < 0 {
			continue
		}
		g := groups[i]
		if k.Root == "" {
			rows = append(rows, t.project(g)...)
			continue
		}
		if t.pinned(scopeKey{Section: g.Key}) {
			continue
		}
		j := -1
		if g.Def != nil {
			j = slices.IndexFunc(g.Def.Roots, func(r projectRoot) bool { return rootKey(r) == k.Root })
		}
		if j < 0 || !rootHasMachine(g, j, g.Def.Roots[j], t.machine) {
			continue
		}
		var sessions []*protocol.SessionInfo
		for _, s := range g.Sessions {
			if at, ok := g.RootOf[s.GetId()]; ok && at == j {
				sessions = append(sessions, s)
			}
		}
		r := g.Def.Roots[j]
		name, parent := r.label(), t.name(g)
		hover := ggui.State(false)
		row := []ggui.Widget{
			hashMark(16),
			widget.Text(name).Size(14).Color(pick[color.Color](t.scope == k, skin.Fg, skin.Muted)).NoWrap(),
			widget.Expanded(widget.Align(widget.Text(parent).Size(12).Color(skin.ExtraMuted).NoWrap().Ellipsis()).Left()),
		}
		if working(sessions) {
			row = append(row, workingDot())
		}
		row = append(row, widget.Text(fmt.Sprint(len(sessions))).Size(12).Color(skin.ExtraMuted).NoWrap())
		label := parent + " / " + name
		btn := ui.ButtonOf(widget.Row(row...).Gap(8).Align(widget.AlignCenter), func() { m.pickScope(k) }).
			Ghost().Pad(6, 8).Radius(7).Name(label)
		var w ggui.Widget = t.selectedRow(widget.Row(
			widget.Expanded(btn),
			pinButton(m, k, label, ggui.Const(true)),
			newSessionButton(label, hover, func() { m.openNewInRoot(g, r) }),
		).Align(widget.AlignCenter), t.scope == k)
		w = ui.ContextMenuFunc(w, func() []ggui.Widget {
			return folderRowMenu(m, g, j, r)
		}).Name(Tf("%s actions", label))
		rows = append(rows, widget.Pointer(w).OnHover(hover.Set))
	}
	return rows
}

// selectedRow marks a row of the tree as the scope picked and scrolls it into
// view once whenever the active scope changes.
func (t treeRows) selectedRow(row ggui.Widget, selected bool) ggui.Widget {
	if !selected {
		return widget.Box(row).Border(1, color.Transparent).Radius(7)
	}
	var box ggui.Widget
	var spNRGBA color.NRGBA
	if t.spaceColor != nil {
		spNRGBA = color.NRGBAModel.Convert(t.spaceColor).(color.NRGBA)
	}
	if spNRGBA.A > 0 {
		fill := color.NRGBA{R: spNRGBA.R, G: spNRGBA.G, B: spNRGBA.B, A: 0x3c}
		border := color.NRGBA{R: spNRGBA.R, G: spNRGBA.G, B: spNRGBA.B, A: 0x70}
		box = widget.Box(row).Fill(fill).Border(1, border).Radius(7)
	} else {
		box = widget.Box(row).Fill(skin.SidebarSel).Border(1, color.Transparent).Radius(7)
	}
	scope := t.scope
	return &paintHook{child: box, fn: func(dst *ggui.Canvas, r ggui.Rect) {
		if t.m.revealedScope == nil {
			t.m.revealedScope = map[int]scopeKey{}
		}
		if t.m.revealedScope[t.space] != scope {
			dst.RequestReveal(r)
			t.m.revealedScope[t.space] = scope
		}
	}}
}

// projectRowMenu is a project row's menu: starting a session in it,
// opening its folder, its pin, and the project's own entries.
func projectRowMenu(m *model, g sessionGroup, name string, key scopeKey) []ggui.Widget {
	items := []ggui.Widget{ui.MenuItem(T("New session here"), func() { m.openNew(m.groupLocation(g)) })}
	if len(g.Roots) > 1 {
		items = append(items, ui.MenuSub(T("New session in…"), startItems(m, g, ggui.Peek(m.Nodes))...))
	}
	if item := projectLocationItem(m, g); item != nil {
		items = append(items, item)
	}
	items = append(items, pinItem(m, key), ui.MenuDivider())
	return append(items, projectMenu(m, g, name)...)
}

// folderRowMenu is a sub-project row's context menu.
func folderRowMenu(m *model, g sessionGroup, j int, r projectRoot) []ggui.Widget {
	label := r.label()
	key := scopeKey{Section: g.Key, Root: rootKey(r)}
	items := []ggui.Widget{pinItem(m, key)}
	for _, l := range r.Locations {
		items = append(items, ui.MenuItem(Tf("New session on %s", nodeName(ggui.Peek(m.Nodes), l.Node)), func() { m.openNew(l.Node, l.Path) }))
	}
	if node, path := m.rootLocation(g, r); path != "" {
		items = append(items, openLocationItem(m, node, path))
	}
	return append(items, ui.MenuDivider(), ui.MenuItem(Tf("Take %s out of the project", label), func() { m.removeRoot(g, j) }))
}

// rootLocation picks where a sub-project root lives: on this machine when
// it is here, else its first location, else the section's own.
func (m *model) rootLocation(g sessionGroup, r projectRoot) (node, path string) {
	if k := slices.IndexFunc(r.Locations, func(f fileRoot) bool { return m.onThisMachine(f.Node) && f.Path != "" }); k >= 0 {
		return r.Locations[k].Node, r.Locations[k].Path
	}
	if len(r.Locations) > 0 {
		return r.Locations[0].Node, r.Locations[0].Path
	}
	return m.groupLocation(g)
}

// groupLocation picks where a project section starts a new session by
// default: on this machine when any of its folders is here (preferring the
// first root's local folder), else the section's default location.
func (m *model) groupLocation(g sessionGroup) (node, path string) {
	if g.Def != nil && len(g.Def.Roots) > 0 {
		if k := slices.IndexFunc(g.Def.Roots[0].Locations, func(f fileRoot) bool { return m.onThisMachine(f.Node) && f.Path != "" }); k >= 0 {
			return g.Def.Roots[0].Locations[k].Node, g.Def.Roots[0].Locations[k].Path
		}
	}
	if k := slices.IndexFunc(g.Roots, func(f fileRoot) bool { return m.onThisMachine(f.Node) && f.Path != "" }); k >= 0 {
		return g.Roots[k].Node, g.Roots[k].Path
	}
	return g.NodeID, g.Project
}

// openNewInRoot opens the new-session tab for a merged project's sub-project.
func (m *model) openNewInRoot(g sessionGroup, r projectRoot) {
	m.openNew(m.rootLocation(g, r))
}

// openLocationItem opens path in the desktop's file manager when on this
// machine, or in mako's remote file browser otherwise.
func openLocationItem(m *model, node, path string) ggui.Widget {
	if m.onThisMachine(node) {
		return ui.MenuItem(fileManagerLabel(), func() { _ = runtime.OpenURL(path) })
	}
	return ui.MenuItem(Tf("Show in %s's files", nodeName(ggui.Peek(m.Nodes), node)), func() {
		m.openBrowser(node, path, "")
	})
}

// projectLocationItem is a project row's menu item (or submenu for a
// merged project) that opens its folder in the desktop's file manager.
func projectLocationItem(m *model, g sessionGroup) ggui.Widget {
	if g.multiRoot() {
		var sub []ggui.Widget
		for _, r := range g.Def.Roots {
			node, path := m.rootLocation(g, r)
			if path == "" {
				continue
			}
			if m.onThisMachine(node) {
				sub = append(sub, ui.MenuItem(r.label(), func() { _ = runtime.OpenURL(path) }))
			} else {
				sub = append(sub, ui.MenuItem(Tf("%s on %s", r.label(), nodeName(ggui.Peek(m.Nodes), node)), func() {
					m.openBrowser(node, path, "")
				}))
			}
		}
		if len(sub) == 0 {
			return nil
		}
		return ui.MenuSub(fileManagerLabel(), sub...)
	}
	node, path := g.NodeID, g.Project
	if k := slices.IndexFunc(g.Roots, func(r fileRoot) bool { return m.onThisMachine(r.Node) && r.Path != "" }); k >= 0 {
		node, path = g.Roots[k].Node, g.Roots[k].Path
	}
	if path == "" {
		return nil
	}
	return openLocationItem(m, node, path)
}

// newSessionButton starts a new session in a project or sub-project row,
// shown while the pointer is over the row.
func newSessionButton(name string, hover ggui.Readable[bool], run func()) ggui.Widget {
	return ggui.View(hover, func(h bool) ggui.Widget {
		if !h {
			return widget.Box()
		}
		label := Tf("New session in %s", name)
		return ui.Tooltip(ui.ButtonOf(icon("plus").Size(13).Color(skin.Muted), run).
			Ghost().Pad(5).Radius(6).Name(label), label)
	})
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
			return widget.Box().Size(23, 23)
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
func workingDot() ggui.Widget { return widget.Box().Size(6, 6).Radius(3).Fill(skin.DotRunning) }

// hashMark is the # a project's folder is marked with.
func hashMark(size float64) ggui.Widget {
	return widget.Text("#").Size(size).Weight(ggui.WeightSemibold).Color(skin.ExtraMuted).NoWrap()
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
