package gui

import (
	"cmp"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/ui/icons"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/sanbo/internal/protocol"
)

// The window follows Paseo's desktop layout: a sidebar of projects and their
// sessions, a workspace of tabs over the selected session, whose Chat view
// carries the composer, and a panel on the right of the project's files and
// the session's details.
const (
	sidebarWidth = 272 // the sidebar's, until dragged, and the settings page's
	detailsWidth = 320 // the right panel's, until dragged
	headerHeight = 48
	tabBarHeight = 38

	minSidebarWidth, maxSidebarWidth = 200, 520
	minDetailsWidth, maxDetailsWidth = 260, 900
	// panelEdgeWidth is a side panel's draggable edge.
	panelEdgeWidth = 4
)

// build draws the window in the language the settings choose, again from
// the top when it changes, so every label is drawn in the new one.
func build(m *model) ggui.Widget {
	lang := ggui.Map(m.Prefs, func(p prefs) string { return resolveLanguage(p.Language) })
	return ggui.Key(lang, func(code string) ggui.Widget {
		setLanguage(code)
		return window(m)
	})
}

func window(m *model) ggui.Widget {
	inSettings := ggui.Map(m.Page, func(p string) bool { return p == "settings" })
	// The side panels slide open and closed beside the workspace.
	main := ggui.Row(
		newDrawer(ggui.Row(ggui.Expanded(sidebar(m)), sidebarEdge(m)).Stretch(), m.SidebarOpen,
			ggui.Map(m.SidebarWidth, func(w float64) float64 { return w + panelEdgeWidth }), false),
		ggui.Expanded(workspace(m)),
		newDrawer(ggui.Row(panelEdge(m), ggui.Expanded(details(m))).Stretch(), m.DetailsOpen,
			ggui.Map(m.DetailsWidth, func(w float64) float64 { return w + panelEdgeWidth }), true),
	).Stretch()
	return ggui.Stack(
		// The settings page takes the whole window, as Paseo's does; the
		// sessions stay attached while it shows.
		ggui.If(inSettings, func() ggui.Widget { return settingsPage(m) }).
			Else(func() ggui.Widget { return main }),
		newSessionDialog(m),
		renameDialog(m),
		fileDialogs(m),
		gitAskDialog(m),
		gitConfirmDialog(m),
		imageViewer(m),
		paletteDialog(m),
		m.toaster,
		quickOpenDialog(m),
		extensionsDialog(m),
		ui.AlertDialog(m.ConfirmOpen, T("Kill this session?"), T("The program is stopped on its node. Its output is lost.")).
			Confirm(T("Kill session"), m.confirmedKill).Destructive(),
		ui.AlertDialog(m.DiscardOpen, T("Close without saving?"), T("Your changes to this file are lost.")).
			Confirm(T("Discard changes"), m.confirmedDiscard).Destructive(),
		ui.AlertDialog(m.HunkOpen, T("Discard this hunk?"), T("Its lines go back to what the last commit has. The change is lost.")).
			Confirm(T("Discard hunk"), func() {
				if fn := m.pendingHunk; fn != nil {
					m.pendingHunk = nil
					fn()
				}
			}).Destructive(),
		ui.AlertDialog(m.QuitOpen, T("Quit without saving?"), T("Files you edited have unsaved changes, which are lost.")).
			Confirm(T("Quit"), func() { m.quitting = true; ggui.UseHost().Close() }).Destructive(),
		ui.AlertDialog(m.StopOpen, T("Stop the app's daemon?"), T("The sessions running in it end with it.")).
			Confirm(T("Stop daemon"), m.confirmedStop).Destructive(),
	).Expand()
}

// muted is secondary text.
func muted(t *ggui.TextWidget) *ggui.TextWidget { return t.Color(tokMuted) }

func hline() ggui.Widget { return ui.Divider() }
func vline() ggui.Widget { return ui.Divider().Vertical() }

// iconButton is a borderless square button around an icon, with a tooltip.
func iconButton(name, label string, onTap func()) ggui.Widget {
	return ui.Tooltip(ui.ButtonOf(icon(name), onTap).Ghost().Pad(6).Radius(6).Name(label), label)
}

// statusLabel names what a session's agent is doing, as its hooks said.
func statusLabel(s *protocol.SessionInfo) string {
	switch {
	case s.GetApproval() != nil:
		return Tf("Waiting for approval: %s", s.GetApproval().GetTool())
	case s.GetStatus() == protocol.AgentWorking:
		return T("Working")
	case s.GetStatus() == protocol.AgentWaiting:
		return T("Waiting for you")
	case s.GetStatus() == protocol.AgentDone:
		return T("Finished its turn")
	}
	return T("At its prompt")
}

// stateLabel names a session's state as the window shows it.
func stateLabel(st protocol.SessionState) string {
	switch st {
	case protocol.StateStopped:
		return T("saved")
	case protocol.StateStarting:
		return T("starting")
	case protocol.StateRunning:
		return T("running")
	case protocol.StateExited:
		return T("exited")
	}
	return string(st)
}

// linkDot is the daemon link's status: green when reachable, red when not.
func linkDot(ok bool) ggui.Widget {
	if ok {
		return dot(tokDotRunning)
	}
	return dot(tokDotDown)
}

func dot(c color.Color) ggui.Widget { return ggui.Box().Size(7, 7).Radius(3.5).Fill(c) }

// Sidebar --------------------------------------------------------------------

func sidebar(m *model) ggui.Widget {
	header := ggui.Box(ggui.Row(
		icon("messages-square").Size(17),
		ggui.Text(T("Sessions")).Size(14).Weight(ggui.WeightMedium),
		ggui.Spacer(),
		iconButton("search", T("Filter sessions"), func() {
			if ggui.Peek(m.FilterOpen) {
				m.SidebarFilter.Set("")
			}
			ggui.Toggle(m.FilterOpen)
		}),
		groupPicker(m),
	).Gap(6)).Pad(0, 10, 0, 16).Height(headerHeight)
	filter := ggui.If(m.FilterOpen, func() ggui.Widget {
		field := ui.TextField(m.SidebarFilter).Placeholder(T("Filter by title, project, branch or host")).Name(T("Filter sessions")).
			OnSubmit(func(string) {})
		return ggui.Box(field).Pad(8, 10, 0, 10)
	})

	grouping := ggui.Map(m.Prefs, func(p prefs) string { return p.SidebarGroup })
	projects := ggui.Map(m.Prefs, func(p prefs) []fileRoot { return p.Projects }).WithEqual(slices.Equal)
	pinned := ggui.Map(m.Prefs, func(p prefs) []string { return p.Pinned }).WithEqual(slices.Equal)
	collapsed := ggui.Map(m.Prefs, func(p prefs) []string { return p.Collapsed }).WithEqual(slices.Equal)
	list := ggui.Reactive(func() ggui.Widget {
		by := grouping.Get()
		nodes := m.Nodes.Get()
		titles := m.Titles.Get()
		selected := m.Selected.Get()
		m.Unread.Get() // the dots show what is unread
		query := strings.TrimSpace(m.SidebarFilter.Get())
		sessions := filterSessions(m.Sessions.Get(), query, nodes, titles)
		// Pinned sessions come first, in a section of their own.
		pins := pinned.Get()
		var top []*protocol.SessionInfo
		rest := sessions[:0:0]
		for _, s := range sessions {
			if slices.Contains(pins, s.GetId()) {
				top = append(top, s)
			} else {
				rest = append(rest, s)
			}
		}
		var groups []sessionGroup
		if query != "" {
			// Filtered, the folders kept without sessions are left out.
			groups = groupSessions(rest, by, nodes, nil)
		} else {
			groups = groupSessions(rest, by, nodes, projects.Get())
		}
		if len(groups) == 0 && len(top) == 0 {
			if query != "" {
				return ggui.Box(muted(ggui.Text(T("No sessions match.")).Size(12))).Pad(16)
			}
			return ggui.Box(muted(ggui.Text(T("No sessions yet. Start one with +.")).Size(12))).Pad(16)
		}
		names := projectLabels(groups, nodes)
		children := make([]ggui.Widget, 0, len(groups)+1)
		if len(top) > 0 {
			children = append(children, pinnedSection(m, top, selected, nodes, titles))
		}
		folded := collapsed.Get()
		for _, g := range groups {
			// A filter shows what it finds in folded groups too.
			fold := query == "" && slices.Contains(folded, foldKey(by, g))
			children = append(children, sessionSection(m, by, g, fold, selected, nodes, titles, names[g.Key]))
		}
		return ggui.Column(children...).Gap(6).Stretch()
	})

	return ggui.Box(ggui.Column(
		header,
		hline(),
		filter,
		ggui.Expanded(ggui.Scroll(ggui.Padding(list, 8, 8)).Bar(tokSurface3)),
		hline(),
		sidebarFooter(m),
	).Stretch()).Fill(tokSidebar)
}

// grouping is a way the sidebar groups sessions, and how its header shows
// it.
type grouping struct{ By, Icon, Label string }

// groupings are the sidebar's ways of grouping sessions, as its header
// offers them.
var groupings = []grouping{
	{groupProject, "folder", "Group by project"},
	{groupHost, "server", "Group by host"},
	{groupFlat, "list", "Show as a list"},
}

// groupPicker switches how the sidebar groups its sessions.
func groupPicker(m *model) ggui.Widget {
	by := make([]string, len(groupings))
	for i, g := range groupings {
		by[i] = g.By
	}
	find := func(v string) grouping {
		return groupings[max(slices.IndexFunc(groupings, func(g grouping) bool { return g.By == v }), 0)]
	}
	return ui.ToggleGroup(prefField(m, func(p *prefs) *string { return &p.SidebarGroup })).Options(by).
		Format(func(v string) string { return T(find(v).Label) }).
		Icon(func(v string) *icons.Widget { return icon(find(v).Icon).Size(14) }).
		IconOnly().Name(T("Group sessions"))
}

// setSidebarGroup changes how the sidebar groups its sessions.
func (m *model) setSidebarGroup(by string) {
	m.Prefs.Update(func(p prefs) prefs { p.SidebarGroup = by; return p })
}

// foldKey names a sidebar group in the folded list: how the sidebar
// groups, and the group.
func foldKey(by string, g sessionGroup) string { return by + ":" + g.Key }

// toggleFold folds a sidebar group to its heading, or opens it.
func (m *model) toggleFold(key string) {
	m.Prefs.Update(func(p prefs) prefs { p.Collapsed = toggled(p.Collapsed, key); return p })
}

// sessionSection is one of the sidebar's groups: a heading for its project
// or host with a button to start another session there, over its sessions.
// A tap on the heading folds the group to it, its sessions counted. The
// list's one group has no heading.
func sessionSection(m *model, by string, g sessionGroup, folded bool, selected string, nodes []*protocol.Node, titles map[string]string, label string) ggui.Widget {
	nodeID, project := g.NodeID, g.Project
	var rows []ggui.Widget
	indent := by != groupFlat
	if indent {
		name, glyph := cmp.Or(label, projectName(project)), "folder"
		var status ggui.Widget = ggui.Box()
		if by == groupHost {
			// A node the mesh no longer lists has gone; its sessions stay
			// until it is back.
			n, up := nodeIn(nodes, nodeID)
			name, glyph = n.GetName(), "monitor"
			if !up {
				name = short(nodeID)
			}
			if n.GetSource() == "self" {
				glyph = "laptop"
			}
			status = linkDot(up)
		}
		title := []ggui.Widget{
			icon(pick(folded, "chevron-right", "chevron-down")).Size(13).Color(tokExtraMuted),
			icon(glyph).Size(15),
			ggui.Expanded(ggui.Align(ggui.Text(name).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis()).Left()),
		}
		if folded && len(g.Sessions) > 0 {
			title = append(title, kindBadges(g.Sessions))
		}
		key := foldKey(by, g)
		// The name takes the room the row has, so it is cut only when it
		// must be.
		toggle := ui.ButtonOf(ggui.Row(append(title, status)...).Gap(6).Align(ggui.AlignCenter),
			func() { m.toggleFold(key) }).Ghost().Pad(2, 4).Radius(5).Name(pick(folded, Tf("Expand %s", name), Tf("Collapse %s", name)))
		row := []ggui.Widget{ggui.Expanded(toggle)}
		// A folder kept with no sessions has nothing to bring it back once
		// it leaves, so its menu can drop it.
		if len(g.Sessions) == 0 {
			ref := fileRoot{Node: nodeID, Path: project}
			row = append(row, iconMenu(ui.MenuOf(icon("ellipsis").Size(14),
				ui.MenuItem(T("Remove from sidebar"), func() { m.forgetProject(ref) }),
			).Name(T("Folder actions")), 3))
		}
		row = append(row, ui.Tooltip(ui.ButtonOf(icon("plus").Size(14), func() { m.openNew(nodeID, project) }).
			Ghost().Pad(3).Radius(5).Name(Tf("New session in %s", name)), T("New session here")))
		// A session dropped on the heading starts again there: its agent
		// in this folder, or on this host in its own folder.
		rows = append(rows, sectionDrop(m, ggui.Box(ggui.Row(row...).Gap(6).Align(ggui.AlignCenter)).Pad(2, 4, 2, 2), nodeID, project))
		if folded {
			return ggui.Column(rows...).Gap(2).Stretch()
		}
		if len(g.Sessions) == 0 {
			rows = append(rows, ggui.Padding(ggui.Box(muted(ggui.Text(T("No sessions")).Size(11.5))).Pad(3, 4), 0, 0, 0, 14))
		}
	}
	for _, s := range g.Sessions {
		var where []string
		switch by {
		case groupProject:
			where = append(where, nodeName(nodes, s.GetNodeId()))
			// A subfolder by its path in the project; a worktree elsewhere
			// by its own name.
			if rel := subfolder(s.GetProject(), project); rel != "" {
				where = append(where, rel)
			} else if s.GetProject() != project {
				where = append(where, projectName(s.GetProject()))
			}
		case groupHost:
			where = append(where, projectName(s.GetProject()))
		default:
			where = append(where, nodeName(nodes, s.GetNodeId()), projectName(s.GetProject()))
		}
		where = append(where, short(s.GetId()))
		rows = append(rows, sessionRow(m, s, s.GetId() == selected, titles[s.GetId()], strings.Join(where, " · "), indent))
	}
	return ggui.Column(rows...).Gap(2).Stretch()
}

// kindBadges counts a folded group's sessions by kind, a badge for each
// with the kind's icon: shells first, then agents as they first appear.
func kindBadges(sessions []*protocol.SessionInfo) ggui.Widget {
	var kinds []string
	count := map[string]int{}
	for _, s := range sessions {
		k := s.GetAgent()
		if agentIcon(k) == agentIcon("shell") {
			k = "shell"
		}
		if count[k] == 0 {
			kinds = append(kinds, k)
		}
		count[k]++
	}
	slices.SortStableFunc(kinds, func(a, b string) int { return cmp.Compare(pick(a == "shell", 0, 1), pick(b == "shell", 0, 1)) })
	badges := make([]ggui.Widget, len(kinds))
	for i, k := range kinds {
		badges[i] = ggui.Box(ggui.Row(
			agentGlyph(k, tokMuted).Size(11),
			ggui.Text(fmt.Sprint(count[k])).Size(11).Color(tokMuted).NoWrap(),
		).Gap(2).Align(ggui.AlignCenter)).Pad(1, 4).Radius(4).Fill(tokSurface2)
	}
	return ggui.Row(badges...).Gap(2).Align(ggui.AlignCenter)
}

// subfolder is where dir is inside root, or "" for root itself.
func subfolder(dir, root string) string {
	if dir == root || !within(dir, root) {
		return ""
	}
	return strings.TrimLeft(dir[len(root):], `/\`)
}

// sessionRow is a session in the sidebar: its title, where it runs and
// the branch checked out there, and on hover a button that closes it as
// its tab's does.
func sessionRow(m *model, s *protocol.SessionInfo, selected bool, title, sub string, indent bool) ggui.Widget {
	id := s.GetId()
	label := sessionTitle(s, title)
	line := []ggui.Widget{ggui.Expanded(ggui.Text(sub).Size(11.5).Color(tokExtraMuted).NoWrap().Ellipsis())}
	if s.GetBranch() != "" {
		line = append(line, ggui.Row(
			icon("git-branch").Size(11).Color(tokExtraMuted),
			ggui.Box(ggui.Text(s.GetBranch()).Size(11.5).Color(tokExtraMuted).NoWrap().Ellipsis()).MaxWidth(110),
		).Gap(3))
	}
	// The title's line ends with when the program last printed.
	head := []ggui.Widget{ggui.Expanded(ggui.Text(label).Size(13).NoWrap().Ellipsis())}
	if s.GetState() == protocol.StateRunning {
		head = append(head, ggui.Reactive(func() ggui.Widget {
			m.Minute.Get()
			t := m.Printed.Get()[id]
			if t.IsZero() {
				return ggui.Box()
			}
			return ggui.Text(ago(t)).Size(11).Color(tokExtraMuted).NoWrap()
		}))
	}
	hover := ggui.State(false)
	end := ggui.If(hover, func() ggui.Widget {
		return ui.ButtonOf(icon("x").Size(13), func() { m.askKill(id) }).Ghost().Pad(2).Radius(4).Name(Tf("Close %s", label))
	}).Else(func() ggui.Widget {
		return &activityDot{state: m.dotFor(id)}
	})
	content := ggui.Row(
		&busyGlyph{icon: agentGlyph(s.GetAgent(), tokMuted).Size(15), busy: func() bool { return m.busy(id) }},
		ggui.Expanded(ggui.Column(
			ggui.Row(head...).Gap(8).Align(ggui.AlignCenter),
			ggui.Row(line...).Gap(8).Align(ggui.AlignCenter),
		).Gap(2).Stretch()),
		end,
	).Gap(10)
	btn := ui.ButtonOf(content, func() { m.selectSession(id) }).Ghost().Pad(7, 10).Radius(7).Name(label)
	// A row drags into a pane, or to a pane's edge, as a tab does.
	var w ggui.Widget = sessionMenu(m, btn, id, label, false)
	if selected {
		w = ggui.Box(btn).Fill(tokSidebarSel).Radius(7)
	}
	w = sessionDrag(m, ggui.Pointer(w).OnHover(hover.Set), id)
	if indent {
		w = ggui.Padding(w, 0, 0, 0, 14)
	}
	return w
}

func sidebarFooter(m *model) ggui.Widget {
	host := ggui.Reactive(func() ggui.Widget {
		c, d := m.Conn.Get(), m.DaemonState.Get()
		name := c.Self.GetName()
		switch {
		case d.Starting:
			name = T("Starting daemon…")
		case !c.OK:
			name = T("Daemon unreachable")
		case d.Running:
			name += " · in app"
		}
		row := ggui.Row(linkDot(c.OK), muted(ggui.Text(name).Size(13).NoWrap().Ellipsis())).Gap(10)
		if c.OK && outdated(c.Self) {
			// A daemon from an older build leaves agents' state and
			// approvals out; say so rather than show them blank.
			warn := ui.Tooltip(icon("info").Size(14).Color(tokDotWaiting),
				T("This daemon is from an older sanbo. Restart it with this build to see agents' state and approve in the window."))
			row = ggui.Row(linkDot(c.OK), ggui.Expanded(muted(ggui.Text(name).Size(13).NoWrap().Ellipsis())), warn).Gap(10)
		}
		return row
	})
	return ggui.Box(ggui.Row(
		ggui.Expanded(host),
		iconButton("plus", T("New session"), func() { m.openNew("", "") }),
		iconButton("settings", T("Settings"), func() { m.openSettings("") }),
	).Gap(4)).Pad(8, 10, 8, 16).Height(52)
}

// Workspace ------------------------------------------------------------------

func workspace(m *model) ggui.Widget {
	return ggui.Box(ggui.Column(
		workspaceHeader(m),
		hline(),
		ggui.Expanded(layoutView(m)),
	).Stretch()).Fill(tokSurface0)
}

func workspaceHeader(m *model) ggui.Widget {
	title := ggui.Reactive(func() ggui.Widget {
		switch id := m.Selected.Get(); {
		case isDocTab(id):
			return docHeader(m, id, m.Docs.Get()[id])
		case isDiffTab(id):
			return diffHeaderBar(m, id, m.Diffs.Get()[id])
		}
		s, ok := sessionIn(m.Sessions.Get(), m.Selected.Get())
		if !ok {
			return ggui.Text("sanbo").Size(14).Weight(ggui.WeightMedium)
		}
		where := fmt.Sprintf("%s:%s", nodeName(m.Nodes.Get(), s.GetNodeId()), prettyPath(s.GetProject()))
		id := s.GetId()
		return ggui.Row(
			ggui.Text(projectName(s.GetProject())).Size(14).Weight(ggui.WeightMedium).NoWrap(),
			muted(ggui.Text(where).Size(13).NoWrap().Ellipsis()),
			iconMenu(ui.MenuOf(icon("ellipsis"),
				ui.MenuItem(T("Rename…"), func() { m.askRename(id) }),
				ui.MenuItem(T("Detach tab"), func() { m.closeTab(id) }),
				ui.MenuItem(T("Show details"), func() { m.DetailsOpen.Set(true) }),
				ui.MenuDivider(),
				ui.MenuItem(T("Kill session"), func() { m.askKill(id) }),
			).Name(T("Session actions")), 6),
		).Gap(10)
	})
	return ggui.Box(ggui.Row(
		iconButton("panel-left", T("Toggle sidebar"), func() { ggui.Toggle(m.SidebarOpen) }),
		ggui.Expanded(title),
		iconButton("panel-right", T("Toggle details"), func() { ggui.Toggle(m.DetailsOpen) }),
	).Gap(8)).Pad(0, 10).Height(headerHeight)
}

func sessionIn(sessions []*protocol.SessionInfo, id string) (*protocol.SessionInfo, bool) {
	for _, s := range sessions {
		if s.GetId() == id {
			return s, true
		}
	}
	return &protocol.SessionInfo{}, false
}

// editSession changes the listed session id through edit, on a copy: the
// listing held is shared, and must not change under it.
func (m *model) editSession(id string, edit func(*protocol.SessionInfo)) {
	sessions := slices.Clone(ggui.Peek(m.Sessions))
	for i, s := range sessions {
		if s.GetId() == id {
			sessions[i] = proto.CloneOf(s)
			edit(sessions[i])
		}
	}
	m.Sessions.Set(sessions)
}

// tab is one of a pane's tabs. The tab a pane shows is raised and
// underlined, in the accent in the current pane.
func tab(m *model, s *protocol.SessionInfo, selected, focused bool, title string) ggui.Widget {
	id := s.GetId()
	label := sessionTitle(s, title)
	fg := tokMuted
	if selected {
		fg = tokFg
	}
	// The tab is one flat cell: its hover, its fill and its underline share
	// the cell's edges, with the content centred in its height.
	content := ggui.Box(ggui.Row(
		&busyGlyph{icon: agentGlyph(s.GetAgent(), fg).Size(14), busy: func() bool { return m.busy(id) }},
		ggui.Box(ggui.Text(label).Size(13).Color(fg).NoWrap().Ellipsis()).MaxWidth(150),
		ui.ButtonOf(icon("x").Size(13), func() { m.askKill(id) }).Ghost().Pad(3).Radius(4).Name(Tf("Close %s", label)),
	).Gap(8)).Height(tabBarHeight)
	btn := ui.ButtonOf(content, func() { m.selectSession(id) }).Ghost().Pad(0, 8, 0, 12).Radius(0).Name(label)
	return tabCell(sessionMenu(m, btn, id, label, true), selected, focused)
}

// tabCell is a tab's flat cell around its button: raised and underlined
// when the pane shows it, in the accent in the current pane.
func tabCell(btn ggui.Widget, selected, focused bool) ggui.Widget {
	underline := tokSurface0
	fill := tokSurface0
	switch {
	case selected && focused:
		underline, fill = tokAccentBright, tokSurface1
	case selected:
		underline, fill = tokBorderAccent, tokSurface1
	}
	cell := ggui.Box(&underlined{child: btn, color: underline}).Height(tabBarHeight).Fill(fill)
	return ggui.Row(cell, vline()).Stretch()
}

// underlined draws a 2px line along the bottom of its child, as wide as the
// child: the active tab's marker.
type underlined struct {
	child    ggui.Widget
	color    color.Color
	resolved color.Color // color under the theme the widget was laid out in
}

func (u *underlined) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	u.resolved = ggui.ResolveColor(u.color, env)
	return u.child.Layout(c, env)
}

func (u *underlined) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(u.child, r)
	dst.FillRect(ggui.Rect{Origin: ggui.Pt(r.Origin.X, r.Origin.Y+r.Size.H-2), Size: ggui.Sz(r.Size.W, 2)}, u.resolved)
}

// borderless drops the outline a stock control draws, for the icon
// triggers in the header.
// iconMenu is a menu whose button is an icon alone, padded by pad as the
// icon buttons beside it are, and drawn as they are, without a border. A
// menu's button is otherwise padded as one with a label.
func iconMenu(menu *ui.MenuWidget, pad float64) ggui.Widget {
	return uitheme.Override(func(t *uitheme.Theme) {
		t.Border = color.Transparent
		t.InputBorder = color.Transparent
		t.CardShadow = ggui.ShadowStyle{}
		t.ButtonPad = ggui.Insets(pad)
	}, menu)
}

func borderless(w ggui.Widget) ggui.Widget {
	return uitheme.Override(func(t *uitheme.Theme) {
		t.Border = color.Transparent
		t.InputBorder = color.Transparent
		t.CardShadow = ggui.ShadowStyle{}
	}, w)
}

// pane identifies what the main area shows.
type pane struct {
	ID    string
	Ready bool
}

func emptyState(m *model) ggui.Widget {
	return ggui.Center(ggui.Column(
		icon("square-terminal").Size(36).Color(tokExtraMuted),
		ggui.Text(T("No session open")).Size(16).Weight(ggui.WeightMedium),
		muted(ggui.Text(T("Pick a session in the sidebar, or start an agent on any node in the mesh.")).Size(13)),
		ggui.Box().Height(4),
		ui.Button(T("New session"), func() { m.openNew("", "") }).Shortcut("cmd+t"),
	).Gap(10).Align(ggui.AlignCenter))
}

// composer is the Chat lens's message box: it types a line into that
// session's live terminal followed by Enter. Terminal mode has no composer;
// the terminal itself takes the keys, as ovis does.
func composer(m *model, id string, draft *ggui.StateValue[string]) ggui.Widget {
	disabled := ggui.Derived(func() bool { return !slices.Contains(m.Attached.Get(), id) })
	// Files dropped or an image pasted go in by their paths.
	insert := func(paths []string) {
		for _, p := range paths {
			m.insertPath(id, p)
		}
	}
	// Enter sends at once; Shift+Enter breaks the line, and the box grows
	// with the lines to a few before it scrolls. ↑ and ↓ over an empty box
	// bring back what was sent.
	field := ui.TextField(draft).Lines(1).
		Placeholder(T("Message the agent. Enter sends, Shift+Enter adds a line")).
		Name(T("Message")).
		BindDisabled(disabled).
		OnKey(func(ev ggui.KeyEvent) bool {
			plain := !ev.Mods.Shift && !ev.Mods.Alt && !ev.Mods.Ctrl && !ev.Mods.Meta
			switch {
			case (ev.Key == ggui.KeyEnter || ev.Key == ggui.KeyNumpadEnter) && !ev.Mods.Shift && !ev.Mods.Alt:
				m.sendComposer(id)
				return true
			case ev.Key == ggui.KeyArrowUp && plain:
				return m.recallComposer(id, -1)
			case ev.Key == ggui.KeyArrowDown && plain:
				return m.recallComposer(id, 1)
			}
			return false
		}).
		OnPasteImage(func(png []byte) bool { m.pasteImage(id, png, insert); return true })
	pills := ggui.Reactive(func() ggui.Widget {
		s, ok := sessionIn(m.Sessions.Get(), id)
		if !ok {
			return ggui.Box()
		}
		return ggui.Row(
			pill(agentIcon(s.GetAgent()), sessionLabel(s)),
			pill("server", nodeName(m.Nodes.Get(), s.GetNodeId())),
			pill("circle-dot", stateLabel(s.GetState())),
		).Gap(6)
	})
	send := ui.ButtonOf(iconColored("arrow-up", uitheme.PrimaryFg).Size(15), func() { m.sendComposer(id) }).
		Pad(6).Radius(14).Name(T("Send")).BindDisabled(disabled)
	// Files dropped on the box, from the desktop or the Files tab, are
	// inserted as paths.
	over := ggui.State(false)
	content := ggui.Column(
		ggui.Box(inlineField(field)).MaxHeight(composerMaxHeight),
		ggui.Row(pills, ggui.Spacer(), send).Gap(8),
	).Gap(8).Stretch()
	box := ggui.View(over, func(o bool) ggui.Widget {
		var border ggui.Widget = ggui.Box(content).Fill(tokSurface1).Border(1, tokBorderAccent).Radius(12).Pad(10, 12)
		if o {
			border = ggui.Box(content).Fill(tokSurface1).Border(1, uitheme.Ring).Radius(12).Pad(10, 12)
		}
		return border
	})
	desktop := ggui.Pointer(box).OnDropHover(over.Set).OnDrop(func(ev ggui.DropEvent) {
		m.handOver(id, ev.Paths(), insert)
	})
	files := ggui.DropZone(desktop, func(d ggui.Dropped[pathDrag]) {
		over.Set(false)
		m.insertPath(id, d.Value.Path)
	}).OnHover(over.Set)
	// Align fills the height it is given, which would leave the feed above
	// none: the box keeps its own height and the whole pane's width.
	return ggui.Box(files).Pad(10, 16, 14, 16)
}

// composerMaxHeight is as tall as the composer's text grows, about ten
// lines, before it scrolls.
const composerMaxHeight = 220

// inlineField drops a text field's own chrome inside the composer box, so
// the box is the one surface, as in Paseo.
func inlineField(w ggui.Widget) ggui.Widget {
	return uitheme.Override(func(t *uitheme.Theme) {
		t.Input = color.Transparent
		t.InputBorder = color.Transparent
		t.Ring = color.Transparent
		t.FieldPad = ggui.Insets(4, 2)
	}, w)
}

func pill(iconName, label string) ggui.Widget {
	return ggui.Box(ggui.Row(icon(iconName).Size(13), muted(ggui.Text(label).Size(12))).Gap(6)).
		Pad(3, 8).Radius(6).Fill(tokSurface2)
}

// Details panel --------------------------------------------------------------

// The details panel's tabs, in order.
const (
	filesTab = iota
	gitTab
	searchTab
	sessionTab
)

func details(m *model) ggui.Widget {
	session := ggui.Reactive(func() ggui.Widget {
		s, ok := sessionIn(m.Sessions.Get(), m.Selected.Get())
		if !ok {
			return ggui.Box(muted(ggui.Text(T("No session selected.")).Size(13))).Pad(16)
		}
		code := "-"
		if s.ExitCode != nil {
			code = fmt.Sprint(*s.ExitCode)
		}
		props := []ggui.Widget{
			prop(T("Agent"), sessionLabel(s)),
		}
		if s.GetStatus() != "" {
			props = append(props, prop(T("Doing"), statusLabel(s)))
		}
		props = append(props,
			prop(T("Node"), nodeName(m.Nodes.Get(), s.GetNodeId())),
			prop(T("Project"), prettyPath(s.GetProject())))
		if w := s.GetWorktree(); w != nil {
			props = append(props, prop(T("Worktree"), Tf("%s, from %s, of %s", w.GetBranch(), cmp.Or(w.GetBase(), "HEAD"), prettyPath(w.GetRepo()))))
		}
		return ggui.Column(append(props,
			prop(T("Command"), strings.Join(s.GetCommand(), " ")),
			prop(T("State"), stateLabel(s.GetState())),
			prop(T("Exit code"), code),
			prop(T("Size"), fmt.Sprintf("%d × %d", s.GetCols(), s.GetRows())),
			prop(T("Started"), protocol.TimeOf(s.GetStartedAt()).Local().Format("Jan 2 15:04:05")),
			prop(T("Uptime"), time.Since(protocol.TimeOf(s.GetStartedAt())).Truncate(time.Second).String()),
			prop(T("Session"), s.GetId()),
		)...).Gap(14).Stretch()
	})
	// Built once, so each keeps its scroll and open folders across tabs.
	pages := []ggui.Widget{
		filesPanel(m),
		gitPanel(m),
		searchPanel(m),
		ggui.Scroll(ggui.Padding(session, 16)),
	}
	return ggui.Box(ggui.Column(
		panelTabs(m.DetailTab, T("Files"), T("Git"), T("Search"), T("Session")),
		hline(),
		ggui.Expanded(ggui.Key(m.DetailTab, func(i int) ggui.Widget { return pages[min(max(i, 0), len(pages)-1)] })),
	).Stretch()).Fill(tokSurface0)
}

// panelEdge is the right panel's left edge, which drags to resize the
// panel; the width is kept when the drag ends.
func panelEdge(m *model) ggui.Widget {
	edge := ggui.Box(ggui.Row(vline(), ggui.Spacer()).Stretch()).Width(panelEdgeWidth).Fill(tokSurface0)
	return resizeEdge(m, edge, m.DetailsWidth, -1, minDetailsWidth, maxDetailsWidth,
		func(p *prefs) *float64 { return &p.DetailsWidth })
}

// sidebarEdge is the sidebar's right edge, which drags to resize it as
// panelEdge does the right panel.
func sidebarEdge(m *model) ggui.Widget {
	edge := ggui.Box(ggui.Row(ggui.Spacer(), vline()).Stretch()).Width(panelEdgeWidth).Fill(tokSidebar)
	return resizeEdge(m, edge, m.SidebarWidth, 1, minSidebarWidth, maxSidebarWidth,
		func(p *prefs) *float64 { return &p.SidebarWidth })
}

// resizeEdge has edge drag width within [lo, hi]: wider as it moves the
// way dir says, 1 for right, and kept in the setting field gives when the
// drag ends.
func resizeEdge(m *model, edge ggui.Widget, width *ggui.StateValue[float64], dir, lo, hi float64, field func(*prefs) *float64) ggui.Widget {
	var from, start float64
	return ggui.Pointer(edge).Cursor(ggui.CursorShapeEWResize).
		OnDown(func(ev ggui.PointerEvent) { from, start = ev.Pos.X, ggui.Peek(width) }).
		OnDrag(func(ev ggui.PointerEvent) { width.Set(min(max(start+dir*(ev.Pos.X-from), lo), hi)) }).
		OnUp(func(ggui.PointerEvent) {
			w := ggui.Peek(width)
			m.Prefs.Update(func(p prefs) prefs { *field(&p) = w; return p })
		})
}

// panelTabs is the right panel's strip of tabs: as tall as the workspace
// header, so the line under it runs on from the header's, and drawn as the
// panes' tabs are, flat cells underlined when chosen.
func panelTabs(chosen *ggui.StateValue[int], labels ...string) ggui.Widget {
	return ggui.View(chosen, func(current int) ggui.Widget {
		cells := make([]ggui.Widget, len(labels))
		for i, label := range labels {
			fg, line := tokMuted, color.Color(tokSurface0)
			if i == current {
				fg, line = tokFg, tokAccentBright
			}
			btn := ui.ButtonOf(ggui.Box(ggui.Row(ggui.Text(label).Size(13).Color(fg).NoWrap())).Height(headerHeight),
				func() { chosen.Set(i) }).Ghost().Pad(0, 9).Radius(0).Name(label)
			cells[i] = &underlined{child: btn, color: line}
		}
		// Too narrow for them all, the tabs scroll sideways.
		return ggui.Box(ggui.Scroll(ggui.Row(cells...)).Horizontal().Bar(color.Transparent)).Height(headerHeight).Pad(0, 4)
	})
}

func prop(label, value string) ggui.Widget {
	return ggui.Column(
		ggui.Text(label).Size(11.5).Color(tokExtraMuted),
		ggui.Text(value).Size(13).Selectable(),
	).Gap(3).Stretch()
}

func nodeList(nodes []*protocol.Node) ggui.Widget {
	if len(nodes) == 0 {
		return muted(ggui.Text(T("No nodes.")).Size(13))
	}
	rows := make([]ggui.Widget, 0, len(nodes))
	for _, n := range nodes {
		kind := "laptop"
		if n.GetSource() != "self" {
			kind = "monitor"
		}
		rows = append(rows, ggui.Box(ggui.Row(
			icon(kind).Size(16),
			ggui.Expanded(ggui.Column(
				ggui.Row(ggui.Text(n.GetName()).Size(13).Weight(ggui.WeightMedium), linkDot(true), outdatedBadge(n)).Gap(8),
				ggui.Text(fmt.Sprintf("%s · %s/%s · %s", n.GetAddr(), n.GetOs(), n.GetArch(), n.GetSource())).Size(11.5).Color(tokExtraMuted).NoWrap().Ellipsis(),
			).Gap(3).Stretch()),
		).Gap(12)).Pad(10).Radius(8).Fill(tokSurface1))
	}
	return ggui.Column(rows...).Gap(8).Stretch()
}

// outdated reports whether a node's daemon is from an older build than
// the window's, which leaves newer parts of its replies out.
func outdated(n *protocol.Node) bool { return n.GetRevision() < protocol.Revision }

func outdatedBadge(n *protocol.Node) ggui.Widget {
	if !outdated(n) {
		return ggui.Box()
	}
	return ggui.Box(ggui.Text(T("out of date")).Size(11).Color(tokDotWaiting)).Pad(1, 6).Radius(4).Border(1, tokDotWaiting)
}

// Dialogs --------------------------------------------------------------------

func newSessionDialog(m *model) ggui.Widget {
	agent := m.Form.Field(func(f *newSession) *string { return &f.Agent })
	node := m.Form.Field(func(f *newSession) *string { return &f.NodeID })
	project := m.Form.Field(func(f *newSession) *string { return &f.Project })
	worktree := m.Form.Field(func(f *newSession) *bool { return &f.Worktree })
	branch := m.Form.Field(func(f *newSession) *string { return &f.Branch })
	nodeIDs := ggui.Map(m.Nodes, func(nodes []*protocol.Node) []string {
		ids := make([]string, len(nodes))
		for i, n := range nodes {
			ids[i] = n.GetId()
		}
		return ids
	})
	browse := ui.Button(T("Browse…"), func() {
		host := ggui.UseHost()
		start := ggui.Peek(project)
		ggui.Async(func() (string, error) {
			return host.Dialogs().PickFolder(runtime.FileDialog{Title: T("Working directory"), Directory: start})
		}, func(dir string, err error) {
			if err == nil && dir != "" {
				project.Set(dir)
			}
		})
	}).Outline()
	// The native picker browses this machine, so it is offered only when
	// the session will run here.
	local := ggui.Derived(func() bool { return m.Form.Get().NodeID == m.Conn.Get().Self.GetId() })
	hasErr := ggui.Map(m.FormErr, func(s string) bool { return s != "" })
	// What the chosen node can launch, profiles included.
	targets := ggui.Derived(func() []string {
		return nodeTargets(m.Nodes.Get(), m.Form.Get().NodeID, m.Agents.Get())
	}).WithEqual(slices.Equal)
	return ui.Dialog(m.NewOpen, ggui.Column(
		ui.Field(T("Agent"), ui.Select(agent).BindOptions(targets).Format(targetLabel).Name(T("Agent"))),
		ui.Field(T("Node"), ui.Select(node).BindOptions(nodeIDs).Format(func(id string) string {
			return nodeName(ggui.Peek(m.Nodes), id)
		}).Name(T("Node"))),
		ui.Field(T("Working directory"), ggui.Row(
			ggui.Expanded(ui.TextField(project).Placeholder(T("The node's home folder")).Name(T("Working directory")).OnSubmit(func(string) { m.submitNew() })),
			ggui.If(local, func() ggui.Widget { return browse }),
		).Gap(8)),
		ui.Field(T("Git"), ggui.Column(
			ui.Checkbox(worktree, T("Work in a new worktree")),
			ggui.If(worktree, func() ggui.Widget {
				return ui.TextField(branch).Placeholder(T("Branch, such as agent/fix-login")).Name(T("Branch")).
					OnSubmit(func(string) { m.submitNew() })
			}),
			muted(ggui.Text(T("A worktree of the folder's repository, beside it, on a new branch: agents in one repository then keep out of each other's files.")).Size(11.5)),
		).Gap(8).Stretch()),
		ggui.If(hasErr, func() ggui.Widget {
			return ggui.TextOf(m.FormErr).Size(12).Color(tokDotDown)
		}),
		ggui.Row(
			ui.Button(T("Cancel"), func() { m.NewOpen.Set(false) }).Outline(),
			ui.Button(T("Start session"), m.submitNew),
		).Gap(8).Justify(ggui.JustifyEnd),
	).Gap(16).Stretch()).Title(T("New session")).Width(460)
}
