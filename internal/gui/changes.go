package gui

import (
	"cmp"
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
		// A commit drafted for another project is not this one's.
		v = gitView{Root: root}
		m.GitSkip.Set(map[string]bool{})
		m.GitMessage.Set("")
		m.GitAmend.Set(false)
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

// pollWhileShown loads a details tab's project when the tab shows it, and
// again every while until it does not.
func pollWhileShown(m *model, root ggui.Readable[fileRoot], tab int, every time.Duration, load func(fileRoot)) {
	ggui.Effect(func() ggui.Cleanup {
		r := root.Get()
		if !m.DetailsOpen.Get() || m.DetailTab.Get() != tab {
			return nil // nothing shows it
		}
		load(r)
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					m.post(func() { load(r) })
				}
			}
		}()
		return func() { close(stop) }
	})
}

func changesPanel(m *model) ggui.Widget {
	root := ggui.Derived(m.filesRoot)
	// Read again every few seconds while shown, as agents change files.
	pollWhileShown(m, root, changesTab, gitRefresh, m.loadGit)
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
				numbers := counts(it.Added, it.Removed)
				if !it.Dir {
					numbers = fileCounts(it.File)
				}
				name := ggui.Text(it.Name).Size(12.5).NoWrap().Ellipsis()
				if !it.Dir && it.File.Status == protocol.GitDeleted {
					name = name.Color(tokMuted)
				}
				return ggui.Row(pickBox(m, it), lead, ggui.Expanded(name), numbers, tail).Gap(7)
			})
		}).
		RowName(func(it gitItem) string { return it.Name }).
		RowHeight(26).Indent(14).
		BindCollapsed(m.GitClosed). // folders start open
		BindSelected(m.GitSel).
		OnSelect(func(it gitItem) {
			if it.Dir {
				closed := maps.Clone(ggui.Peek(m.GitClosed))
				closed[it.Key] = !closed[it.Key]
				m.GitClosed.Set(closed)
				return
			}
			m.openDiff(it.File)
		}).
		Name(T("Changes"))

	// The header is rebuilt when what it shows changes, not on each read.
	type headed struct {
		Root             fileRoot
		Branch, Upstream string
		Repo             bool
		Ahead, Behind    int
		Branches         []string
	}
	head := ggui.Map(m.Git, func(v gitView) headed {
		c := v.Changes
		return headed{v.Root, cmp.Or(c.Branch, projectName(v.Root.Path)), c.Upstream,
			c.Root != "" && !c.NotRepo, c.Ahead, c.Behind, c.Branches}
	}).WithEqual(func(a, b headed) bool {
		return a.Root == b.Root && a.Branch == b.Branch && a.Upstream == b.Upstream && a.Repo == b.Repo &&
			a.Ahead == b.Ahead && a.Behind == b.Behind && slices.Equal(a.Branches, b.Branches)
	})
	header := ggui.View(head, func(h headed) ggui.Widget {
		if !h.Repo {
			return panelHeader(icon("git-branch").Size(15),
				ggui.Expanded(ggui.Text(h.Branch).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
				iconButton("refresh-cw", T("Refresh"), func() { m.loadGit(h.Root) }))
		}
		pull, push := T("Pull"), T("Push")
		if h.Upstream != "" {
			pull, push = Tf("Pull from %s", h.Upstream), Tf("Push to %s", h.Upstream)
		}
		run := func(op, label string) func() {
			return func() { m.gitRun(protocol.GitRun{Op: op}, label, nil) }
		}
		more := borderless(ui.MenuOf(icon("ellipsis"),
			ui.MenuItem(T("Fetch"), run(protocol.GitOpFetch, T("Fetching…"))),
			ui.MenuItem(T("Pull"), run(protocol.GitOpPull, T("Pulling…"))),
			ui.MenuItem(T("Push"), run(protocol.GitOpPush, T("Pushing…"))),
			ui.MenuDivider(),
			ui.MenuItem(T("Refresh"), func() { m.loadGit(h.Root) }),
		).Name(T("Git actions")))
		return panelHeader(
			ggui.Expanded(ggui.Row(branchMenu(m, h.Branch, h.Branches), ggui.Spacer()).Stretch()),
			ggui.Row(
				syncButton("arrow-down", pull, h.Behind, run(protocol.GitOpPull, T("Pulling…"))),
				syncButton("arrow-up", push, h.Ahead, run(protocol.GitOpPush, T("Pushing…"))),
				more,
			).Gap(0),
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
	list := ggui.Column(gitStatusLine(m), ggui.Expanded(ggui.Scroll(body).Bar(tokSurface3)), commitBox(m)).Stretch()
	return treeOverPane(header, list,
		hasDiff, 0.45, func() ggui.Widget { return diffPane(m) }, T("Diff divider"))
}

// maxShownDiffLines caps the lines the pane draws of a diff.
const maxShownDiffLines = 2000

func diffPane(m *model) ggui.Widget {
	head := ggui.View(m.GitDiff, func(d diffView) ggui.Widget {
		return diffHeader(d.File, iconButton("x", T("Close diff"), func() { m.GitDiff.Set(diffView{}); m.GitSel.Set("") }))
	})
	content := ggui.View(m.GitDiff, func(d diffView) ggui.Widget { return diffBody(d.Loading, d.Err, d.Diff) })
	return lowerPane(head, content)
}

// diffHeader names a changed file over its diff: how it changed, where a
// rename moved it from, and its lines; lead and tail go before and after.
func diffHeader(f protocol.GitFile, tail ggui.Widget, lead ...ggui.Widget) ggui.Widget {
	name := f.Path
	if f.From != "" {
		name = f.From + " → " + f.Path
	}
	return panelHeader(append(lead,
		statusIcon(f.Status),
		ggui.Expanded(ggui.Text(name).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
		fileCounts(f),
		tail,
	)...)
}

// diffBody is a diff as read so far: a spinner, why it failed, a note
// for one without lines, or its lines.
func diffBody(loading bool, err string, d protocol.Diff) ggui.Widget {
	var note string
	switch {
	case loading:
		return ggui.Center(ui.Spinner())
	case err != "":
		note = err
	case d.Binary:
		note = T("A binary file; no lines to show.")
	case len(d.Lines) == 0:
		note = T("No changes in its lines.")
	}
	if note != "" {
		return paneNote(note)
	}
	return diffLines(d)
}

// fileCounts is a changed file's +added -removed, or that it is binary.
func fileCounts(f protocol.GitFile) ggui.Widget {
	if f.Binary {
		return muted(ggui.Text(T("binary")).Size(12))
	}
	return counts(f.Added, f.Removed)
}

// diffLines draws a diff's lines, as many as the pane shows.
func diffLines(d protocol.Diff) ggui.Widget {
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 11.5}
	lines := d.Lines[:min(len(d.Lines), maxShownDiffLines)]
	rows := make([]ggui.Widget, 0, len(lines)+1)
	for _, l := range lines {
		rows = append(rows, diffRow(l, mono))
	}
	if len(lines) < len(d.Lines) || d.Truncated {
		rows = append(rows, ggui.Box(muted(ggui.Text(T("The rest of the diff is not shown.")).Size(11.5))).Pad(8, 12))
	}
	return ggui.Scroll(ggui.Column(rows...).Stretch()).Bar(tokSurface3)
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

// pickBox is a changes row's box: whether the next commit takes the file,
// or for a folder all its files, some of them showing as a dash.
func pickBox(m *model, it gitItem) ggui.Widget {
	paths := itemPaths(it, nil)
	state := ggui.Map(m.GitSkip, func(skipped map[string]bool) int {
		n := 0
		for _, p := range paths {
			if skipped[p] {
				n++
			}
		}
		switch n {
		case 0:
			return 1 // all taken
		case len(paths):
			return 0
		}
		return -1
	})
	on := ggui.Bind(func() bool { return state.Get() != 0 }, func(take bool) { m.skipFiles(paths, !take) })
	return ui.Checkbox(on, "").BindIndeterminate(ggui.Map(state, func(s int) bool { return s < 0 })).Name(Tf("Commit %s", it.Name))
}

// commitBox is the message and the button that commits the files picked.
func commitBox(m *model) ggui.Widget {
	hidden := ggui.Map(m.Git, func(v gitView) bool { return v.Changes.Root == "" || v.Changes.NotRepo })
	field := ui.TextField(m.GitMessage).Multiline().Lines(3).Placeholder(T("Commit message")).Name(T("Commit message"))
	// The button changes with what a commit would take, not each key typed.
	type ready struct {
		Files       int
		Typed, Busy bool
	}
	state := ggui.Derived(func() ready {
		return ready{picked(m.Git.Get().Changes.Files, m.GitSkip.Get()),
			strings.TrimSpace(m.GitMessage.Get()) != "", m.GitBusy.Get() != ""}
	})
	button := ggui.Reactive(func() ggui.Widget {
		r, amend := state.Get(), m.GitAmend.Get()
		label := Tf("Commit %d files", r.Files)
		switch {
		case amend:
			label = T("Amend last commit")
		case r.Files == 1:
			label = T("Commit 1 file")
		}
		typed := pick(r.Typed, "x", "")
		return ui.Button(label, m.commit).Disabled(r.Busy || !canCommit(r.Files, typed, amend))
	})
	box := ggui.Column(
		field,
		ggui.Row(ui.Checkbox(m.GitAmend, T("Amend")), ggui.Spacer(), button).Gap(8).Align(ggui.AlignCenter),
	).Gap(8).Stretch()
	return ggui.If(hidden, func() ggui.Widget { return ggui.Box() }).
		Else(func() ggui.Widget { return ggui.Column(hline(), ggui.Box(box).Pad(10, 12)).Stretch() })
}
