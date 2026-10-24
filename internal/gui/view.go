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
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/sanbo/internal/protocol"
)

// The window follows Paseo's desktop layout: a sidebar of projects and their
// sessions, a workspace of tabs over the selected session with a composer
// under it, and an optional details panel on the right.
const (
	sidebarWidth = 272
	detailsWidth = 320
	headerHeight = 48
	tabBarHeight = 38
)

func build(m *model) ggui.Widget {
	inSettings := ggui.Map(m.Page, func(p string) bool { return p == "settings" })
	main := ggui.Row(
		ggui.If(m.SidebarOpen, func() ggui.Widget {
			return ggui.Row(sidebar(m), vline()).Stretch()
		}),
		ggui.Expanded(workspace(m)),
		ggui.If(m.DetailsOpen, func() ggui.Widget {
			return ggui.Row(vline(), details(m)).Stretch()
		}),
	).Stretch()
	return ggui.Stack(
		// The settings page takes the whole window, as Paseo's does; the
		// sessions stay attached while it shows.
		ggui.If(inSettings, func() ggui.Widget { return settingsPage(m) }).
			Else(func() ggui.Widget { return main }),
		newSessionDialog(m),
		ui.AlertDialog(m.ConfirmOpen, "Kill this session?", "The program is stopped on its node. Its output is lost.").
			Confirm("Kill session", m.confirmedKill).Destructive(),
		ui.AlertDialog(m.StopOpen, "Stop the app's daemon?", "The sessions running in it end with it.").
			Confirm("Stop daemon", m.confirmedStop).Destructive(),
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

// stateDot is a session's status: green while it runs, grey once exited.
func stateDot(state protocol.SessionState) ggui.Widget {
	if state == protocol.StateExited {
		return dot(tokDotExited)
	}
	return dot(tokDotRunning)
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
		ggui.Text("Sessions").Size(14).Weight(ggui.WeightMedium),
	).Gap(10)).Pad(14, 16).Height(headerHeight)

	list := ggui.Reactive(func() ggui.Widget {
		groups := groupSessions(m.Sessions.Get())
		selected := m.Selected.Get()
		nodes := m.Nodes.Get()
		titles := m.Titles.Get()
		if len(groups) == 0 {
			return ggui.Box(muted(ggui.Text("No sessions yet. Start one with +.").Size(12))).Pad(16)
		}
		children := make([]ggui.Widget, 0, len(groups))
		for _, g := range groups {
			children = append(children, projectSection(m, g, selected, nodes, titles))
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

func projectSection(m *model, g projectGroup, selected string, nodes []protocol.Node, titles map[string]string) ggui.Widget {
	nodeID, project := g.NodeID, g.Project
	head := ggui.Box(ggui.Row(
		icon("folder").Size(15),
		ggui.Text(projectName(project)).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis(),
		ggui.Spacer(),
		ui.Tooltip(ui.ButtonOf(icon("plus").Size(14), func() { m.openNew(nodeID, project) }).
			Ghost().Pad(3).Radius(5).Name("New session in "+projectName(project)), "New session here"),
	).Gap(10)).Pad(4, 4, 4, 10)

	rows := []ggui.Widget{head}
	for _, s := range g.Sessions {
		rows = append(rows, sessionRow(m, s, s.ID == selected, nodes, titles[s.ID]))
	}
	return ggui.Column(rows...).Gap(2).Stretch()
}

func sessionRow(m *model, s protocol.SessionInfo, selected bool, nodes []protocol.Node, title string) ggui.Widget {
	id := s.ID
	label := sessionLabel(s)
	if title != "" {
		label = title
	}
	sub := fmt.Sprintf("%s · %s", nodeName(nodes, s.NodeID), short(s.ID))
	content := ggui.Row(
		icon(agentIcon(s.Agent)).Size(15),
		ggui.Expanded(ggui.Column(
			ggui.Text(label).Size(13).NoWrap().Ellipsis(),
			ggui.Text(sub).Size(11.5).Color(tokExtraMuted).NoWrap().Ellipsis(),
		).Gap(2).Stretch()),
		stateDot(s.State),
	).Gap(10)
	btn := ui.ButtonOf(content, func() { m.selectSession(id) }).Ghost().Pad(7, 10).Radius(7).Name(label)
	if selected {
		return ggui.Padding(ggui.Box(btn).Fill(tokSidebarSel).Radius(7), 0, 0, 0, 14)
	}
	return ggui.Padding(btn, 0, 0, 0, 14)
}

func sidebarFooter(m *model) ggui.Widget {
	host := ggui.Reactive(func() ggui.Widget {
		c, d := m.Conn.Get(), m.DaemonState.Get()
		name := c.Self.Name
		switch {
		case d.Starting:
			name = "Starting daemon…"
		case !c.OK:
			name = "Daemon unreachable"
		case d.Running:
			name += " · in app"
		}
		return ggui.Row(linkDot(c.OK), muted(ggui.Text(name).Size(13).NoWrap().Ellipsis())).Gap(10)
	})
	return ggui.Box(ggui.Row(
		ggui.Expanded(host),
		iconButton("plus", "New session", func() { m.openNew("", "") }),
		iconButton("settings", "Settings", func() { m.openSettings("") }),
	).Gap(4)).Pad(8, 10, 8, 16).Height(52)
}

// Workspace ------------------------------------------------------------------

func workspace(m *model) ggui.Widget {
	return ggui.Box(ggui.Column(
		workspaceHeader(m),
		hline(),
		tabBar(m),
		hline(),
		ggui.Expanded(body(m)),
		ggui.If(ggui.Map(m.Prefs, func(p prefs) bool { return p.ShowComposer }), func() ggui.Widget { return composer(m) }),
	).Stretch()).Fill(tokSurface0)
}

func workspaceHeader(m *model) ggui.Widget {
	title := ggui.Reactive(func() ggui.Widget {
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
				ui.MenuItem("Detach tab", func() { m.closeTab(id) }),
				ui.MenuItem("Show details", func() { m.DetailsOpen.Set(true) }),
				ui.MenuDivider(),
				ui.MenuItem("Kill session", func() { m.askKill(id) }),
			).Name("Session actions")),
		).Gap(10)
	})
	return ggui.Box(ggui.Row(
		iconButton("panel-left", "Toggle sidebar", func() { ggui.Toggle(m.SidebarOpen) }),
		ggui.Expanded(title),
		iconButton("panel-right", "Toggle details", func() { ggui.Toggle(m.DetailsOpen) }),
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

// tabBar lists the open sessions of the selected session's project, as
// Paseo's workspace tabs do, with quick launchers on the right.
func tabBar(m *model) ggui.Widget {
	tabs := ggui.Reactive(func() ggui.Widget {
		sessions := m.Sessions.Get()
		selected := m.Selected.Get()
		titles := m.Titles.Get()
		cur, ok := sessionIn(sessions, selected)
		var children []ggui.Widget
		for _, id := range m.Tabs.Get() {
			s, found := sessionIn(sessions, id)
			if !found || (ok && (s.Project != cur.Project || s.NodeID != cur.NodeID)) {
				continue
			}
			children = append(children, tab(m, s, id == selected, titles[id]))
		}
		var launch ggui.Widget = ggui.Box()
		if ok {
			nodeID, project := cur.NodeID, cur.Project
			launch = ggui.Row(
				iconButton("square-terminal", "New terminal", func() { m.launch("shell", nodeID, project) }),
				iconButton("sparkles", "New Claude session", func() { m.launch("claude", nodeID, project) }),
				iconButton("plus", "New session…", func() { m.openNew(nodeID, project) }),
			).Gap(2)
		}
		return ggui.Row(
			ggui.Expanded(ggui.Row(children...).Stretch()),
			ggui.Padding(launch, 0, 8),
		).Stretch()
	})
	return ggui.Box(tabs).Height(tabBarHeight)
}

func tab(m *model, s protocol.SessionInfo, selected bool, title string) ggui.Widget {
	id := s.ID
	label := sessionLabel(s)
	if title != "" {
		label = title
	}
	fg := tokMuted
	if selected {
		fg = tokFg
	}
	content := ggui.Row(
		iconColored(agentIcon(s.Agent), fg).Size(14),
		ggui.Box(ggui.Text(label).Size(13).Color(fg).NoWrap().Ellipsis()).MaxWidth(150),
		ui.ButtonOf(icon("x").Size(13), func() { m.closeTab(id) }).Ghost().Pad(2).Radius(4).Name("Close "+label),
	).Gap(8)
	btn := ui.ButtonOf(content, func() { m.selectSession(id) }).Ghost().Pad(5, 8, 5, 10).Radius(6).Name(label)
	// The selected tab is raised onto the workspace surface and underlined,
	// as Paseo marks the active one.
	underline := tokSurface0
	fill := tokSurface0
	if selected {
		underline, fill = tokAccentBright, tokSurface1
	}
	cell := ggui.Box(&underlined{child: ggui.Row(btn), color: underline}).
		Height(tabBarHeight).Pad(0, 4).Fill(fill)
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

func body(m *model) ggui.Widget {
	current := ggui.Derived(func() pane {
		id := m.Selected.Get()
		return pane{ID: id, Ready: slices.Contains(m.Attached.Get(), id)}
	})
	return ggui.Key(current, func(p pane) ggui.Widget {
		switch {
		case p.ID == "":
			return emptyState(m)
		case !p.Ready:
			// Without a daemon there is nothing to attach to; say why.
			return ggui.View(m.Conn, func(c connState) ggui.Widget {
				if !c.OK && c.Err != "" {
					return ggui.Center(ggui.Column(
						icon("server").Size(28).Color(tokExtraMuted),
						muted(ggui.Text(c.Err).Size(13)),
						ui.Button("Daemon settings", func() { m.openSettings("daemon") }).Outline(),
					).Gap(12).Align(ggui.AlignCenter))
				}
				return ggui.Center(ggui.Column(ui.Spinner(), muted(ggui.Text("Attaching…").Size(13))).Gap(12).Align(ggui.AlignCenter))
			})
		default:
			return newTerminalView(m.conns[p.ID], func() prefs { return ggui.Peek(m.Prefs) }, true)
		}
	})
}

func emptyState(m *model) ggui.Widget {
	return ggui.Center(ggui.Column(
		icon("square-terminal").Size(36).Color(tokExtraMuted),
		ggui.Text("No session open").Size(16).Weight(ggui.WeightMedium),
		muted(ggui.Text("Pick a session in the sidebar, or start an agent on any node in the mesh.").Size(13)),
		ggui.Box().Height(4),
		ui.Button("New session", func() { m.openNew("", "") }).Shortcut("cmd+t"),
	).Gap(10).Align(ggui.AlignCenter))
}

// composer types a line into the selected session: Paseo's message box,
// which here becomes the program's input followed by Enter.
func composer(m *model) ggui.Widget {
	disabled := ggui.Derived(func() bool { return !slices.Contains(m.Attached.Get(), m.Selected.Get()) })
	field := ui.TextField(m.Composer).
		Placeholder("Message the agent, or type a command and press Enter").
		Name("Message").
		BindDisabled(disabled).
		OnSubmit(func(string) { m.sendComposer() })
	pills := ggui.Reactive(func() ggui.Widget {
		s, ok := sessionIn(m.Sessions.Get(), m.Selected.Get())
		if !ok {
			return ggui.Box()
		}
		return ggui.Row(
			pill(agentIcon(s.Agent), sessionLabel(s)),
			pill("server", nodeName(m.Nodes.Get(), s.NodeID)),
			pill("circle-dot", string(s.State)),
		).Gap(6)
	})
	send := ui.ButtonOf(iconColored("arrow-up", uitheme.PrimaryFg).Size(15), m.sendComposer).
		Pad(6).Radius(14).Name("Send").BindDisabled(disabled)
	box := ggui.Box(ggui.Column(
		inlineField(field),
		ggui.Row(pills, ggui.Spacer(), send).Gap(8),
	).Gap(8).Stretch()).Fill(tokSurface1).Border(1, tokBorderAccent).Radius(12).Pad(10, 12)
	return ggui.Box(box).Pad(10, 16, 14, 16)
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

func details(m *model) ggui.Widget {
	session := ggui.Reactive(func() ggui.Widget {
		s, ok := sessionIn(m.Sessions.Get(), m.Selected.Get())
		if !ok {
			return ggui.Box(muted(ggui.Text("No session selected.").Size(13))).Pad(16)
		}
		code := "-"
		if s.ExitCode != nil {
			code = fmt.Sprint(*s.ExitCode)
		}
		return ggui.Column(
			prop("Agent", sessionLabel(s)),
			prop("Node", nodeName(m.Nodes.Get(), s.NodeID)),
			prop("Project", prettyPath(s.Project)),
			prop("Command", strings.Join(s.Command, " ")),
			prop("State", string(s.State)),
			prop("Exit code", code),
			prop("Size", fmt.Sprintf("%d × %d", s.Cols, s.Rows)),
			prop("Started", s.StartedAt.Local().Format("Jan 2 15:04:05")),
			prop("Uptime", time.Since(s.StartedAt).Truncate(time.Second).String()),
			prop("Session", s.ID),
		).Gap(14).Stretch()
	})
	nodes := ggui.View(m.Nodes, func(nodes []protocol.Node) ggui.Widget {
		return nodeList(nodes)
	})
	return ggui.Box(ggui.Column(
		ggui.Box(ui.Tabs(m.DetailTab,
			ui.Tab("Session", ggui.Scroll(ggui.Padding(session, 16))),
			ui.Tab("Nodes", ggui.Scroll(ggui.Padding(nodes, 12))),
		).Line()).Pad(8, 8, 0, 8),
	).Stretch()).Width(detailsWidth).Fill(tokSurface0)
}

func prop(label, value string) ggui.Widget {
	return ggui.Column(
		ggui.Text(label).Size(11.5).Color(tokExtraMuted),
		ggui.Text(value).Size(13).Selectable(),
	).Gap(3).Stretch()
}

func nodeList(nodes []protocol.Node) ggui.Widget {
	if len(nodes) == 0 {
		return muted(ggui.Text("No nodes.").Size(13))
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
	browse := ui.Button("Browse…", func() {
		host := ggui.UseHost()
		start := ggui.Peek(project)
		ggui.Async(func() (string, error) {
			return host.Dialogs().PickFolder(runtime.FileDialog{Title: "Working directory", Directory: start})
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
		ui.Field("Agent", ui.Select(agent).BindOptions(targets).Format(targetLabel).Name("Agent")),
		ui.Field("Node", ui.Select(node).BindOptions(nodeIDs).Format(func(id string) string {
			return nodeName(ggui.Peek(m.Nodes), id)
		}).Name("Node")),
		ui.Field("Working directory", ggui.Row(
			ggui.Expanded(ui.TextField(project).Name("Working directory").OnSubmit(func(string) { m.submitNew() })),
			ggui.If(local, func() ggui.Widget { return browse }),
		).Gap(8)),
		ggui.If(hasErr, func() ggui.Widget {
			return ggui.TextOf(m.FormErr).Size(12).Color(tokDotDown)
		}),
		ggui.Row(
			ui.Button("Cancel", func() { m.NewOpen.Set(false) }).Outline(),
			ui.Button("Start session", m.submitNew),
		).Gap(8).Justify(ggui.JustifyEnd),
	).Gap(16).Stretch()).Title("New session").Width(460)
}
