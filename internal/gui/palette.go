package gui

import (
	"image/color"
	"slices"

	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/gui/hangul"
	"github.com/ironpark/mako/internal/gui/skin"
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
	m.pickers.PaletteQuery.Set("")
	m.pickers.commands, m.pickers.tabs = nil, nil
	if m.pickers.palette != nil {
		m.pickers.palette.refresh()
	}
	m.pickers.PaletteOpen.Set(true)
}

// paletteDialog is the palette over the window, which closes as it runs
// what was chosen.
func paletteDialog(m *model) ggui.Widget {
	m.pickers.palette = newPicker(m.pickers.PaletteOpen, m.pickers.PaletteQuery, m.paletteRows)
	m.pickers.palette.Placeholder = T("Execute a command…")
	m.pickers.palette.Name = T("Search commands")
	m.pickers.palette.Empty = T("No results")
	m.pickers.palette.Action = T("Run")
	return m.pickers.palette.dialog()
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
	if m.pickers.commands == nil {
		m.pickers.commands, m.pickers.tabs = m.commands(), m.tabRows()
	}
	recent := ggui.Peek(m.Prefs).RecentCommands
	type ranked struct {
		row   pickRow
		score int
	}
	// English typed with the input method left on Korean comes as
	// Hangul, and is looked for as the keys that typed it too.
	queries := []string{q}
	if typed, ok := hangul.Qwerty(q); ok {
		queries = append(queries, typed)
	}
	var out []ranked
	for _, c := range m.pickers.commands {
		label := T(c.Category) + ": " + T(c.ID)
		// In another language, the English name shows after it, muted.
		detail := c.Category + ": " + c.ID
		if detail == label {
			detail = ""
		}
		var detailHits []int
		at := slices.Index(recent, c.ID)
		score, hits, ok := fuzzyMatch(label, q)
		if !ok {
			// A command is found by its English name, with its category
			// or without, and its keywords as well, whatever language
			// the window is in, without marks, the name first.
			english := append([]string{c.ID, c.Category + " " + c.ID}, c.Keys...)
		search:
			for _, query := range queries {
				for i, k := range english {
					if score, _, ok = fuzzyMatch(k, query); ok {
						score -= pick(i < 2, 20, 30)
						if detail != "" {
							_, detailHits, _ = fuzzyMatch(detail, query)
						}
						break search
					}
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
		out = append(out, ranked{pickRow{Label: label, Hits: hits, Detail: detail, DetailHits: detailHits, Shortcut: c.Shortcut, Recent: at >= 0, Disabled: c.Disabled,
			Run: func() { m.ran(id); run() }}, score})
	}
	for _, r := range m.pickers.tabs {
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
		{ID: "New session…", Category: "Session", Keys: []string{"terminal", "agent"}, Shortcut: m.shortcutChord("new_session"), Run: func() { m.openNew("", "") }},
		{ID: "Rename session…", Category: "Session", Keys: []string{"name", "title"}, Disabled: !isSessionTab(selected), Run: func() { m.askRename(selected) }},
		{ID: pick(m.pinned(selected), "Unpin session", "Pin session to the top"), Category: "Session", Keys: []string{"pin", "favorite"},
			Disabled: !isSessionTab(selected), Run: func() { m.togglePin(selected) }},
		{ID: "Toggle chat and terminal", Category: "Session", Keys: []string{"conversation"}, Shortcut: m.shortcutChord("toggle_chat"),
			Disabled: selected == "" || onDoc || !m.chatCapable(selected), Run: m.toggleChat},
		{ID: "Next waiting session", Category: "Session", Keys: []string{"approval", "question", "attention", "needs"}, Shortcut: m.shortcutChord("next_waiting"),
			Disabled: m.waitingCount() == 0, Run: m.nextWaiting},
		{ID: "Go to file…", Category: "File", Keys: []string{"open", "find", "quick"}, Shortcut: m.shortcutChord("quick_open"), Run: m.openQuickOpen},
		{ID: "Save file", Category: "File", Shortcut: m.shortcutChord("save_file"), Disabled: !onDoc || d.ReadOnly(), Run: m.saveSelected},
		{ID: "Search the project…", Category: "File", Keys: []string{"find", "grep", "text"}, Shortcut: m.shortcutChord("search_project"), Run: m.openSearch},
	}
	if onDoc && isMarkdown(d.Name()) {
		cmds = append(cmds, command{ID: previewLabel(m.docs[selected]), Category: "File", Keys: []string{"markdown", "preview"},
			Shortcut: m.shortcutChord("toggle_preview"), Run: m.toggleSelectedPreview})
	}
	cmds = append(cmds,
		command{ID: "Close tab", Category: "Tab", Shortcut: m.shortcutChord("close_tab"), Disabled: selected == "", Run: func() { m.askKill(selected) }},
		command{ID: "Clean tabs", Category: "Tab", Keys: []string{"clean", "tidy", "close", "idle", "shell", "files", "diffs"}, Disabled: !m.anyTidyable(),
			Run: func() { m.tidyTabs(ggui.Peek(m.Tabs)) }},
		command{ID: "Toggle sidebar", Category: "View", Shortcut: m.shortcutChord("toggle_sidebar"), Run: func() { ggui.Toggle(m.SidebarOpen) }},
		command{ID: "Project agents", Category: "View", Keys: []string{"mcp", "monitor", "messages"}, Run: func() { m.revealBlock(agentsTab) }},
		command{ID: "Toggle details panel", Category: "View", Keys: []string{"files", "changes"}, Shortcut: m.shortcutChord("toggle_details"),
			Run: func() { ggui.Toggle(m.DetailsOpen) }},
		command{ID: "Open pane in new window", Category: "View", Keys: []string{"popout", "detach", "window", "pane", "split", "multi"},
			Disabled: selected == "", Run: m.popoutCurrentPane},
		command{ID: "Return all detached panes", Category: "View", Keys: []string{"dock", "attach", "window", "pane", "restore", "multi"},
			Disabled: len(ggui.Peek(m.DetachedPanes)) == 0, Run: m.returnAllDetachedPanes},
		command{ID: "Open current space in new window", Category: "View", Keys: []string{"space", "window", "popout", "multi"},
			Run: m.openCurrentSpaceWindow},
		command{ID: "Extensions…", Category: "App", Keys: []string{"zed", "language", "grammar", "highlight", "mcp"},
			Run: func() { m.ext.Open.Set(true) }},
		command{ID: "Built-in skills", Category: "App", Keys: []string{"skill", "mako", "plugin", "template", "edit", "settings"},
			Run: func() { m.openSettings("skills") }},
		command{ID: "Account usage & quotas", Category: "App", Keys: []string{"usage", "quota", "limits", "claude", "codex", "agy", "credits"},
			Run: m.openAccountUsage},
		command{ID: "Add SSH host…", Category: "Remote", Keys: []string{"ssh", "remote", "server", "host", "connect"},
			Run: func() { m.openAddSSHHost(nil) }},
		command{ID: "Remote SSH hosts", Category: "Remote", Keys: []string{"ssh", "remote", "nodes", "servers", "hosts"},
			Run: func() { m.openSettings("nodes") }},
		command{ID: "Settings", Category: "App", Keys: []string{"preferences"}, Shortcut: m.shortcutChord("open_settings"), Run: func() { m.openSettings("") }},
	)
	cmds = append(cmds,
		command{ID: "Add project folder…", Category: "Sidebar", Keys: []string{"project", "folder", "add"}, Run: m.addProjectFolder},
		command{ID: "Clone from Git URL…", Category: "Sidebar", Keys: []string{"project", "git", "url", "clone", "repo", "add"}, Run: m.openCloneProject},
		command{ID: "Import git projects…", Category: "Sidebar", Keys: []string{"project", "folder", "git", "repo", "scan", "import"}, Run: m.openImportProjects},
	)
	for _, by := range agentGroups {
		cmds = append(cmds, command{ID: agentGroupCommands[by], Category: "Sidebar", Keys: []string{"group", "agents", "sessions"},
			Run: func() { m.setAgentView(func(p *prefs) { p.AgentGroup = by }) }})
	}
	for _, t := range []string{"system", "dark", "light"} {
		cmds = append(cmds, command{ID: themeLabels[t], Category: "Theme", Keys: []string{"appearance"}, Run: func() {
			m.Prefs.Update(func(p prefs) prefs { p.Theme = t; return p })
		}})
	}
	cmds = append(cmds, m.pluginCommands()...)
	// The window's layout is put right from here, outside it, whatever it
	// has become.
	return append(cmds,
		command{ID: "Open Needs You inbox", Category: "Session", Keys: []string{"inbox", "triage", "approval", "waiting", "attention", "needs"}, Shortcut: m.shortcutChord("open_inbox"), Run: m.toggleInbox},
		command{ID: "Reset window layout", Category: "View", Keys: []string{"layout", "default", "panels", "restore"}, Run: m.resetWindowDoc},
		command{ID: "Reload window layout", Category: "View", Keys: []string{"layout", "layout.json", "panels"}, Run: m.reloadWindowDoc},
		command{ID: "Ask before agents change the window layout", Category: "View", Keys: []string{"layout", "agents", "allow", "approval"}, Run: m.forgetLayoutAllowances},
	)
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
			r = pickRow{Label: d.Name(), Detail: d.Path, Icon: icon(fileIconName(d.Name())).Size(15).Color(skin.Muted)}
		} else if s, ok := m.session(id); ok {
			r = sessionPick(s, titles[id], nodes, skin.Muted)
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
		r := sessionPick(s, titles[id], nodes, skin.ExtraMuted)
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
