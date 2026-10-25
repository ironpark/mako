package gui

import (
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
)

// The command palette, on ⌘K or ⌘⇧P, searches what the window does: its
// actions, the open tabs to switch to, and the sessions to show.

// openPalette shows the palette with an empty search and the commands as
// things stand now.
func (m *model) openPalette() {
	m.PaletteQuery.Set("")
	m.paletteGen.Set(ggui.Peek(m.paletteGen) + 1)
	m.PaletteOpen.Set(true)
}

// paletteDialog is the palette over the window.
func paletteDialog(m *model) ggui.Widget {
	list := ggui.Key(m.paletteGen, func(int) ggui.Widget {
		return ui.Command(m.PaletteQuery, m.commands()...).
			Placeholder(T("Type a command or a tab's name…")).Name(T("Search commands")).
			Borderless().InsetSearch().Hints().Height(340)
	})
	return ui.Dialog(m.PaletteOpen, list).Compact().Name(T("Commands")).Width(540)
}

// commands are the palette's entries.
func (m *model) commands() []ui.CommandEntry {
	run := func(fn func()) func() {
		return func() {
			m.PaletteOpen.Set(false)
			fn()
		}
	}
	// item is a command labelled in the window's language and found by
	// its English name as well.
	item := func(label string, fn func(), keys ...string) ui.CommandEntry {
		return ui.CommandItem(T(label), run(fn)).Keywords(append([]string{label}, keys...)...)
	}
	selected := ggui.Peek(m.Selected)
	docs := ggui.Peek(m.Docs)
	d, onDoc := docs[selected]
	markdown := onDoc && isMarkdown(d.Name())

	group := T("Commands")
	cmds := []ui.CommandEntry{
		item("New session…", func() { m.openNew("", "") }, "terminal", "agent").Shortcut("cmd+t"),
		item("Save file", m.saveSelected).Shortcut("cmd+s").Disabled(!onDoc || d.ReadOnly()),
	}
	if markdown {
		cmds = append(cmds, item(previewLabel(m.docs[selected]), m.toggleSelectedPreview, "markdown", "preview").Shortcut("cmd+shift+v"))
	}
	cmds = append(cmds,
		item("Close tab", func() { m.askKill(selected) }).Shortcut("cmd+w").Disabled(selected == ""),
		item("Extensions…", func() { m.ExtensionsOpen.Set(true) }, "zed", "language", "grammar", "highlight"),
		item("Settings", func() { m.openSettings("") }, "preferences").Shortcut("cmd+,"),
		item("Toggle sidebar", func() { ggui.Toggle(m.SidebarOpen) }).Shortcut("cmd+b"),
		item("Toggle details panel", func() { ggui.Toggle(m.DetailsOpen) }, "files", "changes").Shortcut("cmd+shift+d"),
	)
	for _, g := range groupings {
		cmds = append(cmds, ui.CommandItem(T("Sidebar: ")+T(g.Label), run(func() { m.setSidebarGroup(g.By) })).
			Keywords("sidebar", g.Label, "sessions"))
	}
	for _, t := range []string{"system", "dark", "light"} {
		cmds = append(cmds, ui.CommandItem(T("Theme: ")+T(themeLabels[t]), run(func() {
			m.Prefs.Update(func(p prefs) prefs { p.Theme = t; return p })
		})).Keywords("theme", themeLabels[t], "appearance"))
	}
	for i := range cmds {
		cmds[i] = cmds[i].Group(group)
	}

	// The tabs open, then the sessions not in one.
	titles := ggui.Peek(m.Titles)
	open := map[string]bool{}
	for _, id := range ggui.Peek(m.Tabs) {
		open[id] = true
		var label string
		var keys []string
		if d, ok := docs[id]; ok {
			label, keys = d.Name(), []string{d.Path}
		} else if s, ok := m.session(id); ok {
			label, keys = sessionTitle(s, titles[id]), []string{s.Project, s.Agent}
		} else {
			continue
		}
		cmds = append(cmds, ui.CommandItem(label, run(func() { m.showTab(id) })).Keywords(keys...).Group(T("Open tabs")))
	}
	nodes := ggui.Peek(m.Nodes)
	for _, s := range ggui.Peek(m.Sessions) {
		if open[s.ID] {
			continue
		}
		id := s.ID
		label := sessionTitle(s, titles[id]) + " · " + nodeName(nodes, s.NodeID) + ":" + projectName(s.Project)
		cmds = append(cmds, ui.CommandItem(label, run(func() { m.selectSession(id) })).Keywords(s.Project, s.Agent).Group(T("Sessions")))
	}
	return cmds
}

// extensionsDialog is Settings ▸ Extensions in a window of its own, as
// the palette opens it.
func extensionsDialog(m *model) ggui.Widget {
	content := ggui.If(m.ExtensionsOpen, func() ggui.Widget {
		body := ggui.Column(append([]ggui.Widget{muted(ggui.Text(extensionsAbout()).Size(13))}, extensionSections(m)...)...).Gap(22).Stretch()
		return ggui.Box(ggui.Scroll(ggui.Padding(body, 0, 12, 0, 0)).Bar(tokSurface3)).Height(560)
	})
	return ui.Dialog(m.ExtensionsOpen, content).Title(T("Extensions")).Width(720)
}
