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
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// The right panel's Changes tab shows the git changes of the selected
// session's project, read by the daemon of the node it runs on: a tree of
// the changed files, each folder adding up its files' lines, over the diff
// of the file chosen.

// gitView is what is known of the project's changes.
type gitView struct {
	Root    fileRoot // the project they were read for
	Base    string   // the branch they are read since, "" for HEAD
	Changes *protocol.GitChanges
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
	base := ggui.Peek(m.GitBase)
	if v.Root != root {
		// A commit drafted for another project is not this one's, nor is
		// the base it was compared with.
		v = gitView{Root: root}
		m.GitSkip.Set(map[string]bool{})
		m.GitMessage.Set("")
		m.GitAmend.Set(false)
		m.GitBase.Set("")
		base = ""
	}
	if v.Base != base {
		v = gitView{Root: root, Base: base}
	}
	v.Loading = true
	m.Git.Set(v)
	req := &protocol.GitStatus{NodeId: m.remoteID(root.Node), Path: root.Path, Base: base}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitChanges, error) {
		return c.GitStatus(ctx, req)
	}, func(ch *protocol.GitChanges, err error) {
		if cur := ggui.Peek(m.Git); cur.Root != root || cur.Base != base {
			return // another project, or base, is shown now
		}
		next := gitView{Root: root, Base: base, Changes: ch}
		if err != nil {
			next.Changes, next.Err = ggui.Peek(m.Git).Changes, err.Error()
		}
		m.Git.Set(next)
		if err == nil {
			m.followChanges(next)
		}
	})
}

// openDiff shows a changed file's diff in a tab.
func (m *model) openDiff(f *protocol.GitFile) {
	v := ggui.Peek(m.Git)
	m.openDiffTab(v.Root, v.Changes.GetRoot(), "", v.Base, f, true)
}

// gitItem is a row of the changes tree: a changed file, or a folder
// adding up the files under it.
type gitItem struct {
	Key, Name      string
	Dir            bool
	Added, Removed int
	File           *protocol.GitFile
	Children       []gitItem
}

// gitTree arranges changed files under their folders, folders first and
// then by name, each folder with its files' lines added up. A folder that
// holds one folder alone shares its row, "app/src/gen", so a deep path to
// a change does not step in a level a folder.
func gitTree(files []*protocol.GitFile) []gitItem {
	type node struct {
		item gitItem
		kids map[string]*node
	}
	root := &node{kids: map[string]*node{}}
	for _, f := range files {
		parts := strings.Split(f.GetPath(), "/")
		n := root
		for i, part := range parts {
			k, ok := n.kids[part]
			if !ok {
				k = &node{item: gitItem{Key: strings.Join(parts[:i+1], "/"), Name: part, Dir: i < len(parts)-1}, kids: map[string]*node{}}
				n.kids[part] = k
			}
			k.item.Added += int(f.GetAdded())
			k.item.Removed += int(f.GetRemoved())
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
			for it.Dir && len(it.Children) == 1 && it.Children[0].Dir {
				only := it.Children[0]
				it.Key, it.Name, it.Children = only.Key, it.Name+"/"+only.Name, only.Children
			}
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
	case protocol.GitConflicted:
		return iconColored("shield-alert", tokDotDown).Size(14)
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
		return m.every(every, func() { load(r) })
	})
}

// every runs fn on the UI goroutine each period until the cleanup it
// returns is called.
func (m *model) every(period time.Duration, fn func()) ggui.Cleanup {
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				m.post(fn)
			}
		}
	}()
	return func() { close(stop) }
}

// changesTree is the changed files under their folders, each to pick for
// the next commit, its diff a click away and more in its menu.
func changesTree(m *model) ggui.Widget {
	// A refresh that changes nothing, as most do, leaves the tree be.
	files := ggui.Map(m.Git, func(v gitView) []*protocol.GitFile { return v.Changes.GetFiles() }).WithEqual(protocol.Equal)
	roots := ggui.Map(files, gitTree)
	tree := ui.Tree(roots, func(it gitItem) string { return it.Key }, func(it gitItem) []gitItem { return it.Children },
		func(item ggui.Readable[gitItem]) ggui.Widget {
			return ggui.Reactive(func() ggui.Widget {
				it := item.Get()
				lead := icon("file").Size(14)
				var tail ggui.Widget = ggui.Box().Width(14)
				if it.Dir {
					// A folder says it is open by its icon, as the file tree's does.
					lead = icon(pick(m.GitClosed.Get()[it.Key], "folder", "folder-open")).Size(14)
				} else {
					tail = statusIcon(it.File.GetStatus())
				}
				numbers := counts(it.Added, it.Removed)
				if !it.Dir {
					numbers = fileCounts(it.File)
				}
				name := ggui.Text(it.Name).Size(12.5).NoWrap().Ellipsis()
				if !it.Dir && it.File.GetStatus() == protocol.GitDeleted {
					name = name.Color(tokMuted)
				}
				if ggui.Peek(m.GitBase) != "" {
					// Since a base, the files are the branch's work, not a commit's pick.
					return ggui.Row(lead, ggui.Expanded(name), numbers, tail).Gap(7)
				}
				row := ggui.Row(pickBox(m, it), lead, ggui.Expanded(name), numbers, tail).Gap(7)
				return ui.ContextMenu(row, changeMenu(m, it)...).Name(Tf("%s actions", it.Name))
			})
		}).
		RowName(func(it gitItem) string { return it.Name }).
		RowHeight(26).Indent(14).Chevrons(false).
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
	return ggui.Reactive(func() ggui.Widget {
		v := m.Git.Get()
		var note string
		switch {
		case v.Changes.GetNotRepo():
			note = T("Not a git repository.")
		case v.Err != "" && len(v.Changes.GetFiles()) == 0:
			note = v.Err
		case v.Loading && v.Changes.GetRoot() == "":
			return ggui.Box(ggui.Center(ui.Spinner())).Height(80)
		case len(v.Changes.GetFiles()) == 0:
			note = T("No changes.")
		}
		if note != "" {
			return ggui.Box(muted(ggui.Text(note).Size(12.5))).Pad(6, 12, 10, 30)
		}
		return ggui.Padding(tree, 0, 4)
	})
}

// gitHeader is the Git tab's head line: the branch to switch, push and
// pull with what they would move, and the rest of git in a menu.
func gitHeader(m *model) ggui.Widget {
	// The header is rebuilt when what it shows changes, not on each read.
	type headed struct {
		Root             fileRoot
		Branch, Upstream string
		Repo             bool
		Ahead, Behind    int
		Branches         []string
		Base             string             // compared since, "" for HEAD
		Worktree         *protocol.Worktree // the session's own, for its actions
	}
	head := ggui.Derived(func() headed {
		v := m.Git.Get()
		c := v.Changes
		return headed{v.Root, cmp.Or(c.GetBranch(), projectName(v.Root.Path)), c.GetUpstream(),
			c.GetRoot() != "" && !c.GetNotRepo(), int(c.GetAhead()), int(c.GetBehind()), c.GetBranches(), v.Base, m.worktreeAt(v.Root)}
	}).WithEqual(func(a, b headed) bool {
		return a.Root == b.Root && a.Branch == b.Branch && a.Upstream == b.Upstream && a.Repo == b.Repo &&
			a.Ahead == b.Ahead && a.Behind == b.Behind && slices.Equal(a.Branches, b.Branches) &&
			a.Base == b.Base && protocol.Same(a.Worktree, b.Worktree)
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
			return func() { m.gitRun(&protocol.GitRun{Op: op}, label, nil) }
		}
		files := func() []*protocol.GitFile { return ggui.Peek(m.Git).Changes.GetFiles() }
		items := []ggui.Widget{
			ui.MenuItem(T("Fetch"), run(protocol.GitOpFetch, T("Fetching…"))),
			ui.MenuItem(T("Pull"), run(protocol.GitOpPull, T("Pulling…"))),
			ui.MenuItem(T("Pull (rebase)"), func() {
				m.gitRun(&protocol.GitRun{Op: protocol.GitOpPull, Rebase: true}, T("Pulling…"), nil)
			}),
			ui.MenuItem(T("Push"), run(protocol.GitOpPush, T("Pushing…"))),
			ui.MenuItem(T("Create pull request…"), m.askPR),
			ui.MenuDivider(),
			ui.MenuItem(T("Stash changes…"), m.askStash),
			ui.MenuItem(T("Discard all changes…"), func() {
				if fs := files(); len(fs) > 0 {
					m.discardFiles(changedPaths(fs), Tf("%d files", len(fs)))
				}
			}),
		}
		if h.Worktree != nil {
			items = append(append(items, ui.MenuDivider()), worktreeItems(m, h.Worktree)...)
		}
		items = append(items, ui.MenuDivider(), ui.MenuItem(T("Refresh"), func() { m.loadGit(h.Root) }))
		more := iconMenu(ui.MenuOf(icon("ellipsis"), items...).Width(220).Name(T("Git actions")), 6)
		// A session's worktree compares with HEAD or with the branch it
		// started from.
		var against ggui.Widget = ggui.Box()
		if h.Worktree.GetBase() != "" {
			base := h.Worktree.GetBase()
			label := T("vs HEAD")
			if h.Base != "" {
				label = Tf("since %s", base)
			}
			against = ui.Tooltip(ui.Button(label, func() {
				m.GitBase.Set(pick(h.Base == "", base, ""))
				m.loadGit(h.Root)
			}).Ghost(), T("Compare with HEAD, or with the branch the worktree started from"))
		}
		return panelHeader(
			ggui.Expanded(ggui.Row(branchMenu(m, h.Branch, h.Branches), against, ggui.Spacer()).Gap(4).Stretch()),
			ggui.Row(
				syncButton("arrow-down", pull, h.Behind, run(protocol.GitOpPull, T("Pulling…"))),
				syncButton("arrow-up", push, h.Ahead, run(protocol.GitOpPush, T("Pushing…"))),
				more,
			).Gap(0),
		)
	})

	return header
}

// maxShownDiffLines caps the lines the pane draws of a diff.
const maxShownDiffLines = 2000

// diffHeader names a changed file over its diff: how it changed, where a
// rename moved it from, and its lines; lead and tail go before and after.
func diffHeader(f *protocol.GitFile, tail ggui.Widget, lead ...ggui.Widget) ggui.Widget {
	name := f.GetPath()
	if f.GetFrom() != "" {
		name = f.GetFrom() + " → " + f.GetPath()
	}
	return panelHeader(append(lead,
		statusIcon(f.GetStatus()),
		ggui.Expanded(ggui.Text(name).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
		fileCounts(f),
		tail,
	)...)
}

// fileCounts is a changed file's +added -removed, or that it is binary.
func fileCounts(f *protocol.GitFile) ggui.Widget {
	if f.GetBinary() {
		return muted(ggui.Text(T("binary")).Size(12))
	}
	return counts(int(f.GetAdded()), int(f.GetRemoved()))
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
	box := ui.Checkbox(on, "").BindIndeterminate(ggui.Map(state, func(s int) bool { return s < 0 })).Name(Tf("Commit %s", it.Name))
	return quietBox(box)
}

// quietBox draws the checkboxes of a list, one on every row, smaller and
// ticked in the raised surface rather than the primary colour, which in a
// dark theme is a white block on every line.
func quietBox(box ggui.Widget) ggui.Widget {
	return uitheme.Override(func(t *uitheme.Theme) {
		if raised, ok := t.Get(uitheme.CSSColor("surface-3")); ok {
			t.Primary, t.PrimaryFg = raised, t.Fg
		}
		t.ControlSize = min(t.ControlSize, 14)
	}, box)
}

// commitBox is the message and the button that commits the files picked.
func commitBox(m *model) ggui.Widget {
	// Under a merge or rebase, the banner over the tab goes on with it.
	hidden := ggui.Map(m.Git, func(v gitView) bool {
		return v.Changes.GetRoot() == "" || v.Changes.GetNotRepo() || v.Base != "" || v.Changes.GetOperation() != ""
	})
	field := ui.TextField(m.GitMessage).Multiline().Lines(3).Placeholder(T("Commit message")).Name(T("Commit message"))
	// The button changes with what a commit would take, not each key typed.
	type ready struct {
		Files       int
		Typed, Busy bool
	}
	state := ggui.Derived(func() ready {
		return ready{picked(m.Git.Get().Changes.GetFiles(), m.GitSkip.Get()),
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
