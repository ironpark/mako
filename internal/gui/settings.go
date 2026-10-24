package gui

import (
	"image/color"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
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
}

var settingsNav = []settingsGroup{
	{"App", []settingsSection{
		{"general", "General", "settings"},
		{"appearance", "Appearance", "palette"},
		{"terminal", "Terminal", "square-terminal"},
		{"shortcuts", "Shortcuts", "keyboard"},
	}},
	{"This node", []settingsSection{
		{"daemon", "Daemon", "server"},
		{"host", "Host", "server-cog"},
		{"discovery", "Discovery", "radar"},
		{"agents", "Agents", "bot"},
	}},
	{"Mesh", []settingsSection{
		{"nodes", "Nodes", "network"},
		{"about", "About", "info"},
	}},
}

func settingsPage(m *model) ggui.Widget {
	content := ggui.Key(m.Section, func(id string) ggui.Widget {
		return ggui.Scroll(ggui.Align(ggui.Box(ggui.Padding(settingsContent(m, id), 32, 40)).MaxWidth(760)).Top()).
			Bar(tokSurface3)
	})
	return ggui.Row(
		settingsSidebar(m),
		vline(),
		ggui.Expanded(ggui.Box(content).Fill(tokSurface0)),
	).Stretch()
}

func settingsSidebar(m *model) ggui.Widget {
	back := ui.ButtonOf(ggui.Row(icon("arrow-left").Size(16), ggui.Text("Back to sessions").Size(13)).Gap(10),
		func() { m.Page.Set("workspace") }).Ghost().Pad(7, 10).Radius(7).Name("Back to sessions")
	header := ggui.Box(ggui.Row(back)).Pad(8, 8).Height(headerHeight)

	nav := ggui.View(m.Section, func(current string) ggui.Widget {
		var rows []ggui.Widget
		for _, g := range settingsNav {
			rows = append(rows, ggui.Box(ggui.Text(g.title).Size(11.5).Color(tokExtraMuted)).Pad(14, 10, 4, 10))
			for _, s := range g.sections {
				id := s.id
				fg := tokMuted
				if id == current {
					fg = tokFg
				}
				item := ui.ButtonOf(ggui.Row(
					iconColored(s.icon, fg).Size(15),
					ggui.Text(s.label).Size(13).Color(fg),
				).Gap(10), func() { m.Section.Set(id) }).Ghost().Pad(7, 10).Radius(7).Name(s.label)
				if id == current {
					rows = append(rows, ggui.Box(item).Fill(tokSidebarSel).Radius(7))
				} else {
					rows = append(rows, item)
				}
			}
		}
		return ggui.Column(rows...).Gap(2).Stretch()
	})

	return ggui.Box(ggui.Column(
		header,
		hline(),
		ggui.Expanded(ggui.Scroll(ggui.Padding(nav, 4, 8))),
	).Stretch()).Width(sidebarWidth).Fill(tokSidebar)
}

func settingsContent(m *model, id string) ggui.Widget {
	switch id {
	case "appearance":
		return appearanceSettings(m)
	case "terminal":
		return terminalSettings(m)
	case "shortcuts":
		return shortcutSettings()
	case "daemon":
		return daemonSettings(m)
	case "host":
		return hostSettings(m)
	case "discovery":
		return discoverySettings(m)
	case "agents":
		return agentSettings(m)
	case "nodes":
		return nodeSettings(m)
	case "about":
		return aboutSettings(m)
	}
	return generalSettings(m)
}

// Building blocks -------------------------------------------------------------

func pageTitle(title, description string) ggui.Widget {
	return ggui.Column(
		ggui.Text(title).Size(20).Weight(ggui.WeightSemibold),
		muted(ggui.Text(description).Size(13)),
	).Gap(6).Stretch()
}

// section is a muted heading over its cards.
func section(title string, children ...ggui.Widget) ggui.Widget {
	head := ggui.Box(muted(ggui.Text(title).Size(12.5))).Pad(0, 4)
	return ggui.Column(append([]ggui.Widget{head}, children...)...).Gap(10).Stretch()
}

// card groups rows on a raised surface, a hairline between each.
func card(rows ...ggui.Widget) ggui.Widget {
	children := make([]ggui.Widget, 0, 2*len(rows))
	for i, r := range rows {
		if i > 0 {
			children = append(children, hline())
		}
		children = append(children, r)
	}
	return ggui.Box(ggui.Column(children...).Stretch()).Fill(tokSurface1).Border(1, tokBorder).Radius(10)
}

// row is one setting: what it is on the left, its control on the right.
func row(title, hint string, control ggui.Widget) ggui.Widget {
	text := []ggui.Widget{ggui.Text(title).Size(13.5)}
	if hint != "" {
		text = append(text, muted(ggui.Text(hint).Size(12)))
	}
	return ggui.Box(ggui.Row(
		ggui.Expanded(ggui.Column(text...).Gap(3).Stretch()),
		control,
	).Gap(24)).Pad(14, 16)
}

// stackedRow puts a wide control under its title, for text areas.
func stackedRow(title, hint string, control ggui.Widget) ggui.Widget {
	text := []ggui.Widget{ggui.Text(title).Size(13.5)}
	if hint != "" {
		text = append(text, muted(ggui.Text(hint).Size(12)))
	}
	return ggui.Box(ggui.Column(append(text, ggui.Box().Height(4), control)...).Gap(3).Stretch()).Pad(14, 16)
}

func page(title, description string, sections ...ggui.Widget) ggui.Widget {
	return ggui.Column(append([]ggui.Widget{pageTitle(title, description)}, sections...)...).Gap(28).Stretch()
}

func prefField[T any](m *model, f func(*prefs) *T) ggui.Binding[T] {
	return m.Prefs.Field(f)
}

// App sections ----------------------------------------------------------------

func generalSettings(m *model) ggui.Widget {
	agent := prefField(m, func(p *prefs) *string { return &p.DefaultAgent })
	project := prefField(m, func(p *prefs) *string { return &p.DefaultProject })
	browse := ui.Button("Browse…", func() {
		host := ggui.UseHost()
		start := ggui.Peek(project)
		ggui.Async(func() (string, error) {
			return host.Dialogs().PickFolder(runtime.FileDialog{Title: "Default working directory", Directory: start})
		}, func(dir string, err error) {
			if err == nil && dir != "" {
				project.Set(dir)
			}
		})
	}).Outline()
	daemon := prefField(m, func(p *prefs) *string { return &p.Daemon })
	status := ggui.View(m.Conn, func(c connState) ggui.Widget {
		if !c.OK {
			msg := "Not connected"
			if c.Err != "" {
				msg = c.Err
			}
			return ggui.Row(linkDot(false), muted(ggui.Text(msg).Size(12.5).NoWrap().Ellipsis())).Gap(8)
		}
		return ggui.Row(linkDot(true), muted(ggui.Text("Connected to "+c.Self.Name).Size(12.5))).Gap(8)
	})

	return page("General", "How new sessions start and which daemon this window talks to.",
		section("Sessions",
			card(
				row("Default agent", "Chosen first in the new-session dialog.",
					ggui.Box(ui.Select(agent).BindOptions(m.Agents).Format(targetLabel).Name("Default agent")).Width(200)),
				stackedRow("Default working directory", "Where a new session starts unless you pick another.",
					ggui.Row(ggui.Expanded(ui.TextField(project).Name("Default working directory")), browse).Gap(8)),
				row("Confirm before killing", "Ask before a session is stopped from the window.",
					ui.Switch(prefField(m, func(p *prefs) *bool { return &p.ConfirmKill }), "").Name("Confirm before killing")),
			),
		),
		section("Workspace",
			card(
				row("Composer", "The message box under the terminal, which types a line into the session.",
					ui.Switch(prefField(m, func(p *prefs) *bool { return &p.ShowComposer }), "").Name("Show composer")),
			),
		),
		section("Connection",
			card(
				row("External daemon", "The sanbod to use when the daemon does not run in the app. Empty is the one on this machine; it relays to the rest of the mesh.",
					ggui.Box(ui.TextField(daemon).Placeholder("127.0.0.1:4242").Name("External daemon").
						BindDisabled(ggui.Map(m.Prefs, func(p prefs) bool { return p.DaemonMode == "embedded" }))).Width(220)),
				ggui.Box(status).Pad(12, 16),
			),
		),
	)
}

func appearanceSettings(m *model) ggui.Widget {
	labels := map[string]string{"system": "System", "light": "Light", "dark": "Dark"}
	theme := ui.ToggleGroup(prefField(m, func(p *prefs) *string { return &p.Theme })).
		Options([]string{"system", "light", "dark"}).
		Format(func(s string) string { return labels[s] }).Name("Theme")
	return page("Appearance", "The window's colours, after Paseo's palettes.",
		section("Theme",
			card(
				row("Theme", "System follows the desktop's light or dark appearance.", theme),
			),
			themePreview(),
		),
	)
}

// themePreview is a swatch of the active palette: the surfaces in order,
// then the terminal's sixteen ANSI colours.
func themePreview() ggui.Widget {
	var surfaces []ggui.Widget
	for _, c := range []color.Color{tokSidebar, tokSurface0, tokSurface1, tokSurface2, tokSurface3} {
		surfaces = append(surfaces, ggui.Box().Size(40, 26).Radius(6).Fill(c).Border(1, tokBorder))
	}
	// The ANSI colours are values, not tokens, so this part follows the
	// theme by rebuilding.
	ansi := ggui.Reactive(func() ggui.Widget {
		pal, _ := uitheme.Use().Get(paletteKey)
		var cells []ggui.Widget
		for _, c := range pal.ANSI {
			cells = append(cells, ggui.Box().Size(20, 20).Radius(4).Fill(hex(c)))
		}
		return ggui.Row(cells...).Gap(4)
	})
	return card(
		row("Surfaces", "Sidebar, workspace and the raised steps above it.", ggui.Row(surfaces...).Gap(6)),
		row("Terminal colours", "", ansi),
	)
}

func terminalSettings(m *model) ggui.Widget {
	size := prefField(m, func(p *prefs) *float64 { return &p.FontSize })
	lineHeight := prefField(m, func(p *prefs) *float64 { return &p.LineHeight })
	scrollback := ggui.Bind(
		func() float64 { return float64(m.Prefs.Get().Scrollback) },
		func(v float64) { m.Prefs.Update(func(p prefs) prefs { p.Scrollback = int(v); return p.normalized() }) },
	)
	cursorLabels := map[string]string{"block": "Block", "bar": "Bar", "underline": "Underline"}
	cursor := ui.Select(prefField(m, func(p *prefs) *string { return &p.CursorStyle })).
		Options([]string{"block", "bar", "underline"}).
		Format(func(s string) string { return cursorLabels[s] }).Name("Cursor style")

	return page("Terminal", "How session terminals look and behave in this window.",
		section("Text",
			card(
				row("Font size", "In points, for every terminal.",
					ggui.Box(ui.NumberInput(size).Range(minFontSize, maxFontSize).Step(1).Name("Font size")).Width(120)),
				row("Line height", "Space between rows, as a multiple of the font size.",
					ggui.Row(
						ggui.Box(ui.Slider(lineHeight, minLineHeight, maxLineHeight).Step(0.05).Name("Line height")).Width(160),
						ggui.Box(ggui.Textf("%.2f×", lineHeight).Size(12.5).Color(tokMuted)).Width(44),
					).Gap(10)),
			),
		),
		section("Cursor",
			card(
				row("Style", "The shape shown until a program asks for its own.", ggui.Box(cursor).Width(160)),
				row("Blink", "", ui.Switch(prefField(m, func(p *prefs) *bool { return &p.CursorBlink }), "").Name("Cursor blink")),
			),
		),
		section("Behaviour",
			card(
				row("Scrollback", "Lines each terminal keeps above the screen.",
					ggui.Box(ui.NumberInput(scrollback).Range(minScrollback, maxScrollback).Step(1000).Name("Scrollback")).Width(140)),
				row("Copy on select", "Put selected text on the clipboard as soon as the drag ends.",
					ui.Switch(prefField(m, func(p *prefs) *bool { return &p.CopyOnSelect }), "").Name("Copy on select")),
			),
		),
	)
}

func shortcutSettings() ggui.Widget {
	chord := func(c string) ggui.Widget {
		ch, err := ggui.ParseChord(c)
		label := c
		if err == nil {
			label = ch.Label()
		}
		return ui.Kbd(label)
	}
	var app []ggui.Widget
	for _, s := range shortcutList {
		app = append(app, row(s.Action, "", chord(s.Chord)))
	}
	term := []ggui.Widget{
		row("Copy selection", "", chord(termChord("c"))),
		row("Paste", "Bracketed when the program asks for it.", chord(termChord("v"))),
		row("Select all", "The whole scrollback.", chord(termChord("a"))),
		row("Select a word, a line", "Double-click, triple-click. Alt+drag selects a block.", ui.Kbd("2× / 3×")),
		row("Select in a mouse program", "Hold Shift to select where the program has the mouse.", ui.Kbd("⇧ drag")),
	}
	return page("Shortcuts", "Keys the window keeps. Every other key, Tab and the Ctrl chords included, goes to the terminal.",
		section("Window", card(app...)),
		section("Terminal", card(term...)),
	)
}

// termChord is the terminal's clipboard chord for a letter, as isCopy and
// the others match it.
func termChord(letter string) string {
	if goruntime.GOOS == "darwin" {
		return "cmd+" + letter
	}
	return "ctrl+shift+" + letter
}

// This node -------------------------------------------------------------------

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
			return muted(ggui.Text("Saved to config.json. The daemon reads agents as they change and the rest when it starts.").Size(12))
		}
		return ggui.Text(s).Size(12).Color(tokAccentBright)
	})
	embedded := ggui.Map(m.DaemonState, func(s daemonState) bool { return s.Running })
	return ggui.Row(
		ggui.Expanded(note),
		ggui.If(embedded, func() ggui.Widget {
			return ui.Button("Restart daemon", func() { m.guardStop(m.restartEmbedded) }).Outline()
		}),
		ui.Button("Revert", m.revertHost).Outline().BindDisabled(ggui.Not(dirty)),
		ui.Button("Save", m.saveHost).BindDisabled(ggui.Not(dirty)),
	).Gap(8)
}

// daemonSettings chooses where the daemon runs and shows how it is doing.
func daemonSettings(m *model) ggui.Widget {
	labels := map[string]string{"auto": "Auto", "embedded": "In this app", "external": "External"}
	mode := ui.ToggleGroup(ggui.Bind(
		func() string { return m.Prefs.Get().DaemonMode },
		m.setDaemonMode,
	)).Options([]string{"auto", "embedded", "external"}).
		Format(func(s string) string { return labels[s] }).Name("Daemon mode")

	status := ggui.View(m.DaemonState, func(s daemonState) ggui.Widget {
		var title, hint string
		ok := true
		switch {
		case s.Running:
			title = "Running in this app"
			hint = "Listening on " + s.Addr + ". Its sessions end when it stops or the app quits."
		case s.Starting:
			title, hint = "Starting…", ""
		case s.Mode == "external":
			title = "Using an external daemon"
			hint = s.Addr + ". Sessions keep running after the app quits."
		case s.Mode == "embedded":
			title, ok = "Stopped", false
			hint = "The app's daemon is not running."
		default:
			title, hint = "Looking for a daemon…", ""
		}
		if s.Err != "" {
			ok = false
			hint = s.Err
			if strings.Contains(s.Err, "address already in use") {
				hint += ". Another sanbod is listening there; choose External, or change the listen address under Host."
			}
		}
		text := []ggui.Widget{ggui.Row(linkDot(ok), ggui.Text(title).Size(13.5)).Gap(8)}
		if hint != "" {
			text = append(text, ggui.Box(muted(ggui.Text(hint).Size(12))).Pad(0, 0, 0, 15))
		}
		var actions ggui.Widget = ggui.Box()
		if s.Mode == "embedded" {
			if s.Running {
				actions = ggui.Row(
					ui.Button("Restart", func() { m.guardStop(m.restartEmbedded) }).Outline(),
					ui.Button("Stop", func() { m.guardStop(m.stopEmbedded) }).Outline(),
				).Gap(8)
			} else if !s.Starting {
				actions = ui.Button("Start", m.startEmbedded)
			}
		}
		return ggui.Box(ggui.Row(ggui.Expanded(ggui.Column(text...).Gap(4).Stretch()), actions).Gap(16)).Pad(14, 16)
	})

	logs := ggui.View(m.DaemonLog, func(lines []string) ggui.Widget {
		if len(lines) == 0 {
			return muted(ggui.Text("Nothing logged yet.").Size(12))
		}
		mono := ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 11.5}
		return ggui.Text(strings.Join(lines, "\n")).Style(mono).Color(tokMuted).Selectable()
	})
	inApp := ggui.Map(m.DaemonState, func(s daemonState) bool { return s.Mode == "embedded" })

	return page("Daemon", "Where this machine's daemon runs. The app can run it itself, so nothing else needs installing.",
		section("Mode",
			card(
				row("Run the daemon", "Auto uses a sanbod that is already running and otherwise starts one in the app.", mode),
			),
		),
		section("Status", card(status)),
		ggui.If(inApp, func() ggui.Widget {
			return section("Log",
				ggui.Box(ggui.Scroll(ggui.Padding(logs, 12, 14)).FollowEnd().Bar(tokSurface3)).
					Height(260).Fill(tokSurface1).Border(1, tokBorder).Radius(10),
			)
		}),
	)
}

func hostSettings(m *model) ggui.Widget {
	return page("Host", "This machine's daemon: how it is named and where it listens.",
		section("Identity",
			card(
				row("Node name", "How this machine appears to the others.",
					ggui.Box(ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.Name })).Name("Node name")).Width(240)),
				row("Node ID", "Generated once; the mesh tells nodes apart by it.",
					ggui.Text(m.cfg.NodeID).Size(12).Color(tokMuted).Selectable()),
			),
		),
		section("Network",
			card(
				row("Listen address", "UDP address for QUIC, such as :4242.",
					ggui.Box(ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.Listen })).Name("Listen address")).Width(240)),
				row("Pre-shared key", "Every daemon and front-end on the mesh must share it. Empty accepts anyone who can reach the port.",
					ggui.Box(ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.PSK })).Password().Name("Pre-shared key")).Width(240)),
			),
		),
		saveBar(m),
	)
}

func discoverySettings(m *model) ggui.Widget {
	mdns := m.Host.Field(func(d *hostDraft) *bool { return &d.MDNS })
	tailscale := m.Host.Field(func(d *hostDraft) *bool { return &d.Tailscale })
	return page("Discovery", "How this daemon finds the others. Nodes found either way are gossiped to the whole mesh.",
		section("Automatic",
			card(
				row("Bonjour (mDNS)", "Find daemons on the same local network.", ui.Switch(mdns, "").Name("Bonjour")),
				row("Tailscale", "Probe the machines on your tailnet by their MagicDNS names.", ui.Switch(tailscale, "").Name("Tailscale")),
				stackedRow("Tailnet hosts", "MagicDNS names to probe, one per line. Empty asks the tailscale CLI.",
					ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.TailnetHosts })).Multiline().Lines(3).Name("Tailnet hosts")),
			),
		),
		section("Static",
			card(
				stackedRow("Peers", "Addresses always dialed, one per line: host or host:port.",
					ui.TextField(hostField(m, func(d *hostDraft) *string { return &d.Peers })).Multiline().Lines(3).Name("Peers")),
			),
		),
		saveBar(m),
	)
}

// Mesh ------------------------------------------------------------------------

func nodeSettings(m *model) ggui.Widget {
	nodes := ggui.View(m.Nodes, func(nodes []protocol.Node) ggui.Widget { return nodeList(nodes) })
	return page("Nodes", "Daemons this window can reach. Sessions can start on any of them.",
		section("Mesh", nodes),
	)
}

func aboutSettings(m *model) ggui.Widget {
	dir, _ := config.Dir()
	return page("About", "sanbo runs terminal agents on any of your computers and drives them from one place.",
		section("Build",
			card(
				row("Version", "", ggui.Text(config.Version).Size(13).Color(tokMuted).Selectable()),
				row("Daemon config", "", ggui.Text(filepath.Join(dir, "config.json")).Size(12).Color(tokMuted).Selectable()),
				row("App settings", "", ggui.Text(filepath.Join(dir, "gui.json")).Size(12).Color(tokMuted).Selectable()),
			),
		),
	)
}
