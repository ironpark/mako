package gui

import (
	"cmp"
	"image/color"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/protocol"
)

// A pane's tabs are kept together by project: a session's tab is of the
// project section the sidebar lists it in, and a file's or a diff's of
// the section holding the file, or the diff's repository; of a project of
// several folders, merged from several, each folder counts as a project
// of its own. The tabs of one project stand side by side, the projects in
// the order their first tabs came, and where a pane has tabs of several
// projects, each project's run of tabs is headed by its name and lined in
// its colour, as the sidebar marks it.

// tabProject is the project a tab is of: its key, "" for none, and how
// the tab bar names and marks it.
type tabProject struct {
	Key  string
	Name string
	Look projectLook
	Root bool // a folder of a project of several, marked # as the sidebar marks it
}

// tabProjects are the projects of tabs, by tab, among groups, named as the
// sidebar names them: a folder of a project of several by its own name.
func tabProjects(tabs []string, groups []sessionGroup, nodes []*protocol.Node, looks map[string]projectLook, docs map[string]docInfo, diffs map[string]diffTab, ptabs ...map[string]pluginTab) map[string]tabProject {
	labels := projectLabels(groups, nodes)
	out := make(map[string]tabProject, len(tabs))
	for _, id := range tabs {
		i, root := tabPlace(id, groups, docs, diffs, ptabs...)
		if i < 0 {
			continue
		}
		g := groups[i]
		name := cmp.Or(g.Name, labels[g.Key], groupName(g))
		look := lookOf(looks, g.Key)
		if root >= 0 {
			// The folders of one project are told apart by colour too,
			// the first in the project's own.
			name = g.Def.Roots[root].label()
			look.Color = rootColor(look.Color, root)
		}
		out[id] = tabProject{Key: tabProjectKey(g, root), Name: name, Look: look, Root: root >= 0}
	}
	// A new session's tab goes with the tab it opened after, rather than
	// in a run of its own.
	for i, id := range tabs {
		if p, ok := out[tabs[max(i-1, 0)]]; ok && i > 0 && isLaunchTab(id) {
			out[id] = p
		}
	}
	return out
}

// rootColor is the colour of a project's folder root, of a project whose
// colour is base: the colours after it in turn, never the grey.
func rootColor(base string, root int) string {
	n := len(lookColors) - 1
	i := max(lookColorIndex(base), 1) - 1
	return lookColors[1+(i+root)%n].Name
}

// tabProjectKey keys section g's tabs, or of a project of several folders,
// those of its folder root.
func tabProjectKey(g sessionGroup, root int) string {
	if root < 0 {
		return g.Key
	}
	return g.Key + "\x00" + strconv.Itoa(root)
}

// tabPlace is where tab id is among groups: the index of its section, -1
// for none, and of a project of several folders, the index of its folder
// among the project's roots, else -1. A session is where the sidebar lists
// it; a file in the deepest folder holding it, and a diff in the one
// holding its repository, else the folder it was opened from.
func tabPlace(id string, groups []sessionGroup, docs map[string]docInfo, diffs map[string]diffTab, ptabs ...map[string]pluginTab) (section, root int) {
	var from fileRoot
	switch {
	case isDocTab(id):
		d := docs[id]
		from = fileRoot{Node: d.Node, Path: d.Path}
	case isDiffTab(id):
		d := diffs[id]
		from = d.Root
		if d.Repo != "" {
			from.Path = d.Repo
		}
	case isPluginTab(id):
		var pt pluginTab
		if len(ptabs) > 0 && ptabs[0] != nil {
			pt = ptabs[0][id]
		}
		if pt.Root.Path != "" {
			from = pt.Root
		} else if projID, ok := strings.CutPrefix(pt.Scope, protocol.ScopeProject+":"); ok && projID != "" {
			for i, g := range groups {
				if g.Key == projID {
					return i, -1
				}
			}
			return -1, -1
		}
	default:
		for i, g := range groups {
			if !slices.ContainsFunc(g.Sessions, func(s *protocol.SessionInfo) bool { return s.GetId() == id }) {
				continue
			}
			if r, ok := g.RootOf[id]; ok && g.multiRoot() {
				return i, r
			}
			return i, -1
		}
		return -1, -1
	}
	if from.Path == "" {
		return -1, -1
	}
	section, root, depth := -1, -1, -1
	holds := func(r fileRoot) bool {
		return r.Node == from.Node && pathscope.Within(from.Path, r.Path) && len(r.Path) > depth
	}
	for i, g := range groups {
		if g.multiRoot() {
			for j, pr := range g.Def.Roots {
				for _, l := range pr.Locations {
					if holds(l) {
						section, root, depth = i, j, len(l.Path)
					}
				}
			}
			continue
		}
		for _, r := range g.Roots {
			if holds(r) {
				section, root, depth = i, -1, len(r.Path)
			}
		}
	}
	return section, root
}

// tabsByProject orders tabs so that those of one project stand together,
// the projects in the order their first tabs come, and each project's tabs
// in the order they were.
func tabsByProject(tabs []string, key func(string) string) []string {
	var order []string
	runs := map[string][]string{}
	for _, id := range tabs {
		k := key(id)
		if _, ok := runs[k]; !ok {
			order = append(order, k)
		}
		runs[k] = append(runs[k], id)
	}
	if len(order) < 2 {
		return tabs
	}
	out := make([]string, 0, len(tabs))
	for _, k := range order {
		out = append(out, runs[k]...)
	}
	return out
}

// tabKeys are the keys of the projects tabs are of, as the window holds
// them now.
func (m *model) tabKeys(tabs []string) func(string) string {
	l := ggui.Peek(m.Spaces)
	groups := groupProjects(ggui.Peek(m.Sessions), ggui.Peek(m.Nodes), ggui.Peek(m.Prefs).Projects, l.Projects, l.Names)
	docs, diffs, ptabs := ggui.Peek(m.Docs), ggui.Peek(m.Diffs), ggui.Peek(m.PluginTabs)
	keys := make(map[string]string, len(tabs))
	for idx, id := range tabs {
		if idx > 0 && isLaunchTab(id) {
			if prev, ok := keys[tabs[idx-1]]; ok {
				keys[id] = prev
				continue
			}
		}
		if i, root := tabPlace(id, groups, docs, diffs, ptabs); i >= 0 {
			keys[id] = tabProjectKey(groups[i], root)
		}
	}
	return func(id string) string { return keys[id] }
}

// paneProjects follows the sections of the tabs of a pane.
func paneProjects(m *model, group ggui.Readable[tabGroup]) ggui.Readable[map[string]tabProject] {
	kept := ggui.Map(m.Prefs, func(p prefs) []keptProject { return p.Projects }).WithEqual(slices.Equal)
	return ggui.Derived(func() map[string]tabProject {
		l := m.Spaces.Get()
		nodes := m.Nodes.Get()
		groups := groupProjects(m.Sessions.Get(), nodes, kept.Get(), l.Projects, l.Names)
		return tabProjects(group.Get().Tabs, groups, nodes, l.Looks, m.Docs.Get(), m.Diffs.Get(), m.PluginTabs.Get())
	}).WithEqual(func(a, b map[string]tabProject) bool {
		if len(a) != len(b) {
			return false
		}
		for k, v := range a {
			if w, ok := b[k]; !ok || w != v {
				return false
			}
		}
		return true
	})
}

// manyProjects reports whether tabs are of more than one project.
func manyProjects(tabs []string, projects map[string]tabProject) bool {
	for _, id := range tabs {
		if projects[id].Key != projects[tabs[0]].Key {
			return true
		}
	}
	return false
}

// showProjectChips reports whether a pane's tab bar should display project
// chips: whenever tabs span multiple projects, or any tab belongs to a
// sub-project (a folder root of a multi-root project).
func showProjectChips(tabs []string, projects map[string]tabProject) bool {
	if manyProjects(tabs, projects) {
		return true
	}
	for _, id := range tabs {
		if projects[id].Root {
			return true
		}
	}
	return false
}

// projectChipMax is the widest a project's name in the tab bar is drawn.
const projectChipMax = 120

// projectChip is the label heading a project's run of tabs: its icon and
// name, in its colour, and with folded tabs folded away, how many.
func projectChip(p tabProject, folded int) ggui.Widget {
	name := cmp.Or(p.Name, T("Other"))
	var glyph ggui.Widget = icon("folder").Size(12).Color(skin.Muted)
	fg := color.Color(skin.Muted)
	switch {
	case p.Root:
		fg = p.Look.color()
		glyph = widget.Text("#").Size(13).Weight(ggui.WeightBold).Color(fg).NoWrap()
	case p.Key != "":
		glyph, fg = p.Look.glyph(12), p.Look.color()
	}
	parts := []ggui.Widget{
		glyph,
		widget.Box(widget.Text(name).Size(13).Weight(ggui.WeightSemibold).Color(fg).NoWrap().Ellipsis()).MaxWidth(projectChipMax),
	}
	if folded > 0 {
		parts = append(parts, widget.Text(strconv.Itoa(folded)).Size(12).Weight(ggui.WeightMedium).Color(fg).NoWrap())
	}
	// As tall as the chip the group draws it on.
	return ui.Tooltip(widget.Row(parts...).Gap(5).Align(widget.AlignCenter), name)
}

// projectChipWidth estimates how wide a project's label is drawn, with the
// room its group takes round its run: 10 inside the label on each side, 2
// after it, and 3 outside the group on each side.
func projectChipWidth(name string) float64 {
	return min(float64(len([]rune(name)))*7.5, projectChipMax) + 17 + 20 + 2 + 6
}

// foldKey keys project's run of tabs in pane, folded or not.
func foldKey(pane int, project string) string { return strconv.Itoa(pane) + "\x00" + project }

// toggleFold folds the run of tabs of key away under its label, but for
// the tab its pane shows, or unfolds it.
func (m *model) toggleFold(key string) {
	m.FoldedTabs.Update(func(f map[string]bool) map[string]bool {
		f = maps.Clone(f)
		if f[key] {
			delete(f, key)
		} else {
			f[key] = true
		}
		return f
	})
}

func (m *model) clearFold(key string) {
	if !ggui.Peek(m.FoldedTabs)[key] {
		return
	}
	m.FoldedTabs.Update(func(f map[string]bool) map[string]bool {
		f = maps.Clone(f)
		delete(f, key)
		return f
	})
}

// hiddenTabsOf is how many of pane's tabs are out of sight.
func (m *model) hiddenTabsOf(pane int) *ggui.StateValue[int] {
	h := m.hiddenTabs[pane]
	if h == nil {
		h = ggui.State(0)
		m.hiddenTabs[pane] = h
	}
	return h
}
