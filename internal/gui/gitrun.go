package gui

import (
	"cmp"
	"context"
	"image/color"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// gitOpTimeout bounds a git operation as the window waits on it: the
// daemon's own bound and a little more.
const gitOpTimeout = 150 * time.Second

// gitNote is what the last operation said: command output, or why it failed.
type gitNote struct {
	Text string
	Err  bool
}

// gitRun runs a git operation on the target repository (or the currently
// selected repoFolder when req.Root is empty).
func (m *model) gitRun(req *protocol.GitRun, label string, done func()) {
	node := req.GetNodeId()
	if req.GetRoot() == "" {
		root := ggui.Untrack(m.repoFolder)
		if root.Path == "" {
			return
		}
		node = root.Node
		req.NodeId = m.remoteID(root.Node)
		req.Root = cmp.Or(req.GetWorktree().GetPath(), root.Path)
	}
	if node == "" {
		node = ggui.Peek(m.Conn).Self.GetId()
	}
	fetch(m, gitOpTimeout, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
		return c.GitRun(ctx, req)
	}, func(_ *protocol.GitResult, err error) {
		if err != nil {
			if m.toaster != nil {
				m.toaster.Push(ui.Toast(cmp.Or(label, T("Git")), nodeErr(err)).Destructive())
			}
			return
		}
		if done != nil {
			done()
		}
		m.refreshDiffChangesForRoot(node, req.GetRoot())
	})
}

// statusLine is the operation under way, busy, or what the last one said,
// note, under a panel's header.
func statusLine(busy *ggui.StateValue[string], note *ggui.StateValue[gitNote]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		if b := busy.Get(); b != "" {
			return widget.Box(widget.Row(ui.Spinner(), muted(widget.Text(b).Size(12))).Gap(8)).Pad(6, 12)
		}
		n := note.Get()
		if n.Text == "" {
			return widget.Box()
		}
		return widget.Column(widget.Box(widget.Row(
			widget.Expanded(widget.Text(n.Text).Size(11.5).Color(pick[color.Color](n.Err, skin.DotDown, skin.Muted)).Font(ggui.DefaultMonoFont()).Selectable()),
			iconButton("x", T("Dismiss"), func() { note.Set(gitNote{}) }),
		).Gap(6).Align(widget.AlignStart)).Pad(6, 6, 6, 12), hline()).Stretch()
	})
}
