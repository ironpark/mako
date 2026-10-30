package gui

import (
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
)

// The right panel's Git tab is the selected session's project as git sees
// it, as a source control panel: the branch and its sync at the head; the
// changed files, the stashes and the branches over the commit box; the
// history under them. Each section folds, and the history, folded, gives
// the rest its room.

func gitPanel(m *model) ggui.Widget {
	root := ggui.Derived(m.filesRoot)
	// Read again every while the tab shows, as agents change files and
	// commit.
	pollWhileShown(m, root, gitTab, gitRefresh, m.loadGit)
	pollWhileShown(m, root, gitTab, historyRefresh, m.loadHistory)
	return ggui.Key(root, func(r fileRoot) ggui.Widget {
		if r.Path == "" {
			return ggui.Box(muted(ggui.Text(T("Open a session to see its changes.")).Size(13))).Pad(16)
		}
		return gitTabView(m)
	})
}

func gitTabView(m *model) ggui.Widget {
	count := ggui.Map(m.Git, func(v gitView) int { return len(v.Changes.GetFiles()) })
	changesHead := ggui.View(count, func(n int) ggui.Widget {
		return foldSection(m.GitChangesOpen, T("Changes"), n)
	})
	tree := changesTree(m)
	sections := ggui.Column(
		changesHead,
		ggui.If(m.GitChangesOpen, func() ggui.Widget { return tree }),
		stashSection(m, m.GitStashOpen),
		branchSection(m, m.GitBranchOpen),
	).Stretch()
	top := ggui.Column(
		ggui.Expanded(ggui.Scroll(ggui.Padding(sections, 2, 0, 6, 0)).Bar(tokSurface3)),
		commitBox(m),
	).Stretch()

	historyHead := foldSection(m.GitHistoryOpen, T("History"), 0,
		iconButton("refresh-cw", T("Refresh"), func() { m.loadHistory(ggui.Peek(m.GitHistory).Root) }))
	history := historyBody(m)
	body := ggui.View(m.GitHistoryOpen, func(open bool) ggui.Widget {
		if !open {
			return ggui.Column(ggui.Expanded(top), hline(), historyHead).Stretch()
		}
		// The divider between them draws the line over the history.
		bottom := ggui.Column(historyHead, ggui.Expanded(history)).Stretch()
		return ui.Resizable(m.GitSplit, top, bottom).Vertical().MinSizes(140, 120).Hairline().Name(T("History divider"))
	})
	return ggui.Column(
		gitHeader(m),
		hline(),
		forgeBranchLine(m),
		gitStatusLine(m),
		operationBanner(m),
		ggui.Expanded(body),
	).Stretch()
}

// gitTabRefresh reads the Git tab's changes and history again, after an
// operation that changes either.
func (m *model) gitTabRefresh() {
	root := ggui.Peek(m.Git).Root
	m.loadGit(root)
	if ggui.Peek(m.DetailTab) == gitTab {
		m.loadHistory(root)
	}
}
