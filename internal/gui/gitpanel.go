package gui

import (
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/protocol"
)

// The right panel's Git tab is the selected session's project as git sees
// it, as a source control panel: the branch and its sync at the head; the
// changed files, the stashes and the branches over the commit box; the
// history under them. Each section folds, and the history, folded, gives
// the rest its room. Of a project of several folders, it shows one at a
// time, picked over the header (repoFolder).

func gitPanel(m *model) ggui.Widget {
	root := ggui.Derived(m.repoFolder)
	// Read again every while the tab shows, as agents change files and
	// commit; and at once when a commit, a checkout or a stash, by an agent
	// or by a window on any node, moves the stamp of the repository the
	// sessions in it are in, which their node's state brings within a few
	// seconds.
	stamp := ggui.Derived(func() string {
		return repoStamp(m.Sessions.Get(), root.Get(), m.git.View.Get().Changes.GetRoot())
	})
	// The changes are polled only of a tree whose node does not tell of
	// them: one that does tells of a change in the work tree, and in its
	// index, as it comes.
	rest := m.treeWatched
	pollWhileShown(m, root, gitTab, gitRefresh, m.loadGit, rest, stamp)
	pollWhileShown(m, root, gitTab, historyRefresh, m.loadHistory, nil, stamp)
	ggui.Effect(func() ggui.Cleanup {
		if r := root.Get(); r.Path != "" && m.blockShown(gitTab) {
			return m.watchTree(r)
		}
		return nil
	})
	return withRootPicker(m, ggui.Key(root, func(r fileRoot) ggui.Widget {
		if r.Path == "" {
			return widget.Box(muted(widget.Text(T("Open a session to see its changes.")).Size(13))).Pad(16)
		}
		return gitTabView(m)
	}))
}

func gitTabView(m *model) ggui.Widget {
	count := ggui.Map(m.git.View, func(v gitView) int { return len(v.Changes.GetFiles()) })
	changesHead := ggui.View(count, func(n int) ggui.Widget {
		return foldSection(m.git.ChangesOpen, T("Changes"), n)
	})
	tree := changesTree(m)
	sections := widget.Column(
		changesHead,
		ggui.If(m.git.ChangesOpen, func() ggui.Widget { return tree }),
		stashSection(m, m.git.StashOpen),
		branchSection(m, m.git.BranchOpen),
	).Stretch()
	top := widget.Column(
		widget.Expanded(widget.Scroll(widget.Padding(sections, 2, 0, 6, 0)).Bar(skin.Surface3)),
		commitBox(m),
	).Stretch()

	historyHead := foldSection(m.git.HistoryOpen, T("History"), 0,
		iconButton("refresh-cw", T("Refresh"), func() { m.loadHistory(ggui.Peek(m.git.History).Root) }))
	history := historyBody(m)
	body := ggui.View(m.git.HistoryOpen, func(open bool) ggui.Widget {
		if !open {
			return widget.Column(widget.Expanded(top), hline(), historyHead).Stretch()
		}
		// The divider between them draws the line over the history.
		bottom := widget.Column(historyHead, widget.Expanded(history)).Stretch()
		return ui.Resizable(m.git.Split, top, bottom).Vertical().MinSizes(140, 120).Hairline().Name(T("History divider"))
	})
	return widget.Column(
		gitHeader(m),
		hline(),
		forgeBranchLine(m),
		gitStatusLine(m),
		operationBanner(m),
		widget.Expanded(body),
	).Stretch()
}

// repoStamp is the stamps of the sessions on root's node in its
// repository, top being the top of its work tree once known: "" for none.
// A session is in it by its folder, or by the repository it is of, as a
// linked work tree's is, whose commits move the branches all share.
func repoStamp(sessions []*protocol.SessionInfo, root fileRoot, top string) string {
	repos := map[string]bool{}
	for _, s := range sessions {
		p := s.GetProject()
		if s.GetNodeId() == root.Node && s.GetRepo() != "" &&
			(pathscope.Within(p, root.Path) || top != "" && pathscope.Within(p, top) || pathscope.Within(root.Path, p)) {
			repos[s.GetRepo()] = true
		}
	}
	var stamps []string
	for _, s := range sessions {
		if s.GetNodeId() == root.Node && s.GetGitStamp() != "" && repos[s.GetRepo()] {
			stamps = append(stamps, s.GetId()+"="+s.GetGitStamp())
		}
	}
	slices.Sort(stamps)
	return strings.Join(stamps, ",")
}

// gitTabRefresh reads the Git tab's changes and history again, after an
// operation that changes either.
func (m *model) gitTabRefresh() {
	root := ggui.Peek(m.git.View).Root
	m.loadGit(root)
	if ggui.Untrack(func() bool { return m.blockChosen(gitTab) }) {
		m.loadHistory(root)
	}
}
