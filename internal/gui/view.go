package gui

import (
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

	"github.com/ironpark/sanbo/internal/protocol"
)

// The window follows Paseo's desktop layout: a sidebar of projects and their
// sessions, a workspace of tabs over the selected session, whose Chat view
// carries the composer, and a panel on the right of the project's files and
// the session's details.
const (
	sidebarWidth = 272
	detailsWidth = 320
	headerHeight = 48
	tabBarHeight = 38
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
		newDrawer(ggui.Row(sidebar(m), vline()).Stretch(), m.SidebarOpen, sidebarWidth+1, false),
		ggui.Expanded(workspace(m)),
		newDrawer(ggui.Row(vline(), details(m)).Stretch(), m.DetailsOpen, detailsWidth+1, true),
	).Stretch()
	return ggui.Stack(
		// The settings page takes the whole window, as Paseo's does; the
		// sessions stay attached while it shows.
		ggui.If(inSettings, func() ggui.Widget { return settingsPage(m) }).
			Else(func() ggui.Widget { return main }),
		newSessionDialog(m),
		renameDialog(m),
		imageViewer(m),
		paletteDialog(m),
		extensionsDialog(m),
		ui.AlertDialog(m.ConfirmOpen, T("Kill this session?"), T("The program is stopped on its node. Its output is lost.")).
			Confirm(T("Kill session"), m.confirmedKill).Destructive(),
		ui.AlertDialog(m.DiscardOpen, T("Close without saving?"), T("Your changes to this file are lost.")).
			Confirm(T("Discard changes"), m.confirmedDiscard).Destructive(),
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
		groupPicker(m),
	).Gap(10)).Pad(0, 10, 0, 16).Height(headerHeight)

	grouping := ggui.Map(m.Prefs, func(p prefs) string { return p.SidebarGroup })
	projects := ggui.Map(m.Prefs, func(p prefs) []fileRoot { return p.Projects }).WithEqual(slices.Equal)
	list := ggui.Reactive(func() ggui.Widget {
		by := grouping.Get()
		nodes := m.Nodes.Get()
		groups := groupSessions(m.Sessions.Get(), by, nodes, projects.Get())
		selected := m.Selected.Get()
		titles := m.Titles.Get()
		if len(groups) == 0 {
			return ggui.Box(muted(ggui.Text(T("No sessions yet. Start one with +.")).Size(12))).Pad(16)
		}
		children := make([]ggui.Widget, 0, len(groups))
		for _, g := range groups {
			children = append(children, sessionSection(m, by, g, selected, nodes, titles))
		}
		return ggui.Column(children...).Gap(6).Stretch()
	})

	return ggui.Box(ggui.Column(
		header,
		hline(),
		ggui.Expanded(ggui.Scroll(ggui.Padding(list, 8, 8)).Bar(tokSurface3)),
		hline(),
		sidebarFooter(m),
	).Stretch()).Width(sidebarWidth).Fill(tokSidebar)
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

// sessionSection is one of the sidebar's groups: a heading for its project
// or host with a button to start another session there, over its sessions.
// The list's one group has no heading.
func sessionSection(m *model, by string, g sessionGroup, selected string, nodes []protocol.Node, titles map[string]string) ggui.Widget {
	nodeID, project := g.NodeID, g.Project
	var rows []ggui.Widget
	indent := by != groupFlat
	if indent {
		name, glyph := projectName(project), "folder"
		var status ggui.Widget = ggui.Box()
		if by == groupHost {
			// A node the mesh no longer lists has gone; its sessions stay
			// until it is back.
			n, up := nodeIn(nodes, nodeID)
			name, glyph = n.Name, "monitor"
			if !up {
				name = short(nodeID)
			}
			if n.Source == "self" {
				glyph = "laptop"
			}
			status = linkDot(up)
		}
		row := []ggui.Widget{
			icon(glyph).Size(15),
			ggui.Box(ggui.Text(name).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis()).MaxWidth(sidebarWidth - 90),
			status,
			ggui.Spacer(),
		}
		// A folder kept with no sessions has nothing to bring it back once
		// it leaves, so its menu can drop it.
		if len(g.Sessions) == 0 {
			ref := fileRoot{Node: nodeID, Path: project}
			row = append(row, borderless(ui.MenuOf(icon("ellipsis").Size(14),
				ui.MenuItem(T("Remove from sidebar"), func() { m.forgetProject(ref) }),
			).Name(T("Folder actions"))))
		}
		row = append(row, ui.Tooltip(ui.ButtonOf(icon("plus").Size(14), func() { m.openNew(nodeID, project) }).
			Ghost().Pad(3).Radius(5).Name(Tf("New session in %s", name)), T("New session here")))
		rows = append(rows, ggui.Box(ggui.Row(row...).Gap(10)).Pad(4, 4, 4, 10))
		if len(g.Sessions) == 0 {
			rows = append(rows, ggui.Padding(ggui.Box(muted(ggui.Text(T("No sessions")).Size(11.5))).Pad(3, 4), 0, 0, 0, 14))
		}
	}
	for _, s := range g.Sessions {
		var where []string
		switch by {
		case groupProject:
			if rel := subfolder(s.Project, project); rel != "" {
				where = append(where, rel)
			}
			if len(nodes) > 1 {
				where = append(where, nodeName(nodes, s.NodeID))
			}
		case groupHost:
			where = append(where, projectName(s.Project))
		default:
			if len(nodes) > 1 {
				where = append(where, nodeName(nodes, s.NodeID))
			}
			where = append(where, projectName(s.Project))
		}
		where = append(where, short(s.ID))
		rows = append(rows, sessionRow(m, s, s.ID == selected, titles[s.ID], strings.Join(where, " · "), indent))
	}
	return ggui.Column(rows...).Gap(2).Stretch()
}

// subfolder is where dir is inside root, or "" for root itself.
func subfolder(dir, root string) string {
	if dir == root || !within(dir, root) {
		return ""
	}
	return strings.TrimLeft(dir[len(root):], `/\`)
}

func sessionRow(m *model, s protocol.SessionInfo, selected bool, title, sub string, indent bool) ggui.Widget {
	id := s.ID
	label := sessionTitle(s, title)
	content := ggui.Row(
		&busyGlyph{icon: agentGlyph(s.Agent, tokMuted).Size(15), busy: func() bool { return m.busy(id) }},
		ggui.Expanded(ggui.Column(
			ggui.Text(label).Size(13).NoWrap().Ellipsis(),
			ggui.Text(sub).Size(11.5).Color(tokExtraMuted).NoWrap().Ellipsis(),
		).Gap(2).Stretch()),
		&activityDot{busy: func() bool { return s.State != protocol.StateExited && m.busy(id) }},
	).Gap(10)
	btn := ui.ButtonOf(content, func() { m.selectSession(id) }).Ghost().Pad(7, 10).Radius(7).Name(label)
	// A row drags into a pane, or to a pane's edge, as a tab does.
	var w ggui.Widget = sessionMenu(m, btn, id, label, false)
	if selected {
		w = ggui.Box(btn).Fill(tokSidebarSel).Radius(7)
	}
	w = sessionDrag(m, w, id)
	if indent {
		w = ggui.Padding(w, 0, 0, 0, 14)
	}
	return w
}

func sidebarFooter(m *model) ggui.Widget {
	host := ggui.Reactive(func() ggui.Widget {
		c, d := m.Conn.Get(), m.DaemonState.Get()
		name := c.Self.Name
		switch {
		case d.Starting:
			name = T("Starting daemon…")
		case !c.OK:
			name = T("Daemon unreachable")
		case d.Running:
			name += " · in app"
		}
		return ggui.Row(linkDot(c.OK), muted(ggui.Text(name).Size(13).NoWrap().Ellipsis())).Gap(10)
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
		if id := m.Selected.Get(); isDocTab(id) {
			return docHeader(m, id, m.Docs.Get()[id])
		}
		s, ok := sessionIn(m.Sessions.Get(), m.Selected.Get())
		if !ok {
			return ggui.Text("sanbo").Size(14).Weight(ggui.WeightMedium)
		}
		where := fmt.Sprintf("%s:%s", nodeName(m.Nodes.Get(), s.NodeID), prettyPath(s.Project))
		id := s.ID
		return ggui.Row(
			ggui.Text(projectName(s.Project)).Size(14).Weight(ggui.WeightMedium).NoWrap(),
			muted(ggui.Text(where).Size(13).NoWrap().Ellipsis()),
			borderless(ui.MenuOf(icon("ellipsis"),
				ui.MenuItem(T("Rename…"), func() { m.askRename(id) }),
				ui.MenuItem(T("Detach tab"), func() { m.closeTab(id) }),
				ui.MenuItem(T("Show details"), func() { m.DetailsOpen.Set(true) }),
				ui.MenuDivider(),
				ui.MenuItem(T("Kill session"), func() { m.askKill(id) }),
			).Name(T("Session actions"))),
		).Gap(10)
	})
	return ggui.Box(ggui.Row(
		iconButton("panel-left", T("Toggle sidebar"), func() { ggui.Toggle(m.SidebarOpen) }),
		ggui.Expanded(title),
		iconButton("panel-right", T("Toggle details"), func() { ggui.Toggle(m.DetailsOpen) }),
	).Gap(8)).Pad(0, 10).Height(headerHeight)
}

func sessionIn(sessions []protocol.SessionInfo, id string) (protocol.SessionInfo, bool) {
	for _, s := range sessions {
		if s.ID == id {
			return s, true
		}
	}
	return protocol.SessionInfo{}, false
}

// tab is one of a pane's tabs. The tab a pane shows is raised and
// underlined, in the accent in the current pane.
func tab(m *model, s protocol.SessionInfo, selected, focused bool, title string) ggui.Widget {
	id := s.ID
	label := sessionTitle(s, title)
	fg := tokMuted
	if selected {
		fg = tokFg
	}
	// The tab is one flat cell: its hover, its fill and its underline share
	// the cell's edges, with the content centred in its height.
	content := ggui.Box(ggui.Row(
		&busyGlyph{icon: agentGlyph(s.Agent, fg).Size(14), busy: func() bool { return m.busy(id) }},
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
	field := ui.TextField(draft).
		Placeholder(T("Message the agent, or type a command and press Enter")).
		Name(T("Message")).
		BindDisabled(disabled).
		OnSubmit(func(string) { m.sendComposer(id) })
	pills := ggui.Reactive(func() ggui.Widget {
		s, ok := sessionIn(m.Sessions.Get(), id)
		if !ok {
			return ggui.Box()
		}
		return ggui.Row(
			pill(agentIcon(s.Agent), sessionLabel(s)),
			pill("server", nodeName(m.Nodes.Get(), s.NodeID)),
			pill("circle-dot", stateLabel(s.State)),
		).Gap(6)
	})
	send := ui.ButtonOf(iconColored("arrow-up", uitheme.PrimaryFg).Size(15), func() { m.sendComposer(id) }).
		Pad(6).Radius(14).Name(T("Send")).BindDisabled(disabled)
	// Files dropped on the box, from the desktop or the Files tab, are
	// inserted as paths.
	over := ggui.State(false)
	content := ggui.Column(
		inlineField(field),
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
		for _, p := range ev.Paths() {
			m.insertPath(id, p)
		}
	})
	files := ggui.DropZone(desktop, func(d ggui.Dropped[pathDrag]) {
		over.Set(false)
		m.insertPath(id, d.Value.Path)
	}).OnHover(over.Set)
	// Align fills the height it is given, which would leave the feed above
	// none: the box keeps its own height and the whole pane's width.
	return ggui.Box(files).Pad(10, 16, 14, 16)
}

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

// changesTab is the Changes page's place among the details tabs.
const changesTab = 1

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
		return ggui.Column(
			prop(T("Agent"), sessionLabel(s)),
			prop(T("Node"), nodeName(m.Nodes.Get(), s.NodeID)),
			prop(T("Project"), prettyPath(s.Project)),
			prop(T("Command"), strings.Join(s.Command, " ")),
			prop(T("State"), stateLabel(s.State)),
			prop(T("Exit code"), code),
			prop(T("Size"), fmt.Sprintf("%d × %d", s.Cols, s.Rows)),
			prop(T("Started"), s.StartedAt.Local().Format("Jan 2 15:04:05")),
			prop(T("Uptime"), time.Since(s.StartedAt).Truncate(time.Second).String()),
			prop(T("Session"), s.ID),
		).Gap(14).Stretch()
	})
	nodes := ggui.View(m.Nodes, func(nodes []protocol.Node) ggui.Widget {
		return nodeList(nodes)
	})
	// Built once, so each keeps its scroll and open folders across tabs.
	pages := []ggui.Widget{
		filesPanel(m),
		changesPanel(m),
		ggui.Scroll(ggui.Padding(session, 16)),
		ggui.Scroll(ggui.Padding(nodes, 12)),
	}
	return ggui.Box(ggui.Column(
		panelTabs(m.DetailTab, T("Files"), T("Changes"), T("Session"), T("Nodes")),
		hline(),
		ggui.Expanded(ggui.Key(m.DetailTab, func(i int) ggui.Widget { return pages[min(max(i, 0), len(pages)-1)] })),
	).Stretch()).Width(detailsWidth).Fill(tokSurface0)
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
			btn := ui.ButtonOf(ggui.Box(ggui.Row(ggui.Text(label).Size(13).Color(fg))).Height(headerHeight),
				func() { chosen.Set(i) }).Ghost().Pad(0, 12).Radius(0).Name(label)
			cells[i] = &underlined{child: btn, color: line}
		}
		return ggui.Box(ggui.Row(cells...)).Height(headerHeight).Pad(0, 4)
	})
}

func prop(label, value string) ggui.Widget {
	return ggui.Column(
		ggui.Text(label).Size(11.5).Color(tokExtraMuted),
		ggui.Text(value).Size(13).Selectable(),
	).Gap(3).Stretch()
}

func nodeList(nodes []protocol.Node) ggui.Widget {
	if len(nodes) == 0 {
		return muted(ggui.Text(T("No nodes.")).Size(13))
	}
	rows := make([]ggui.Widget, 0, len(nodes))
	for _, n := range nodes {
		kind := "laptop"
		if n.Source != "self" {
			kind = "monitor"
		}
		rows = append(rows, ggui.Box(ggui.Row(
			icon(kind).Size(16),
			ggui.Expanded(ggui.Column(
				ggui.Row(ggui.Text(n.Name).Size(13).Weight(ggui.WeightMedium), linkDot(true)).Gap(8),
				ggui.Text(fmt.Sprintf("%s · %s/%s · %s", n.Addr, n.OS, n.Arch, n.Source)).Size(11.5).Color(tokExtraMuted).NoWrap().Ellipsis(),
			).Gap(3).Stretch()),
		).Gap(12)).Pad(10).Radius(8).Fill(tokSurface1))
	}
	return ggui.Column(rows...).Gap(8).Stretch()
}

// Dialogs --------------------------------------------------------------------

func newSessionDialog(m *model) ggui.Widget {
	agent := m.Form.Field(func(f *newSession) *string { return &f.Agent })
	node := m.Form.Field(func(f *newSession) *string { return &f.NodeID })
	project := m.Form.Field(func(f *newSession) *string { return &f.Project })
	nodeIDs := ggui.Map(m.Nodes, func(nodes []protocol.Node) []string {
		ids := make([]string, len(nodes))
		for i, n := range nodes {
			ids[i] = n.ID
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
	local := ggui.Derived(func() bool { return m.Form.Get().NodeID == m.Conn.Get().Self.ID })
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
		ggui.If(hasErr, func() ggui.Widget {
			return ggui.TextOf(m.FormErr).Size(12).Color(tokDotDown)
		}),
		ggui.Row(
			ui.Button(T("Cancel"), func() { m.NewOpen.Set(false) }).Outline(),
			ui.Button(T("Start session"), m.submitNew),
		).Gap(8).Justify(ggui.JustifyEnd),
	).Gap(16).Stretch()).Title(T("New session")).Width(460)
}
