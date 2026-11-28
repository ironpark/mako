package gui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
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
// it reads (internal/repotrust). The window shows the file whole, as the
// node it is on read it, and trusts it through that node's daemon, by the
// hash of what it showed: a file that changed since is refused, and is
// read again before it can be trusted.

// repoTrustAsk is a command file the user is asked to trust, as its node
// showed it; File is nil when nothing is asked.
type repoTrustAsk struct {
	Owner string // the window that asks: "" for the main window, or a Space window's spaceID
	Node  string // the node the repository is on, as a request names it
	Path  string // the file, as it was asked about
	Note  string // what asking it is for, such as the commands skipped
	File  *protocol.TrustedFile
	Err   string
	// Busy is a request about the file under way. The dialog stays open
	// for its answer, whatever closes it otherwise: a trust sent is not
	// taken back by closing it.
	Busy bool
	// Changed is the file read otherwise than it was shown when it was
	// to be trusted: it is to be read again before it can be.
	Changed bool
}

// askRepoTrust reads the command file at path on node, and asks the user,
// in the window they are in, whether to trust it as it reads, saying note.
func (m *model) askRepoTrust(node, path, note string) {
	m.askRepoTrustIn(m.focusedOwner(), node, path, note)
}

// askRepoTrustIn is askRepoTrust in the window owner: "" for the main
// window, or a Space window's spaceID.
func (m *model) askRepoTrustIn(owner, node, path, note string) {
	ask := repoTrustAsk{Owner: owner, Node: node, Path: path, Note: note}
	m.readRepoFile(ask, func(f *protocol.TrustedFile, err error) {
		if err != nil {
			m.toastIn(owner, ui.Toast(T("Could not read the repository's commands"), err.Error()).Destructive())
			return
		}
		if f.GetTrusted() {
			m.toastIn(owner, ui.Toast(T("Already trusted"), protocol.Printable(repoFilePath(f))))
			return
		}
		if ggui.Peek(m.RepoTrust).Busy {
			return // another file's answer is awaited; asking again asks then
		}
		ask.File = f
		m.RepoTrust.Set(ask)
	})
}

// readRepoFile has the node of a show its file as it reads now, and hands
// done the file, or why it could not.
func (m *model) readRepoFile(a repoTrustAsk, done func(*protocol.TrustedFile, error)) {
	req := &protocol.RepoTrust{NodeId: a.Node, Path: a.Path}
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.RepoTrustInfo, error) {
		return c.RepoTrust(ctx, req)
	}, func(info *protocol.RepoTrustInfo, err error) {
		if err == nil && len(info.GetFiles()) != 1 {
			err = fmt.Errorf("the node showed %d files", len(info.GetFiles()))
		}
		if err != nil {
			done(nil, err)
			return
		}
		done(info.GetFiles()[0], nil)
	})
}

// trustRepoFile trusts the file the dialog shows, as it showed it.
func (m *model) trustRepoFile() {
	a := ggui.Peek(m.RepoTrust)
	if a.File == nil || a.Busy || a.Changed {
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
			return // another asked since
		}
		if err != nil {
			cur.Busy = false
			if protocol.HasCode(err, protocol.CodeChanged) {
				cur.Changed = true
			} else {
				cur.Err = err.Error()
			}
			m.RepoTrust.Set(cur)
			return
		}
		m.RepoTrust.Set(repoTrustAsk{})
		m.toastIn(cur.Owner, ui.Toast(T("Trusted"), Tf("Mako runs the commands of %s while it reads as you saw it.", protocol.Printable(repoFilePath(a.File)))))
	})
}

// reloadRepoFile reads the file the dialog shows again, as it reads now,
// in place of what it showed.
func (m *model) reloadRepoFile() {
	a := ggui.Peek(m.RepoTrust)
	if a.File == nil || a.Busy {
		return
	}
	shown := a.File
	a.Busy, a.Err = true, ""
	m.RepoTrust.Set(a)
	m.readRepoFile(a, func(f *protocol.TrustedFile, err error) {
		cur := ggui.Peek(m.RepoTrust)
		if cur.File != shown {
			return // another asked since
		}
		cur.Busy = false
		switch {
		case err != nil:
			cur.Err = err.Error()
		case f.GetTrusted(): // trusted as it reads now, from elsewhere
			m.RepoTrust.Set(repoTrustAsk{})
			m.toastIn(cur.Owner, ui.Toast(T("Already trusted"), protocol.Printable(repoFilePath(f))))
			return
		default:
			cur.File, cur.Changed = f, false
		}
		m.RepoTrust.Set(cur)
	})
}

// repoTrustClosed is what the Space window spaceID closing does to a file
// it asks about: one whose answer is awaited is asked about in the main
// window, so the answer is not lost; any other is no longer asked.
func (m *model) repoTrustClosed(spaceID string) {
	a := ggui.Peek(m.RepoTrust)
	if a.File == nil || a.Owner != spaceID {
		return
	}
	if !a.Busy {
		m.RepoTrust.Set(repoTrustAsk{})
		return
	}
	a.Owner = ""
	m.RepoTrust.Set(a)
}

// repoFilePath is where a command file a node showed is, joined as that
// node's paths are.
func repoFilePath(f *protocol.TrustedFile) string {
	root := f.GetRoot()
	return joinPath(root, strings.ReplaceAll(f.GetPath(), "/", pathSep(root)))
}

// focusedOwner is the window the user is in: the spaceID of the Space
// window that has the focus, or "" for the main window, as when none has.
func (m *model) focusedOwner() string {
	for id, sw := range m.spaceWindows {
		if sw != nil && sw.win != nil && ggui.Peek(sw.win.Focused()) {
			return id
		}
	}
	return ""
}

// toast shows t over the window the user is in.
func (m *model) toast(t ui.ToastMessage) { m.toastIn(m.focusedOwner(), t) }

// toastIn shows t over the window owner, "" for the main window, or over
// the main window once that Space window is closed.
func (m *model) toastIn(owner string, t ui.ToastMessage) {
	if sw := m.spaceWindows[owner]; owner != "" && sw != nil && sw.toaster != nil {
		sw.toaster.Push(t)
		return
	}
	if m.toaster != nil {
		m.toaster.Push(t)
	}
}

// repoTrustDialog is repoTrustDialogFor the main window.
func repoTrustDialog(m *model) ggui.Widget { return repoTrustDialogFor(m, "") }

// repoTrustDialogFor shows the command file asked about in the window
// owner ("" for the main window, or a Space window's spaceID) whole, every
// control character in it made visible, with what trusting it means. Each
// window has its own, and shows only what it asks.
func repoTrustDialogFor(m *model, owner string) ggui.Widget {
	open := ggui.Bind(func() bool {
		a := m.RepoTrust.Get()
		return a.File != nil && a.Owner == owner
	}, func(v bool) {
		// Cancel, Escape and the scrim close it, but not while an answer
		// is awaited.
		if a := ggui.Peek(m.RepoTrust); !v && a.Owner == owner && !a.Busy {
			m.RepoTrust.Set(repoTrustAsk{})
		}
	})
	var shown shownFile
	return ggui.View(m.RepoTrust, func(a repoTrustAsk) ggui.Widget {
		if a.File == nil || a.Owner != owner {
			return keyedDialog(open, widget.Box())
		}
		var rows []ggui.Widget
		if a.Note != "" {
			rows = append(rows, widget.Text(a.Note).Size(13).Selectable())
		}
		mono := ggui.DefaultMonoFont()
		rows = append(rows,
			muted(widget.Text(T("Mako runs the commands this file gives with your permissions. Trust it only if you have read all of it and know what it does. If the file changes, it is not trusted until you trust it again.")).Size(12.5)),
			widget.Text(protocol.Printable(repoFilePath(a.File))).Font(mono).Size(12.5).Selectable(),
			shown.of(a.File.GetContent()),
			muted(widget.Text(Tf("SHA-256 %s", a.File.GetSha256())).Font(mono).Size(11.5).Selectable()),
		)
		if a.Changed {
			rows = append(rows, widget.Text(T("The file changed since it was shown. Reload it to read it as it is now, and trust it only once you have read it again.")).Size(12).Color(skin.DotDown).Selectable())
		}
		if a.Err != "" {
			rows = append(rows, widget.Text(a.Err).Size(12).Color(skin.DotDown).Selectable())
		}
		buttons := []ggui.Widget{ui.Button(T("Cancel"), func() { open.Set(false) }).Outline().Disabled(a.Busy)}
		if a.Changed {
			buttons = append(buttons, ui.Button(T("Reload"), m.reloadRepoFile).Disabled(a.Busy))
		}
		buttons = append(buttons, ui.Button(T("Trust"), m.trustRepoFile).Destructive().Disabled(a.Busy || a.Changed))
		return keyedDialog(open, widget.Column(rows...).Gap(10).Stretch()).
			Title(T("Trust this repository's commands?")).
			Width(600).
			Footer(widget.Row(buttons...).Gap(8))
	})
}

// shownFile is a command file as a dialog shows it, made printable and
// laid out once for each content rather than at every change of the
// dialog's state, such as Busy: a file can be repotrust.MaxSize long.
type shownFile struct {
	content []byte
	block   ggui.Widget
}

// of is the file of content, laid out, the one laid out last if it was of
// the same content.
func (s *shownFile) of(content []byte) ggui.Widget {
	if s.block == nil || !bytes.Equal(s.content, content) {
		s.content = content
		s.block = widget.Box(widget.Scroll(toolBlock(protocol.Printable(string(content)), false)).Bar(skin.Surface3)).MaxHeight(320)
	}
	return s.block
}
