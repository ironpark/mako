package gui

import (
	"slices"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/protocol"
)

// The Files and Git tabs of a session in a project of several folders show
// the project's other folders too: the Files tab each of them, as a folder
// of its own at the top of the tree, the session's open; the Git tab one
// at a time, the session's unless another was picked over its changes.

// panelRoot is one of the folders a panel shows: where it is, and how the
// panel names it.
type panelRoot struct {
	fileRoot
	Label string
}

// panelRoots are the folders a panel shows of the project of the selected
// session or tab.
type panelRoots struct {
	Name  string // the project's, "" for a lone folder
	Roots []panelRoot
	Own   int // the index in Roots of the session's, or tab's, own folder
}

// multi reports whether there are several to show.
func (p panelRoots) multi() bool { return len(p.Roots) > 1 }

// rootFolder is the folder a project root shows when own was placed in it:
// the deepest location of r on own's node that holds own (so a session
// started in a subfolder still browses the root whose name it bears), or
// own itself when own sits outside r's locations (such as a git worktree).
func rootFolder(r projectRoot, own fileRoot) fileRoot {
	best, depth := own, -1
	for _, l := range r.Locations {
		if l.Node == own.Node && pathscope.Within(own.Path, l.Path) && len(l.Path) > depth {
			best, depth = l, len(l.Path)
		}
	}
	return best
}

// projectRoots are the folders of the project own is in, as pl places it:
// one for each of its roots, own's root folder (or own itself for an external
// worktree) and, for the others, their folder on own's node, else on one of
// reachable; own alone outside a project of several.
func projectRoots(defs []project, nodes []*protocol.Node, own fileRoot, pl placement, reachable func(string) bool) panelRoots {
	if own.Path == "" {
		return panelRoots{}
	}
	if pl.At < 0 {
		return panelRoots{Roots: []panelRoot{{fileRoot: own}}}
	}
	p := defs[pl.At]
	own = rootFolder(p.Roots[pl.Root], own)
	if len(p.Roots) < 2 {
		return panelRoots{Roots: []panelRoot{{fileRoot: own}}}
	}
	out := panelRoots{Name: projectLabel(defs, p.ID)}
	for i, r := range p.Roots {
		if i == pl.Root {
			out.Own = len(out.Roots)
			out.Roots = append(out.Roots, panelRoot{own, r.label()})
			continue
		}
		at := slices.IndexFunc(r.Locations, func(l fileRoot) bool { return l.Node == own.Node })
		if at < 0 {
			at = slices.IndexFunc(r.Locations, func(l fileRoot) bool { return reachable(l.Node) })
		}
		if at < 0 {
			continue // on no node this window reaches
		}
		l := r.Locations[at]
		if l == own {
			continue // own is shown already, at its own root
		}
		if slices.ContainsFunc(out.Roots, func(o panelRoot) bool { return o.fileRoot == l }) {
			continue // two roots at one folder show it once
		}
		label := r.label()
		if l.Node != own.Node {
			label += " · " + nodeName(nodes, l.Node)
		}
		out.Roots = append(out.Roots, panelRoot{l, label})
	}
	return out
}

// panelRoots are the folders the Files, Git and forge tabs show now; the
// model keeps them as m.Roots.
func (m *model) panelRoots() panelRoots {
	own := m.filesRoot()
	defs := m.Spaces.Get().Projects
	var pl placement
	// The selected session places the folder only when the folder is its:
	// a scope's folder, or a tab's, is placed by where it is, or it would
	// take the session's root and show twice.
	if s, ok := sessionIn(m.Sessions.Get(), m.Selected.Get()); ok && s.GetNodeId() == own.Node &&
		(s.GetProject() == own.Path || sessionProjectRoot(s) == own.Path) {
		pl = placeSession(defs, s) // its repository places a worktree
	} else {
		pl = placeIn(defs, own.Node, own.Path, "", "")
	}
	nodes := m.Nodes.Get()
	return projectRoots(defs, nodes, own, pl, func(id string) bool {
		n, ok := nodeIn(nodes, id)
		return ok && reachable(n)
	})
}

// own is the folder of the selected session, or tab, itself.
func (p panelRoots) own() fileRoot {
	if len(p.Roots) == 0 {
		return fileRoot{}
	}
	return p.Roots[p.Own].fileRoot
}

// repoFolder is the folder the Git and forge tabs show: the one picked over
// them, when it was picked for the folder the panels follow now, else that
// folder.
func (m *model) repoFolder() fileRoot {
	roots := m.Roots.Get()
	pick := m.RepoPick.Get()
	if pick.For == roots.own() && slices.ContainsFunc(roots.Roots, func(r panelRoot) bool { return r.fileRoot == pick.At }) {
		return pick.At
	}
	return roots.own()
}

// repoPick is a folder picked for the Git and forge tabs, At, while they
// follow the folder For.
type repoPick struct{ For, At fileRoot }

// withRootPicker is a Git or forge tab under the picker of the project's
// folder it shows, for a project of several.
func withRootPicker(m *model, tab ggui.Widget) ggui.Widget {
	shown := ggui.Bind(func() panelRoot {
		at := m.repoFolder()
		for _, r := range m.Roots.Get().Roots {
			if r.fileRoot == at {
				return r
			}
		}
		return panelRoot{}
	}, func(r panelRoot) {
		m.RepoPick.Set(repoPick{For: ggui.Peek(m.Roots).own(), At: r.fileRoot})
	})
	options := ggui.Map(m.Roots, func(p panelRoots) []panelRoot { return p.Roots })
	picker := ggui.View(ggui.Map(m.Roots, panelRoots.multi), func(multi bool) ggui.Widget {
		if !multi {
			return widget.Box()
		}
		pick := ui.Select(shown).BindOptions(options).Format(func(r panelRoot) string { return r.Label }).Name(T("Repository"))
		return widget.Column(
			widget.Box(widget.Row(icon("folder").Size(14).Color(skin.Muted), widget.Expanded(pick)).Gap(8).Align(widget.AlignCenter)).Pad(6, 10, 6, 12),
			hline(),
		).Stretch()
	})
	return widget.Column(picker, widget.Expanded(tab)).Stretch()
}
