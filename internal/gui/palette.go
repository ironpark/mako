package gui

import (
	"image/color"
	"slices"

	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/protocol"
)

// The command palette, on ⌘K or ⌘⇧P, searches what the window does, as
// Zed's does: its commands, named "category: action", those run lately
// first, then the open tabs to switch to and the sessions to show.

// command is one of the palette's commands. Its ID, the English label,
// is what the recent list keeps.
type command struct {
	ID, Category string
	Keys         []string
	Shortcut     string
	Disabled     bool
	Run          func()
}

// maxRecent is how many commands the palette keeps as run lately.
const maxRecent = 12

// openPalette shows the palette with an empty search and the commands as
// things stand now.
func (m *model) openPalette() {
	m.PaletteQuery.Set("")
	m.paletteCommands, m.paletteTabs = nil, nil
	if m.palette != nil {
		m.palette.refresh()
	}
	m.PaletteOpen.Set(true)
}

// paletteDialog is the palette over the window, which closes as it runs
// what was chosen.
func paletteDialog(m *model) ggui.Widget {
	m.palette = newPicker(m.PaletteOpen, m.PaletteQuery, m.paletteRows)
	m.palette.Placeholder = T("Execute a command…")
	m.palette.Name = T("Search commands")
	m.palette.Empty = T("No results")
	m.palette.Action = T("Run")
	return m.palette.dialog()
}

// ran puts a command first in the recent list.
func (m *model) ran(id string) {
	m.Prefs.Update(func(p prefs) prefs {
		recent := append([]string{id}, slices.DeleteFunc(slices.Clone(p.RecentCommands), func(o string) bool { return o == id })...)
		p.RecentCommands = recent[:min(len(recent), maxRecent)]
		return p
	})
}

// paletteRows are the palette's rows for what is typed: with nothing,
// the commands run lately, then the others as listed; with a search,
// those that match, best first.
func (m *model) paletteRows(q string) []pickRow {
	// What the palette lists is read once as it opens, and searched as
	// the query changes.
	if m.paletteCommands == nil {
		m.paletteCommands, m.paletteTabs = m.commands(), m.tabRows()
	}
	recent := ggui.Peek(m.Prefs).RecentCommands
	type ranked struct {
		row   pickRow
		score int
	}
	var out []ranked
	for _, c := range m.paletteCommands {
		label := T(c.Category) + ": " + T(c.ID)
		at := slices.Index(recent, c.ID)
		score, hits, ok := fuzzyMatch(label, q)
		if !ok {
			// A command is found by its English name and its keywords
			// as well, without marks, the name first.
			for i, k := range append([]string{c.ID}, c.Keys...) {
				if score, _, ok = fuzzyMatch(k, q); ok {
					score -= pick(i == 0, 20, 30)
					break
				}
			}
			if !ok {
				continue
			}
		}
		switch {
		case q == "" && at >= 0:
			score = 1000 - at
		case at >= 0:
			score += 3
		}
		id, run := c.ID, c.Run
		out = append(out, ranked{pickRow{Label: label, Hits: hits, Shortcut: c.Shortcut, Recent: at >= 0, Disabled: c.Disabled,
			Run: func() { m.ran(id); run() }}, score})
	}
	for _, r := range m.paletteTabs {
		score, hits, ok := fuzzyMatch(r.Label, q)
		if !ok {
			if score, _, ok = fuzzyMatch(r.Detail, q); !ok {
				continue
			}
			score -= 10
		}
		r.Hits = hits
		out = append(out, ranked{r, score - 1})
	}
	slices.SortStableFunc(out, func(a, b ranked) int {
		if a.row.Disabled != b.row.Disabled {
			return pick(a.row.Disabled, 1, -1)
		}
		return b.score - a.score
	})
	rows := make([]pickRow, len(out))
	for i, r := range out {
		rows[i] = r.row
	}
	return rows
}

// commands are the palette's commands as things stand now.
func (m *model) commands() []command {
	selected := ggui.Peek(m.Selected)
	docs := ggui.Peek(m.Docs)
	d, onDoc := docs[selected]
	cmds := []command{
		{ID: "New session…", Category: "Session", Keys: []string{"terminal", "agent"}, Shortcut: "cmd+t", Run: func() { m.openNew("", "") }},
		{ID: "Rename session…", Category: "Session", Keys: []string{"name", "title"}, Disabled: !isSessionTab(selected), Run: func() { m.askRename(selected) }},
		{ID: pick(m.pinned(selected), "Unpin session", "Pin session to the top"), Category: "Session", Keys: []string{"pin", "favorite"},
			Disabled: !isSessionTab(selected), Run: func() { m.togglePin(selected) }},
		{ID: "Toggle chat and terminal", Category: "Session", Keys: []string{"conversation"}, Shortcut: "cmd+shift+j",
			Disabled: selected == "" || onDoc || !m.chatCapable(selected), Run: m.toggleChat},
		{ID: "Go to file…", Category: "File", Keys: []string{"open", "find", "quick"}, Shortcut: "cmd+p", Run: m.openQuickOpen},
		{ID: "Save file", Category: "File", Shortcut: "cmd+s", Disabled: !onDoc || d.ReadOnly(), Run: m.saveSelected},
		{ID: "Search the project…", Category: "File", Keys: []string{"find", "grep", "text"}, Shortcut: "cmd+shift+f", Run: m.openSearch},
	}
	if onDoc && isMarkdown(d.Name()) {
		cmds = append(cmds, command{ID: previewLabel(m.docs[selected]), Category: "File", Keys: []string{"markdown", "preview"},
			Shortcut: "cmd+shift+v", Run: m.toggleSelectedPreview})
	}
	cmds = append(cmds,
		command{ID: "Close tab", Category: "Tab", Shortcut: "cmd+w", Disabled: selected == "", Run: func() { m.askKill(selected) }},
		command{ID: "Tidy tabs", Category: "Tab", Keys: []string{"close", "clean", "idle", "shell"}, Disabled: !m.anyTidyable(),
			Run: func() { m.tidyTabs(ggui.Peek(m.Tabs)) }},
		command{ID: "Toggle sidebar", Category: "View", Shortcut: "cmd+b", Run: func() { ggui.Toggle(m.SidebarOpen) }},
		command{ID: "Project agents", Category: "View", Keys: []string{"mcp", "monitor", "messages"}, Run: func() { m.DetailsOpen.Set(true); m.DetailTab.Set(agentsTab) }},
		command{ID: "Toggle details panel", Category: "View", Keys: []string{"files", "changes"}, Shortcut: "cmd+shift+d",
			Run: func() { ggui.Toggle(m.DetailsOpen) }},
		command{ID: "Extensions…", Category: "App", Keys: []string{"zed", "language", "grammar", "highlight", "mcp"},
			Run: func() { m.ExtensionsOpen.Set(true) }},
		command{ID: "Settings", Category: "App", Keys: []string{"preferences"}, Shortcut: "cmd+,", Run: func() { m.openSettings("") }},
	)
	for _, g := range groupings {
		cmds = append(cmds, command{ID: g.Label, Category: "Sidebar", Keys: []string{"group", "sessions"}, Run: func() { m.setSidebarGroup(g.By) }})
	}
	for _, t := range []string{"system", "dark", "light"} {
		cmds = append(cmds, command{ID: themeLabels[t], Category: "Theme", Keys: []string{"appearance"}, Run: func() {
			m.Prefs.Update(func(p prefs) prefs { p.Theme = t; return p })
		}})
	}
	return cmds
}

// tabRows are the open tabs, then the sessions not in one, each with
// where it is.
func (m *model) tabRows() []pickRow {
	titles := ggui.Peek(m.Titles)
	docs := ggui.Peek(m.Docs)
	nodes := ggui.Peek(m.Nodes)
	open := map[string]bool{}
	var rows []pickRow
	for _, id := range ggui.Peek(m.Tabs) {
		open[id] = true
		var r pickRow
		if d, ok := docs[id]; ok {
			r = pickRow{Label: d.Name(), Detail: d.Path, Icon: icon(fileIconName(d.Name())).Size(15).Color(tokMuted)}
		} else if s, ok := m.session(id); ok {
			r = sessionPick(s, titles[id], nodes, tokMuted)
		} else {
			continue
		}
		r.Run = func() { m.showTab(id) }
		rows = append(rows, r)
	}
	for _, s := range ggui.Peek(m.Sessions) {
		if open[s.GetId()] {
			continue
		}
		id := s.GetId()
		r := sessionPick(s, titles[id], nodes, tokExtraMuted)
		r.Run = func() { m.selectSession(id) }
		rows = append(rows, r)
	}
	return rows
}

// sessionPick is a session as the palette lists it: its title, where it
// runs, and its agent's glyph in c.
func sessionPick(s *protocol.SessionInfo, title string, nodes []*protocol.Node, c color.Color) pickRow {
	return pickRow{Label: sessionTitle(s, title), Detail: nodeName(nodes, s.GetNodeId()) + ":" + projectName(s.GetProject()),
		Icon: agentGlyph(s.GetAgent(), c).Size(15)}
}
