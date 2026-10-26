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
	if m.palette != nil {
		m.palette.Entries(m.commands()...)
	}
	m.PaletteOpen.Set(true)
}

// paletteDialog is the palette over the window, which closes as it runs
// what was chosen.
func paletteDialog(m *model) ggui.Widget {
	m.palette = ui.Command(m.PaletteQuery).
		Placeholder(T("Type a command or a tab's name…")).Name(T("Search commands")).
		Hints().HintLabels(T("Navigate"), T("Select")).Empty(T("No results")).Height(340)
	return ui.CommandDialog(m.PaletteOpen, m.palette).Name(T("Commands")).Width(540)
}

// commands are the palette's entries.
func (m *model) commands() []ui.CommandEntry {
	// item is a command labelled in the window's language and found by
	// its English name as well.
	item := func(label string, fn func(), keys ...string) ui.CommandEntry {
		return ui.CommandItem(T(label), fn).Keywords(append([]string{label}, keys...)...)
	}
	selected := ggui.Peek(m.Selected)
	docs := ggui.Peek(m.Docs)
	d, onDoc := docs[selected]
	markdown := onDoc && isMarkdown(d.Name())

	group := T("Commands")
	cmds := []ui.CommandEntry{
		item("New session…", func() { m.openNew("", "") }, "terminal", "agent").Shortcut("cmd+t"),
		item("Save file", m.saveSelected).Shortcut("cmd+s").Disabled(!onDoc || d.ReadOnly()),
		item("Go to file…", m.openQuickOpen, "open", "find", "quick").Shortcut("cmd+p"),
		item("Search the project…", m.openSearch, "find", "grep", "text").Shortcut("cmd+shift+f"),
	}
	if markdown {
		cmds = append(cmds, item(previewLabel(m.docs[selected]), m.toggleSelectedPreview, "markdown", "preview").Shortcut("cmd+shift+v"))
	}
	cmds = append(cmds,
		item("Toggle chat and terminal", m.toggleChat, "conversation").Shortcut("cmd+shift+j").Disabled(selected == "" || onDoc || !m.chatCapable(selected)),
		item("Close tab", func() { m.askKill(selected) }).Shortcut("cmd+w").Disabled(selected == ""),
		item("Rename session…", func() { m.askRename(selected) }, "name", "title").Disabled(!isSessionTab(selected)),
		item(pick(m.pinned(selected), "Unpin session", "Pin session to the top"), func() { m.togglePin(selected) }, "pin", "favorite").Disabled(!isSessionTab(selected)),
		item("Filter sessions…", func() { m.FilterOpen.Set(true); m.SidebarOpen.Set(true) }, "find", "search", "sidebar"),
		item("Extensions…", func() { m.ExtensionsOpen.Set(true) }, "zed", "language", "grammar", "highlight"),
		item("Settings", func() { m.openSettings("") }, "preferences").Shortcut("cmd+,"),
		item("Toggle sidebar", func() { ggui.Toggle(m.SidebarOpen) }).Shortcut("cmd+b"),
		item("Toggle details panel", func() { ggui.Toggle(m.DetailsOpen) }, "files", "changes").Shortcut("cmd+shift+d"),
	)
	for _, g := range groupings {
		cmds = append(cmds, ui.CommandItem(T("Sidebar: ")+T(g.Label), func() { m.setSidebarGroup(g.By) }).
			Keywords("sidebar", g.Label, "sessions"))
	}
	for _, t := range []string{"system", "dark", "light"} {
		cmds = append(cmds, ui.CommandItem(T("Theme: ")+T(themeLabels[t]), func() {
			m.Prefs.Update(func(p prefs) prefs { p.Theme = t; return p })
		}).Keywords("theme", themeLabels[t], "appearance"))
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
		cmds = append(cmds, ui.CommandItem(label, func() { m.showTab(id) }).Keywords(keys...).Group(T("Open tabs")))
	}
	nodes := ggui.Peek(m.Nodes)
	for _, s := range ggui.Peek(m.Sessions) {
		if open[s.ID] {
			continue
		}
		id := s.ID
		label := sessionTitle(s, titles[id]) + " · " + nodeName(nodes, s.NodeID) + ":" + projectName(s.Project)
		cmds = append(cmds, ui.CommandItem(label, func() { m.selectSession(id) }).Keywords(s.Project, s.Agent).Group(T("Sessions")))
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
