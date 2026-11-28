package gui

import (
	"context"
	"fmt"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A repository's command file, a file it tracks that names commands for
// mako to run on its own, has them run only while the user trusts it as
// it reads (internal/repotrust). The window shows the file whole, as the node it
// is on read it, and trusts it through that node's daemon, by the hash of
// what it showed: a file that changed since is refused.

// repoTrustAsk is a command file the user is asked to trust, as its node
// showed it; File is nil when nothing is asked.
type repoTrustAsk struct {
	Node string // the node the repository is on, as a request names it
	Path string // the file, as it was asked about
	Note string // what asking it is for, such as the commands skipped
	File *protocol.TrustedFile
	Err  string
	Busy bool
}

// askRepoTrust reads the command file at path on node, and asks the user
// whether to trust it as it reads, saying note.
func (m *model) askRepoTrust(node, path, note string) {
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.RepoTrustInfo, error) {
		return c.RepoTrust(ctx, &protocol.RepoTrust{NodeId: node, Path: path})
	}, func(info *protocol.RepoTrustInfo, err error) {
		if err == nil && len(info.GetFiles()) != 1 {
			err = fmt.Errorf("the node showed %d files", len(info.GetFiles()))
		}
		if err != nil {
			m.toast(ui.Toast(T("Could not read the repository's commands"), err.Error()).Destructive())
			return
		}
		f := info.GetFiles()[0]
		if f.GetTrusted() {
			m.toast(ui.Toast(T("Already trusted"), protocol.Printable(path)))
			return
		}
		m.RepoTrust.Set(repoTrustAsk{Node: node, Path: path, Note: note, File: f})
	})
}

// trustRepoFile trusts the file the dialog shows, as it showed it.
func (m *model) trustRepoFile() {
	a := ggui.Peek(m.RepoTrust)
	if a.File == nil || a.Busy {
		return
	}
	a.Busy, a.Err = true, ""
	m.RepoTrust.Set(a)
	req := &protocol.RepoTrust{NodeId: a.Node, Op: protocol.RepoTrustAllow, Path: a.Path, Sha256: a.File.GetSha256()}
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.RepoTrustInfo, error) {
		return c.RepoTrust(ctx, req)
	}, func(_ *protocol.RepoTrustInfo, err error) {
		cur := ggui.Peek(m.RepoTrust)
		if cur.File != a.File {
			return // closed, or another asked since
		}
		if err != nil {
			cur.Busy, cur.Err = false, err.Error()
			m.RepoTrust.Set(cur)
			return
		}
		m.RepoTrust.Set(repoTrustAsk{})
		m.toast(ui.Toast(T("Trusted"), Tf("Mako runs the commands of %s while it reads as you saw it.", protocol.Printable(a.Path))))
	})
}

// toast shows t over the window, when it has one.
func (m *model) toast(t ui.ToastMessage) {
	if m.toaster != nil {
		m.toaster.Push(t)
	}
}

// repoTrustDialog shows the command file asked about whole, every control
// character in it made visible, with what trusting it means.
func repoTrustDialog(m *model) ggui.Widget {
	open := ggui.Bind(func() bool { return m.RepoTrust.Get().File != nil }, func(v bool) {
		if !v {
			m.RepoTrust.Set(repoTrustAsk{})
		}
	})
	return ggui.View(m.RepoTrust, func(a repoTrustAsk) ggui.Widget {
		var rows []ggui.Widget
		if a.Note != "" {
			rows = append(rows, widget.Text(a.Note).Size(13).Selectable())
		}
		mono := ggui.DefaultMonoFont()
		rows = append(rows,
			muted(widget.Text(T("Mako runs the commands this file gives with your permissions. Trust it only if you have read all of it and know what it does. If the file changes, it is not trusted until you trust it again.")).Size(12.5)),
			widget.Text(protocol.Printable(a.Path)).Font(mono).Size(12.5).Selectable(),
			widget.Box(widget.Scroll(toolBlock(protocol.Printable(string(a.File.GetContent())), false)).Bar(skin.Surface3)).MaxHeight(320),
			muted(widget.Text(Tf("SHA-256 %s", a.File.GetSha256())).Font(mono).Size(11.5).Selectable()),
		)
		if a.Err != "" {
			rows = append(rows, widget.Text(a.Err).Size(12).Color(skin.DotDown).Selectable())
		}
		trust := ui.Button(T("Trust"), m.trustRepoFile).Destructive().Disabled(a.Busy)
		return keyedDialog(open, widget.Column(rows...).Gap(10).Stretch()).
			Title(T("Trust this repository's commands?")).
			Width(600).
			Footer(widget.Row(ui.Button(T("Cancel"), func() { open.Set(false) }).Outline(), trust).Gap(8))
	})
}
