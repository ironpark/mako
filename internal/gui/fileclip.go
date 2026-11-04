package gui

import (
	"context"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// A file or folder is copied or cut in the Files tab or a node's file
// browser, and pasted into a folder there: on the same node, the node
// copies or moves it itself; from another, it comes through the daemons a
// piece at a time, as a row dragged onto another node's folder does.

// clipItems are a row's Copy and Cut.
func clipItems(m *model, it fileItem) []ggui.Widget {
	clip := func(cut bool) func() {
		return func() { m.FileClip.Set(pathDrag{Node: it.Node, Path: it.Path, Dir: it.Dir, Cut: cut}) }
	}
	return []ggui.Widget{
		ui.MenuItem(T("Copy"), clip(false)),
		ui.MenuItem(T("Cut"), clip(true)),
	}
}

// folderItems are what makes something in the folder dir, asked with ask,
// and Paste while something is copied.
func folderItems(m *model, dir fileItem, ask func(op string, it fileItem)) []ggui.Widget {
	items := []ggui.Widget{
		ui.MenuItem(T("New file…"), func() { ask(protocol.FileCreate, dir) }),
		ui.MenuItem(T("New folder…"), func() { ask(protocol.FileMkdir, dir) }),
	}
	return append(items, pasteItems(m, dir)...)
}

// pasteItems is Paste into the folder dir, while something is copied.
func pasteItems(m *model, dir fileItem) []ggui.Widget {
	clip := ggui.Peek(m.FileClip)
	if clip.Path == "" {
		return nil
	}
	label := Tf("Paste %s", baseName(clip.Path))
	if clip.Node != dir.Node {
		label = Tf("Paste %s from %s", baseName(clip.Path), nodeName(ggui.Peek(m.Nodes), clip.Node))
	}
	return []ggui.Widget{ui.MenuItem(label, func() { m.pasteInto(dir) })}
}

// pasteInto puts what was copied or cut into the folder dir.
func (m *model) pasteInto(dir fileItem) {
	clip := ggui.Peek(m.FileClip)
	if clip.Path == "" {
		return
	}
	if clip.Cut {
		m.FileClip.Set(pathDrag{}) // a move is pasted once
		if clip.Node == dir.Node {
			m.moveInto(clip, dir)
			return
		}
	}
	m.placeFile(clip, dir)
}

// placeFile copies the file or folder src into the folder dir, under a
// name not taken there, and with src.Cut takes it away from where it was
// once copied. It says how it went in a toast and reads both folders
// again.
func (m *model) placeFile(src pathDrag, dir fileItem) {
	same := src.Node == dir.Node
	if same && (dir.Path == src.Path || strings.HasPrefix(dir.Path, src.Path+pathSep(src.Path))) {
		m.FileNote.Set(T("A folder cannot be copied into itself."))
		return
	}
	for _, node := range []string{src.Node, dir.Node} {
		if err := m.canTransfer(node); err != nil {
			m.toaster.Push(ui.Toast(Tf("Could not copy to %s", baseName(dir.Path)), nodeErr(err)).Destructive())
			return
		}
	}
	from, to := m.remoteID(src.Node), m.remoteID(dir.Node)
	// A node's own copy keeps the bytes there; one too old for it has them
	// come through.
	n, _ := m.nodeInfo(dir.Node)
	native := same && n.GetRevision() >= protocol.RevisionLinks
	name := baseName(src.Path)
	copying := m.toaster.Push(ui.Toast(Tf("Copying %s to %s…", name, baseName(dir.Path)), "").Duration(0))
	fetch(m, copyTimeout, func(ctx context.Context, c *client.Client) (struct{}, error) {
		taken, err := remoteNames(ctx, c, to, dir.Path)
		if err != nil {
			return struct{}{}, err
		}
		dst := joinPath(dir.Path, freeName(name, func(n string) bool { return taken[n] }))
		if native {
			_, err = c.FileOp(ctx, &protocol.FileOp{NodeId: to, Op: protocol.FileCopy, Path: src.Path, To: dst})
		} else {
			err = copyAcross(ctx, c, from, src.Path, src.Dir, to, dst)
		}
		if err == nil && src.Cut {
			_, err = c.FileOp(ctx, &protocol.FileOp{NodeId: from, Op: protocol.FileDelete, Path: src.Path})
		}
		return struct{}{}, err
	}, func(_ struct{}, err error) {
		m.toaster.Dismiss(copying)
		m.loadDir(dir.Node, dir.Path, true)
		if src.Cut {
			m.loadDir(src.Node, parentDir(src.Path), true)
		}
		if err != nil {
			m.toaster.Push(ui.Toast(Tf("Could not copy to %s", baseName(dir.Path)), nodeErr(err)).Destructive())
		}
	})
}

// nodeInfo is node's record: this daemon's own for this machine, else as
// the mesh lists it; ok is false for one not listed.
func (m *model) nodeInfo(node string) (*protocol.Node, bool) {
	if m.onThisMachine(node) {
		return ggui.Peek(m.Conn).Self, true
	}
	return nodeIn(ggui.Peek(m.Nodes), node)
}

// copyAcross copies the file or folder at from on the node fromNode to the
// new path to on toNode, a piece at a time through the daemons.
func copyAcross(ctx context.Context, c *client.Client, fromNode, from string, isDir bool, toNode, to string) error {
	if !isDir {
		if _, err := c.FileOp(ctx, &protocol.FileOp{NodeId: toNode, Op: protocol.FileCreate, Path: to}); err != nil {
			return err
		}
		return readPieces(ctx, c, fromNode, from, func(off int64, p []byte) error {
			_, err := c.WriteFile(ctx, &protocol.WriteFile{NodeId: toNode, Path: to, Data: p, Offset: off})
			return err
		})
	}
	d, err := c.ListDir(ctx, &protocol.ListDir{NodeId: fromNode, Path: from})
	if err != nil {
		return err
	}
	if _, err := c.FileOp(ctx, &protocol.FileOp{NodeId: toNode, Op: protocol.FileMkdir, Path: to}); err != nil {
		return err
	}
	for _, e := range d.GetEntries() {
		if err := copyAcross(ctx, c, fromNode, joinPath(from, e.GetName()), e.GetDir(), toNode, joinPath(to, e.GetName())); err != nil {
			return err
		}
	}
	return nil
}
