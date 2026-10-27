package gui

import (
	"image/color"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
)

// filterSessions keeps the sessions whose title, name, project, branch,
// agent or host has every word of query in it.
func filterSessions(sessions []*protocol.SessionInfo, query string, nodes []*protocol.Node, titles map[string]string) []*protocol.SessionInfo {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return sessions
	}
	var out []*protocol.SessionInfo
	for _, s := range sessions {
		hay := strings.ToLower(strings.Join([]string{
			sessionTitle(s, titles[s.GetId()]), s.GetProject(), s.GetBranch(), sessionLabel(s), nodeName(nodes, s.GetNodeId()),
		}, "\x00"))
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(hay, w) }) {
			out = append(out, s)
		}
	}
	return out
}

// projectLabels names the project sections whose folders share a name by
// as much of their paths as tells them apart, "work/api" and "oss/api", or
// where the paths are the same, on hosts of their own, by their hosts. The
// rest go by their names, and are left out.
func projectLabels(groups []sessionGroup, nodes []*protocol.Node) map[string]string {
	byName := map[string][]sessionGroup{}
	for _, g := range groups {
		if g.Project != "" {
			byName[projectName(g.Project)] = append(byName[projectName(g.Project)], g)
		}
	}
	out := map[string]string{}
	for name, same := range byName {
		if len(same) < 2 {
			continue
		}
		depth := 2
		for ; depth <= 4; depth++ {
			labels := map[string]bool{}
			for _, g := range same {
				labels[tail(g.Project, depth)] = true
			}
			if len(labels) == len(same) {
				break
			}
		}
		for _, g := range same {
			if depth <= 4 {
				out[g.Key] = tail(g.Project, depth)
			} else {
				out[g.Key] = name + " · " + nodeName(nodes, g.NodeID)
			}
		}
	}
	return out
}

// tail is the last n elements of path, slash-separated.
func tail(path string, n int) string {
	parts := strings.FieldsFunc(filepath.ToSlash(path), func(r rune) bool { return r == '/' })
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	return strings.Join(parts, "/")
}

// ownerMenu is what an owner's heading offers: ending the sessions in its
// folders, or taking the folders off the sidebar, their sessions ended
// too, as they would bring the folders back. Either names the sessions it
// ends and asks first.
func ownerMenu(m *model, o ownerSection, name string) []ggui.Widget {
	var ids []string
	roots := make([]fileRoot, len(o.Groups))
	for i, g := range o.Groups {
		roots[i] = fileRoot{Node: g.NodeID, Path: g.Project}
		for _, s := range g.Sessions {
			ids = append(ids, s.GetId())
		}
	}
	remove := func() {
		m.forgetProjects(roots)
		// A poll before the sessions are gone keeps their folders again.
		m.killAll(ids, func() { m.forgetProjects(roots) })
	}
	items := []ggui.Widget{ui.MenuItem(T("Remove from sidebar"), func() {
		if len(ids) == 0 {
			remove()
			return
		}
		m.askConfirm(confirmation{
			Title:  Tf("Remove %s from the sidebar?", name),
			Body:   T("Its folders leave the sidebar, and these sessions end, their output lost:") + "\n" + sessionList(m, ids),
			Action: T("Remove"), Run: remove,
		})
	})}
	if len(ids) > 0 {
		items = append(items, ui.MenuItem(T("End all sessions…"), func() {
			m.askConfirm(confirmation{
				Title:  Tf("End %d sessions of %s?", len(ids), name),
				Body:   T("These sessions end, their output lost:") + "\n" + sessionList(m, ids),
				Action: T("End sessions"), Run: func() { m.killAll(ids, nil) },
			})
		}))
	}
	return items
}

// sessionList names sessions a line each, by title and where they run,
// as many as a dialog has room for.
func sessionList(m *model, ids []string) string {
	const most = 12
	nodes, titles := ggui.Peek(m.Nodes), ggui.Peek(m.Titles)
	var lines []string
	for _, id := range ids[:min(len(ids), most)] {
		s, ok := m.session(id)
		if !ok {
			continue
		}
		lines = append(lines, "• "+sessionTitle(s, titles[id])+" — "+nodeName(nodes, s.GetNodeId())+" · "+projectName(s.GetProject()))
	}
	if len(ids) > most {
		lines = append(lines, Tf("and %d more", len(ids)-most))
	}
	return strings.Join(lines, "\n")
}

// pinnedSection is the sidebar's pinned sessions, above the groups.
func pinnedSection(m *model, sessions []*protocol.SessionInfo, selected string, nodes []*protocol.Node, titles map[string]string) ggui.Widget {
	rows := []ggui.Widget{ggui.Box(ggui.Row(
		icon("pin").Size(14),
		ggui.Text(T("Pinned")).Size(13).Weight(ggui.WeightMedium),
	).Gap(10)).Pad(4, 4, 4, 10)}
	for _, s := range sessions {
		where := strings.Join([]string{nodeName(nodes, s.GetNodeId()), projectName(s.GetProject()), short(s.GetId())}, " · ")
		rows = append(rows, sessionRow(m, s, s.GetId() == selected, titles[s.GetId()], where, 1))
	}
	return ggui.Column(rows...).Gap(2).Stretch()
}

// pinned reports whether the sidebar lists a session first.
func (m *model) pinned(id string) bool { return slices.Contains(ggui.Peek(m.Prefs).Pinned, id) }

// togglePin pins a session to the top of the sidebar, or unpins it.
func (m *model) togglePin(id string) {
	m.Prefs.Update(func(p prefs) prefs { p.Pinned = toggled(p.Pinned, id); return p })
}

// toggled is list with v taken out, or put at its end when it is not in
// it: a copy, so the list a setting held stays as it was.
func toggled(list []string, v string) []string {
	if slices.Contains(list, v) {
		return slices.DeleteFunc(slices.Clone(list), func(o string) bool { return o == v })
	}
	return append(slices.Clone(list), v)
}

// sectionDrop starts a session dropped on a section's heading again
// there: its agent in that folder, or on that host in the session's own.
func sectionDrop(m *model, w ggui.Widget, nodeID, project string) ggui.Widget {
	over := ggui.State(false)
	marked := ggui.View(over, func(o bool) ggui.Widget {
		if o {
			return ggui.Box(w).Border(1, tokAccentBright).Radius(7)
		}
		return ggui.Box(w).Border(1, color.Transparent).Radius(7)
	})
	return ggui.DropZone(marked, func(d ggui.Dropped[tabDrag]) {
		over.Set(false)
		m.endDrag()
		s, ok := m.session(d.Value.ID)
		if !ok {
			return
		}
		dir := project
		if dir == "" {
			dir = s.GetProject()
		}
		if s.GetNodeId() == nodeID && s.GetProject() == dir {
			return // where it already is
		}
		m.launch(agentspec.Target(s.GetAgent(), s.GetProfile()), nodeID, dir)
	}).OnHover(over.Set)
}
