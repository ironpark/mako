package gui

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// Files and folders reach the window two ways: dragged from the desktop,
// which ggui delivers as a DropEvent, and dragged from the Files tab, as a
// pathDrag. On a terminal they are typed as paths; in the message box they
// are inserted; on a folder of the Files tab, or of a node's file browser,
// they are copied into it, and one dragged within the tab is moved there.
// Anywhere else a file opens in a tab, and a folder opens a new session in
// it.

// pathDrag is a file or folder of the Files tab being dragged.
type pathDrag struct{ Node, Path string }

// dropFolder opens the files dropped where nothing else takes them, each
// in a tab, or the New session dialog on the first folder dropped.
func (m *model) dropFolder(ev ggui.DropEvent) {
	self := ggui.Peek(m.Conn).Self.GetId()
	for _, f := range ev.Files {
		if f.Path == "" {
			continue
		}
		if f.Dir {
			m.openNew("", f.Path)
			return
		}
		m.openItem(fileItem{Key: dirKey(self, f.Path), Node: self, Path: f.Path, Name: filepath.Base(f.Path)}, 0)
	}
}

// copyInto copies the files and folders at paths on this machine into the
// folder dir on node, saying how it went in a toast, and reads the folder
// again.
func (m *model) copyInto(node, dir string, paths []string) {
	if len(paths) == 0 {
		return
	}
	if err := m.canTransfer(node); err != nil {
		m.toaster.Push(ui.Toast(Tf("Could not copy to %s", baseName(dir)), nodeErr(err)).Destructive())
		return
	}
	remote := m.remoteID(node)
	copying := m.toaster.Push(ui.Toast(Tf("Copying %s to %s…", filesLabel(paths), baseName(dir)), "").Duration(0))
	fetch(m, copyTimeout, func(ctx context.Context, c *client.Client) (struct{}, error) {
		return struct{}{}, upload(ctx, c, remote, dir, paths, &transfer{})
	}, func(_ struct{}, err error) {
		m.toaster.Dismiss(copying)
		m.loadDir(node, dir, true)
		if err != nil {
			m.toaster.Push(ui.Toast(Tf("Could not copy to %s", baseName(dir)), nodeErr(err)).Destructive())
		}
	})
}

// copyTimeout bounds a copy the Files tab starts, which shows no progress.
const copyTimeout = 30 * time.Minute

// moveInto moves a file or folder dragged within the Files tab into the
// folder dir, on the same node.
func (m *model) moveInto(d pathDrag, dir fileItem) {
	if d.Node != dir.Node || parentDir(d.Path) == dir.Path || d.Path == dir.Path ||
		strings.HasPrefix(dir.Path, d.Path+pathSep(d.Path)) {
		return // where it is, or into itself
	}
	req := &protocol.FileOp{NodeId: m.remoteID(d.Node), Op: protocol.FileRename, Path: d.Path, To: joinPath(dir.Path, baseName(d.Path))}
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.File, error) {
		return c.FileOp(ctx, req)
	}, func(_ *protocol.File, err error) {
		if err != nil {
			m.FileNote.Set(nodeErr(err))
			return
		}
		m.loadDir(d.Node, parentDir(d.Path), true)
		m.loadDir(dir.Node, dir.Path, true)
	})
}

// litDrop makes w, a folder's row, take files dropped from the desktop,
// which go to drop, and when moved is set, rows of the Files tab dragged
// onto it. It is lit while they are over it.
func litDrop(w ggui.Widget, moved func(pathDrag), drop func([]string)) ggui.Widget {
	over := ggui.State(false)
	lit := ggui.View(over, func(o bool) ggui.Widget {
		if o {
			return ggui.Box(w).Fill(tokSurface3).Radius(4)
		}
		return w
	})
	var out ggui.Widget = ggui.Pointer(lit).OnDropHover(over.Set).OnDrop(func(ev ggui.DropEvent) {
		over.Set(false)
		drop(ev.Paths())
	})
	if moved != nil {
		out = ggui.DropZone(out, func(d ggui.Dropped[pathDrag]) {
			over.Set(false)
			moved(d.Value)
		}).OnHover(over.Set)
	}
	return out
}

// folderDrop makes w, a folder of the Files tab, take files: from the
// desktop, copied in, and from the tab, moved in.
func folderDrop(m *model, w ggui.Widget, dir fileItem) ggui.Widget {
	return litDrop(w, func(d pathDrag) { m.moveInto(d, dir) }, func(paths []string) { m.copyInto(dir.Node, dir.Path, paths) })
}

// shellQuote writes a path as a shell reads it back: as it is when it has
// nothing a shell would split or expand, else in single quotes.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+=:,@%~", r) || r > 0x7f)
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellPaths is paths quoted for a shell, each followed by a space, as a
// terminal types the files dropped on it.
func shellPaths(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(shellQuote(p))
		b.WriteByte(' ')
	}
	return b.String()
}
