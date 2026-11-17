package gui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A node's file browser has, as a file manager has, the places to go to
// down its side: the node's home and its root, and the folders of the
// projects on it; and its list sorts by a column and narrows to the names
// typed over it.

// browserSort is how the list is sorted: by a column, either way. Folders
// come before files whichever.
type browserSort struct {
	By   sortColumn
	Desc bool
}

type sortColumn int

const (
	sortName sortColumn = iota
	sortSize
	sortMod
)

// sorted is items in the order s says, a stub left as it is.
func (s browserSort) sorted(items []fileItem) []fileItem {
	if len(items) < 2 {
		return items
	}
	out := slices.Clone(items)
	slices.SortStableFunc(out, func(a, b fileItem) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		var c int
		switch s.By {
		case sortSize:
			c = cmp.Compare(a.Size, b.Size)
		case sortMod:
			c = a.Mod.Compare(b.Mod)
		}
		if c == 0 {
			c = cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Name, b.Name))
		}
		if s.Desc {
			return -c
		}
		return c
	})
	return out
}

// matching is the items whose names hold query, in any case; all of them
// for none, and a stub always.
func matching(items []fileItem, query string) []fileItem {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return items
	}
	return slices.DeleteFunc(slices.Clone(items), func(it fileItem) bool {
		return !it.Stub && !strings.Contains(strings.ToLower(it.Name), q)
	})
}

// browserPlace is a folder down the browser's side.
type browserPlace struct {
	Name, Path string
	Icon       string      // an icon's name, when Look is not to mark it
	Look       projectLook // a project's look
}

// projectPlaces are the folders of the projects on the node on says, by
// name, each once.
func projectPlaces(groups []sessionGroup, nodes []*protocol.Node, looks map[string]projectLook, on func(node string) bool) []browserPlace {
	labels := projectLabels(groups, nodes)
	var out []browserPlace
	seen := map[string]bool{}
	for _, g := range groups {
		name := cmp.Or(g.Name, labels[g.Key], groupName(g))
		for _, r := range g.Roots {
			if !on(r.Node) || seen[r.Path] {
				continue
			}
			seen[r.Path] = true
			label := name
			if g.rootCount() > 1 {
				label = name + " / " + baseName(r.Path)
			}
			out = append(out, browserPlace{Name: label, Path: r.Path, Look: lookOf(looks, g.Key)})
		}
	}
	slices.SortStableFunc(out, func(a, b browserPlace) int {
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}

// rootOf is the root of the file system a path is on: / for a Unix path,
// its drive's for a Windows one.
func rootOf(p string) string {
	if pathSep(p) == `\` {
		if i := strings.Index(p, `\`); i > 0 {
			return p[:i+1]
		}
		return p
	}
	return "/"
}

// browserPlaces is the browser's side: its places, then its projects.
func browserPlaces(b *browser) ggui.Widget {
	m := b.m
	kept := ggui.Map(m.Prefs, func(p prefs) []keptProject { return p.Projects }).WithEqual(slices.Equal)
	projects := ggui.Derived(func() []browserPlace {
		l := m.Spaces.Get()
		nodes := m.Nodes.Get()
		groups := groupProjects(m.Sessions.Get(), nodes, kept.Get(), l.Projects, l.Names)
		// The node is "" for the one the window talks through, as a
		// request names it.
		self := m.Conn.Get().Self.GetId()
		node := cmp.Or(b.node, self)
		return projectPlaces(groups, nodes, l.Looks, func(n string) bool { return cmp.Or(n, self) == node })
	}).WithEqual(slices.Equal)
	return ggui.Reactive(func() ggui.Widget {
		cur := b.Path.Get()
		home := b.Home.Get()
		var places []browserPlace
		if home != "" {
			places = append(places, browserPlace{Name: T("Home"), Path: home, Icon: "house"})
		}
		root := rootOf(cmp.Or(home, cur))
		places = append(places, browserPlace{Name: cmp.Or(nodeName(m.Nodes.Get(), b.node), root), Path: root, Icon: "hard-drive"})
		rows := []ggui.Widget{placesHeading(T("Places"))}
		for _, p := range places {
			rows = append(rows, placeRow(b, p, cur))
		}
		if ps := projects.Get(); len(ps) > 0 {
			rows = append(rows, widget.Box().Height(10), placesHeading(T("Projects")))
			for _, p := range ps {
				rows = append(rows, placeRow(b, p, cur))
			}
		}
		list := widget.Scroll(widget.Box(widget.Column(rows...).Gap(1).Stretch()).Pad(8)).Bar(skin.Surface3)
		return widget.Box(list).Width(placesWidth).Fill(skin.Surface0)
	})
}

// placesWidth is how wide the browser's side is.
const placesWidth = 188

func placesHeading(text string) ggui.Widget {
	return widget.Box(widget.Text(text).Size(11).Weight(ggui.WeightSemibold).Color(skin.ExtraMuted).NoWrap()).Pad(6, 8, 4, 8)
}

// placeRow is a place: its icon and name, lit while the list shows it;
// files dropped on it go up into it.
func placeRow(b *browser, p browserPlace, cur string) ggui.Widget {
	var glyph ggui.Widget
	if p.Icon != "" {
		glyph = icon(p.Icon).Size(14).Color(skin.Muted)
	} else {
		glyph = p.Look.glyph(14)
	}
	here := p.Path == cur
	label := widget.Text(p.Name).Size(13).NoWrap().Ellipsis()
	if !here {
		label = label.Color(skin.Muted)
	}
	row := ui.ButtonOf(widget.Row(glyph, widget.Expanded(widget.Align(label).Left())).Gap(8).Align(widget.AlignCenter),
		func() { b.goTo(p.Path) }).Ghost().Pad(5, 8).Radius(6).Name(p.Name)
	var w ggui.Widget = ui.Tooltip(row, prettyPath(p.Path))
	if here {
		w = widget.Box(w).Fill(skin.SidebarSel).Radius(6)
	}
	return litDrop(w, nil, func(paths []string) { b.upload(p.Path, paths) })
}

// columnButton heads a column of the list: its name, which sorts by it,
// and again the other way, with an arrow on the column sorted by.
func columnButton(b *browser, label string, by sortColumn, align float64) ggui.Widget {
	return ggui.View(b.Sort, func(s browserSort) ggui.Widget {
		parts := []ggui.Widget{columnHead(label, align)}
		if s.By == by {
			arrow := icon(pick(s.Desc, "arrow-down", "arrow-up")).Size(11).Color(skin.Muted)
			if align == 1 {
				parts = []ggui.Widget{arrow, parts[0]}
			} else {
				parts = append(parts, arrow)
			}
		}
		btn := ui.ButtonOf(widget.Row(parts...).Gap(3).Align(widget.AlignCenter), func() {
			b.Sort.Set(browserSort{By: by, Desc: s.By == by && !s.Desc})
		}).Ghost().Pad(2, 0).Radius(4).Name(Tf("Sort by %s", label))
		if align == 1 {
			return widget.Row(widget.Expanded(widget.Box()), btn)
		}
		return widget.Row(btn, widget.Expanded(widget.Box()))
	})
}

// filterField narrows the list to the names typed in it; Escape clears it.
func filterField(b *browser) ggui.Widget {
	input := widget.TextInput(b.Query).Placeholder(T("Filter")).Name(T("Filter files")).
		Style(ggui.TextStyle{Size: 12.5}).OnKey(func(ev ggui.KeyEvent) bool {
		if ev.Key == ggui.KeyEscape && ggui.Peek(b.Query) != "" {
			b.Query.Set("")
			return true
		}
		return false
	})
	clear := ggui.If(ggui.Map(b.Query, func(q string) bool { return q != "" }), func() ggui.Widget {
		return ui.ButtonOf(icon("x").Size(12), func() { b.Query.Set("") }).Ghost().Pad(2).Radius(4).Name(T("Clear filter"))
	})
	return widget.Box(widget.Row(
		icon("search").Size(13).Color(skin.Muted),
		widget.Expanded(input),
		clear,
	).Gap(6).Align(widget.AlignCenter)).Pad(4, 8).Border(1, skin.Border).Radius(7).Fill(skin.Surface0).Width(180)
}

// emptyFolder stands in the list for a folder with nothing in it, or
// nothing that matches the filter.
func emptyFolder(filtered bool) ggui.Widget {
	text := T("This folder is empty.")
	glyph := "folder-open"
	if filtered {
		text, glyph = T("No files match the filter."), "search"
	}
	return widget.Center(widget.Column(
		icon(glyph).Size(28).Color(skin.ExtraMuted),
		muted(widget.Text(text).Size(13)),
	).Gap(10).Align(widget.AlignCenter))
}
