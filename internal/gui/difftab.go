package gui

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
)

// A changed file's diff, chosen in the Changes tab or in a commit of the
// History tab, shows as a tab of its own, as an open file does: beside the
// terminals and arranged into panes as they are.
//
// A diff tab's ID is diffPrefix, the commit ("" for the work tree's
// changes, or the base they are read against), the node and the file's
// path.
//
// A diff opened with a click is a preview: the next file clicked takes its
// tab rather than opening another, until the tab is kept, with a double
// click on it or its menu, or the diff is worked on.

const diffPrefix = "diff\x00"

// isDiffTab reports whether a tab shows a diff.
func isDiffTab(id string) bool { return strings.HasPrefix(id, diffPrefix) }

// isSessionTab reports whether a tab shows a session, rather than a file
// or a diff.
func isSessionTab(id string) bool { return id != "" && !isDocTab(id) && !isDiffTab(id) }

// diffTab is what a diff tab shows.
type diffTab struct {
	Root    fileRoot // the project it was opened from, which the panels keep showing
	Repo    string   // the work tree on the node
	Commit  string   // the commit whose change it is, "" for the work tree's
	Base    string   // the branch the work tree's changes are read since, "" for HEAD
	File    *protocol.GitFile
	Diff    *protocol.Diff
	Err     string
	Loading bool
	Preview bool // the next file opened takes this tab
}

// Name is the file's name.
func (d diffTab) Name() string { return baseName(d.File.GetPath()) }

// workTree reports whether the diff is of the work tree against HEAD, the
// changes a hunk can be undone in.
func (d diffTab) workTree() bool { return d.Commit == "" && d.Base == "" }

func diffTabID(node, repo, against, path string) string {
	return diffPrefix + against + "\x00" + dirKey(node, joinPath(repo, path))
}

// openDiffTab shows a file's diff in a tab, reading it again when the tab
// is already open. A preview takes the tab of the last preview, if any.
func (m *model) openDiffTab(root fileRoot, repo, commit, base string, f *protocol.GitFile, preview bool) {
	id := diffTabID(root.Node, repo, commit+base, f.GetPath())
	var replaced string
	if _, open := ggui.Peek(m.Diffs)[id]; !open && preview {
		for other, d := range ggui.Peek(m.Diffs) {
			if d.Preview && slices.Contains(ggui.Peek(m.Tabs), other) {
				replaced = other
			}
		}
	}
	m.Diffs.Update(func(v map[string]diffTab) map[string]diffTab {
		v = maps.Clone(v)
		d, ok := v[id]
		if !ok {
			d = diffTab{Root: root, Repo: repo, Commit: commit, Base: base, Loading: true, Preview: preview}
		} else if !preview {
			d.Preview = false
		}
		d.File = f
		v[id] = d
		return v
	})
	if replaced != "" {
		m.replaceTab(replaced, id)
		m.Diffs.Update(func(v map[string]diffTab) map[string]diffTab {
			v = maps.Clone(v)
			delete(v, replaced)
			return v
		})
	}
	m.showTab(id)
	m.readDiff(id)
}

// doubleTap is how soon a second tap on a tab makes a double tap.
const doubleTap = 400 * time.Millisecond

// tabTap is the last tap on a tab, and when.
type tabTap struct {
	id string
	at time.Time
}

// keepDiff makes a preview diff's tab its own.
func (m *model) keepDiff(id string) {
	m.updateDiff(id, func(d *diffTab) { d.Preview = false })
}

// readDiff reads a diff tab's diff, keeping the last on show until it
// comes.
func (m *model) readDiff(id string) {
	d, ok := ggui.Peek(m.Diffs)[id]
	if !ok {
		return
	}
	req := &protocol.GitDiff{NodeId: m.remoteID(d.Root.Node), Root: d.Repo, Path: d.File.GetPath(), From: d.File.GetFrom(), Commit: d.Commit, Base: d.Base}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Diff, error) {
		return c.GitDiff(ctx, m.key(), req)
	}, func(diff *protocol.Diff, err error) {
		m.updateDiff(id, func(d *diffTab) {
			d.Diff, d.Err, d.Loading = diff, "", false
			if err != nil {
				d.Err = nodeErr(err)
			}
		})
	})
}

func (m *model) updateDiff(id string, fn func(*diffTab)) {
	m.Diffs.Update(func(v map[string]diffTab) map[string]diffTab {
		d, ok := v[id]
		if !ok {
			return v // closed meanwhile
		}
		v = maps.Clone(v)
		fn(&d)
		v[id] = d
		return v
	})
}

// followChanges reads again the open diffs of a work tree's changes, as
// its changes are read again: a file may have changed with the rest, or
// have none left.
func (m *model) followChanges(v gitView) {
	for id, d := range ggui.Peek(m.Diffs) {
		if d.Commit != "" || d.Base != v.Base || d.Root != v.Root || d.Repo != v.Changes.GetRoot() {
			continue
		}
		i := slices.IndexFunc(v.Changes.GetFiles(), func(f *protocol.GitFile) bool { return f.GetPath() == d.File.GetPath() })
		if i < 0 {
			m.updateDiff(id, func(d *diffTab) { d.Diff, d.Err = &protocol.Diff{}, "" })
			continue
		}
		m.updateDiff(id, func(d *diffTab) { d.File = v.Changes.GetFiles()[i] })
		m.readDiff(id)
	}
}

// closeDiff closes a diff's tab.
func (m *model) closeDiff(id string) {
	m.closeTab(id)
	m.Diffs.Update(func(v map[string]diffTab) map[string]diffTab {
		v = maps.Clone(v)
		delete(v, id)
		return v
	})
}

// siblings are the files a diff steps through: the changes it is one of,
// or the commit's files.
func (m *model) siblings(d diffTab) []*protocol.GitFile {
	if d.Commit != "" {
		if c := ggui.Peek(m.GitCommit); c.Hash == d.Commit {
			return c.Info.GetFiles()
		}
		return nil
	}
	if v := ggui.Peek(m.Git); v.Root == d.Root && v.Base == d.Base && v.Changes.GetRoot() == d.Repo {
		return v.Changes.GetFiles()
	}
	return nil
}

// stepDiff shows the next changed file's diff, or with -1 the one before,
// in the tab the diff shown is in when it is a preview.
func (m *model) stepDiff(dir int) {
	id := ggui.Peek(m.Selected)
	d, ok := ggui.Peek(m.Diffs)[id]
	if !ok {
		return
	}
	files := m.siblings(d)
	i := slices.IndexFunc(files, func(f *protocol.GitFile) bool { return f.GetPath() == d.File.GetPath() })
	if i < 0 || i+dir < 0 || i+dir >= len(files) {
		return
	}
	m.openDiffTab(d.Root, d.Repo, d.Commit, d.Base, files[i+dir], d.Preview)
}

// discardHunk undoes a hunk of a work tree's diff, once confirmed.
func (m *model) askDiscardHunk(id string, hunk []*protocol.DiffLine) {
	m.pendingHunk = func() {
		d, ok := ggui.Peek(m.Diffs)[id]
		if !ok {
			return
		}
		m.keepDiff(id)
		req := &protocol.GitRun{NodeId: m.remoteID(d.Root.Node), Root: d.Repo, Op: protocol.GitOpDiscard, Paths: []string{d.File.GetPath()}, Hunk: hunk}
		fetch(m, gitOpTimeout, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
			return c.GitRun(ctx, m.key(), req)
		}, func(_ *protocol.GitResult, err error) {
			if err != nil {
				m.updateDiff(id, func(d *diffTab) { d.Err = nodeErr(err) })
				return
			}
			m.readDiff(id)
			m.loadGit(ggui.Peek(m.Git).Root)
		})
	}
	m.HunkOpen.Set(true)
}

// View --------------------------------------------------------------------------

// diffTabFace is a diff's tab: how the file changed, its name, and in a
// commit, which. A preview's name is dimmed, and a double click keeps it.
func diffTabFace(m *model, id string, d diffTab, selected, focused bool) ggui.Widget {
	fg := tokMuted
	if selected && !d.Preview {
		fg = tokFg
	}
	name := d.Name()
	label := name
	switch {
	case d.Commit != "":
		label += " @ " + shortHash(d.Commit)
	case d.Base != "":
		label += " ← " + d.Base
	}
	content := ggui.Box(ggui.Row(
		statusIcon(d.File.GetStatus()),
		ggui.Box(ggui.Text(label).Size(13).Color(fg).NoWrap().Ellipsis()).MaxWidth(190),
		ui.ButtonOf(icon("x").Size(13), func() { m.closeDiff(id) }).Ghost().Pad(3).Radius(4).Name(Tf("Close %s", label)),
	).Gap(8)).Height(tabBarHeight)
	btn := ui.ButtonOf(content, func() {
		// The bar is drawn again as the first tap shows the tab, so the
		// last tap is kept with the model.
		if last := m.lastTap; last.id == id && time.Since(last.at) < doubleTap {
			m.keepDiff(id)
		}
		m.lastTap = tabTap{id, time.Now()}
		m.showTab(id)
	}).Ghost().Pad(0, 8, 0, 12).Radius(0).Name(label)
	return tabCell(btn, selected, focused)
}

// diffTabView is a diff tab's content: the file's line and the diff's
// tools over its lines, and while lines are picked, what to do with them.
func diffTabView(m *model, id string) ggui.Widget {
	d := ggui.Map(m.Diffs, func(v map[string]diffTab) diffTab { return v[id] }).WithEqual(func(a, b diffTab) bool {
		return a.Err == b.Err && a.Loading == b.Loading && protocol.Same(a.File, b.File) && protocol.Same(a.Diff, b.Diff)
	})
	mode := ggui.Map(m.Prefs, func(p prefs) diffMode { return diffMode{Split: p.DiffSplit, Wrap: p.DiffWrap} })
	sel := ggui.State(noSel)
	type shown struct {
		D    diffTab
		Mode diffMode
		Sel  lineSel
	}
	state := ggui.Derived(func() shown {
		cur := d.Get()
		s := sel.Get()
		if s.To >= len(cur.Diff.GetLines()) {
			s = noSel // the diff was read again, shorter
		}
		return shown{cur, mode.Get(), s}
	}).WithEqual(func(a, b shown) bool {
		return a.Mode == b.Mode && a.Sel == b.Sel && a.D.Err == b.D.Err && a.D.Loading == b.D.Loading &&
			protocol.Same(a.D.File, b.D.File) && protocol.Same(a.D.Diff, b.D.Diff)
	})
	return ggui.View(state, func(v shown) ggui.Widget {
		dt := v.D
		pick := func(i int, extend bool) {
			cur := ggui.Peek(sel)
			switch {
			case extend && !cur.empty():
				sel.Set(lineSel{min(cur.From, i), max(cur.To, i)})
			case cur.From == i && cur.To == i:
				sel.Set(noSel)
			default:
				sel.Set(lineSel{i, i})
			}
		}
		hunk := func(i int) ggui.Widget {
			start, lines := hunkAt(dt.Diff.GetLines(), i)
			actions := []ggui.Widget{
				iconButton("send-horizontal", T("Ask an agent about this hunk"), func() { sel.Set(lineSel{start + 1, start + len(lines)}) }),
			}
			if dt.workTree() && (dt.File.GetStatus() == protocol.GitModified || dt.File.GetStatus() == protocol.GitRenamed) {
				actions = append(actions, iconButton("undo-2", T("Discard this hunk"), func() { m.askDiscardHunk(id, lines) }))
			}
			return ggui.Row(actions...).Gap(2)
		}
		var body ggui.Widget
		if note := diffNote(dt.Loading, dt.Err, dt.Diff); note != nil {
			body = note
		} else {
			body = diffRows(dt.Diff, v.Mode, v.Sel, pick, hunk)
		}
		rows := []ggui.Widget{
			diffHeader(dt.File, diffTools(m, id, dt, v.Mode)),
			hline(),
			ggui.Expanded(ggui.Box(body).Fill(tokSurface1)),
		}
		if !v.Sel.empty() {
			rows = append(rows, selectionBar(m, dt.Root, joinPath(subfolder(dt.Repo, dt.Root.Path), dt.File.GetPath()), dt.Diff.GetLines(), sel))
		}
		return ggui.Column(rows...).Stretch()
	})
}

// diffNote is what a diff shows in place of its lines, nil when it has
// lines to show.
func diffNote(loading bool, err string, d *protocol.Diff) ggui.Widget {
	switch {
	case loading:
		return ggui.Center(ui.Spinner())
	case err != "":
		return paneNote(err)
	case d.GetBinary():
		return paneNote(T("A binary file; no lines to show."))
	case len(d.GetLines()) == 0:
		return paneNote(T("No changes in its lines."))
	}
	return nil
}

// diffTools are a diff's controls: the files before and after it, how it
// is drawn, and reading it again.
func diffTools(m *model, id string, d diffTab, mode diffMode) ggui.Widget {
	toggle := func(glyph, label string, on bool, set func(*prefs)) ggui.Widget {
		c := tokExtraMuted
		if on {
			c = tokFg
		}
		return ui.Tooltip(ui.ButtonOf(icon(glyph).Color(c), func() {
			m.Prefs.Update(func(p prefs) prefs { set(&p); return p })
		}).Ghost().Pad(6).Radius(6).Name(label), label)
	}
	tools := []ggui.Widget{}
	if len(m.siblings(d)) > 1 {
		tools = append(tools,
			iconButton("chevron-up", T("Previous file (⌥⌘↑)"), func() { m.showTab(id); m.stepDiff(-1) }),
			iconButton("chevron-down", T("Next file (⌥⌘↓)"), func() { m.showTab(id); m.stepDiff(1) }),
		)
	}
	return ggui.Row(append(tools,
		toggle("columns-2", T("Side by side"), mode.Split, func(p *prefs) { p.DiffSplit = !p.DiffSplit }),
		toggle("wrap-text", T("Wrap long lines"), mode.Wrap, func(p *prefs) { p.DiffWrap = !p.DiffWrap }),
		iconButton("refresh-cw", T("Refresh"), func() { m.readDiff(id) }),
	)...).Gap(2)
}

// diffHeaderBar is the workspace header over a diff tab: the file, where
// it is, and in a commit, which.
func diffHeaderBar(m *model, id string, d diffTab) ggui.Widget {
	where := nodeName(ggui.Peek(m.Nodes), d.Root.Node) + ":" + prettyPath(joinPath(d.Repo, d.File.GetPath()))
	what := T("Changes")
	switch {
	case d.Commit != "":
		what = shortHash(d.Commit)
	case d.Base != "":
		what = Tf("Since %s", d.Base)
	}
	path := joinPath(d.Repo, d.File.GetPath())
	items := []ggui.Widget{
		ui.MenuItem(T("Open file"), func() {
			m.openFile(fileItem{Key: dirKey(d.Root.Node, path), Node: d.Root.Node, Path: path, Name: d.Name()})
		}),
		ui.MenuItem(T("Copy path"), func() { ggui.CurrentClipboard().Write(path) }),
		ui.MenuItem(T("Refresh"), func() { m.readDiff(id) }),
	}
	if d.Preview {
		items = append(items, ui.MenuItem(T("Keep open"), func() { m.keepDiff(id) }))
	}
	items = append(items, ui.MenuDivider(), ui.MenuItem(T("Close diff"), func() { m.closeDiff(id) }))
	return ggui.Row(
		ggui.Text(d.Name()).Size(14).Weight(ggui.WeightMedium).NoWrap(),
		ggui.Expanded(muted(ggui.Text(where).Size(13).NoWrap().Ellipsis())),
		muted(ggui.Text(what).Size(12.5).NoWrap()),
		iconMenu(ui.MenuOf(icon("ellipsis"), items...).Name(T("Diff actions")), 6),
	).Gap(10)
}
