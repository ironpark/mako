package gui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
)

// The right panel's Changes tab shows the git changes of the selected
// session's project, read by the daemon of the node it runs on: a tree of
// the changed files, each folder adding up its files' lines, over the diff
// of the file chosen.

// gitView is what is known of the project's changes.
type gitView struct {
	Root    fileRoot // the project they were read for
	Changes protocol.GitChanges
	Err     string
	Loading bool
}

// diffView is the diff shown under the tree.
type diffView struct {
	Key     string // dirKey of the work tree and the file
	File    protocol.GitFile
	Diff    protocol.Diff
	Err     string
	Loading bool
}

// gitRefresh is how often the shown changes are read again.
const gitRefresh = 5 * time.Second

// loadGit reads the project's changes, keeping the last ones on show until
// the new come.
func (m *model) loadGit(root fileRoot) {
	if root.Path == "" {
		return
	}
	v := ggui.Peek(m.Git)
	if v.Loading && v.Root == root {
		return
	}
	if v.Root != root {
		v = gitView{Root: root}
	}
	v.Loading = true
	m.Git.Set(v)
	req := protocol.GitStatus{NodeID: m.remoteID(root.Node), Path: root.Path}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (protocol.GitChanges, error) {
		return c.GitStatus(ctx, m.key(), req)
	}, func(ch protocol.GitChanges, err error) {
		if ggui.Peek(m.Git).Root != root {
			return // another project is shown now
		}
		next := gitView{Root: root, Changes: ch}
		if err != nil {
			next.Changes, next.Err = ggui.Peek(m.Git).Changes, err.Error()
		}
		m.Git.Set(next)
		if d := ggui.Peek(m.GitDiff); d.Key != "" && !d.Loading {
			m.reloadDiff() // the file may have changed with the rest
		}
	})
}

// openDiff reads a changed file's diff into the pane under the tree.
func (m *model) openDiff(f protocol.GitFile) {
	v := ggui.Peek(m.Git)
	key := dirKey(v.Root.Node, joinPath(v.Changes.Root, f.Path))
	m.GitDiff.Set(diffView{Key: key, File: f, Loading: true})
	m.fetchDiff(v, key, f)
}

func (m *model) reloadDiff() {
	d := ggui.Peek(m.GitDiff)
	v := ggui.Peek(m.Git)
	i := slices.IndexFunc(v.Changes.Files, func(f protocol.GitFile) bool { return f.Path == d.File.Path })
	if i < 0 {
		m.GitDiff.Set(diffView{}) // no longer changed
		return
	}
	m.fetchDiff(v, d.Key, v.Changes.Files[i])
}

func (m *model) fetchDiff(v gitView, key string, f protocol.GitFile) {
	req := protocol.GitDiff{NodeID: m.remoteID(v.Root.Node), Root: v.Changes.Root, Path: f.Path, From: f.From}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (protocol.Diff, error) {
		return c.GitDiff(ctx, m.key(), req)
	}, func(d protocol.Diff, err error) {
		if ggui.Peek(m.GitDiff).Key != key {
			return // another file was chosen meanwhile
		}
		next := diffView{Key: key, File: f, Diff: d}
		if err != nil {
			next.Err = err.Error()
		}
		m.GitDiff.Set(next)
	})
}

// gitItem is a row of the changes tree: a changed file, or a folder
// adding up the files under it.
type gitItem struct {
	Key, Name      string
	Dir            bool
	Added, Removed int
	File           protocol.GitFile
	Children       []gitItem
}

// gitTree arranges changed files under their folders, folders first and
// then by name, each folder with its files' lines added up.
func gitTree(files []protocol.GitFile) []gitItem {
	type node struct {
		item gitItem
		kids map[string]*node
	}
	root := &node{kids: map[string]*node{}}
	for _, f := range files {
		parts := strings.Split(f.Path, "/")
		n := root
		for i, part := range parts {
			k, ok := n.kids[part]
			if !ok {
				k = &node{item: gitItem{Key: strings.Join(parts[:i+1], "/"), Name: part, Dir: i < len(parts)-1}, kids: map[string]*node{}}
				n.kids[part] = k
			}
			k.item.Added += f.Added
			k.item.Removed += f.Removed
			if i == len(parts)-1 {
				k.item.File = f
			}
			n = k
		}
	}
	var build func(n *node) []gitItem
	build = func(n *node) []gitItem {
		out := make([]gitItem, 0, len(n.kids))
		for _, k := range n.kids {
			it := k.item
			it.Children = build(k)
			out = append(out, it)
		}
		slices.SortFunc(out, func(a, b gitItem) int {
			if a.Dir != b.Dir {
				if a.Dir {
					return -1
				}
				return 1
			}
			// As the Files tab orders them.
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
		return out
	}
	return build(root)
}

// gitDirs lists the keys of every folder in a tree.
func gitDirs(items []gitItem, into map[string]bool) {
	for _, it := range items {
		if it.Dir {
			into[it.Key] = true
			gitDirs(it.Children, into)
		}
	}
}

// lineCount writes a count of lines as the changes tree shows it: 2.7k
// from a thousand up.
func lineCount(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
}

// statusIcon is a change kind's box, in its colour.
func statusIcon(status string) ggui.Widget {
	switch status {
	case protocol.GitAdded:
		return iconColored("square-plus", tokDiffAdded).Size(14)
	case protocol.GitDeleted:
		return iconColored("square-minus", tokDiffRemoved).Size(14)
	case protocol.GitRenamed:
		return iconColored("square-arrow-right", tokDiffModified).Size(14)
	}
	return iconColored("square-dot", tokDiffModified).Size(14)
}

// counts is a row's +added -removed.
func counts(added, removed int) ggui.Widget {
	return ggui.Row(
		ggui.Text("+"+lineCount(added)).Size(12).Color(tokDiffAdded).NoWrap(),
		ggui.Text("-"+lineCount(removed)).Size(12).Color(tokDiffRemoved).NoWrap(),
	).Gap(5)
}

// Panel ---------------------------------------------------------------------------

func changesPanel(m *model) ggui.Widget {
	root := ggui.Derived(m.filesRoot)
	// Read when shown and for each project, and again every few seconds
	// while shown, as agents change files.
	ggui.Effect(func() ggui.Cleanup {
		r := root.Get()
		if !m.DetailsOpen.Get() || m.DetailTab.Get() != changesTab {
			return nil // nothing shows them
		}
		m.loadGit(r)
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(gitRefresh)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					m.post(func() { m.loadGit(r) })
				}
			}
		}()
		return func() { close(stop) }
	})
	return ggui.Key(root, func(r fileRoot) ggui.Widget {
		if r.Path == "" {
			return ggui.Box(muted(ggui.Text(T("Open a session to see its changes.")).Size(13))).Pad(16)
		}
		return changesView(m)
	})
}

func changesView(m *model) ggui.Widget {
	// A refresh that changes nothing, as most do, leaves the tree be.
	files := ggui.Map(m.Git, func(v gitView) []protocol.GitFile { return v.Changes.Files }).WithEqual(slices.Equal)
	roots := ggui.Map(files, gitTree)
	dirs := ggui.Map(roots, func(items []gitItem) map[string]bool {
		all := map[string]bool{}
		gitDirs(items, all)
		return all
	})
	// Folders start open: the closed ones are kept, and the rest are open.
	expanded := ggui.Bind(func() map[string]bool {
		open := maps.Clone(dirs.Get())
		for k := range m.GitClosed.Get() {
			delete(open, k)
		}
		return open
	}, func(open map[string]bool) {
		closed := maps.Clone(ggui.Peek(m.GitClosed))
		for k := range ggui.Peek(dirs) {
			if open[k] {
				delete(closed, k)
			} else {
				closed[k] = true
			}
		}
		m.GitClosed.Set(closed)
	})
	tree := ui.Tree(roots, func(it gitItem) string { return it.Key }, func(it gitItem) []gitItem { return it.Children },
		func(item ggui.Readable[gitItem]) ggui.Widget {
			return ggui.Reactive(func() ggui.Widget {
				it := item.Get()
				lead := icon("file").Size(14)
				var tail ggui.Widget = ggui.Box().Width(14)
				if it.Dir {
					lead = icon("folder").Size(14)
				} else {
					tail = statusIcon(it.File.Status)
				}
				var numbers ggui.Widget = counts(it.Added, it.Removed)
				if !it.Dir && it.File.Binary {
					numbers = muted(ggui.Text(T("binary")).Size(12))
				}
				name := ggui.Text(it.Name).Size(12.5).NoWrap().Ellipsis()
				if !it.Dir && it.File.Status == protocol.GitDeleted {
					name = name.Color(tokMuted)
				}
				return ggui.Row(lead, ggui.Expanded(name), numbers, tail).Gap(7)
			})
		}).
		RowName(func(it gitItem) string { return it.Name }).
		RowHeight(26).Indent(14).
		BindExpanded(expanded).
		BindSelected(m.GitSel).
		OnSelect(func(it gitItem) {
			if it.Dir {
				open := maps.Clone(expanded.Get())
				open[it.Key] = !open[it.Key]
				expanded.Set(open)
				return
			}
			m.openDiff(it.File)
		}).
		Name(T("Changes"))

	header := ggui.Reactive(func() ggui.Widget {
		v := m.Git.Get()
		var added, removed int
		for _, f := range v.Changes.Files {
			added, removed = added+f.Added, removed+f.Removed
		}
		branch := v.Changes.Branch
		if branch == "" {
			branch = projectName(v.Root.Path)
		}
		var sum ggui.Widget = ggui.Box()
		if len(v.Changes.Files) > 0 {
			sum = counts(added, removed)
		}
		return panelHeader(
			icon("git-branch").Size(15),
			ggui.Expanded(ggui.Text(branch).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
			sum,
			iconButton("refresh-cw", T("Refresh"), func() { m.loadGit(v.Root) }),
		)
	})

	body := ggui.Reactive(func() ggui.Widget {
		v := m.Git.Get()
		var note string
		switch {
		case v.Changes.NotRepo:
			note = T("Not a git repository.")
		case v.Err != "" && len(v.Changes.Files) == 0:
			note = v.Err
		case v.Loading && v.Changes.Root == "":
			return ggui.Box(ggui.Center(ui.Spinner())).Height(80)
		case len(v.Changes.Files) == 0:
			note = T("No changes.")
		}
		if note != "" {
			return ggui.Box(muted(ggui.Text(note).Size(12.5))).Pad(14, 16)
		}
		return ggui.Padding(tree, 4, 4)
	})
	hasDiff := ggui.Map(m.GitDiff, func(d diffView) bool { return d.Key != "" })
	return treeOverPane(header, ggui.Scroll(body).Bar(tokSurface3),
		hasDiff, 0.45, func() ggui.Widget { return diffPane(m) }, T("Diff divider"))
}

// maxShownDiffLines caps the lines the pane draws of a diff.
const maxShownDiffLines = 2000

func diffPane(m *model) ggui.Widget {
	head := ggui.View(m.GitDiff, func(d diffView) ggui.Widget {
		f := d.File
		name := f.Path
		if f.From != "" {
			name = f.From + " → " + f.Path
		}
		return panelHeader(
			statusIcon(f.Status),
			ggui.Expanded(ggui.Text(name).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
			counts(f.Added, f.Removed),
			iconButton("x", T("Close diff"), func() { m.GitDiff.Set(diffView{}); m.GitSel.Set("") }),
		)
	})
	content := ggui.View(m.GitDiff, func(d diffView) ggui.Widget {
		var note string
		switch {
		case d.Loading:
			return ggui.Center(ui.Spinner())
		case d.Err != "":
			note = d.Err
		case d.Diff.Binary:
			note = T("A binary file; no lines to show.")
		case len(d.Diff.Lines) == 0:
			note = T("No changes in its lines.")
		}
		if note != "" {
			return paneNote(note)
		}
		mono := ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 11.5}
		lines := d.Diff.Lines[:min(len(d.Diff.Lines), maxShownDiffLines)]
		rows := make([]ggui.Widget, 0, len(lines)+1)
		for _, l := range lines {
			rows = append(rows, diffRow(l, mono))
		}
		if len(lines) < len(d.Diff.Lines) || d.Diff.Truncated {
			rows = append(rows, ggui.Box(muted(ggui.Text(T("The rest of the diff is not shown.")).Size(11.5))).Pad(8, 12))
		}
		return ggui.Scroll(ggui.Column(rows...).Stretch()).Bar(tokSurface3)
	})
	return lowerPane(head, content)
}

// diffRow is one line of a diff: its numbers on each side, and the line,
// tinted green where added and red where removed.
func diffRow(l protocol.DiffLine, mono ggui.TextStyle) ggui.Widget {
	if l.Op == "@" {
		return ggui.Box(ggui.Text(l.Text).Style(mono).Color(tokMuted).NoWrap().Ellipsis()).
			Pad(4, 10).Fill(tokSurface2)
	}
	num := func(n int) ggui.Widget {
		s := ""
		if n > 0 {
			s = fmt.Sprint(n)
		}
		return ggui.Box(ggui.Text(s).Style(mono).Color(tokExtraMuted).NoWrap()).Width(34)
	}
	text := ggui.Text(l.Op + " " + strings.ReplaceAll(l.Text, "\t", "    ")).Style(mono).NoWrap()
	row := ggui.Box(ggui.Row(num(l.Old), num(l.New), ggui.Expanded(text)).Gap(4)).Pad(1, 8)
	switch l.Op {
	case "+":
		return row.Fill(tokDiffAddedBg)
	case "-":
		return row.Fill(tokDiffRemovedBg)
	}
	return row
}
