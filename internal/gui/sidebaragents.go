package gui

import (
	"cmp"
	"fmt"
	"image/color"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// The sidebar's agents: the sessions of the project or folder picked
// above (sidebartree.go), or of the whole space, under a heading that
// counts them and picks how they are sorted and grouped, and a filter by
// status. Pinned sessions come first, whatever is picked. Each row says
// what its group does not: its folder or project, and its machine.

// How the agents are grouped: in one list, by the folder of the project
// picked (or by project, with none picked), by machine, by agent, or by
// status.
const (
	agentGroupNone    = "none"
	agentGroupFolder  = "folder"
	agentGroupMachine = "machine"
	agentGroupAgent   = "agent"
	agentGroupStatus  = "status"
)

// agentGroups are the groupings in the order the view menu offers them,
// with their labels in English for T.
var (
	agentGroups      = []string{agentGroupNone, agentGroupFolder, agentGroupMachine, agentGroupAgent, agentGroupStatus}
	agentGroupLabels = map[string]string{
		agentGroupNone: "None", agentGroupFolder: "Sub-project", agentGroupMachine: "Machine",
		agentGroupAgent: "Agent", agentGroupStatus: "Status",
	}
	// agentGroupCommands are the palette's commands for them.
	agentGroupCommands = map[string]string{
		agentGroupNone: "List agents in one list", agentGroupFolder: "Group agents by sub-project",
		agentGroupMachine: "Group agents by machine", agentGroupAgent: "Group agents by agent", agentGroupStatus: "Group agents by status",
	}
)

// How the agents are sorted: the latest to say something first, by name,
// or those waiting on the user first.
const (
	agentSortRecent = "recent"
	agentSortName   = "name"
	agentSortStatus = "status"
)

// agentSorts are the sorts in the order the sort menu offers them, with
// their labels and their short labels for the button, in English for T.
var (
	agentSorts       = []string{agentSortRecent, agentSortName, agentSortStatus}
	agentSortLabels  = map[string]string{agentSortRecent: "Recent activity", agentSortName: "Name", agentSortStatus: "Status, waiting first"}
	agentSortButtons = map[string]string{agentSortRecent: "Activity", agentSortName: "Name", agentSortStatus: "Status"}
)

// agentStatus is what an agent is doing, as the filter and the status
// grouping and sort have it, in the order the sort puts them.
type agentStatus int

const (
	agentWaiting agentStatus = iota
	agentWorking
	agentDone
	agentIdle
)

// agentFilters are the filter's choices: every agent, then those of a
// status, by name.
var (
	agentFilters      = []string{"", "working", "waiting", "done"}
	agentFilterStatus = map[string]agentStatus{"working": agentWorking, "waiting": agentWaiting, "done": agentDone}
	agentFilterLabels = map[string]string{"": "All", "working": "Working", "waiting": "Waiting", "done": "Done"}
	agentStatusLabels = map[agentStatus]string{agentWaiting: "Waiting for you", agentWorking: "Working", agentDone: "Done", agentIdle: "Idle"}
)

// statusOf is what session s's agent is doing: as its dot shows, or done
// once it finished a turn, though seen.
func (m *model) statusOf(s *protocol.SessionInfo) agentStatus {
	switch m.dotOf(s) {
	case dotWaiting:
		return agentWaiting
	case dotWorking:
		return agentWorking
	case dotDone:
		return agentDone
	}
	if s.GetStatus() == protocol.AgentDone && s.GetState() == protocol.StateRunning {
		return agentDone
	}
	return agentIdle
}

// agentView is how the settings have the agents listed.
type agentView struct {
	Group, Sort, Filter string
	Compact             bool
}

// sidebarState is what of the settings the sidebar follows.
type sidebarState struct {
	kept           ggui.Readable[[]keptProject]
	pinned         ggui.Readable[[]string]
	pinnedProjects ggui.Readable[[]scopeKey]
	view           ggui.Readable[agentView]
}

func newSidebarState(m *model) sidebarState {
	return sidebarState{
		kept:           ggui.Map(m.Prefs, func(p prefs) []keptProject { return p.Projects }).WithEqual(slices.Equal),
		pinned:         ggui.Map(m.Spaces, func(l spaceList) []string { return l.Pinned }).WithEqual(slices.Equal),
		pinnedProjects: ggui.Map(m.Spaces, func(l spaceList) []scopeKey { return l.PinnedProjects }).WithEqual(slices.Equal),
		view: ggui.Map(m.Prefs, func(p prefs) agentView {
			return agentView{Group: p.AgentGroup, Sort: p.AgentSort, Filter: p.AgentFilter, Compact: p.AgentCompact}
		}),
	}
}

// agentList is the agents of a space as the sidebar lists them.
type agentList struct {
	scope   scopeKey
	section *sessionGroup // the scope's, nil for the whole space
	by      string        // the grouping, as far as it means something here
	counts  map[string]int
	groups  []agentGroup
	// where is what each session's row says of where it is in the scope:
	// its folder, or its project, unless its group says so.
	where map[string]string
}

// agentGroup is a group of the list: Key as the grouping has it, and its
// sessions, sorted.
type agentGroup struct {
	Key      string
	Pinned   bool
	Sessions []*protocol.SessionInfo
}

// shown is how many agents the list shows.
func (l agentList) shown() int {
	n := 0
	for _, g := range l.groups {
		n += len(g.Sessions)
	}
	return n
}

// listAgents lists the agents of space i as v has them.
func (m *model) listAgents(st sidebarState, i int, v agentView) agentList {
	groups := m.spaceSections(st, i)
	l := agentList{scope: m.scopeIn(i), counts: map[string]int{}, where: map[string]string{}, by: v.Group}
	defs := m.spaces().Projects
	if at := slices.IndexFunc(groups, func(g sessionGroup) bool { return g.Key == l.scope.Section }); at >= 0 {
		l.section = &groups[at]
	} else {
		l.scope = scopeKey{}
	}
	// By folder means something across the folders of a project, or the
	// projects of the space; within one folder the list is one.
	multi := l.section != nil && l.section.multiRoot() && l.scope.Root == ""
	if l.by == agentGroupFolder && l.section != nil && !multi {
		l.by = agentGroupNone
	}
	pins := st.pinned.Get()
	var pinned, rest []*protocol.SessionInfo
	sectionOf := map[string]int{}
	for gi, g := range groups {
		for _, s := range g.Sessions {
			sectionOf[s.GetId()] = gi
			status := m.statusOf(s)
			in := l.scope.holds(sessionScope(defs, s))
			if in {
				l.counts[""]++
				for name, fs := range agentFilterStatus {
					if fs == status {
						l.counts[name]++
					}
				}
			}
			if v.Filter != "" && agentFilterStatus[v.Filter] != status {
				continue
			}
			switch {
			case slices.Contains(pins, s.GetId()):
				pinned = append(pinned, s)
			case in:
				rest = append(rest, s)
			}
			switch {
			case multi:
				if r := g.rootOf(s); r != nil {
					l.where[s.GetId()] = "#" + r.label()
				}
			case l.section == nil:
				l.where[s.GetId()] = groupName(g)
			}
		}
	}
	m.sortAgents(pinned, v.Sort)
	m.sortAgents(rest, v.Sort)
	if len(pinned) > 0 {
		l.groups = append(l.groups, agentGroup{Pinned: true, Sessions: pinned})
	}
	key := func(s *protocol.SessionInfo) string {
		switch l.by {
		case agentGroupFolder:
			if multi {
				if j, ok := l.section.RootOf[s.GetId()]; ok {
					return strconv.Itoa(j)
				}
				return ""
			}
			return groups[sectionOf[s.GetId()]].Key
		case agentGroupMachine:
			return s.GetNodeId()
		case agentGroupAgent:
			return s.GetAgent()
		case agentGroupStatus:
			return strconv.Itoa(int(m.statusOf(s)))
		}
		return ""
	}
	var order []agentGroup
	index := map[string]int{}
	for _, s := range rest {
		k := key(s)
		j, ok := index[k]
		if !ok {
			j = len(order)
			index[k] = j
			order = append(order, agentGroup{Key: k})
		}
		order[j].Sessions = append(order[j].Sessions, s)
	}
	// The folders in the project's order, then any in none of them; the
	// projects by name; the machines this one first, then by name; the
	// statuses waiting first.
	nodes := m.Nodes.Get()
	rank := func(g agentGroup) (int, string) {
		switch l.by {
		case agentGroupFolder:
			if multi {
				if j, err := strconv.Atoi(g.Key); err == nil {
					return j, ""
				}
				return len(l.section.Def.Roots), ""
			}
			return slices.IndexFunc(groups, func(o sessionGroup) bool { return o.Key == g.Key }), ""
		case agentGroupMachine:
			n, _ := nodeIn(nodes, g.Key)
			return pick(n.GetSource() == "self", 0, 1), strings.ToLower(nodeName(nodes, g.Key))
		case agentGroupStatus:
			st, _ := strconv.Atoi(g.Key)
			return st, ""
		}
		return 0, ""
	}
	slices.SortStableFunc(order, func(a, b agentGroup) int {
		ra, sa := rank(a)
		rb, sb := rank(b)
		return cmp.Or(cmp.Compare(ra, rb), cmp.Compare(sa, sb))
	})
	l.groups = append(l.groups, order...)
	return l
}

// sortAgents sorts sessions as the sort called by has them.
func (m *model) sortAgents(sessions []*protocol.SessionInfo, by string) {
	printed, titles := m.Printed.Get(), m.Titles.Get()
	recent := func(a, b *protocol.SessionInfo) int { return printed[b.GetId()].Compare(printed[a.GetId()]) }
	name := func(a, b *protocol.SessionInfo) int {
		return cmp.Compare(strings.ToLower(sessionTitle(a, titles[a.GetId()])), strings.ToLower(sessionTitle(b, titles[b.GetId()])))
	}
	slices.SortStableFunc(sessions, func(a, b *protocol.SessionInfo) int {
		switch by {
		case agentSortName:
			return name(a, b)
		case agentSortStatus:
			return cmp.Or(cmp.Compare(m.statusOf(a), m.statusOf(b)), recent(a, b))
		}
		return recent(a, b)
	})
}

// agentsPanel is the sidebar's agents in space i: the heading, the filter,
// and the list, which fades out at its foot under the dots of the spaces.
func agentsPanel(m *model, st sidebarState, i int) ggui.Widget {
	list := ggui.Derived(func() agentList {
		m.Unread.Get() // the statuses follow what is unread
		return m.listAgents(st, i, st.view.Get())
	}).WithEqual(func(a, b agentList) bool { return false })
	head := ggui.Reactive(func() ggui.Widget { return agentsHeading(m, st.view.Get(), list.Get()) })
	filter := ggui.Reactive(func() ggui.Widget { return agentFilter(m, st.view.Get().Filter, list.Get().counts) })
	rows := ggui.Reactive(func() ggui.Widget { return agentRows(m, st.view.Get(), list.Get()) })
	// The foot leaves room for the spaces' dots, which float over it
	// (sidebar), staying put as a swipe moves the list.
	body := &bottomFade{child: ggui.Scroll(ggui.Padding(rows, 0, 8, 44, 8)).Bar(skin.Surface3), width: 56}
	return ggui.Column(head, filter, ggui.Expanded(body)).Stretch()
}

// agentsHeading is the agents' title, how many are listed, the sort and
// the view, and a button to start one in the scope.
func agentsHeading(m *model, v agentView, l agentList) ggui.Widget {
	count := ggui.Box(ggui.Text(fmt.Sprint(l.shown())).Size(11).Color(skin.Muted).NoWrap()).Pad(1, 6).Radius(9).Fill(skin.Surface2)
	sorts := make([]ggui.Widget, len(agentSorts))
	for i, by := range agentSorts {
		sorts[i] = ui.MenuItem(T(agentSortLabels[by]), func() { m.setAgentView(func(p *prefs) { p.AgentSort = by }) }).Checked(v.Sort == by)
	}
	sort := iconMenu(ui.MenuOf(toolFace("arrow-down-wide-narrow", T(agentSortButtons[v.Sort])), sorts...).
		Name(Tf("Sort: %s", T(agentSortLabels[v.Sort]))), 0)
	views := []ggui.Widget{ui.MenuLabel(T("Group"))}
	for _, by := range agentGroups {
		item := ui.MenuItem(T(agentGroupLabels[by]), func() { m.setAgentView(func(p *prefs) { p.AgentGroup = by }) }).Checked(v.Group == by)
		if by == agentGroupFolder && l.section != nil && l.by != agentGroupFolder {
			item = item.Shortcut(T("with several folders"))
		}
		views = append(views, item)
	}
	views = append(views, ui.MenuDivider(), ui.MenuLabel(T("Show")),
		ui.MenuItem(T("Detailed"), func() { m.setAgentView(func(p *prefs) { p.AgentCompact = false }) }).Checked(!v.Compact),
		ui.MenuItem(T("Compact"), func() { m.setAgentView(func(p *prefs) { p.AgentCompact = true }) }).Checked(v.Compact))
	summary := pick(l.by == agentGroupNone, T("List"), T(agentGroupLabels[l.by]))
	view := iconMenu(ui.MenuOf(toolFace("sliders-horizontal", ""), views...).Name(Tf("View: %s", summary)), 0)
	add := ui.Tooltip(ui.ButtonOf(icon("plus").Size(15), func() { m.newInScope(l) }).Ghost().Pad(5).Radius(6).Name(T("New session")), T("New session"))
	return ggui.Box(ggui.Row(
		ggui.Text(T("Agents")).Size(13).Weight(ggui.WeightSemibold).NoWrap(),
		count,
		ggui.Spacer(),
		sort,
		view,
		ggui.Box().Size(1, 14).Fill(skin.Border),
		add,
	).Gap(2).Align(ggui.AlignCenter)).Pad(10, 6, 8, 16)
}

// toolFace is a quiet button's face in the agents' heading: an icon, a
// label unless it is "", and a chevron.
func toolFace(glyph, label string) ggui.Widget {
	face := []ggui.Widget{icon(glyph).Size(13)}
	if label != "" {
		face = append(face, ggui.Text(label).Size(12).Color(skin.Muted).NoWrap())
	}
	face = append(face, icon("chevron-down").Size(10).Color(skin.ExtraMuted))
	return ggui.Box(ggui.Row(face...).Gap(4).Align(ggui.AlignCenter)).Pad(0, 6).Height(26)
}

// setAgentView changes how the sidebar lists agents.
func (m *model) setAgentView(fn func(*prefs)) {
	m.Prefs.Update(func(p prefs) prefs { fn(&p); return p })
}

// newInScope starts a session where the scope is: in the folder picked,
// on this machine if it is there, else in the project's folder by
// default; with nothing picked, wherever the dialog says.
func (m *model) newInScope(l agentList) {
	g := l.section
	if g == nil {
		m.openNew("", "")
		return
	}
	node, dir := g.NodeID, g.Project
	if l.scope.Root != "" && g.Def != nil {
		if j := slices.IndexFunc(g.Def.Roots, func(r projectRoot) bool { return rootKey(r) == l.scope.Root }); j >= 0 {
			locs := g.Def.Roots[j].Locations
			if k := slices.IndexFunc(locs, func(f fileRoot) bool { return m.onThisMachine(f.Node) }); k >= 0 {
				node, dir = locs[k].Node, locs[k].Path
			} else if len(locs) > 0 {
				node, dir = locs[0].Node, locs[0].Path
			}
		}
	}
	m.openNew(node, dir)
}

// agentFilter picks the status the agents are of, each but All counting
// those of the scope.
func agentFilter(m *model, current string, counts map[string]int) ggui.Widget {
	cells := make([]ggui.Widget, len(agentFilters))
	for i, f := range agentFilters {
		on := f == current
		face := []ggui.Widget{ggui.Text(T(agentFilterLabels[f])).Size(12).Color(pick[color.Color](on, skin.Fg, skin.Muted)).NoWrap()}
		if f != "" {
			face = append(face, ggui.Text(fmt.Sprint(counts[f])).Size(11).Color(pick[color.Color](on, skin.Muted, skin.ExtraMuted)).NoWrap())
		}
		btn := ui.ButtonOf(ggui.Box(ggui.Center(ggui.Row(face...).Gap(4).Align(ggui.AlignCenter))).Height(26),
			func() { m.setAgentView(func(p *prefs) { p.AgentFilter = f }) }).Ghost().Pad(0).Radius(6).Name(T(agentFilterLabels[f]))
		var cell ggui.Widget = btn
		if on {
			cell = ggui.Box(btn).Fill(skin.Surface3).Radius(6)
		}
		cells[i] = ggui.Expanded(cell)
	}
	return ggui.Box(ggui.Box(ggui.Row(cells...).Gap(2)).Pad(2).Radius(8).Fill(skin.Surface2)).Pad(0, 8, 6, 8)
}

// agentRows are the list's groups, each under a heading but the list's
// one.
func agentRows(m *model, v agentView, l agentList) ggui.Widget {
	if l.shown() == 0 {
		return ggui.Box(ggui.Center(muted(ggui.Text(T("No agents here.")).Size(12.5)))).Pad(24, 12)
	}
	nodes, titles, selected := m.Nodes.Get(), m.Titles.Get(), m.Selected.Get()
	looks := m.spaces().Looks
	var rows []ggui.Widget
	for _, g := range l.groups {
		if g.Pinned || l.by != agentGroupNone {
			rows = append(rows, agentGroupHeading(m, l, g, nodes, looks))
		}
		for _, s := range g.Sessions {
			rows = append(rows, agentRow(m, s, s.GetId() == selected, titles[s.GetId()], agentTagOf(m, l, s, nodes), v.Compact))
		}
	}
	return ggui.Column(rows...).Gap(1).Stretch()
}

// agentGroupHeading names a group of the list and counts it, with a line
// on to the end, where a machine's says how it is.
func agentGroupHeading(m *model, l agentList, g agentGroup, nodes []*protocol.Node, looks map[string]projectLook) ggui.Widget {
	var lead ggui.Widget
	name, note := g.Key, ""
	switch {
	case g.Pinned:
		lead, name = icon("pin").Size(12).Color(skin.ExtraMuted), T("Pinned")
	case l.by == agentGroupFolder && l.section != nil:
		lead, name = hashMark(12), T("Elsewhere")
		if j, err := strconv.Atoi(g.Key); err == nil && l.section.Def != nil && j < len(l.section.Def.Roots) {
			name = l.section.Def.Roots[j].label()
		}
	case l.by == agentGroupFolder:
		lead = lookOf(looks, g.Key).glyph(12)
		if len(g.Sessions) > 0 {
			name = cmp.Or(l.where[g.Sessions[0].GetId()], name)
		}
	case l.by == agentGroupMachine:
		n, up := nodeIn(nodes, g.Key)
		lead, name = icon(osIcon(n.GetOs())).Size(12).Color(skin.Muted), cmp.Or(n.GetName(), short(g.Key))
		switch {
		case n.GetSource() == "self":
			note = T("This machine")
		case up && reachable(n):
			note = T("Connected")
		default:
			note = T("Not reachable")
		}
	case l.by == agentGroupAgent:
		lead, name = agentGlyph(g.Key, skin.Muted).Size(12), cmp.Or(g.Key, "shell")
	case l.by == agentGroupStatus:
		st, _ := strconv.Atoi(g.Key)
		lead, name = ggui.Box().Size(7, 7).Radius(3.5).Fill(statusColor(agentStatus(st))), T(agentStatusLabels[agentStatus(st)])
	}
	row := []ggui.Widget{
		lead,
		ggui.Text(name).Size(12).Color(skin.Muted).NoWrap().Ellipsis(),
		ggui.Text(fmt.Sprint(len(g.Sessions))).Size(12).Color(skin.ExtraMuted).NoWrap(),
		ggui.Expanded(ggui.Box().Height(1).Fill(skin.Surface3)),
	}
	if note != "" {
		row = append(row, ggui.Text(note).Size(11).Color(skin.ExtraMuted).NoWrap())
	}
	return ggui.Box(ggui.Row(row...).Gap(6).Align(ggui.AlignCenter)).Pad(8, 10, 4, 10)
}

// statusColor is the colour of a status's dot.
func statusColor(s agentStatus) color.Color {
	switch s {
	case agentWaiting:
		return skin.DotWaiting
	case agentWorking:
		return skin.DotRunning
	case agentDone:
		return skin.DotDone
	}
	return skin.DotExited
}

// osIcon is the mark of an operating system, as a node names its own.
func osIcon(goos string) string {
	switch goos {
	case "darwin", "ios":
		return "os-macos"
	case "windows":
		return "os-windows"
	case "linux", "android", "freebsd", "openbsd", "netbsd":
		return "os-linux"
	}
	return "monitor"
}

// agentTag is what a row says of where its session is, beyond its group.
type agentTag struct {
	Where string // its folder, or its project
	Host  string // its machine, "" when its group says
	OS    string
	Here  bool // the machine is this one
}

// agentTagOf is session s's tag in list l.
func agentTagOf(m *model, l agentList, s *protocol.SessionInfo, nodes []*protocol.Node) agentTag {
	t := agentTag{}
	if l.by != agentGroupFolder {
		t.Where = l.where[s.GetId()]
	}
	if l.by != agentGroupMachine {
		n, _ := nodeIn(nodes, s.GetNodeId())
		t.Host, t.OS, t.Here = nodeName(nodes, s.GetNodeId()), n.GetOs(), m.onThisMachine(s.GetNodeId())
	}
	return t
}

// agentRow is a session in the agents list: its agent's icon, its title
// and when it last said something, and under them where it is, its ID and
// its branch, or in a compact row, after the title, the first of where it
// is and its machine, if not this one. Its dot is at the end, and on hover
// a pin and a button that closes it.
func agentRow(m *model, s *protocol.SessionInfo, selected bool, title string, tag agentTag, compact bool) ggui.Widget {
	id := s.GetId()
	label := sessionTitle(s, title)
	head := []ggui.Widget{
		ggui.Padding(&busyGlyph{icon: agentGlyph(s.GetAgent(), skin.Muted).Size(15), busy: func() bool { return m.busy(id) }}, 0, 2.5, 0, 2.5),
		ggui.Expanded(ggui.Text(label).Size(pick(compact, 13.0, 13.5)).NoWrap().Ellipsis()),
	}
	if compact {
		switch {
		case tag.Where != "":
			head = append(head, ggui.Box(ggui.Text(tag.Where).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis()).MaxWidth(110))
		case tag.Host != "" && !tag.Here:
			head = append(head, ggui.Box(ggui.Row(
				icon(osIcon(tag.OS)).Size(10).Color(skin.ExtraMuted),
				ggui.Text(tag.Host).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis(),
			).Gap(3).Align(ggui.AlignCenter)).MaxWidth(110))
		}
	}
	head = append(head, ggui.Reactive(func() ggui.Widget {
		m.Minute.Get()
		t := m.Printed.Get()[id]
		if t.IsZero() || s.GetState() != protocol.StateRunning {
			return ggui.Box()
		}
		return ggui.Text(ago(t)).Size(11).Color(skin.ExtraMuted).NoWrap()
	}))
	lines := []ggui.Widget{ggui.Row(head...).Gap(8).Align(ggui.AlignCenter)}
	if !compact {
		lines = append(lines, agentMeta(s, tag))
	}
	hover := ggui.State(false)
	end := ggui.If(hover, func() ggui.Widget {
		pin := pick(m.pinned(id), T("Unpin"), T("Pin to top"))
		return ggui.Row(
			ui.Tooltip(ui.ButtonOf(icon("pin").Size(13).Color(pick[color.Color](m.pinned(id), skin.DotWaiting, skin.Muted)), func() { m.togglePin(id) }).
				Ghost().Pad(3).Radius(4).Name(pin), pin),
			ui.ButtonOf(icon("x").Size(13), func() { m.askKill(id) }).Ghost().Pad(3).Radius(4).Name(Tf("Close %s", label)),
		).Gap(0).Align(ggui.AlignCenter)
	}).Else(func() ggui.Widget {
		return ggui.Box(ggui.Center(&activityDot{state: m.dotFor(id)})).Size(19, 19)
	})
	content := ggui.Row(
		ggui.Expanded(ggui.Column(lines...).Gap(3).Stretch()),
		end,
	).Gap(6).Align(ggui.AlignCenter)
	btn := ui.ButtonOf(content, func() { m.selectSession(id) }).Ghost().Pad(pick(compact, 5.0, 8.0), 10).Radius(8).Name(label)
	var w ggui.Widget = sessionMenu(m, btn, id, label, false)
	if selected {
		w = ggui.Box(w).Fill(skin.SidebarSel).Radius(8)
	}
	// A row drags into a pane, or to a pane's edge, as a tab does.
	return sessionDrag(m, ggui.Pointer(w).OnHover(hover.Set), id)
}

// agentMeta is a detailed row's second line, from its left edge: where
// its session is, its machine and ID, and at the end its branch and its
// tasks.
func agentMeta(s *protocol.SessionInfo, tag agentTag) ggui.Widget {
	quiet := func(t string) ggui.Widget { return ggui.Text(t).Size(12).Color(skin.ExtraMuted).NoWrap() }
	var where []ggui.Widget
	if tag.Where != "" {
		where = append(where, quiet(tag.Where+" ·"))
	}
	if tag.Host != "" {
		where = append(where, icon(osIcon(tag.OS)).Size(11).Color(skin.ExtraMuted),
			ggui.Flexible(ggui.Text(tag.Host+" ·").Size(12).Color(skin.ExtraMuted).NoWrap().Ellipsis()))
	}
	where = append(where, quiet(short(s.GetId())))
	line := []ggui.Widget{ggui.Expanded(ggui.Row(where...).Gap(4).Align(ggui.AlignCenter))}
	if s.GetBranch() != "" {
		line = append(line, ggui.Row(
			icon("git-branch").Size(11).Color(skin.ExtraMuted),
			ggui.Box(ggui.Text(s.GetBranch()).Size(12).Color(skin.ExtraMuted).NoWrap().Ellipsis()).MaxWidth(110),
		).Gap(3).Align(ggui.AlignCenter))
	}
	if badge := tasksBadge(s); badge != nil {
		line = append(line, badge)
	}
	return ggui.Box(ggui.Row(line...).Gap(8).Align(ggui.AlignCenter)).Pad(0, 0, 0, 2)
}

// bottomFade paints its child faded out over its last width pixels, so
// what scrolls under the spaces' dots fades away rather than meeting a
// line.
type bottomFade struct {
	child ggui.Widget
	width float64
}

func (f *bottomFade) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	return f.child.Layout(c, env)
}

func (f *bottomFade) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.FadeEdges(r, false, 0, f.width, func(dst *ggui.Canvas) { dst.Paint(f.child, r) })
}
