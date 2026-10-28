package gui

import (
	"context"
	"maps"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// The Files tab's context menu: open a file, put its path in a message or
// on the clipboard, show it in the desktop's file manager when it is on
// this machine, start a session in a folder, and make, rename or delete
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

// fileOps is a file operation waiting on the user in a window, the Files
// tab's or a node's file browser: the operation, the name typed for it,
// why it failed, and the dialogs asking.
type fileOps struct {
	Ask        *ggui.StateValue[fileAsk]
	Name, Err  *ggui.StateValue[string]
	NameOpen   *ggui.StateValue[bool]
	DeleteOpen *ggui.StateValue[bool]
}

func newFileOps() fileOps {
	return fileOps{Ask: ggui.State(fileAsk{}), Name: ggui.State(""), Err: ggui.State(""),
		NameOpen: ggui.State(false), DeleteOpen: ggui.State(false)}
}

// ask asks for what op needs: a name, or a yes.
func (o fileOps) ask(op string, it fileItem) {
	o.Ask.Set(fileAsk{Op: op, Item: it})
	o.Err.Set("")
	switch op {
	case protocol.FileDelete:
		o.DeleteOpen.Set(true)
		return
	case protocol.FileRename:
		o.Name.Set(it.Name)
	default:
		o.Name.Set("")
	}
	o.NameOpen.Set(true)
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

// submit carries out the operation asked, on the node the item is on,
// reads its folder again, and hands done what it made. A failed delete,
// whose confirmation has closed, says why to note.
func (o fileOps) submit(m *model, note func(string), done func(fileAsk, *protocol.File)) {
	a := ggui.Peek(o.Ask)
	it := a.Item
	req := &protocol.FileOp{NodeId: m.remoteID(it.Node), Op: a.Op, Path: it.Path}
	if a.Op != protocol.FileDelete {
		name := strings.TrimSpace(ggui.Peek(o.Name))
		if why := validName(name); why != "" {
			o.Err.Set(why)
			return
		}
		switch a.Op {
		case protocol.FileRename:
			if name == it.Name {
				o.NameOpen.Set(false)
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
			o.Err.Set(nodeErr(err))
			if a.Op == protocol.FileDelete {
				note(nodeErr(err))
			}
			return
		}
		o.NameOpen.Set(false)
		folder := it.Path
		if a.Op != protocol.FileCreate && a.Op != protocol.FileMkdir {
			folder = parentDir(it.Path)
		}
		m.loadDir(it.Node, folder, true)
		done(a, f)
	})
}

// dialogs are the name prompt and the delete confirmation, which submit.
func (o fileOps) dialogs(submit func()) ggui.Widget {
	return ggui.View(o.Ask, func(a fileAsk) ggui.Widget {
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
			nameDialog(nameAsk{Open: o.NameOpen, Text: o.Name, Err: o.Err,
				Title: title, Field: T("File name"), Action: action, Submit: submit}),
			ui.AlertDialog(o.DeleteOpen, Tf("Delete %s?", a.Item.Name), what).
				Confirm(T("Delete"), submit).Destructive(),
		)
	})
}

// askFile asks, in the Files tab, for what op needs.
func (m *model) askFile(op string, it fileItem) { m.FileOps.ask(op, it) }

// submitFile carries out the operation the Files tab asked, then shows
// what it made: a new file in a tab, a new folder open.
func (m *model) submitFile() {
	m.FileOps.submit(m, m.FileNote.Set, func(a fileAsk, f *protocol.File) {
		it := a.Item
		m.FileNote.Set("")
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
	if m.onThisMachine(it.Node) {
		items = append(items, ui.MenuItem(fileManagerLabel(), func() { m.showInFileManager(it) }))
	} else {
		// Another node's files show in a window of mako's own.
		sel := it.Path // a file is chosen in its folder
		if it.Dir {
			sel = ""
		}
		items = append(items, ui.MenuItem(Tf("Show in %s's files", nodeName(ggui.Peek(m.Nodes), it.Node)), func() {
			m.openBrowser(it.Node, folderOf(it).Path, sel)
		}))
	}
	items = append(items, m.editorItems(it.Node, it.Path, nil, m.FileNote.Set)...)
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

// fileManagerLabel names the desktop's file manager in its menu item.
func fileManagerLabel() string {
	switch goruntime.GOOS {
	case "darwin":
		return T("Show in Finder")
	case "windows":
		return T("Show in Explorer")
	}
	return T("Show in file manager")
}

// showInFileManager opens a folder in the desktop's file manager, or shows
// a file selected in the folder it is in.
func (m *model) showInFileManager(it fileItem) {
	open := runtime.Reveal
	if it.Dir {
		open = runtime.OpenURL
	}
	if err := open(it.Path); err != nil {
		m.FileNote.Set(err.Error())
	}
}

// newInRoot is the Files header's menu of what to make in the project.
func newInRoot(m *model, r fileRoot) ggui.Widget {
	root := dirItem(r.Node, r.Path)
	return iconMenu(ui.MenuOf(icon("plus"),
		ui.MenuItem(T("New file…"), func() { m.askFile(protocol.FileCreate, root) }),
		ui.MenuItem(T("New folder…"), func() { m.askFile(protocol.FileMkdir, root) }),
	).Name(T("New in project")), 6)
}

// fileDialogs are the Files tab's name prompt and delete confirmation.
func fileDialogs(m *model) ggui.Widget { return m.FileOps.dialogs(m.submitFile) }
