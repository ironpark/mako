package gui

import (
	"path/filepath"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// The settings page follows Paseo's: it takes the window, a sidebar lists
// the sections, and each section is a column of titled cards whose rows put
// a title and a hint on the left and the control on the right.
//
// App settings (gui.json) apply as they change. This node's settings are the
// daemon's config.json, edited as a draft and saved together, since the
// daemon reads them only when it starts.

type settingsSection struct {
	id, label, icon string
}

type settingsGroup struct {
	title    string
	sections []settingsSection
	bottom   bool // pinned at the bottom of the sidebar
}

var settingsNav = []settingsGroup{
	{title: "App", sections: []settingsSection{
		{"general", "General", "settings"},
		{"appearance", "Appearance", "palette"},
		{"spaces", "Spaces", "layout-grid"},
		{"shortcuts", "Shortcuts", "keyboard"},
		{"about", "About", "info"},
	}},
	{title: "Agents & extensions", sections: []settingsSection{
		{"agents", "Agents", "bot"},
		{"apikeys", "API keys", "key-round"},
		{"teams", "Teams", "users"},
		{"templates", "Templates", "file-text"},
		{"extensions", "Extensions", "layers"},
	}},
	{title: "Node & mesh", sections: []settingsSection{
		{"daemon", "Daemon", "server"},
		{"shell", "Shell", "square-terminal"},
		{"nodes", "Nodes", "network"},
		{"access", "Access", "lock"},
	}},
	{title: "Developer", bottom: true, sections: []settingsSection{
		{"developer", "Developer", "code-xml"},
	}},
}

// canonicalSettingsSection maps legacy section IDs that were merged into
// another section onto their current destination.
func canonicalSettingsSection(id string) string {
	switch id {
	case "terminal":
		return "appearance"
	case "host", "discovery":
		return "daemon"
	default:
		return id
	}
}

// The choices of the settings' toggles, by value, in English for T.
var (
	themeLabels      = map[string]string{"system": "System", "light": "Light", "dark": "Dark"}
	cursorLabels     = map[string]string{"block": "Block", "bar": "Bar", "underline": "Underline"}
	daemonModeLabels = map[string]string{"auto": "Auto", "embedded": "In this app", "external": "External"}
	openViewLabels   = map[string]string{openTerminal: "Terminal", openChat: "Chat"}
)

func settingsPage(m *model) ggui.Widget {
	content := ggui.Key(m.Section, func(id string) ggui.Widget {
		return widget.Scroll(widget.Align(widget.Box(widget.Padding(settingsContent(m, id), 32, 40)).MaxWidth(settingsMaxWidth)).Top()).
			Bar(skin.Surface3)
	})
	return widget.Row(
		settingsSidebar(m),
		vline(),
		widget.Expanded(widget.Box(content).Fill(skin.Surface0)),
	).Stretch()
}

func settingsSidebar(m *model) ggui.Widget {
	back := ui.ButtonOf(widget.Row(icon("arrow-left").Size(16), widget.Text(T("Back to sessions")).Size(13)).Gap(10),
		func() { m.Page.Set("workspace") }).Ghost().Pad(7, 10).Radius(7).Name(T("Back to sessions"))
	header := titlebar(m, widget.Box(widget.Row(titlebarGap(m.titlebarRoom(), 8), back)).Pad(8, 8, 8, 0).Height(headerHeight))

	// Plugins is listed while the daemon has one, or registries to
	// install one from.
	hasPlugins := ggui.Derived(func() bool {
		return len(m.plugins.List.Get()) > 0 || m.Conn.Get().Self.GetRevision() >= protocol.RevisionPluginStore
	})
	nav := ggui.View(hasPlugins, func(plugins bool) ggui.Widget {
		var main []settingsGroup
		for _, g := range settingsGroups(plugins) {
			if !g.bottom {
				main = append(main, g)
			}
		}
		return settingsNavList(m, main)
	})
	var bottom []settingsSection
	for _, g := range settingsNav {
		if g.bottom {
			bottom = append(bottom, g.sections...)
		}
	}

	cols := []ggui.Widget{
		header,
		hline(),
		widget.Expanded(widget.Scroll(widget.Padding(nav, 4, 8))),
	}
	if len(bottom) > 0 {
		cols = append(cols, hline(), widget.Padding(settingsNavFooter(m, bottom), 8, 8))
	}
	return widget.Box(widget.Column(cols...).Stretch()).Width(sidebarWidth).Fill(skin.Sidebar)
}

// settingsNavList is the settings' sections, the one shown marked.
func settingsNavList(m *model, groups []settingsGroup) ggui.Widget {
	return ggui.View(m.Section, func(current string) ggui.Widget {
		current = canonicalSettingsSection(current)
		var rows []ggui.Widget
		for _, g := range groups {
			rows = append(rows, widget.Box(widget.Text(T(g.title)).Size(11.5).Color(skin.ExtraMuted)).Pad(14, 10, 4, 10))
			for _, s := range g.sections {
				rows = append(rows, settingsNavItem(m, s, current))
			}
		}
		return widget.Column(rows...).Gap(2).Stretch()
	})
}

// settingsNavFooter is the settings sections pinned at the bottom of the
// sidebar, separated from the scrollable groups above.
func settingsNavFooter(m *model, sections []settingsSection) ggui.Widget {
	return ggui.View(m.Section, func(current string) ggui.Widget {
		current = canonicalSettingsSection(current)
		rows := make([]ggui.Widget, 0, len(sections))
		for _, s := range sections {
			rows = append(rows, settingsNavItem(m, s, current))
		}
		return widget.Column(rows...).Gap(2).Stretch()
	})
}

func settingsNavItem(m *model, s settingsSection, current string) ggui.Widget {
	id := s.id
	fg := skin.Muted
	if id == current {
		fg = skin.Fg
	}
	// The row runs the button's width, so its label stays at the
	// start, as the chosen one's does, rather than centred in it.
	item := ui.ButtonOf(widget.Row(
		iconColored(s.icon, fg).Size(15),
		widget.Text(T(s.label)).Size(13).Color(fg),
		widget.Spacer(),
	).Gap(10), func() { m.Section.Set(id) }).Ghost().Pad(7, 10).Radius(7).Name(T(s.label))
	if id == current {
		return widget.Box(item).Fill(skin.SidebarSel).Radius(7)
	}
	return item
}

func settingsContent(m *model, id string) ggui.Widget {
	switch canonicalSettingsSection(id) {
	case "appearance":
		return appearanceSettings(m)
	case "spaces":
		return spacesSettings(m)
	case "shortcuts":
		return shortcutsSettings(m)
	case "templates":
		return templateSettings(m)
	case "teams":
		return teamSettings(m)
	case "extensions":
		return extensionSettings(m)
	case "daemon":
		return daemonSettings(m)
	case "agents":
		return agentSettings(m)
	case "apikeys":
		return providerSettings(m)
	case "shell":
		return shellSettings(m)
	case "nodes":
		return nodeSettings(m)
	case "access":
		return accessSettings(m)
	case "about":
		return aboutSettings(m)
	case "developer":
		return developerSettings(m)
	case pluginsSection.id:
		return pluginSettings(m)
	}
	return generalSettings(m)
}

// Building blocks -------------------------------------------------------------

// card groups rows on a raised surface, a hairline between each.
func card(rows ...ggui.Widget) ggui.Widget { return cardWithTail(nil, rows...) }

// cardWithTail is card with tail after the rows and no hairline of its
// own, for rows that come and go and draw their own.
func cardWithTail(tail ggui.Widget, rows ...ggui.Widget) ggui.Widget {
	children := make([]ggui.Widget, 0, 2*len(rows)+1)
	for i, r := range rows {
		if i > 0 {
			children = append(children, hline())
		}
		children = append(children, r)
	}
	if tail != nil {
		children = append(children, tail)
	}
	return widget.Box(widget.Column(children...).Stretch()).Fill(skin.Surface1).Border(1, skin.Border).Radius(10)
}

// row is one setting: what it is on the left, its control on the right.
func row(title, hint string, control ggui.Widget) ggui.Widget {
	text := []ggui.Widget{widget.Text(title).Size(13.5)}
	if hint != "" {
		text = append(text, muted(widget.Text(hint).Size(12)))
	}
	return widget.Box(widget.Row(
		widget.Expanded(widget.Column(text...).Gap(3).Stretch()),
		control,
	).Gap(24)).Pad(14, 16)
}

// stackedRow puts a wide control under its title, for text areas.
func stackedRow(title, hint string, control ggui.Widget) ggui.Widget {
	text := []ggui.Widget{widget.Text(title).Size(13.5)}
	if hint != "" {
		text = append(text, muted(widget.Text(hint).Size(12)))
	}
	return widget.Box(widget.Column(append(text, widget.Box().Height(4), control)...).Gap(3).Stretch()).Pad(14, 16)
}

// browseButton is a Browse… button that sets b to what pick chooses in the
// platform's dialog, unless the dialog is cancelled.
func browseButton(b ggui.Binding[string], pick func(runtime.Dialogs) (string, error)) *ui.ButtonWidget {
	return ui.Button(T("Browse…"), func() { browseInto(b.Set, pick) }).Outline()
}

// browseInto gives set what pick chooses in the host's dialogs, unless it
// is nothing.
func browseInto(set func(string), pick func(runtime.Dialogs) (string, error)) {
	dialogs := ggui.UseHost().Dialogs()
	ggui.Async(func() (string, error) { return pick(dialogs) }, func(v string, err error) {
		if err == nil && v != "" {
			set(v)
		}
	})
}

func prefField[T any](m *model, f func(*prefs) *T) ggui.Binding[T] {
	return m.Prefs.Field(f)
}

// App sections ----------------------------------------------------------------

func generalSettings(m *model) ggui.Widget {
	agent := prefField(m, func(p *prefs) *string { return &p.DefaultAgent })
	project := prefField(m, func(p *prefs) *string { return &p.DefaultProject })
	browse := browseButton(project, func(d runtime.Dialogs) (string, error) {
		return d.PickFolder(runtime.FileDialog{Title: T("Default working directory"), Directory: ggui.Peek(project)})
	})
	daemon := prefField(m, func(p *prefs) *string { return &p.Daemon })
	status := ggui.View(m.Conn, func(c connState) ggui.Widget {
		if !c.OK {
			msg := T("Not connected")
			if c.Err != "" {
				msg = c.Err
			}
			return widget.Row(linkDot(false), muted(widget.Text(msg).Size(12.5).NoWrap().Ellipsis())).Gap(8)
		}
		return widget.Row(linkDot(true), muted(widget.Text(Tf("Connected to %s", c.Self.GetName())).Size(12.5))).Gap(8)
	})

	codes := make([]string, 0, len(i18n.Languages)+1)
	codes = append(codes, "system")
	names := map[string]string{"system": T("System")}
	for _, l := range i18n.Languages {
		codes = append(codes, l.Code)
		names[l.Code] = l.Name
	}
	language := ui.Select(prefField(m, func(p *prefs) *string { return &p.Language })).
		Options(codes).Format(func(code string) string { return names[code] }).Name(T("Language"))

	sleepAfter := ggui.Bind(
		func() float64 { return float64(m.Prefs.Get().SleepAfter) },
		func(v float64) { m.Prefs.Update(func(p prefs) prefs { p.SleepAfter = int(v); return p.normalized() }) },
	)

	languageSection := section(T("Language"),
		card(
			row(T("Language"), T("The language the window is written in. System follows the desktop's."),
				widget.Box(language).Width(200)),
		),
	)
	sessionsSection := section(T("Sessions"),
		card(
			row(T("Default agent"), T("Chosen first in the new-session dialog."),
				widget.Box(ui.Select(agent).BindOptions(m.Agents).Format(targetLabel).Name(T("Default agent"))).Width(200)),
			row(T("Open agents in"), T("What a session's tab shows until you switch it. Chat is for agents whose conversation mako reads; the others open in their terminals."),
				ui.ToggleGroup(prefField(m, func(p *prefs) *string { return &p.OpenView })).Options([]string{openTerminal, openChat}).
					Format(func(s string) string { return T(openViewLabels[s]) }).Name(T("Open agents in"))),
			stackedRow(T("Default working directory"), T("Where a new session starts unless you pick another."),
				widget.Row(widget.Expanded(ui.TextField(project).Name(T("Default working directory"))), browse).Gap(8)),
			row(T("Follow other nodes"), T("Share the spaces, projects, pins, tabs and panes with the nodes this one trusts both ways. Off, this node keeps its own."),
				ui.Switch(ggui.Bind(m.WorkspaceSync.Get, m.setWorkspaceSync), "").Name(T("Follow other nodes"))),
			row(T("Confirm before killing"), T("Ask before a session is stopped from the window."),
				ui.Switch(prefField(m, func(p *prefs) *bool { return &p.ConfirmKill }), "").Name(T("Confirm before killing"))),
			row(T("Approve in the window"), T("While the window is open, agents that ask through mako ask for approvals here, in Chat and over the terminal. Otherwise they ask in their terminals."),
				ui.Switch(prefField(m, func(p *prefs) *bool { return &p.ApproveInApp }), "").Name(T("Approve in the window"))),
			row(T("Notifications"), T("Tell the desktop when an agent you are not looking at finishes or asks for you."),
				ui.Switch(prefField(m, func(p *prefs) *bool { return &p.Notify }), "").Name(T("Notifications"))),
			row(T("Keep in the menu bar"), T("Closing the window hides it to an icon in the menu bar, and the sessions and the app's daemon carry on. Quit from the icon or with ⌘Q."),
				ui.Switch(prefField(m, func(p *prefs) *bool { return &p.Tray }), "").Name(T("Keep in the menu bar"))),
			row(T("Hide from the Dock"), T("While the window is hidden in the menu bar, the app leaves the Dock and ⌘-Tab. It comes back with the window."),
				ui.Switch(prefField(m, func(p *prefs) *bool { return &p.HideDock }), "").Name(T("Hide from the Dock")).
					BindDisabled(ggui.Map(m.Prefs, func(p prefs) bool { return !p.Tray }))),
		),
	)
	idleSection := section(T("Idle agents"),
		card(
			row(T("Put idle agents to sleep"), T("Stop the program of an agent out of view once it has rested at its prompt, with no subagent, background task or session of its own at work and nothing asked of you, to free its memory. It picks its conversation up again when you open it."),
				ui.Switch(prefField(m, func(p *prefs) *bool { return &p.SleepIdle }), "").Name(T("Put idle agents to sleep"))),
			row(T("After"), T("Minutes at rest before an agent is put to sleep."),
				widget.Box(ui.NumberInput(sleepAfter).Range(minSleepAfter, maxSleepAfter).Step(1).Name(T("Minutes before sleep")).
					BindDisabled(ggui.Map(m.Prefs, func(p prefs) bool { return !p.SleepIdle }))).Width(120)),
		),
	)
	connSection := section(T("Connection"),
		card(
			row(T("External daemon"), T("The socket of the makod to use when the daemon does not run in the app. Empty is this machine's; it relays to the rest of the mesh."),
				widget.Box(ui.TextField(daemon).Placeholder("daemon.sock").Name(T("External daemon")).
					BindDisabled(ggui.Map(m.Prefs, func(p prefs) bool { return p.DaemonMode == "embedded" }))).Width(220)),
			widget.Box(status).Pad(12, 16),
		),
	)

	return widePage(T("General"), T("How new sessions start and which daemon this window talks to."),
		twoColumns(
			[]ggui.Widget{languageSection, sessionsSection, idleSection, connSection},
			[]int{1},
			[]int{0, 2, 3},
		),
	)
}

// Node & mesh -----------------------------------------------------------------

func hostField(m *model, f func(*hostDraft) *string) ggui.Binding[string] { return m.Host.Field(f) }

// saveBar is the foot of a page that edits the daemon's configuration.
func saveBar(m *model) ggui.Widget {
	// The saved configuration is no signal; reading the note, which every
	// save and revert writes, keeps the comparison current.
	dirty := ggui.Derived(func() bool {
		_ = m.HostNote.Get()
		return m.Host.Get() != draftFrom(m.cfg) || !sameAgents(m.AgentDraft.Get(), draftAgents(m.cfg.Agents))
	})
	note := ggui.View(m.HostNote, func(s string) ggui.Widget {
		if s == "" {
			return muted(widget.Text(T("Saved to config.json. The daemon reads agents as they change and the rest when it starts.")).Size(12))
		}
		return widget.Text(s).Size(12).Color(skin.AccentBright)
	})
	embedded := ggui.Map(m.DaemonState, func(s daemonState) bool { return s.Running })
	return widget.Row(
		widget.Expanded(note),
		ggui.If(embedded, func() ggui.Widget {
			return ui.Button(T("Restart daemon"), func() { m.guardStop(m.restartEmbedded) }).Outline()
		}),
		ui.Button(T("Revert"), m.revertHost).Outline().BindDisabled(ggui.Not(dirty)),
		ui.Button(T("Save"), m.saveHost).BindDisabled(ggui.Not(dirty)),
	).Gap(8)
}

// daemonSettings brings together how this node's daemon runs, how it presents
// itself on the mesh (identity, listen address, libp2p), and how it discovers
// peers, plus the embedded daemon log.
func daemonSettings(m *model) ggui.Widget {
	mode := ui.ToggleGroup(ggui.Bind(
		func() string { return m.Prefs.Get().DaemonMode },
		m.setDaemonMode,
	)).Options([]string{"auto", "embedded", "external"}).
		Format(func(s string) string { return T(daemonModeLabels[s]) }).Name(T("Daemon mode"))

	status := ggui.View(m.DaemonState, func(s daemonState) ggui.Widget {
		var title, hint string
		ok := true
		switch {
		case s.Running:
			title = T("Running in this app")
			hint = Tf("Listening on %s. Its sessions end when it stops or the app quits.", s.Addr)
		case s.Starting:
			title, hint = T("Starting…"), ""
		case s.Mode == "external":
			title = T("Using an external daemon")
			hint = Tf("%s. Sessions keep running after the app quits.", s.Addr)
		case s.Mode == "embedded":
			title, ok = T("Stopped"), false
			hint = T("The app's daemon is not running.")
		default:
			title, hint = T("Looking for a daemon…"), ""
		}
		if s.Err != "" {
			ok = false
			hint = s.Err
			if s.InUse {
				hint += T(". Another makod runs on this configuration; choose External or Auto.")
			}
		}
		text := []ggui.Widget{widget.Row(linkDot(ok), widget.Text(title).Size(13.5)).Gap(8)}
		if hint != "" {
			text = append(text, widget.Box(muted(widget.Text(hint).Size(12))).Pad(0, 0, 0, 15))
		}
		var actions ggui.Widget = widget.Box()
		if s.Mode == "embedded" {
			if s.Running {
				actions = widget.Row(
					ui.Button(T("Restart"), func() { m.guardStop(m.restartEmbedded) }).Outline(),
					ui.Button(T("Stop"), func() { m.guardStop(m.stopEmbedded) }).Outline(),
				).Gap(8)
			} else if !s.Starting {
				actions = ui.Button(T("Start"), m.startEmbedded)
			}
		}
		return widget.Box(widget.Row(widget.Expanded(widget.Column(text...).Gap(4).Stretch()), actions).Gap(16)).Pad(14, 16)
	})

	identity := section(T("Identity"),
		card(
			row(T("Node name"), T("How this machine appears to the others."),
				widget.Box(ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.Name })).Name(T("Node name"))).Width(240)),
			row(T("Node ID"), T("Generated once; the mesh tells nodes apart by it."),
				widget.Text(m.cfg.NodeID).Size(12).Color(skin.Muted).Selectable()),
		),
	)
	network := section(T("Network"),
		card(
			row(T("Listen address"), T("Where other daemons reach this one over libp2p, as host:port. Empty is every interface on port 4243."),
				widget.Box(ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.Listen })).Placeholder(":4243").Name(T("Listen address"))).Width(240)),
		),
	)
	libp2p := section(T("libp2p"),
		card(
			row(T("Link over libp2p"), T("Daemons link to each other over libp2p, through a relay from behind NAT, and let in those whose keys they have (Settings ▸ Access). Off, this node has no peers."),
				ui.Switch(m.Host.Field(func(d *hostDraft) *bool { return &d.P2P }), "").Name(T("Link over libp2p"))),
			row(T("Relay for the mesh"), T("For a node with a public address: the others reach each other through it when they cannot directly."),
				ui.Switch(m.Host.Field(func(d *hostDraft) *bool { return &d.Relay }), "").Name(T("Relay for the mesh"))),
			stackedRow(T("Relays"), T("The mesh's relay nodes, one per line, as each one's address below shows it."),
				ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.Relays })).Multiline().Lines(2).Name(T("Relays"))),
			stackedRow(T("This node's addresses"), T("To list as another node's peer or relay."),
				ggui.View(m.Conn, func(c connState) ggui.Widget {
					text := T("Not on libp2p")
					if addrs := c.Self.DialAddrs(); len(addrs) > 0 {
						text = strings.Join(addrs, "\n")
					}
					return widget.Text(text).Size(12).Color(skin.Muted).Selectable()
				})),
		),
	)

	mdns := m.Host.Field(func(d *hostDraft) *bool { return &d.MDNS })
	tailscale := m.Host.Field(func(d *hostDraft) *bool { return &d.Tailscale })
	discovery := section(T("Discovery"),
		responsiveGrid(settingsTwoCol, 2, settingsColumnGap, sectionGap,
			card(
				row(T("Bonjour (mDNS)"), T("Find daemons on the same local network."), ui.Switch(mdns, "").Name(T("Bonjour"))),
				row(T("Tailscale"), T("Probe the machines on your tailnet by their MagicDNS names."), ui.Switch(tailscale, "").Name(T("Tailscale"))),
				stackedRow(T("Tailnet hosts"), T("MagicDNS names to probe, one per line. Empty asks the tailscale CLI."),
					ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.TailnetHosts })).Multiline().Lines(3).Name(T("Tailnet hosts"))),
			),
			card(
				stackedRow(T("Peers"), T("Addresses always dialed, one per line: host, host:port, or a libp2p address ending in /p2p/ and its ID."),
					ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.Peers })).Multiline().Lines(5).Name(T("Peers"))),
			),
		),
	)

	logs := ggui.View(m.DaemonLog, func(lines []string) ggui.Widget {
		if len(lines) == 0 {
			return muted(widget.Text(T("Nothing logged yet.")).Size(12))
		}
		mono := ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 11.5}
		return widget.Text(strings.Join(lines, "\n")).Style(mono).Color(skin.Muted).Selectable()
	})
	inApp := ggui.Map(m.DaemonState, func(s daemonState) bool { return s.Mode == "embedded" })

	return widePage(T("Daemon"), T("Where this node's daemon runs, how it is identified on the mesh, and how it finds other nodes."),
		responsiveGrid(settingsTwoCol, 2, settingsColumnGap, pageGap,
			section(T("Mode"),
				card(
					row(T("Run the daemon"), T("Auto uses a makod that is already running and otherwise starts one in the app."), mode),
				),
			),
			section(T("Status"), card(status)),
		),
		twoColumns(
			[]ggui.Widget{identity, network, libp2p},
			[]int{0, 1},
			[]int{2},
		),
		discovery,
		saveBar(m),
		ggui.If(inApp, func() ggui.Widget {
			return section(T("Log"),
				widget.Box(widget.Scroll(widget.Padding(logs, 12, 14)).FollowEnd().Bar(skin.Surface3)).
					Height(320).Fill(skin.Surface1).Border(1, skin.Border).Radius(10),
			)
		}),
	)
}

// Mesh ------------------------------------------------------------------------

func nodeSettings(m *model) ggui.Widget {
	nodes := ggui.View(m.Nodes, func(nodes []*protocol.Node) ggui.Widget { return nodeList(m, nodes) })
	return page(T("Nodes"), T("Daemons this one knows of. Sessions start on those that let it in; ask the others to."),
		section(T("Mesh"), nodes),
	)
}

func aboutSettings(m *model) ggui.Widget {
	dir, _ := config.Dir()
	return page(T("About"), "mako runs terminal agents on any of your computers and drives them from one place.",
		section(T("Build"),
			card(
				row(T("Version"), "", widget.Text(config.Version).Size(13).Color(skin.Muted).Selectable()),
				row(T("Daemon config"), "", widget.Text(filepath.Join(dir, "config.json")).Size(12).Color(skin.Muted).Selectable()),
				row(T("App settings"), "", widget.Text(filepath.Join(dir, "gui.json")).Size(12).Color(skin.Muted).Selectable()),
			),
		),
	)
}
