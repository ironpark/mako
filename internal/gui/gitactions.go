package gui

import (
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/protocol"
)

// What the Git tab does beyond committing and syncing: stashes, throwing
// away and ignoring files, branches, and what a commit in the history
// offers. A name an operation needs is asked for in one dialog, and an
// operation that loses work asks to be sure in another.

// gitAsk is a name a git operation asks for.
type gitAsk struct {
	Title, Field, Action, Placeholder, Note string
	Blank                                   bool                // a blank name will do
	Check                                   func(string) string // why a name will not do, "" if it will
	Submit                                  func(string)
}

// ask opens the name dialog for a.
func (m *model) ask(a gitAsk) {
	m.GitAsk.Set(a)
	m.GitAskText.Set("")
	m.GitAskErr.Set("")
	m.GitAskOpen.Set(true)
}

func (m *model) submitAsk() {
	a := ggui.Peek(m.GitAsk)
	name := strings.TrimSpace(ggui.Peek(m.GitAskText))
	why := ""
	switch {
	case name == "" && !a.Blank:
		why = T("Enter a name.")
	case a.Check != nil:
		why = a.Check(name)
	}
	if why != "" {
		m.GitAskErr.Set(why)
		return
	}
	m.GitAskOpen.Set(false)
	a.Submit(name)
}

func gitAskDialog(m *model) ggui.Widget {
	return ggui.View(m.GitAsk, func(a gitAsk) ggui.Widget {
		return nameDialog(nameAsk{
			Open: m.GitAskOpen, Text: m.GitAskText, Err: m.GitAskErr,
			Title: a.Title, Field: a.Field, Action: a.Action, Placeholder: a.Placeholder, Note: a.Note,
			Submit: m.submitAsk,
		})
	})
}

// askBranch asks for a new branch's name and makes it, at commit or at
// HEAD for "", and switches to it. The node's git judges the name.
func (m *model) askBranch(commit string) {
	note := T("The branch starts at the current commit, and your changes come along.")
	if commit != "" {
		note = Tf("The branch starts at %s.", shortHash(commit))
	}
	m.ask(gitAsk{
		Title: T("New branch"), Field: T("Branch name"), Action: T("Create branch"), Placeholder: "feature/name", Note: note,
		Check: func(name string) string {
			if slices.Contains(ggui.Peek(m.Git).Changes.GetBranches(), name) {
				return T("That branch already exists.")
			}
			return ""
		},
		Submit: func(name string) {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpBranch, Branch: name, Commit: commit}, T("Making branch…"), nil)
		},
	})
}

// askStash stashes every change, new files too, under a message asked for.
func (m *model) askStash() {
	m.ask(gitAsk{
		Title: T("Stash changes"), Field: T("Message"), Action: T("Stash"), Placeholder: T("Optional"), Blank: true,
		Note: T("Every change, new files too, is put aside and the work tree left clean."),
		Submit: func(msg string) {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpStash, Message: msg}, T("Stashing…"), nil)
		},
	})
}

// askTag tags a commit.
func (m *model) askTag(commit string) {
	m.ask(gitAsk{
		Title: T("New tag"), Field: T("Tag name"), Action: T("Create tag"), Placeholder: "v1.0.0",
		Note: Tf("The tag marks %s.", shortHash(commit)),
		Submit: func(name string) {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpTag, Tag: name, Commit: commit}, T("Tagging…"), nil)
		},
	})
}

// discardFiles throws away the changes of paths, once sure.
func (m *model) discardFiles(paths []string, what string) {
	m.askConfirm(confirmation{
		Title: Tf("Discard changes to %s?", what), Body: T("The changes are lost, and new files deleted. This cannot be undone."),
		Action: T("Discard"),
		Run: func() {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpDiscardFiles, Paths: paths}, T("Discarding…"), nil)
		},
	})
}

// changedPaths are the paths a change touches: where a rename moved from too.
func changedPaths(files []*protocol.GitFile) []string {
	var paths []string
	for _, f := range files {
		paths = append(paths, f.GetPath())
		if f.GetFrom() != "" {
			paths = append(paths, f.GetFrom())
		}
	}
	return paths
}

// itemFiles are the changed files under a row of the changes tree.
func itemFiles(it gitItem, out []*protocol.GitFile) []*protocol.GitFile {
	if !it.Dir {
		return append(out, it.File)
	}
	for _, c := range it.Children {
		out = itemFiles(c, out)
	}
	return out
}

// changeMenu is what a row of the changes tree offers.
func changeMenu(m *model, it gitItem) []ggui.Widget {
	files := itemFiles(it, nil)
	items := []ggui.Widget{}
	if !it.Dir {
		f := it.File
		items = append(items, ui.MenuItem(T("Open diff"), func() { m.openDiff(f) }))
	}
	items = append(items,
		ui.MenuItem(T("Discard changes…"), func() { m.discardFiles(changedPaths(files), it.Name) }),
		ui.MenuItem(T("Add to .gitignore"), func() {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpIgnore, Paths: []string{it.Key}}, T("Ignoring…"), nil)
		}),
		ui.MenuDivider(),
		ui.MenuItem(T("Copy path"), func() { ggui.CurrentClipboard().Write(it.Key) }),
	)
	return items
}

// commitMenu is what a commit in the history offers.
func commitMenu(m *model, c *protocol.GitCommit) []ggui.Widget {
	hash := c.GetHash()
	run := func(op, label string) func() {
		return func() { m.gitRun(&protocol.GitRun{Op: op, Commit: hash}, label, nil) }
	}
	return []ggui.Widget{
		ui.MenuItem(T("Check out"), func() {
			m.askConfirm(confirmation{
				Title: Tf("Check out %s?", shortHash(hash)), Body: T("The work tree leaves its branch for this commit, on no branch. Changes that conflict stop it."),
				Action: T("Check out"), Run: run(protocol.GitOpDetach, T("Checking out…")),
			})
		}),
		ui.MenuItem(T("Create branch here…"), func() { m.askBranch(hash) }),
		ui.MenuItem(T("Create tag…"), func() { m.askTag(hash) }),
		ui.MenuDivider(),
		ui.MenuItem(T("Cherry-pick"), run(protocol.GitOpCherryPick, T("Cherry-picking…"))),
		ui.MenuItem(T("Revert"), run(protocol.GitOpRevert, T("Reverting…"))),
		ui.MenuDivider(),
		ui.MenuItem(T("Copy hash"), func() { ggui.CurrentClipboard().Write(hash) }),
		ui.MenuItem(T("Copy subject"), func() { ggui.CurrentClipboard().Write(c.GetSubject()) }),
	}
}

// Sections ------------------------------------------------------------------------

// foldSection is a section's heading, which folds it: its title, a count,
// and actions at its end.
func foldSection(open *ggui.StateValue[bool], title string, count int, actions ...ggui.Widget) ggui.Widget {
	return ggui.View(open, func(o bool) ggui.Widget {
		label := []ggui.Widget{
			icon(pick(o, "chevron-down", "chevron-right")).Size(12).Color(tokExtraMuted),
			ggui.Text(title).Size(11.5).Weight(ggui.WeightSemibold).Color(tokMuted).NoWrap(),
		}
		if count > 0 {
			label = append(label, ggui.Text(lineCount(count)).Size(11).Color(tokExtraMuted).NoWrap())
		}
		toggle := ui.ButtonOf(ggui.Row(append(label, ggui.Spacer())...).Gap(6).Align(ggui.AlignCenter),
			func() { open.Set(!ggui.Peek(open)) }).Ghost().Pad(4, 8).Radius(0).Name(pick(o, Tf("Collapse %s", title), Tf("Expand %s", title)))
		return ggui.Box(ggui.Row(append([]ggui.Widget{ggui.Expanded(toggle)}, actions...)...).Gap(2).Align(ggui.AlignCenter)).Pad(0, 6, 0, 0)
	})
}

// stashSection lists the stashes, each to pop, apply or drop.
func stashSection(m *model, open *ggui.StateValue[bool]) ggui.Widget {
	stashes := ggui.Map(m.Git, func(v gitView) []*protocol.GitStash { return v.Changes.GetStashes() }).WithEqual(protocol.Equal)
	return ggui.View(stashes, func(list []*protocol.GitStash) ggui.Widget {
		head := foldSection(open, T("Stashes"), len(list), iconButton("plus", T("Stash changes…"), m.askStash))
		rows := ggui.If(open, func() ggui.Widget {
			if len(list) == 0 {
				return ggui.Box(muted(ggui.Text(T("No stashes.")).Size(12))).Pad(4, 12, 8, 30)
			}
			out := make([]ggui.Widget, len(list))
			for i, s := range list {
				idx := s.GetIndex()
				run := func(op, label string) func() {
					return func() { m.gitRun(&protocol.GitRun{Op: op, Stash: idx}, label, nil) }
				}
				drop := func() {
					m.askConfirm(confirmation{Title: T("Drop this stash?"), Body: T("Its changes are lost."), Action: T("Drop"),
						Run: run(protocol.GitOpStashDrop, T("Dropping…"))})
				}
				out[i] = ggui.Box(ggui.Row(
					icon("layers").Size(13).Color(tokMuted),
					ggui.Expanded(ggui.Text(s.GetMessage()).Size(12.5).NoWrap().Ellipsis()),
					ggui.Text(ago(protocol.TimeOf(s.GetTime()))).Size(11).Color(tokExtraMuted).NoWrap(),
					ui.Tooltip(ui.Button(T("Pop"), run(protocol.GitOpStashPop, T("Popping…"))).Ghost(), T("Apply the stash and drop it")),
					iconMenu(ui.MenuOf(icon("ellipsis").Size(14),
						ui.MenuItem(T("Apply"), run(protocol.GitOpStashApply, T("Applying…"))),
						ui.MenuItem(T("Drop…"), drop),
					).Name(T("Stash actions")), 3),
				).Gap(6).Align(ggui.AlignCenter)).Pad(1, 6, 1, 30)
			}
			return ggui.Column(out...).Stretch()
		})
		return ggui.Column(head, rows).Stretch()
	})
}

// branchSection lists the local branches, then the remote ones: a click
// switches to one, and its menu merges, rebases onto or deletes it.
func branchSection(m *model, open *ggui.StateValue[bool]) ggui.Widget {
	type branches struct {
		Current       string
		Local, Remote []string
	}
	state := ggui.Map(m.Git, func(v gitView) branches {
		c := v.Changes
		return branches{c.GetBranch(), c.GetBranches(), c.GetRemoteBranches()}
	}).WithEqual(func(a, b branches) bool {
		return a.Current == b.Current && slices.Equal(a.Local, b.Local) && slices.Equal(a.Remote, b.Remote)
	})
	return ggui.View(state, func(b branches) ggui.Widget {
		head := foldSection(open, T("Branches"), len(b.Local), iconButton("plus", T("New branch…"), func() { m.askBranch("") }))
		rows := ggui.If(open, func() ggui.Widget {
			var out []ggui.Widget
			for _, name := range b.Local {
				out = append(out, localBranchRow(m, name, b.Current))
			}
			if len(b.Remote) > 0 {
				out = append(out, ggui.Box(ggui.Text(T("Remote")).Size(11).Color(tokExtraMuted)).Pad(6, 12, 2, 30))
				for _, name := range b.Remote {
					out = append(out, remoteBranchRow(m, name, b.Current))
				}
			}
			return ggui.Column(out...).Stretch()
		})
		return ggui.Column(head, rows).Stretch()
	})
}

func localBranchRow(m *model, name, current string) ggui.Widget {
	on := name == current
	glyph := icon("git-branch").Size(13).Color(tokMuted)
	text := ggui.Text(name).Size(12.5).NoWrap().Ellipsis()
	if on {
		glyph, text = icon("check").Size(13).Color(tokAccentBright), text.Weight(ggui.WeightMedium)
	}
	row := ui.ButtonOf(ggui.Row(glyph, ggui.Expanded(text)).Gap(6).Align(ggui.AlignCenter), func() {
		if !on {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpCheckout, Branch: name}, T("Switching branch…"), nil)
		}
	}).Ghost().Pad(4, 12, 4, 30).Radius(0).Name(name)
	if on {
		return ui.ContextMenu(row, ui.MenuItem(T("Copy name"), func() { ggui.CurrentClipboard().Write(name) })).Name(Tf("%s actions", name))
	}
	del := func(force bool) func() {
		return func() {
			body := T("A branch not merged anywhere is refused; delete it anyway to lose its commits.")
			if force {
				body = T("Its commits are lost unless another branch has them.")
			}
			m.askConfirm(confirmation{Title: Tf("Delete branch %s?", name), Body: body, Action: T("Delete"), Run: func() {
				m.gitRun(&protocol.GitRun{Op: protocol.GitOpDeleteBranch, Branch: name, Force: force}, T("Deleting branch…"), nil)
			}})
		}
	}
	return ui.ContextMenu(row,
		ui.MenuItem(T("Switch to branch"), func() {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpCheckout, Branch: name}, T("Switching branch…"), nil)
		}),
		ui.MenuItem(Tf("Merge into %s", current), func() {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpMergeBranch, Branch: name}, T("Merging…"), nil)
		}),
		ui.MenuItem(Tf("Rebase %s onto this", current), func() {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpRebase, Branch: name}, T("Rebasing…"), nil)
		}),
		ui.MenuDivider(),
		ui.MenuItem(T("Delete…"), del(false)),
		ui.MenuItem(T("Force delete…"), del(true)),
		ui.MenuDivider(),
		ui.MenuItem(T("Copy name"), func() { ggui.CurrentClipboard().Write(name) }),
	).Name(Tf("%s actions", name))
}

func remoteBranchRow(m *model, name, current string) ggui.Widget {
	track := func() {
		m.gitRun(&protocol.GitRun{Op: protocol.GitOpTrack, Branch: name}, T("Checking out…"), nil)
	}
	row := ui.ButtonOf(ggui.Row(
		icon("cloud-download").Size(13).Color(tokExtraMuted),
		ggui.Expanded(ggui.Text(name).Size(12.5).Color(tokMuted).NoWrap().Ellipsis()),
	).Gap(6).Align(ggui.AlignCenter), track).Ghost().Pad(4, 12, 4, 30).Radius(0).Name(name)
	return ui.ContextMenu(row,
		ui.MenuItem(T("Check out as a local branch"), track),
		ui.MenuItem(Tf("Merge into %s", current), func() {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpMergeBranch, Branch: name}, T("Merging…"), nil)
		}),
		ui.MenuItem(Tf("Rebase %s onto this", current), func() {
			m.gitRun(&protocol.GitRun{Op: protocol.GitOpRebase, Branch: name}, T("Rebasing…"), nil)
		}),
		ui.MenuDivider(),
		ui.MenuItem(T("Copy name"), func() { ggui.CurrentClipboard().Write(name) }),
	).Name(Tf("%s actions", name))
}

// operationBanner says a merge, rebase, cherry-pick or revert stopped on
// conflicts, with what to do about it: go on once they are resolved, or
// abort.
func operationBanner(m *model) ggui.Widget {
	type stopped struct {
		Op        string
		Conflicts int
	}
	state := ggui.Map(m.Git, func(v gitView) stopped {
		n := 0
		for _, f := range v.Changes.GetFiles() {
			if f.GetStatus() == protocol.GitConflicted {
				n++
			}
		}
		return stopped{v.Changes.GetOperation(), n}
	})
	return ggui.View(state, func(s stopped) ggui.Widget {
		if s.Op == "" {
			return ggui.Box()
		}
		what := map[string]string{"merge": T("A merge"), "rebase": T("A rebase"), "cherry-pick": T("A cherry-pick"), "revert": T("A revert")}[s.Op]
		note := Tf("%s is under way.", what)
		if s.Conflicts > 0 {
			note = Tf("%s stopped on %d conflicts. Resolve them, then continue.", what, s.Conflicts)
		}
		run := func(op, label string) func() { return func() { m.gitRun(&protocol.GitRun{Op: op}, label, nil) } }
		abort := func() {
			m.askConfirm(confirmation{Title: T("Abort the operation?"), Body: T("The work tree goes back to where it was before it began."),
				Action: T("Abort"), Run: run(protocol.GitOpAbort, T("Aborting…"))})
		}
		return ggui.Column(ggui.Box(ggui.Column(
			ggui.Row(icon("shield-alert").Size(15).Color(tokDotWaiting), ggui.Expanded(ggui.Text(note).Size(12.5))).Gap(8).Align(ggui.AlignStart),
			ggui.Row(ggui.Spacer(),
				ui.Button(T("Abort"), abort).Ghost(),
				ui.Button(T("Continue"), func() {
					if s.Conflicts == 0 {
						run(protocol.GitOpContinue, T("Continuing…"))()
						return
					}
					// git marks a file resolved once it is added, which
					// continuing does: a file still in conflict goes in
					// with its markers.
					m.askConfirm(confirmation{Title: T("Mark the conflicts resolved?"),
						Body:   Tf("%d files are still marked as conflicted. Continuing takes them as they are now.", s.Conflicts),
						Action: T("Continue"), Run: run(protocol.GitOpContinue, T("Continuing…"))})
				}),
			).Gap(6),
		).Gap(8).Stretch()).Pad(10, 12).Fill(tokSurface2), hline()).Stretch()
	})
}
