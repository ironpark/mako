package gui

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// The Files tab's context menu: open a file, put its path in a message or
// on the clipboard, start a session in a folder, and make, rename or delete
// files and folders on the node, each after asking.

// fileAsk is a file operation waiting on the user: a name for a new file
// or folder, or for a rename, or a yes to a delete.
type fileAsk struct {
	Op   string   // a protocol.File* operation
	Item fileItem // the folder a new one goes in, or what is renamed or deleted
}

// parentDir is the folder a path on a node is in.
func parentDir(p string) string {
	sep := pathSep(p)
	i := strings.LastIndex(strings.TrimSuffix(p, sep), sep)
	if i <= 0 {
		return p[:i+1]
	}
	return p[:i]
}

// dirItem is the folder at path on a node, as a row of the tree.
func dirItem(node, path string) fileItem {
	return fileItem{Key: dirKey(node, path), Node: node, Path: path, Name: baseName(path), Dir: true}
}

// folderOf is the folder a new file goes in from an item: the item itself
// when it is one, else the folder it is in.
func folderOf(it fileItem) fileItem {
	if it.Dir {
		return it
	}
	return dirItem(it.Node, parentDir(it.Path))
}

// askFile asks for what op needs: a name, or a yes.
func (m *model) askFile(op string, it fileItem) {
	m.FileAsk.Set(fileAsk{Op: op, Item: it})
	m.FileErr.Set("")
	switch op {
	case protocol.FileDelete:
		m.FileDeleteOpen.Set(true)
		return
	case protocol.FileRename:
		m.FileName.Set(it.Name)
	default:
		m.FileName.Set("")
	}
	m.FileNameOpen.Set(true)
}

// validName reports why a name cannot be a file's, or "" when it can.
func validName(name string) string {
	switch {
	case name == "":
		return T("Enter a name.")
	case name == "." || name == ".." || strings.ContainsAny(name, `/\`):
		return T("A name cannot be a path.")
	}
	return ""
}

// submitFile carries out the operation asked, on the node the item is on,
// and reads its folder again.
func (m *model) submitFile() {
	a := ggui.Peek(m.FileAsk)
	it := a.Item
	req := &protocol.FileOp{NodeId: m.remoteID(it.Node), Op: a.Op, Path: it.Path}
	if a.Op != protocol.FileDelete {
		name := strings.TrimSpace(ggui.Peek(m.FileName))
		if why := validName(name); why != "" {
			m.FileErr.Set(why)
			return
		}
		switch a.Op {
		case protocol.FileRename:
			if name == it.Name {
				m.FileNameOpen.Set(false)
				return
			}
			req.To = joinPath(parentDir(it.Path), name)
		default:
			req.Path = joinPath(it.Path, name)
		}
	}
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.File, error) {
		return c.FileOp(ctx, req)
	}, func(f *protocol.File, err error) {
		if err != nil {
			m.FileErr.Set(nodeErr(err))
			if a.Op == protocol.FileDelete {
				m.FileNote.Set(nodeErr(err)) // the confirmation has closed
			}
			return
		}
		m.FileNameOpen.Set(false)
		m.FileNote.Set("")
		folder := it.Path
		if a.Op != protocol.FileCreate && a.Op != protocol.FileMkdir {
			folder = parentDir(it.Path)
		}
		m.loadDir(it.Node, folder, true)
		switch a.Op {
		case protocol.FileCreate:
			m.openFile(fileItem{Key: dirKey(it.Node, f.GetPath()), Node: it.Node, Path: f.GetPath(), Name: baseName(f.GetPath())})
		case protocol.FileMkdir:
			m.FilesOpen.Update(func(v map[string]bool) map[string]bool {
				v = maps.Clone(v)
				v[it.Key] = true
				return v
			})
		case protocol.FileRename, protocol.FileDelete:
			if p := ggui.Peek(m.Preview); p.Key == it.Key {
				m.Preview.Set(filePreview{})
			}
		}
	})
}

// fileMenu gives a row of the Files tab its context menu.
func fileMenu(m *model, w ggui.Widget, it fileItem) ggui.Widget {
	root := ggui.Untrack(m.filesRoot)
	var items []ggui.Widget
	switch {
	case it.Dir:
		items = append(items,
			ui.MenuItem(T("New session here…"), func() { m.openNew(it.Node, it.Path) }))
	case isImage(it.Name):
		items = append(items, ui.MenuItem(T("Preview"), func() { m.FileSel.Set(it.Key); m.openPreview(it) }))
	default:
		items = append(items, ui.MenuItem(T("Open"), func() { m.FileSel.Set(it.Key); m.openFile(it) }))
	}
	items = append(items,
		ui.MenuItem(T("Insert path in the message"), func() { m.insertPath("", it.Path) }),
		ui.MenuItem(T("Copy path"), func() { ggui.CurrentClipboard().Write(it.Path) }),
	)
	if rel := subfolder(it.Path, root.Path); rel != "" {
		items = append(items, ui.MenuItem(T("Copy relative path"), func() { ggui.CurrentClipboard().Write(rel) }))
	}
	folder := folderOf(it)
	items = append(items,
		ui.MenuDivider(),
		ui.MenuItem(T("New file…"), func() { m.askFile(protocol.FileCreate, folder) }),
		ui.MenuItem(T("New folder…"), func() { m.askFile(protocol.FileMkdir, folder) }),
		ui.MenuItem(T("Rename…"), func() { m.askFile(protocol.FileRename, it) }),
		ui.MenuDivider(),
		ui.MenuItem(T("Delete…"), func() { m.askFile(protocol.FileDelete, it) }),
	)
	return ui.ContextMenu(w, items...).Name(Tf("%s actions", it.Name))
}

// newInRoot is the Files header's menu of what to make in the project.
func newInRoot(m *model, r fileRoot) ggui.Widget {
	root := dirItem(r.Node, r.Path)
	return iconMenu(ui.MenuOf(icon("plus"),
		ui.MenuItem(T("New file…"), func() { m.askFile(protocol.FileCreate, root) }),
		ui.MenuItem(T("New folder…"), func() { m.askFile(protocol.FileMkdir, root) }),
	).Name(T("New in project")), 6)
}

// fileDialogs are the name prompt and the delete confirmation.
func fileDialogs(m *model) ggui.Widget {
	return ggui.View(m.FileAsk, func(a fileAsk) ggui.Widget {
		title, action := T("New file"), T("Create")
		switch a.Op {
		case protocol.FileMkdir:
			title = T("New folder")
		case protocol.FileRename:
			title, action = T("Rename"), T("Rename")
		}
		what := T("This file is deleted from its node. It cannot be undone.")
		if a.Item.Dir {
			what = T("This folder and everything in it are deleted from its node. It cannot be undone.")
		}
		return ggui.Stack(
			nameDialog(nameAsk{Open: m.FileNameOpen, Text: m.FileName, Err: m.FileErr,
				Title: title, Field: T("File name"), Action: action, Submit: m.submitFile}),
			ui.AlertDialog(m.FileDeleteOpen, Tf("Delete %s?", a.Item.Name), what).
				Confirm(T("Delete"), m.submitFile).Destructive(),
		)
	})
}
