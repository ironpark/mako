package gui

import (
	"context"
	"image/color"
	"maps"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
)

// What the Changes and History tabs do to a repository: commit, push,
// pull, fetch and switch branches, each through the daemon of the node the
// project is on, one at a time.

// gitOpTimeout bounds a git operation as the window waits on it: the
// daemon's own bound and a little more.
const gitOpTimeout = 150 * time.Second

// gitNote is what the last operation said: git's output, or why it failed.
type gitNote struct {
	Text string
	Err  bool
}

// historyView is what is known of the project's commits.
type historyView struct {
	Root    fileRoot
	History *protocol.GitHistory
	Err     string
	Loading bool
}

// gitRoot is the work tree of the project the Changes tab shows, once read.
func (m *model) gitRoot() (fileRoot, bool) {
	v := ggui.Peek(m.Git)
	if v.Changes.GetRoot() == "" || v.Changes.GetNotRepo() {
		return fileRoot{}, false
	}
	return fileRoot{Node: v.Root.Node, Path: v.Changes.GetRoot()}, true
}

// gitRun runs an operation on the project's repository, then reads its
// changes and history again. done runs after it succeeds.
func (m *model) gitRun(req *protocol.GitRun, label string, done func()) {
	root, ok := m.gitRoot()
	if !ok || ggui.Peek(m.GitBusy) != "" {
		return
	}
	req.NodeId, req.Root = m.remoteID(root.Node), root.Path
	m.GitBusy.Set(label)
	m.GitNote.Set(gitNote{})
	fetch(m, gitOpTimeout, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
		return c.GitRun(ctx, m.key(), req)
	}, func(r *protocol.GitResult, err error) {
		m.GitBusy.Set("")
		if err != nil {
			m.GitNote.Set(gitNote{Text: nodeErr(err), Err: true})
		} else {
			m.GitNote.Set(gitNote{Text: lastLines(r.GetOutput(), 4)})
			if done != nil {
				done()
			}
		}
		m.gitTabRefresh()
	})
}

// nodeErr says why a request to a node failed, in words for one whose
// daemon predates the request.
func nodeErr(err error) string {
	if protocol.HasCode(err, protocol.CodeUnsupported) {
		return T("The sanbo on this node is older and cannot do this. Update it there.")
	}
	return err.Error()
}

// lastLines is the end of git's output, which says how an operation went.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(len(lines)-n, 0):], "\n")
}

// loadHistory reads the project's commits, unless they are being read. Once
// they have been, only a changed history comes back, and the list shows on
// while it is asked for.
func (m *model) loadHistory(root fileRoot) {
	if root.Path == "" || m.historyReading {
		return
	}
	v := ggui.Peek(m.GitHistory)
	if v.Root != root {
		v = historyView{Root: root, Loading: true}
		m.GitHistory.Set(v)
		m.GitCommit.Set(commitView{})
	}
	m.historyReading = true
	req := &protocol.GitLog{NodeId: m.remoteID(root.Node), Root: root.Path, Known: v.History.GetFingerprint()}
	fetch(m, 30*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitHistory, error) {
		return c.GitLog(ctx, m.key(), req)
	}, func(h *protocol.GitHistory, err error) {
		m.historyReading = false
		v := ggui.Peek(m.GitHistory)
		switch {
		case v.Root != root:
		case err != nil:
			v.Err, v.Loading = nodeErr(err), false
			m.GitHistory.Set(v)
		case !h.GetUnchanged() || v.Err != "" || v.Loading:
			if h.GetUnchanged() {
				h = v.History
			}
			m.GitHistory.Set(historyView{Root: root, History: h})
		}
	})
}

// commitPaths are the paths a commit of the picked files names: every
// change's, and where a rename moved from; nil when every file is picked.
func commitPaths(files []*protocol.GitFile, skipped map[string]bool) []string {
	if picked(files, skipped) == len(files) {
		return nil
	}
	var paths []string
	for _, f := range files {
		if skipped[f.GetPath()] {
			continue
		}
		paths = append(paths, f.GetPath())
		if f.GetFrom() != "" {
			paths = append(paths, f.GetFrom())
		}
	}
	return paths
}

// picked counts the changed files a commit takes.
func picked(files []*protocol.GitFile, skipped map[string]bool) int {
	n := 0
	for _, f := range files {
		if !skipped[f.GetPath()] {
			n++
		}
	}
	return n
}

// commit commits the picked files with the message typed.
func (m *model) commit() {
	files := ggui.Peek(m.Git).Changes.GetFiles()
	skipped := ggui.Peek(m.GitSkip)
	amend := ggui.Peek(m.GitAmend)
	msg := strings.TrimSpace(ggui.Peek(m.GitMessage))
	if !canCommit(picked(files, skipped), msg, amend) {
		return
	}
	req := &protocol.GitRun{Op: protocol.GitOpCommit, Message: msg, Paths: commitPaths(files, skipped), Amend: amend}
	m.gitRun(req, T("Committing…"), func() {
		m.GitMessage.Set("")
		m.GitAmend.Set(false)
		m.GitSkip.Set(map[string]bool{})
	})
}

// canCommit reports whether a commit can go ahead: one amending the last
// needs nothing new, any other a message and a file.
func canCommit(files int, msg string, amend bool) bool {
	return amend || (strings.TrimSpace(msg) != "" && files > 0)
}

// skipFiles leaves files out of the next commit, or takes them back in.
func (m *model) skipFiles(paths []string, skip bool) {
	m.GitSkip.Update(func(v map[string]bool) map[string]bool {
		v = maps.Clone(v)
		for _, p := range paths {
			if skip {
				v[p] = true
			} else {
				delete(v, p)
			}
		}
		return v
	})
}

// itemPaths adds the changed files under a row of the changes tree to out.
func itemPaths(it gitItem, out []string) []string {
	if !it.Dir {
		return append(out, it.File.GetPath())
	}
	for _, c := range it.Children {
		out = itemPaths(c, out)
	}
	return out
}

// branchMenu is the Changes header's branch: a menu of the local branches
// to switch to, and of making a new one.
func branchMenu(m *model, branch string, branches []string) ggui.Widget {
	var items []ggui.Widget
	for _, b := range branches {
		label := b
		if b == branch {
			label = "✓ " + b
		}
		items = append(items, ui.MenuItem(label, func() {
			if b != branch {
				m.gitRun(&protocol.GitRun{Op: protocol.GitOpCheckout, Branch: b}, T("Switching branch…"), nil)
			}
		}))
	}
	if len(items) > 0 {
		items = append(items, ui.MenuDivider())
	}
	items = append(items, ui.MenuItem(T("New branch…"), func() { m.askBranch("") }))
	label := ggui.Row(
		icon("git-branch").Size(15),
		ggui.Box(ggui.Text(branch).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis()).MaxWidth(130),
		icon("chevron-down").Size(12).Color(tokExtraMuted),
	).Gap(6)
	return borderless(ui.MenuOf(label, items...).Name(T("Branch")))
}

// syncButton is a push or pull button, with the commits it would move.
func syncButton(name, label string, count int, onTap func()) ggui.Widget {
	content := []ggui.Widget{icon(name).Size(14)}
	if count > 0 {
		content = append(content, ggui.Text(lineCount(count)).Size(11.5).Color(tokMuted).NoWrap())
	}
	return ui.Tooltip(ui.ButtonOf(ggui.Row(content...).Gap(3), onTap).Ghost().Pad(5, 6).Radius(6).Name(label), label)
}

// gitStatusLine is the operation under way, or what the last one said,
// under a git tab's header.
func gitStatusLine(m *model) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		if busy := m.GitBusy.Get(); busy != "" {
			return ggui.Box(ggui.Row(ui.Spinner(), muted(ggui.Text(busy).Size(12))).Gap(8)).Pad(6, 12)
		}
		n := m.GitNote.Get()
		if n.Text == "" {
			return ggui.Box()
		}
		return ggui.Column(ggui.Box(ggui.Row(
			ggui.Expanded(ggui.Text(n.Text).Size(11.5).Color(pick[color.Color](n.Err, tokDotDown, tokMuted)).Font(ggui.DefaultMonoFont()).Selectable()),
			iconButton("x", T("Dismiss"), func() { m.GitNote.Set(gitNote{}) }),
		).Gap(6).Align(ggui.AlignStart)).Pad(6, 6, 6, 12), hline()).Stretch()
	})
}
