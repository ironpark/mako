package gui

import (
	"fmt"
	"image/color"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// The window follows Paseo's desktop layout: a sidebar of projects and their
// sessions, a workspace of tabs over the selected session, whose Chat view
// carries the composer, and a panel on the right of the project's files and
// the session's details.
const (
	sidebarWidth = 272 // the sidebar's in the default layout, until dragged, and the settings page's
	detailsWidth = 320 // the right panel's in the default layout, until dragged
	headerHeight = 38
	tabBarHeight = 38

	minSidebarWidth, maxSidebarWidth = 200, 520
	minDetailsWidth, maxDetailsWidth = 260, 900
	// panelEdgeWidth is a side panel's draggable edge.
	panelEdgeWidth = 4
)

// build draws the window in the language the settings choose, again from
// the top when it changes, so every label is drawn in the new one.
func build(m *model) ggui.Widget {
	lang := ggui.Map(m.Prefs, func(p prefs) string { return i18n.Resolve(p.Language) })
	return ggui.Key(lang, func(code string) ggui.Widget {
		i18n.Set(code)
		return window(m)
	})
}

func window(m *model) ggui.Widget {
	inSettings := ggui.Map(m.Page, func(p string) bool { return p == "settings" })
	// The selected project's forge is followed whatever the document
	// places: the Git tab shows its branch too.
	watchForge(m)
	// The side panels slide open and closed beside the workspace, as the
	// window's layout document places them (winview.go).
	main := windowView(m)
	return widget.Stack(
		// The settings page takes the whole window, as Paseo's does; the
		// sessions stay attached while it shows.
		backdropped(m, windowBackdrop,
			crossfade(inSettings, func(settings bool) ggui.Widget {
				if settings {
					return settingsPage(m)
				}
				return main
			})),
		// Outside the document, so that no layout hides it.
		layoutBar(m),
		pluginAskBar(m),
		pluginAgentBar(m),
		folderPickerDialog(m),
		renameDialog(m),
		projectDialogs(m),
		fileDialogs(m),
		tunnelDialog(m),
		forgeDialogs(m),
		gitAskDialog(m),
		confirmDialog(m),
		homeMigrateDialog(m),
		imageViewer(m),
		shellDialog(m),
		searchDialog(m),
		paletteDialog(m),
		m.toaster,
		quickOpenDialog(m),
		extensionsDialog(m),
	).Expand()
}

// muted is secondary text.
func muted(t *widget.TextWidget) *widget.TextWidget { return t.Color(skin.Muted) }

func hline() ggui.Widget { return ui.Divider() }
func vline() ggui.Widget { return ui.Divider().Vertical() }

// iconButton is a borderless square button around an icon, with a tooltip.
func iconButton(name, label string, onTap func()) ggui.Widget {
	return ui.Tooltip(ui.ButtonOf(icon(name), onTap).Ghost().Pad(6).Radius(6).Name(label), label)
}

// hiddenFilesToggle shows or hides the files whose names start with a dot,
// in every list of files: a button of a dot and a star, in the accent
// while they are shown, quiet while they are hidden.
func hiddenFilesToggle(m *model) ggui.Widget {
	return ggui.View(ggui.Map(m.Prefs, func(p prefs) bool { return p.ShowHidden }), func(shown bool) ggui.Widget {
		label := pick(shown, T("Hide hidden files"), T("Show hidden files"))
		glyph := widget.Text(".*").Font(ggui.DefaultMonoFont()).Size(13).Weight(ggui.WeightSemibold).
			Color(pick[color.Color](shown, skin.AccentBright, skin.ExtraMuted)).NoWrap()
		button := ui.ButtonOf(widget.Box(widget.Center(glyph)).Size(16, 16), m.toggleHidden).Ghost().Pad(6).Radius(6).Name(label)
		return ui.Tooltip(button, label)
	})
}

// statusLabel names what a session's agent is doing, as its hooks said.
func statusLabel(s *protocol.SessionInfo) string {
	switch {
	case s.GetApproval() != nil:
		return Tf("Waiting for approval: %s", s.GetApproval().GetTool())
	case s.GetStatus() == protocol.AgentWorking:
		return T("Working")
	case s.GetStatus() == protocol.AgentCompacting:
		return T("Compacting conversation…")
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
		return dot(skin.DotRunning)
	}
	return dot(skin.DotDown)
}

func dot(c color.Color) ggui.Widget { return widget.Box().Size(7, 7).Radius(3.5).Fill(c) }

// Sidebar --------------------------------------------------------------------

// The sidebar: the space's projects over its agents, a divider between
// them to drag; see sidebartree.go and sidebaragents.go.
func sidebar(m *model) ggui.Widget {
	return ggui.Component(func() ggui.Widget { return sidebarIn(m, spaceTint(m)) })
}

// sidebarIn is the sidebar washed in tint, the colour of its space, from
// the top of its header down.
func sidebarIn(m *model, tint ggui.Readable[color.NRGBA]) ggui.Widget {
	// The traffic lights take the start of the header, where the window
	// has no title bar of its own.
	header := titlebar(m, widget.Box(widget.Row(
		titlebarGap(m.titlebarRoom(), 10),
		icon("messages-square").Size(17),
		widget.Expanded(widget.Align(spaceSwitcher(m)).Left()),
		waitingPill(m),
		iconButton("plus", T("Add a project"), m.addProjectFolder),
	).Gap(6).Align(widget.AlignCenter)).Pad(0, 8, 0, 0).Height(headerHeight))

	st := newSidebarState(m)
	// Two fingers across anywhere on it move the sidebar to the space
	// beside.
	swipe := newSpaceSwipe(m)
	// body is the space by from the one shown: 0 that one, and -1 and 1
	// those beside, there only while a swipe moves the sidebar. Each is
	// made for a space, so its scrolls are that space's.
	body := func(by int) ggui.Widget {
		space := ggui.Derived(func() int {
			p := m.spaces()
			if i := p.Space + by; p.hasSpace(i) && (by == 0 || swipe.moving.Get()) {
				return i
			}
			return -1
		})
		return ggui.Key(space, func(i int) ggui.Widget {
			if i < 0 {
				return widget.Box()
			}
			tree := widget.Scroll(widget.Padding(projectTree(m, st, i), 10, 8, 10, 8)).Bar(skin.Surface3)
			return &sidebarBody{top: tree, grip: sidebarGrip(m), bottom: agentsPanel(m, st, i), split: m.SidebarSplit}
		})
	}
	column := widget.Column(
		header,
		hline(),
		// The spaces' dots stay put over the foot of the agents as a
		// swipe moves the sidebar under them.
		widget.Expanded(widget.Stack(
			swipe.slide(body(-1), body(0), body(1)),
			widget.Padding(widget.Align(spaceDots(m)).Bottom(), 0, 0, 6, 0),
		).Expand()),
		hline(),
		sidebarFooter(m),
	).Stretch()
	washed := ggui.View(tint, func(c color.NRGBA) ggui.Widget {
		return widget.Box(column).Fill(skin.Sidebar).Gradient(spaceWash(c))
	})
	// The menus' faces sit on the sidebar's fill, not the page's.
	return uitheme.Override(func(t *uitheme.Theme) { t.Bg = t.Sidebar }, swipe.area(washed))
}

// The sidebar's projects are sidebarSplit tall until the divider under
// them is dragged, within these bounds; a window too short for that and
// minSidebarAgents of agents under them takes the room from the projects.
const (
	sidebarSplit                     = 300
	minSidebarSplit, maxSidebarSplit = 80, 900
	minSidebarAgents                 = 180
)

// sidebarBody lays the sidebar's projects over the divider and its agents:
// the projects as tall as the divider was dragged, as far as that leaves
// the agents minSidebarAgents, the agents the rest. Each is drawn within
// its own room, so a short window cuts them rather than letting one run
// over the other, or over the foot of the sidebar.
type sidebarBody struct {
	top, grip, bottom ggui.Widget
	split             ggui.Readable[float64]

	topH, gripH, bottomH float64
}

func (b *sidebarBody) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w, h := c.MaxW, c.MaxH
	b.gripH = b.grip.Layout(ggui.Constraints{MinW: w, MaxW: w, MaxH: ggui.Unbounded}, env).H
	b.topH = max(min(b.split.Get(), h-b.gripH-minSidebarAgents), 0)
	b.bottomH = max(h-b.topH-b.gripH, 0)
	b.top.Layout(ggui.Tight(ggui.Sz(w, b.topH)), env)
	b.bottom.Layout(ggui.Tight(ggui.Sz(w, b.bottomH)), env)
	return c.Constrain(ggui.Sz(w, h))
}

func (b *sidebarBody) Paint(dst *ggui.Canvas, r ggui.Rect) {
	x, y, w := r.Origin.X, r.Origin.Y, r.Size.W
	top := ggui.Rct(ggui.Pt(x, y), ggui.Sz(w, b.topH))
	grip := ggui.Rct(ggui.Pt(x, y+b.topH), ggui.Sz(w, b.gripH))
	bottom := ggui.Rct(ggui.Pt(x, y+b.topH+b.gripH), ggui.Sz(w, b.bottomH))
	dst.Clip(top).Paint(b.top, top)
	dst.Paint(b.grip, grip)
	dst.Clip(bottom).Paint(b.bottom, bottom)
}

// sidebarGrip is the divider between the sidebar's projects and its
// agents, which drags to give either more room; its handle shows under the
// pointer.
func sidebarGrip(m *model) ggui.Widget {
	hover := ggui.State(false)
	handle := ggui.View(hover, func(h bool) ggui.Widget {
		c := pick[color.Color](h, skin.Surface3, color.Transparent)
		return widget.Box(widget.Center(widget.Box().Size(32, 3).Radius(1.5).Fill(c))).Height(8)
	})
	var from, start float64
	return widget.Pointer(widget.Column(hline(), handle).Stretch()).Cursor(ggui.CursorShapeNSResize).OnHover(hover.Set).
		OnDown(func(ev ggui.PointerEvent) { from, start = ev.Pos.Y, ggui.Peek(m.SidebarSplit) }).
		OnDrag(func(ev ggui.PointerEvent) {
			m.SidebarSplit.Set(min(max(start+ev.Pos.Y-from, minSidebarSplit), maxSidebarSplit))
		}).
		OnUp(func(ggui.PointerEvent) {
			h := ggui.Peek(m.SidebarSplit)
			m.Prefs.Update(func(p prefs) prefs { p.SidebarSplit = h; return p })
		})
}

// addProjectFolder asks for a folder on this machine and keeps it in the
// sidebar, a project of its own, given a look at random, unless a project
// holds it already; either is picked.
func (m *model) addProjectFolder() {
	self := ggui.Peek(m.Conn).Self.GetId()
	fail := func(why string) { m.toaster.Push(ui.Toast(T("Could not open the folder picker"), why).Destructive()) }
	m.pickFolderFor(ggui.State(false), self, "", fail, func(dir string) {
		if dir == "" {
			return
		}
		r := keptProject{Node: self, Path: dir}
		m.Prefs.Update(func(p prefs) prefs {
			if !slices.ContainsFunc(p.Projects, func(o keptProject) bool { return o.fileRoot == r.fileRoot }) {
				p.Projects = append(slices.Clone(p.Projects), r)
			}
			return p
		})
		defs := m.projectsNow()
		if pl := placeKept(defs, r); pl.At >= 0 {
			m.setScope(scopeKey{Section: defs[pl.At].ID})
			return
		}
		key := sectionKey(defs, implicitKey(self, dir, ""))
		if _, ok := ggui.Peek(m.Spaces).Looks[key]; !ok {
			m.setLook(key, randomLook())
		}
		// Added in a space, it goes in that one, unless it was moved before.
		set := m.spacesNow()
		if sp, ok := set.spaceOf(set.Space); ok {
			if _, why := set.placeOf(key, nil); why != byProject {
				m.moveToSpace(sp.ID, key)
			}
		}
		m.setScope(scopeKey{Section: key})
	})
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
		mark := icon(osIcon(c.Self.GetOs())).Size(13)
		row := widget.Row(linkDot(c.OK), mark, muted(widget.Text(name).Size(13).NoWrap().Ellipsis())).Gap(8)
		if c.OK && outdated(c.Self) {
			// A daemon from an older build leaves agents' state and
			// approvals out; say so rather than show them blank.
			warn := ui.Tooltip(icon("info").Size(14).Color(skin.DotWaiting),
				T("This daemon is from an older mako. Restart it with this build to see agents' state and approve in the window."))
			row = widget.Row(linkDot(c.OK), mark, widget.Expanded(muted(widget.Text(name).Size(13).NoWrap().Ellipsis())), warn).Gap(8)
		}
		return row
	})
	return widget.Box(widget.Row(
		widget.Expanded(host),
		devicesMenu(m),
		tunnelsMenu(m),
		iconButton("settings", T("Settings"), func() { m.openSettings("") }),
	).Gap(4)).Pad(8, 10, 8, 16).Height(52)
}

// Workspace ------------------------------------------------------------------

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
			return widget.Text("mako").Size(14).Weight(ggui.WeightMedium)
		}
		host, path := nodeName(m.Nodes.Get(), s.GetNodeId()), prettyPath(s.GetProject())
		id := s.GetId()
		row := []ggui.Widget{
			widget.Text(projectName(s.GetProject())).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
			whereCrumb(host, path),
		}
		if tasks := tasksMenu(m, s); tasks != nil {
			row = append(row, tasks)
		}
		return widget.Row(append(row,
			iconMenu(ui.MenuOf(icon("ellipsis"),
				ui.MenuItem(T("Rename…"), func() { m.askRename(id) }),
				ui.MenuItem(T("Detach tab"), func() { m.closeTab(id) }),
				ui.MenuItem(T("Show details"), func() { m.DetailsOpen.Set(true) }),
				ui.MenuDivider(),
				ui.MenuItem(T("Kill session"), func() { m.askKill(id) }),
			).Name(T("Session actions")), 6))...,
		).Gap(10)
	})
	// With the sidebar closed, the header runs from the window's edge, and
	// leaves the traffic lights their room.
	room := ggui.Combine(m.titlebarRoom(), m.SidebarOpen, func(r float64, open bool) float64 {
		return pick(open, 0, r)
	})
	return titlebar(m, widget.Box(widget.Row(
		titlebarGap(room, 2),
		iconButton("panel-left", T("Toggle sidebar"), func() { ggui.Toggle(m.SidebarOpen) }),
		widget.Expanded(title),
		iconButton("panel-right", T("Toggle details"), func() { ggui.Toggle(m.DetailsOpen) }),
	).Gap(8)).Pad(0, 10, 0, 0).Height(headerHeight))
}

// whereCrumb is where a session runs, short: its node on a chip, and the
// last two folders of its path, the whole in a tooltip.
func whereCrumb(host, path string) ggui.Widget {
	short := path
	if parts := strings.Split(path, "/"); len(parts) > 3 {
		short = "…/" + strings.Join(parts[len(parts)-2:], "/")
	}
	chip := widget.Box(muted(widget.Text(strings.TrimSuffix(host, ".local")).Size(11.5).NoWrap())).
		Pad(2, 7).Radius(5).Fill(skin.Surface2)
	return ui.Tooltip(widget.Row(chip, muted(widget.Text(short).Size(13).NoWrap().Ellipsis())).Gap(6).Align(widget.AlignCenter),
		fmt.Sprintf("%s:%s", host, path))
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

// tab is one of a pane's tabs: the session's program and its title, in
// a rounded box, raised as the one the pane shows and ringed in tint, its
// project's colour, in the current pane.
//
// kids are the agents it spawned, shown in it, marked after its glyph.
func tab(m *model, s *protocol.SessionInfo, selected, focused bool, title string, kids []*protocol.SessionInfo) ggui.Widget {
	id := s.GetId()
	label := sessionTitle(s, title)
	fg := pick[color.Color](selected, skin.Fg, skin.Muted)
	dot := m.dotFor(id)
	var glyph ggui.Widget = agentGlyph(s.GetAgent(), fg).Size(14)
	if len(kids) > 0 {
		glyph = widget.Row(glyph, familyMarks(m, kids)).Gap(6).Align(widget.AlignCenter)
	}
	// The dot has room only while it shows, at work, waiting or done.
	t := ui.EditorTab(label, func() { m.selectSession(id) }).
		Status(&activityDot{state: dot, quiet: true}).StatusWhile(func() bool { return dot() != dotRest }).
		Icon(glyph).
		MaxLabelWidth(168).Selected(selected).Current(focused).
		OnClose(func() { m.closeTabButton(id) }).CloseName(Tf("Close %s", label))
	return sessionMenu(m, tabCloseMod(m, id, t), id, label, true)
}

// withAlpha is c at alpha a, out of 0xff; nothing for no colour.
func withAlpha(c color.Color, a uint8) color.NRGBA {
	if c == nil {
		return color.NRGBA{}
	}
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	n.A = uint8(uint16(n.A) * uint16(a) / 0xff)
	return n
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
	return widget.Center(widget.Column(
		icon("square-terminal").Size(36).Color(skin.ExtraMuted),
		widget.Text(T("No session open")).Size(16).Weight(ggui.WeightMedium),
		muted(widget.Text(T("Pick a session in the sidebar, or start an agent on any node in the mesh.")).Size(13)),
		widget.Box().Height(4),
		ui.Button(T("New session"), func() { m.openNew("", "") }).Shortcut("cmd+t"),
	).Gap(10).Align(widget.AlignCenter))
}

// composer is the Chat lens's message box: it types a line into that
// session's live terminal followed by Enter. Terminal mode has no composer;
// the terminal itself takes the keys, as ovis does.
func composer(m *model, id string, state *chatState) ggui.Widget {
	draft := state.draft
	disabled := ggui.Derived(func() bool { return !slices.Contains(m.Attached.Get(), id) })
	// Files dropped, attached or an image pasted go with the message, shown
	// over its text, and are sent by their paths after it.
	insert := func(paths []string) { m.attachTo(id, paths) }
	// Enter sends at once; Shift+Enter breaks the line, and the box grows
	// with the lines to a few before it scrolls. ↑ and ↓ over an empty box
	// bring back what was sent. A draft that begins with a slash lists the
	// agent's commands over the box, whose keys come first.
	field := ui.TextField(draft).Lines(2).
		Placeholder(T("Message the agent. Enter sends, Shift+Enter adds a line")).
		Name(T("Message")).
		BindDisabled(disabled).
		// Tab completes a command or template the menu lists, rather
		// than moving on.
		ClaimKeys(func(ev ggui.KeyEvent) bool { return m.menuClaims(id, state, ev) }).
		OnKey(func(ev ggui.KeyEvent) bool {
			if m.menuKey(id, state, ev) {
				return true
			}
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
	// Files dropped on the box, from the desktop or the Files tab, go
	// with the message.
	over := ggui.State(false)
	content := widget.Column(
		attachmentStrip(m, id, state),
		composerField(field),
		composerBar(m, id, state, disabled),
	).Gap(6).Stretch()
	box := ggui.View(over, func(o bool) ggui.Widget {
		var border color.Color = skin.Border
		if o {
			border = uitheme.Ring
		}
		return widget.Box(content).Fill(skin.Surface1).Border(1, border).Radius(composerRadius).Pad(12, 12, 8, 12)
	})
	desktop := widget.Pointer(box).OnDropHover(over.Set).OnDrop(func(ev ggui.DropEvent) {
		m.handOver(id, ev.Paths(), insert)
	})
	files := widget.DropZone(desktop, func(d widget.Dropped[pathDrag]) {
		over.Set(false)
		m.attachTo(id, []string{d.Value.Path})
	}).OnHover(over.Set)
	// In the transcript's lane, as wide as its text.
	return &lane{child: widget.Box(widget.Column(composerMenu(m, id, state), files).Stretch()).Pad(10, 0, 14, 0)}
}

// composerMaxHeight is as tall as the composer's text grows, about ten
// lines, before it scrolls.
const composerMaxHeight = 220

// composerField wraps a multiline text field so it grows up to
// composerMaxHeight and scrolls smoothly beyond that, both with the mouse
// wheel / scrollbar and as typing or cursor movement moves the caret.
func composerField(field *ui.TextFieldWidget) ggui.Widget {
	tracked := &caretFollow{child: field, input: field.Input()}
	body := widget.Padding(inlineField(tracked), 0, 6, 0, 0)
	return widget.Box(widget.Scroll(body).Shrink().Bar(skin.Surface3)).MaxHeight(composerMaxHeight)
}

// caretFollow observes a multiline TextInputWidget inside a ScrollWidget and
// asks the enclosing Scroll to reveal the caret line whenever the text,
// caret, focus, or layout size changes while focused.
type caretFollow struct {
	child ggui.Widget
	input *widget.TextInputWidget
	env   ggui.Env

	lastText    string
	lastCaret   int
	lastFocused bool
	lastSize    ggui.Size
}

func (w *caretFollow) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	w.env = env
	return w.child.Layout(c, env)
}

func (w *caretFollow) Baseline() (float64, bool) { return ggui.BaselineOf(w.child) }

func (w *caretFollow) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(w.child, r)
	st := w.input.EditingState()
	changed := st.Text != w.lastText || st.Caret != w.lastCaret || st.Focused != w.lastFocused || r.Size != w.lastSize
	w.lastText, w.lastCaret, w.lastFocused, w.lastSize = st.Text, st.Caret, st.Focused, r.Size
	if !st.Focused || !changed {
		return
	}
	win, ok := widget.ScrollWindowOf(w.env)
	if !ok || r.Size.H <= win.Size.H+0.5 {
		return
	}
	top, bot := caretBand(st.Text, st.Caret, r.Size, w.env)
	dst.RequestReveal(ggui.Rct(ggui.Pt(r.Origin.X, r.Origin.Y+top), ggui.Sz(r.Size.W, bot-top)))
}

// caretBand returns the vertical span [top, bot] of the caret's line inside a
// multiline ui.TextField of size, relative to the field's top edge.
func caretBand(text string, caret int, size ggui.Size, env ggui.Env) (top, bot float64) {
	t := uitheme.Of(env)
	padTop, padBottom, padX := t.Space, t.Space, t.FieldPad.Left
	textW := max(size.W-2*padX, 1)
	caret = min(max(caret, 0), len(text))
	prefix := caretPrefix(text, caret, textW, env)
	oneH := widget.Text("M").Layout(ggui.Constraints{MaxW: ggui.Unbounded, MaxH: ggui.Unbounded}, env).H
	prefixH := oneH
	if prefix != "" {
		prefixH = widget.Text(prefix).Layout(ggui.Constraints{MaxW: textW, MaxH: ggui.Unbounded}, env).H
	}
	top = padTop + max(prefixH-oneH, 0)
	bot = padTop + prefixH
	if top <= padTop+1 {
		top = 0
	}
	if bot >= size.H-padBottom-1 {
		bot = size.H
	}
	top = min(max(top, 0), max(size.H-1, 0))
	bot = min(max(bot, top+1), size.H)
	return top, bot
}

// caretPrefix returns the prefix of text whose last wrapped line is the line
// the caret sits on when wrapped at textW.
func caretPrefix(text string, caret int, textW float64, env ggui.Env) string {
	if caret >= len(text) {
		return text
	}
	if text[caret] == '\n' || text[caret] == ' ' || text[caret] == '\t' {
		return text[:caret]
	}
	start := caret
	for start > 0 && text[start-1] != '\n' && text[start-1] != ' ' && text[start-1] != '\t' {
		start--
	}
	end := caret
	for end < len(text) && text[end] != '\n' && text[end] != ' ' && text[end] != '\t' {
		end++
	}
	if widget.Text(text[start:end]).Layout(ggui.Constraints{MaxW: ggui.Unbounded, MaxH: ggui.Unbounded}, env).W <= textW {
		return text[:end]
	}
	return text[:caret]
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

// Details panel --------------------------------------------------------------

// panelTabs is the right panel's strip of tabs: as tall as the workspace
// header, so the line under it runs on from the header's, and drawn as the
// panes' tabs are, flat cells underlined when chosen.
func panelTabs(chosen ggui.Readable[int], choose func(int), labels ...string) ggui.Widget {
	return ggui.View(chosen, func(current int) ggui.Widget {
		cells := make([]ggui.Widget, len(labels))
		for i, label := range labels {
			fg, line := skin.Muted, color.Color(skin.Surface0)
			if i == current {
				fg, line = skin.Fg, skin.AccentBright
			}
			btn := ui.ButtonOf(widget.Box(widget.Row(widget.Text(label).Size(13).Color(fg).NoWrap())).Height(headerHeight),
				func() { choose(i) }).Ghost().Pad(0, 9).Radius(0).Name(label)
			cells[i] = &underlined{child: btn, color: line}
		}
		// Too narrow for them all, the tabs scroll sideways.
		return widget.Box(widget.Scroll(widget.Row(cells...)).Horizontal().Bar(color.Transparent)).Height(headerHeight).Pad(0, 4)
	})
}

func prop(label, value string) ggui.Widget {
	return widget.Column(
		widget.Text(label).Size(11.5).Color(skin.ExtraMuted),
		widget.Text(value).Size(13).Selectable(),
	).Gap(3).Stretch()
}

func nodeList(m *model, nodes []*protocol.Node) ggui.Widget {
	if len(nodes) == 0 {
		return muted(widget.Text(T("No nodes.")).Size(13))
	}
	rows := make([]ggui.Widget, 0, len(nodes))
	for _, n := range nodes {
		kind := "laptop"
		if n.GetSource() != "self" {
			kind = "monitor"
		}
		var ask ggui.Widget = widget.Box()
		switch {
		case reachable(n) && n.GetSource() != "self":
			id := n.GetId()
			actions := []ggui.Widget{
				iconButton("hard-drive", T("Browse files"), func() { m.openBrowser(id, "~", "") }),
				iconButton("arrow-right-left", T("Forward a port…"), func() { m.askTunnel(id) }),
			}
			if linkDown(n) { // reached through another node meanwhile
				actions = append(actions, iconButton("refresh-cw", T("Reconnect"), func() { m.relink(n) }))
			}
			ask = widget.Row(actions...).Gap(2)
		case linkDown(n):
			// Not let in reads the same as not linked: only a link made
			// says which.
			ask = relinkButton(m, n)
		case !reachable(n) && n.GetPeerId() != "":
			ask = ui.Button(T("Ask to be let in"), func() { m.requestAccess(n) }).Outline()
		}
		rows = append(rows, widget.Box(widget.Row(
			icon(kind).Size(16),
			widget.Expanded(widget.Column(
				widget.Row(widget.Text(n.GetName()).Size(13).Weight(ggui.WeightMedium), nodeDot(n), outdatedBadge(n), trustBadge(n, nodes)).Gap(8),
				widget.Text(joinNonEmpty(n.GetAddr(), n.Platform(), n.GetSource())).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis(),
				nodeLinkLine(m, n),
			).Gap(3).Stretch()),
			ask,
		).Gap(12)).Pad(10).Radius(8).Fill(skin.Surface1))
	}
	return widget.Column(rows...).Gap(8).Stretch()
}

// trustBadge says how this node and n stand: whether each lets the other
// in, or which of nodes this one reaches n through.
func trustBadge(n *protocol.Node, nodes []*protocol.Node) ggui.Widget {
	if linkDown(n) && n.Standing() == protocol.StandingNone {
		return widget.Box() // unknown until it is linked
	}
	var label string
	switch n.Standing() {
	case protocol.StandingSelf:
		return widget.Box()
	case protocol.StandingBoth:
		label = T("both ways")
	case protocol.StandingUs:
		label = T("lets this node in")
	case protocol.StandingVia:
		label = Tf("through %s", nodeName(nodes, n.GetVia()))
	case protocol.StandingThem:
		label = T("let in here")
	default:
		label = T("no access")
	}
	return widget.Box(widget.Text(label).Size(11).Color(skin.Muted)).Pad(1, 6).Radius(4).Border(1, skin.Border)
}

// outdated reports whether a node's daemon is from an older build than
// the window's, which leaves newer parts of its replies out.
func outdated(n *protocol.Node) bool { return n.GetRevision() < protocol.Revision }

func outdatedBadge(n *protocol.Node) ggui.Widget {
	if !outdated(n) {
		return widget.Box()
	}
	return widget.Box(widget.Text(T("out of date")).Size(11).Color(skin.DotWaiting)).Pad(1, 6).Radius(4).Border(1, skin.DotWaiting)
}
