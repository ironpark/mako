package gui

import (
	"context"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// A node other than this machine has no file manager here, so mako gives
// it one: a window of its own browsing the node's folders, as Finder or
// Explorer would this machine's. A file opens in a tab of the main window;
// files dropped on the window go up to the node, and a download comes
// down to this machine. It reads folders through the Files tab's cache, so
// the two show the same.

// browser is one window onto a node's files.
type browser struct {
	m    *model
	node string

	*navigator
	Open *ggui.StateValue[map[string]bool] // folders open in the list, by key
	Sel  *ggui.StateValue[string]          // the row chosen, by key

	Status   *ggui.StateValue[browserNote]
	Busy     *ggui.StateValue[string] // a copy under way, as it stands
	DropOver *ggui.StateValue[bool]
	cancel   context.CancelFunc // the copy under way

	Ops fileOps // the name prompt and the delete confirmation

	taps taps // a second tap on a row opens it
}

// browserNote is what the status bar says of the last copy or operation:
// a failure, or where a download went, with that to show in Finder.
type browserNote struct{ Text, Reveal string }

// openBrowser opens a window onto the folder path of node, with the file
// sel chosen when it is not "". A path that starts with ~ is the node's
// home.
func (m *model) openBrowser(node, path, sel string) {
	if m.window == nil {
		return // a probe has no windows to open
	}
	if err := m.canTransfer(node); err != nil {
		m.toaster.Push(ui.Toast(T("Could not browse the node"), nodeErr(err)).Destructive())
		return
	}
	if strings.HasPrefix(path, "~") {
		m.resolveHome(node, path, func(p string) { m.openBrowser(node, p, sel) }, func(why string) {
			m.toaster.Push(ui.Toast(T("Could not browse the node"), why).Destructive())
		})
		return
	}
	b := newBrowser(m, node, path)
	if sel != "" {
		b.Sel.Set(dirKey(node, sel))
	}
	w, err := m.window.App().OpenWindow(ggui.Config{
		Title: b.title(path), Width: 860, Height: 560, MinWidth: 520, MinHeight: 320, Resizable: true,
	}, func() ggui.Widget {
		// Built under the window's own owner, which its closing disposes.
		w := ggui.UseWindow()
		ggui.Watch(b.Path, func(p string) { w.SetTitle(b.title(p)) })
		ggui.OnCleanup(b.stop)
		return browserWindow(b)
	})
	if err != nil {
		m.toaster.Push(ui.Toast(T("Could not browse the node"), err.Error()).Destructive())
		return
	}
	// Files dropped where no folder row takes them go into the folder
	// shown.
	w.OnDrop(b.dropHere)
}

// resolveHome hands done path on node with its ~ written out, as the node
// reads it, or fail why it could not.
func (m *model) resolveHome(node, path string, done, fail func(string)) {
	req := &protocol.ListDir{NodeId: m.remoteID(node), Path: path}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Dir, error) {
		return c.ListDir(ctx, req)
	}, func(d *protocol.Dir, err error) {
		if err != nil {
			fail(nodeErr(err))
			return
		}
		done(d.GetPath())
	})
}

func newBrowser(m *model, node, path string) *browser {
	b := &browser{
		m: m, node: node,
		Open:     ggui.State(map[string]bool{}),
		Sel:      ggui.State(""),
		Status:   ggui.State(browserNote{}),
		Busy:     ggui.State(""),
		DropOver: ggui.State(false),
		Ops:      newFileOps(),
	}
	b.navigator = newNavigator(path, func(p string) {
		b.Sel.Set("")
		m.loadDir(node, p, true)
	})
	m.loadDir(node, path, true)
	return b
}

// title is the window's title at path: the folder, and the node.
func (b *browser) title(path string) string {
	return baseName(path) + " — " + nodeName(ggui.Peek(b.m.Nodes), b.node)
}

func (b *browser) goHome() {
	b.m.resolveHome(b.node, "~", b.goTo, func(why string) { b.say(why, "") })
}

// refresh reads the folder shown again, and every folder open in it.
func (b *browser) refresh() {
	b.m.reloadOpen(b.node, ggui.Peek(b.Path), ggui.Peek(b.Open))
}

// activate opens a row: a folder in the window, a file in a tab of the
// main window, which comes to the front.
func (b *browser) activate(it fileItem) {
	if it.Dir {
		b.goTo(it.Path)
		return
	}
	b.m.openItem(it, 0)
	b.m.window.Focus()
}

// tap chooses a row, and opens it when it was the row tapped just before.
func (b *browser) tap(it fileItem) {
	if it.Stub {
		return
	}
	if b.taps.again(it.Key) {
		b.activate(it)
	}
}

// taps tells a second tap on a row from a first: the row tapped last, and
// when.
type taps struct {
	key string
	at  time.Time
}

// again records a tap on the row key, and reports whether it was the
// second of a double tap.
func (t *taps) again(key string) bool {
	now := time.Now()
	if t.key == key && now.Sub(t.at) < doubleTap {
		t.key = ""
		return true
	}
	t.key, t.at = key, now
	return false
}

// say shows a note in the status bar, with a download to show by it.
func (b *browser) say(text, reveal string) { b.Status.Set(browserNote{text, reveal}) }

// run copies files on a goroutine, showing how far it has got, then hands
// done what it made of it. One copy runs at a time.
func (b *browser) run(verb string, t *transfer, work func(context.Context, *client.Client) error, done func(error)) {
	if b.cancel != nil {
		b.say(T("Wait for the copy under way, or cancel it."), "")
		return
	}
	if err := b.m.canTransfer(b.node); err != nil {
		b.say(nodeErr(err), "")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.say("", "")
	b.Busy.Set(verb + "…")
	tick := time.NewTicker(250 * time.Millisecond)
	go func() {
		for range tick.C {
			text := verb + " · " + byteSize(t.done.Load())
			if total := t.total.Load(); total > 0 {
				text += " / " + byteSize(total)
			}
			b.m.post(func() {
				if b.cancel != nil {
					b.Busy.Set(text)
				}
			})
		}
	}()
	go func() {
		c, err := b.m.client(ctx)
		if err == nil {
			err = work(ctx, c)
		}
		tick.Stop()
		b.m.post(func() {
			cancel()
			b.cancel = nil
			b.Busy.Set("")
			if ctx.Err() != nil && err != nil {
				b.say(T("Cancelled."), "")
				return
			}
			done(err)
		})
	}()
}

// stop cancels the copy under way.
func (b *browser) stop() {
	if b.cancel != nil {
		b.cancel()
	}
}

// dropHere uploads files dropped where no folder row takes them into the
// folder shown.
func (b *browser) dropHere(ev ggui.DropEvent) {
	b.DropOver.Set(false)
	b.upload(ggui.Peek(b.Path), ev.Paths())
}

// upload copies the files at paths on this machine into the folder dir on
// the node.
func (b *browser) upload(dir string, paths []string) {
	if len(paths) == 0 {
		return
	}
	t := &transfer{}
	node := b.m.remoteID(b.node)
	b.run(Tf("Uploading %s", filesLabel(paths)), t, func(ctx context.Context, c *client.Client) error {
		t.total.Store(localSize(paths)) // off the window's goroutine: a folder may be large
		return upload(ctx, c, node, dir, paths, t)
	}, func(err error) {
		b.m.loadDir(b.node, dir, true)
		if err != nil {
			b.say(nodeErr(err), "")
			return
		}
		b.say(Tf("Uploaded %s.", filesLabel(paths)), "")
	})
}

// downloadsDir is where a download goes unless asked: the user's Downloads
// folder, else the home folder.
func downloadsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	d := filepath.Join(home, "Downloads")
	if info, err := os.Stat(d); err == nil && info.IsDir() {
		return d
	}
	return home
}

// download copies a file or folder of the node into the folder dir here.
func (b *browser) download(it fileItem, dir string) {
	t := &transfer{}
	if !it.Dir {
		t.total.Store(it.Size)
	}
	node := b.m.remoteID(b.node)
	var to string
	b.run(Tf("Downloading %s", it.Name), t, func(ctx context.Context, c *client.Client) (err error) {
		to, err = download(ctx, c, node, it.Path, it.Dir, dir, t)
		return err
	}, func(err error) {
		if err != nil {
			b.say(nodeErr(err), "")
			return
		}
		b.say(Tf("Downloaded to %s.", prettyPath(to)), to)
	})
}

// downloadTo asks where a download goes, then makes it.
func (b *browser) downloadTo(it fileItem) {
	dialogs := ggui.UseHost().Dialogs()
	ggui.Async(func() (string, error) {
		return dialogs.PickFolder(runtime.FileDialog{Title: Tf("Download %s to", it.Name), Directory: downloadsDir()})
	}, func(dir string, err error) {
		if err == nil && dir != "" {
			b.download(it, dir)
		}
	})
}

// uploadFrom asks for files on this machine, then sends them into dir.
func (b *browser) uploadFrom(dir string) {
	dialogs := ggui.UseHost().Dialogs()
	ggui.Async(func() ([]string, error) {
		return dialogs.OpenFiles(runtime.FileDialog{Title: Tf("Upload to %s", baseName(dir))})
	}, func(paths []string, err error) {
		if err == nil {
			b.upload(dir, paths)
		}
	})
}

// askFile asks for what op on it needs: a name, or a yes.
func (b *browser) askFile(op string, it fileItem) { b.Ops.ask(op, it) }

// submitFile carries out the operation asked, and chooses what it made.
func (b *browser) submitFile() {
	b.Ops.submit(b.m, func(why string) { b.say(why, "") }, func(a fileAsk, f *protocol.File) {
		if a.Op != protocol.FileDelete {
			b.Sel.Set(dirKey(a.Item.Node, f.GetPath()))
		}
	})
}

// View -------------------------------------------------------------------------

// browserWindow is the window: in the language the settings choose, as
// the main one is.
func browserWindow(b *browser) ggui.Widget {
	lang := ggui.Map(b.m.Prefs, func(p prefs) string { return resolveLanguage(p.Language) })
	return ggui.Key(lang, func(code string) ggui.Widget {
		setLanguage(code)
		return browserView(b)
	})
}

func browserView(b *browser) ggui.Widget {
	m := b.m
	rows := ggui.Derived(func() []fileItem {
		return m.children(dirItem(b.node, b.Path.Get()))
	})
	tree := ui.Tree(rows, func(it fileItem) string { return it.Key }, m.children,
		func(item ggui.Readable[fileItem]) ggui.Widget {
			it := ggui.Untrack(item.Get)
			if it.Stub {
				return muted(ggui.Text(it.Name).Size(13).NoWrap().Ellipsis())
			}
			return browserRow(b, it)
		}).
		RowName(func(it fileItem) string { return it.Name }).
		RowHeight(28).Indent(16).
		BindExpanded(b.Open).
		OnToggle(func(it fileItem, open bool) {
			if open {
				m.loadDir(it.Node, it.Path, false)
			}
		}).
		BindSelected(b.Sel).
		OnSelect(b.tap).
		Name(T("Files"))

	columns := ggui.Box(ggui.Row(
		ggui.Expanded(columnHead(T("Name"), 0)),
		ggui.Box(columnHead(T("Size"), 1)).Width(sizeColumn),
		ggui.Box(columnHead(T("Modified"), 0)).Width(modColumn),
	).Gap(12)).Pad(6, 16, 6, 38)
	var list ggui.Widget = ggui.Column(columns, hline(), ggui.Expanded(ggui.Scroll(ggui.Padding(tree, 4, 8)).Bar(tokSurface3))).Stretch()
	// A drop over the list says where it goes.
	list = ggui.View(b.DropOver, func(over bool) ggui.Widget {
		if over {
			return ggui.Box(list).Border(2, tokAccentBright)
		}
		return list
	})
	// The empty part of the list has the folder's own menu.
	here := func() fileItem { return dirItem(b.node, ggui.Peek(b.Path)) }
	body := ui.ContextMenu(ggui.Pointer(list).OnDropHover(b.DropOver.Set).OnDrop(b.dropHere),
		ui.MenuItem(T("New folder…"), func() { b.askFile(protocol.FileMkdir, here()) }),
		ui.MenuItem(T("Upload files…"), func() { b.uploadFrom(ggui.Peek(b.Path)) }),
		ui.MenuItem(T("Refresh"), b.refresh),
	)

	return ggui.Stack(
		ggui.Box(ggui.Column(
			browserToolbar(b),
			hline(),
			ggui.Expanded(body),
			hline(),
			browserStatus(b, rows),
		).Stretch()).Fill(tokSolidBg),
		b.Ops.dialogs(b.submitFile),
	).Expand()
}

// The widths of the list's size and modified columns.
const (
	sizeColumn = 80
	modColumn  = 130
)

func columnHead(label string, align float64) ggui.Widget {
	return ggui.Text(label).Size(11.5).Color(tokExtraMuted).Weight(ggui.WeightMedium).NoWrap().Align(align)
}

// browserRow is a file's row: its name, size and when it last changed, and
// its menu. A folder's row takes files dropped on it.
func browserRow(b *browser, it fileItem) ggui.Widget {
	size, mod := "", ""
	if !it.Dir {
		size = byteSize(it.Size)
	}
	if !it.Mod.IsZero() {
		mod = it.Mod.Local().Format("2006-01-02 15:04")
	}
	lead := fileTypeIcon(it.Name, it.Dir, false).Size(15)
	label := ggui.Text(it.Name).Size(13).NoWrap().Ellipsis()
	if strings.HasPrefix(it.Name, ".") {
		label = label.Color(tokMuted)
	}
	var row ggui.Widget = ggui.Row(
		ggui.Expanded(ggui.Row(lead, ggui.Expanded(label)).Gap(8)),
		ggui.Box(ggui.Text(size).Size(12).Color(tokMuted).NoWrap().Align(1)).Width(sizeColumn),
		ggui.Box(ggui.Text(mod).Size(12).Color(tokMuted).NoWrap()).Width(modColumn),
	).Gap(12)
	if it.Dir {
		row = litDrop(row, nil, func(paths []string) {
			b.DropOver.Set(false)
			b.upload(it.Path, paths)
		})
	}
	return browserMenu(b, row, it)
}

// browserMenu is a row's menu.
func browserMenu(b *browser, w ggui.Widget, it fileItem) ggui.Widget {
	m := b.m
	folder := folderOf(it)
	items := []ggui.Widget{
		ui.MenuItem(T("Open"), func() { b.activate(it) }),
	}
	if it.Dir {
		items = append(items, ui.MenuItem(T("New session here…"), func() { m.openNew(b.node, it.Path); m.window.Focus() }))
	}
	items = append(items,
		ui.MenuDivider(),
		ui.MenuItem(T("Download"), func() { b.download(it, downloadsDir()) }),
		ui.MenuItem(T("Download to…"), func() { b.downloadTo(it) }),
		ui.MenuItem(Tf("Upload files to %s…", folder.Name), func() { b.uploadFrom(folder.Path) }),
		ui.MenuDivider(),
		ui.MenuItem(T("Copy path"), func() { ggui.CurrentClipboard().Write(it.Path) }),
		ui.MenuItem(T("Insert path in the message"), func() { m.insertPath("", it.Path) }),
		ui.MenuDivider(),
		ui.MenuItem(T("New folder…"), func() { b.askFile(protocol.FileMkdir, folder) }),
		ui.MenuItem(T("Rename…"), func() { b.askFile(protocol.FileRename, it) }),
		ui.MenuDivider(),
		ui.MenuItem(T("Delete…"), func() { b.askFile(protocol.FileDelete, it) }),
	)
	return ui.ContextMenu(w, items...).Name(Tf("%s actions", it.Name))
}

func browserToolbar(b *browser) ggui.Widget {
	m := b.m
	nav := navButtons(b.navigator)
	crumbs := ggui.View(b.Path, func(p string) ggui.Widget { return pathCrumbs(p, b.goTo) })
	hidden := ggui.View(ggui.Map(m.Prefs, func(p prefs) bool { return p.ShowHidden }), func(shown bool) ggui.Widget {
		if shown {
			return iconButton("eye", T("Hide hidden files"), m.toggleHidden)
		}
		return iconButton("eye-off", T("Show hidden files"), m.toggleHidden)
	})
	where := ggui.Row(icon("hard-drive").Size(14).Color(tokMuted),
		muted(ggui.Text(nodeName(ggui.Peek(m.Nodes), b.node)).Size(12.5).NoWrap())).Gap(6)
	return ggui.Box(ggui.Row(
		nav,
		iconButton("house", T("Home"), b.goHome),
		ggui.Expanded(crumbs),
		where,
		hidden,
		iconButton("folder-plus", T("New folder…"), func() { b.askFile(protocol.FileMkdir, dirItem(b.node, ggui.Peek(b.Path))) }),
		iconButton("upload", T("Upload files…"), func() { b.uploadFrom(ggui.Peek(b.Path)) }),
		iconButton("refresh-cw", T("Refresh"), b.refresh),
	).Gap(4)).Pad(8, 12).Height(48)
}

// pathCrumbs is the path of a folder shown, each folder of it a button
// that goes there.
func pathCrumbs(p string, goTo func(string)) ggui.Widget {
	sep := pathSep(p)
	var parts []ggui.Widget
	at := ""
	segs := strings.Split(strings.TrimSuffix(p, sep), sep)
	for i, seg := range segs {
		switch {
		case i == 0 && seg == "": // the root of a Unix path
			at = sep
			seg = sep
		case i == 0 && strings.HasSuffix(seg, ":"): // a Windows drive, whose root has the separator
			at = seg + sep
		case at == "" || strings.HasSuffix(at, sep):
			at += seg
		default:
			at += sep + seg
		}
		if i > 0 && segs[0] != "" || i > 1 {
			parts = append(parts, muted(ggui.Text("›").Size(12.5)))
		}
		target := at
		text := ggui.Text(seg).Size(12.5).NoWrap()
		if i == len(segs)-1 {
			text = text.Weight(ggui.WeightMedium)
		} else {
			text = text.Color(tokMuted)
		}
		parts = append(parts, ui.ButtonOf(text, func() { goTo(target) }).Ghost().Pad(3, 5).Radius(5).Name(target))
	}
	return ggui.Scroll(ggui.Row(parts...).Gap(1)).Horizontal().Bar(color.Transparent).FollowEnd()
}

// browserStatus is the bar under the list: how many items, a copy under
// way, or what came of the last.
func browserStatus(b *browser, rows ggui.Readable[[]fileItem]) ggui.Widget {
	count := ggui.Map(rows, func(r []fileItem) string {
		if len(r) == 1 && r[0].Stub {
			return r[0].Name
		}
		return Tf("%d items", len(r))
	})
	return ggui.Reactive(func() ggui.Widget {
		var left ggui.Widget = muted(ggui.TextOf(count).Size(12).NoWrap())
		var right []ggui.Widget
		switch busy, note := b.Busy.Get(), b.Status.Get(); {
		case busy != "":
			left = ggui.Row(icon("loader-circle").Size(13).Color(tokMuted), muted(ggui.Text(busy).Size(12).NoWrap().Ellipsis())).Gap(6)
			right = append(right, ui.Button(T("Cancel"), b.stop).Ghost().Pad(3, 8))
		case note.Text != "":
			left = ggui.Text(note.Text).Size(12).NoWrap().Ellipsis()
			if note.Reveal != "" {
				right = append(right, ui.Button(fileManagerLabel(), func() { _ = runtime.Reveal(note.Reveal) }).Ghost().Pad(3, 8))
			}
			right = append(right, iconButton("x", T("Dismiss"), func() { b.say("", "") }))
		}
		return ggui.Box(ggui.Row(append([]ggui.Widget{ggui.Expanded(left)}, right...)...).Gap(6)).Pad(4, 12).Height(32)
	})
}
