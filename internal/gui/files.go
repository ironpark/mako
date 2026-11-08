package gui

import (
	"context"
	"fmt"
	"image"
	_ "image/gif" // the formats the preview shows
	_ "image/jpeg"
	_ "image/png"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/gui/svgdraw"
	"github.com/ironpark/mako/internal/protocol"
)

// The right panel's Files tab browses the selected session's project on
// the node it runs on, through that node's daemon: a tree whose folders are
// read as they open, over a preview of the file chosen.

// fileRoot is a project folder on a node: the one the panel browses, or one
// the sidebar keeps after its last session.
type fileRoot struct {
	Node string `json:"node"`
	Path string `json:"path"`
}

// dirKey names a directory, or a file, on a node: the tree's keys.
func dirKey(node, path string) string { return node + "\x00" + path }

func splitKey(key string) (node, path string) {
	node, path, _ = strings.Cut(key, "\x00")
	return node, path
}

// dirListing is what is known of a directory: its entries once read, and
// while it is read again, the ones from before.
type dirListing struct {
	Entries []*protocol.DirEntry
	Err     string
	Loading bool
}

// fileItem is one row of the tree. A stub stands under a folder not yet
// read, so it shows a chevron, and says it is loading or why it failed.
type fileItem struct {
	Key, Node, Path, Name string
	Dir, Stub             bool
	Root                  bool // a project's folder, at the top of the tree
	Ignored               bool // left out of the repository, and shown dimmed
	Size                  int64
	Mod                   time.Time
}

// filePreview is the file shown under the tree.
type filePreview struct {
	fileItem
	File    *protocol.File
	Img     *ggfx.Image // an image file, decoded; its bytes are not kept
	Err     string
	Loading bool
}

// same reports whether two previews show the same, telling images apart by
// identity rather than by their pixels.
func (p filePreview) same(o filePreview) bool {
	if p.Img != o.Img {
		return false
	}
	p.Img, o.Img = nil, nil
	return deepEqual(p, o)
}

// isImage reports whether a file's name says it is an image the preview
// decodes.
func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// decodedImage is an image file as read, and decoded off the window's
// goroutine: a picture, or an SVG drawn.
type decodedImage struct {
	File  *protocol.File
	Img   image.Image
	Drawn *svgdraw.Picture // an SVG's; nil for none, or one that cannot be drawn
}

// drawnSVG is an SVG read as f, drawn, as it is read: off the window's
// goroutine, which only wraps its pixels.
func drawnSVG(f *protocol.File, data []byte) decodedImage {
	d := decodedImage{File: f}
	if p, ok := svgdraw.Draw(data); ok {
		d.Drawn = &p
	}
	return d
}

// fetch makes a daemon call off the window's goroutine and hands done its
// result back on it.
func fetch[T any](m *model, timeout time.Duration, call func(context.Context, *client.Client) (T, error), done func(T, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var v T
		c, err := m.link.client(ctx)
		if err == nil {
			v, err = call(ctx, c)
		}
		m.post(func() { done(v, err) })
	}()
}

// remoteID is the node a request names: "" for the daemon the window talks
// through, which serves its own node.
func (m *model) remoteID(node string) string {
	if node == ggui.Peek(m.Conn).Self.GetId() {
		return ""
	}
	return node
}

// filesRoot is the selected session's project, or the one the selected
// file or diff was opened from.
func (m *model) filesRoot() fileRoot {
	switch id := m.Selected.Get(); {
	case isDocTab(id):
		return m.Docs.Get()[id].Root
	case isDiffTab(id):
		return m.Diffs.Get()[id].Root
	}
	s, ok := sessionIn(m.Sessions.Get(), m.Selected.Get())
	if !ok {
		return fileRoot{}
	}
	return fileRoot{Node: s.GetNodeId(), Path: s.GetProject()}
}

// loadDir reads a directory unless it has been, or always when force is
// set, keeping what was read before on show until the new entries come.
func (m *model) loadDir(node, path string, force bool) {
	key := dirKey(node, path)
	dirs := ggui.Peek(m.files.Dirs)
	if l, ok := dirs[key]; ok && (l.Loading || !force) {
		return
	}
	next := maps.Clone(dirs)
	l := next[key]
	l.Loading = true
	next[key] = l
	m.files.Dirs.Set(next)

	req := &protocol.ListDir{NodeId: m.remoteID(node), Path: path}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Dir, error) {
		return c.ListDir(ctx, req)
	}, func(dir *protocol.Dir, err error) {
		next := maps.Clone(ggui.Peek(m.files.Dirs))
		if err != nil {
			next[key] = dirListing{Err: err.Error()}
		} else {
			next[key] = dirListing{Entries: dir.GetEntries()}
		}
		m.files.Dirs.Set(next)
	})
}

// pruneDirs lets go of the listings of folders nothing shows, as the
// window would otherwise keep every folder it ever listed: it keeps those
// in the project shown and those open in the Files tab, which show again
// with their project, and every folder of a node a file browser or the
// folder picker is open on, as either goes anywhere there. A folder let
// go of is read again when it is opened.
func (m *model) pruneDirs() {
	roots := ggui.Peek(m.Roots).Roots
	open := ggui.Peek(m.files.Open)
	picking := ggui.Peek(m.Pick.On)
	dirs := ggui.Peek(m.files.Dirs)
	keep := func(key string, l dirListing) bool {
		node, p := splitKey(key)
		return l.Loading || open[key] || m.files.browsing[node] > 0 || picking && node == m.Pick.Node ||
			slices.ContainsFunc(roots, func(r panelRoot) bool { return r.Node == node && inDir(r.Path, p) })
	}
	var next map[string]dirListing
	for key, l := range dirs {
		if keep(key, l) {
			continue
		}
		if next == nil {
			next = maps.Clone(dirs)
		}
		delete(next, key)
	}
	if next != nil {
		m.files.Dirs.Set(next)
	}
}

// refreshFiles reads the project again, every folder of it shown, and
// every folder open in them.
func (m *model) refreshFiles() {
	open := ggui.Peek(m.files.Open)
	for _, r := range ggui.Peek(m.Roots).Roots {
		m.reloadOpen(r.Node, r.Path, open)
	}
	if p := ggui.Peek(m.files.Preview); p.Key != "" {
		m.openPreview(p.fileItem)
	}
}

// reloadOpen reads the folder dir of node again, and every folder open in
// it, by the keys of open.
func (m *model) reloadOpen(node, dir string, open map[string]bool) {
	m.loadDir(node, dir, true)
	for key, o := range open {
		if n, p := splitKey(key); o && n == node && inDir(dir, p) {
			m.loadDir(n, p, true)
		}
	}
}

// children are the rows under a folder: its entries once read, else a stub.
// Dotfiles are left out unless the settings show them.
func (m *model) children(it fileItem) []fileItem {
	if !it.Dir || it.Stub {
		return nil
	}
	l, ok := m.files.Dirs.Get()[it.Key]
	switch {
	case l.Err != "":
		return []fileItem{{Key: it.Key + "\x00stub", Name: l.Err, Stub: true}}
	case !ok || (l.Loading && l.Entries == nil):
		return []fileItem{{Key: it.Key + "\x00stub", Name: T("Loading…"), Stub: true}}
	}
	hidden := m.Prefs.Get().ShowHidden
	items := make([]fileItem, 0, len(l.Entries))
	for _, e := range l.Entries {
		if !hidden && strings.HasPrefix(e.GetName(), ".") {
			continue
		}
		path := joinPath(it.Path, e.GetName())
		items = append(items, fileItem{Key: dirKey(it.Node, path), Node: it.Node, Path: path, Name: e.GetName(), Dir: e.GetDir(), Ignored: e.GetIgnored(),
			Size: e.GetSize(), Mod: protocol.TimeOf(e.GetModTime())})
	}
	return items
}

// joinPath joins a name onto a directory of the node it is on, which may
// not share this machine's separator.
func joinPath(dir, name string) string {
	sep := pathSep(dir)
	return strings.TrimSuffix(dir, sep) + sep + name
}

// resolvePath is rel from dir on a node, cleaned, or rel itself when it is
// absolute there. A Windows node's paths, by their separator, resolve the
// same on whatever this machine is: a link in them may be written with
// either.
func resolvePath(dir, rel string) string {
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return rel
	}
	if pathSep(dir) != `\` {
		return path.Join(dir, rel)
	}
	rel = strings.ReplaceAll(rel, `\`, "/")
	if strings.HasPrefix(rel, "/") || winDrive(rel) {
		return strings.ReplaceAll(rel, "/", `\`) // absolute on the node
	}
	slashed := strings.ReplaceAll(dir, `\`, "/")
	unc := strings.HasPrefix(slashed, "//") // \\server\share, which Join would fold
	joined := path.Join(slashed, rel)
	if unc {
		joined = "/" + joined
	}
	return strings.ReplaceAll(joined, "/", `\`)
}

// winDrive reports whether p starts with a Windows drive, as C: does.
func winDrive(p string) bool {
	return len(p) >= 2 && p[1] == ':' && ('a' <= p[0]|0x20 && p[0]|0x20 <= 'z')
}

// openItem opens a file in what shows it: an image in the Files tab's
// preview, which it brings out, any other file in a tab, at line when it
// is not 0.
func (m *model) openItem(it fileItem, line int) {
	switch {
	case isImage(it.Name):
		m.DetailsOpen.Set(true)
		m.DetailTab.Set(filesTab)
		m.openPreview(it)
	case line > 0:
		m.openFileAt(it, line)
	default:
		m.openFile(it)
	}
}

// pathSep is the separator of a path on a node: a backslash for a
// Windows path, which has no slashes.
func pathSep(p string) string {
	if strings.Contains(p, `\`) && !strings.Contains(p, "/") {
		return `\`
	}
	return "/"
}

// inDir reports whether p is the folder dir or in it, by the separator
// dir is written with: /a/bc is not in /a/b.
func inDir(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, pathSep(dir))+pathSep(dir))
}

// baseName is a path's last element, by either separator.
func baseName(p string) string { return path.Base(strings.ReplaceAll(p, `\`, "/")) }

// toggleHidden shows or hides dotfiles in the Files tab.
func (m *model) toggleHidden() {
	m.Prefs.Update(func(p prefs) prefs { p.ShowHidden = !p.ShowHidden; return p })
}

// toggleDir opens a closed folder, reading it, and closes an open one.
func (m *model) toggleDir(it fileItem) {
	open := maps.Clone(ggui.Peek(m.files.Open))
	open[it.Key] = !open[it.Key]
	if open[it.Key] {
		m.loadDir(it.Node, it.Path, false)
	}
	m.files.Open.Set(open)
}

// fetchImage reads an image file from its node and decodes it, off the
// window's goroutine; one too large to send comes without an image.
func fetchImage(m *model, node, path string, done func(decodedImage, error)) {
	req := &protocol.ReadFile{NodeId: m.remoteID(node), Path: path, Image: true}
	fetch(m, 30*time.Second, func(ctx context.Context, c *client.Client) (decodedImage, error) {
		f, err := c.ReadFile(ctx, req)
		if err != nil || f.GetData() == nil {
			return decodedImage{File: f}, err
		}
		if isSVG(path) {
			data := f.GetData()
			f.Data = nil
			return drawnSVG(f, data), nil
		}
		img, err := svgdraw.DecodeImage(f.GetData(), svgdraw.ImagePixels)
		f.Data = nil
		return decodedImage{File: f, Img: img}, err
	}, done)
}

// openPreview reads an image into the preview under the tree; any other
// file opens in a tab.
func (m *model) openPreview(it fileItem) {
	m.files.Preview.Set(filePreview{fileItem: it, Loading: true})
	fetchImage(m, it.Node, it.Path, func(d decodedImage, err error) {
		if ggui.Peek(m.files.Preview).Key != it.Key {
			return // another file was chosen meanwhile
		}
		p := filePreview{fileItem: it, File: d.File}
		switch {
		case err != nil:
			p.Err = err.Error()
		case d.Img != nil:
			p.Img = ggfx.NewImageFromImage(d.Img)
		}
		m.files.Preview.Set(p)
	})
}

// insertPath types a path, relative to the project, into a session's
// composer draft. With no session named, it goes to the chat in view.
func (m *model) insertPath(id, path string) {
	if id == "" {
		id = m.chatTarget()
	}
	if id == "" {
		return
	}
	root := ggui.Untrack(m.filesRoot)
	rel := path
	if r := subfolder(path, root.Path); r != "" {
		rel = r
	}
	text := m.composerDraft(id)
	if text != "" && !strings.HasSuffix(text, " ") {
		text += " "
	}
	m.setComposerDraft(id, text+rel+" ") // to write on after it
}

// Panel -------------------------------------------------------------------------

func filesPanel(m *model) ggui.Widget {
	shown := m.Roots
	// The project is read when it is shown, and a new one clears the
	// preview of the last. Of a project of several folders, the session's
	// own opens at first, and those left open are read.
	ggui.Effect(func() ggui.Cleanup {
		p := shown.Get()
		if len(p.Roots) == 0 {
			return nil
		}
		open := ggui.Peek(m.files.Open)
		own := p.own()
		if _, seen := open[dirKey(own.Node, own.Path)]; p.multi() && !seen {
			open = maps.Clone(open)
			open[dirKey(own.Node, own.Path)] = true
			m.files.Open.Set(open)
		}
		m.pruneDirs()
		for _, r := range p.Roots {
			if !p.multi() || open[dirKey(r.Node, r.Path)] {
				m.loadDir(r.Node, r.Path, false)
			}
		}
		if pv := ggui.Peek(m.files.Preview); pv.Key != "" && !slices.ContainsFunc(p.Roots, func(r panelRoot) bool {
			return pv.Node == r.Node && strings.HasPrefix(pv.Path, r.Path)
		}) {
			m.files.Preview.Set(filePreview{})
		}
		return nil
	})
	return ggui.View(shown, func(p panelRoots) ggui.Widget {
		if len(p.Roots) == 0 {
			return ggui.Box(muted(ggui.Text(T("Open a session to browse its project.")).Size(13))).Pad(16)
		}
		return filesView(m, p)
	})
}

// rootItems are the rows at the top of the tree: the folder's entries, or
// of a project of several folders, each folder.
func (m *model) rootItems(p panelRoots) []fileItem {
	if !p.multi() {
		r := p.Roots[0]
		return m.children(fileItem{Key: dirKey(r.Node, r.Path), Node: r.Node, Path: r.Path, Dir: true})
	}
	items := make([]fileItem, len(p.Roots))
	for i, r := range p.Roots {
		items[i] = fileItem{Key: dirKey(r.Node, r.Path), Node: r.Node, Path: r.Path, Name: r.Label, Dir: true, Root: true}
	}
	return items
}

func filesView(m *model, p panelRoots) ggui.Widget {
	r := p.own()
	roots := ggui.Derived(func() []fileItem { return m.rootItems(p) })
	tree := ui.Tree(roots, func(it fileItem) string { return it.Key }, m.children,
		func(item ggui.Readable[fileItem]) ggui.Widget {
			it := ggui.Untrack(item.Get)
			if it.Stub {
				return muted(ggui.Text(it.Name).Size(12.5).NoWrap().Ellipsis())
			}
			label := ggui.Text(it.Name).Size(12.5).NoWrap().Ellipsis()
			if it.Ignored {
				label = label.Color(skin.ExtraMuted)
			}
			leadIcon := func(open bool) ggui.Widget {
				w := fileTypeIcon(it.Name, it.Dir, open).Size(14)
				if it.Ignored {
					w = w.Color(skin.ExtraMuted)
				}
				return w
			}
			lead := leadIcon(false)
			if it.Dir { // a folder's icon opens with it
				lead = ggui.View(ggui.Map(m.files.Open, func(o map[string]bool) bool { return o[it.Key] }), leadIcon)
			}
			var row ggui.Widget = ggui.Row(lead, ggui.Expanded(label)).Gap(7)
			if it.Dir {
				row = folderDrop(m, row, it)
			}
			// Dragged to a terminal or the message box, it is typed as
			// its path; to a folder, it moves there. A project's folder
			// stays put, and is not to rename or delete here.
			drag := ggui.DragSource(row, pathDrag{Node: it.Node, Path: it.Path, Dir: it.Dir})
			if it.Root {
				return drag
			}
			return fileMenu(m, drag, it)
		}).
		RowName(func(it fileItem) string { return it.Name }).
		// A folder says it is open by its icon, and opens on a press.
		RowHeight(26).Indent(14).Chevrons(false).
		BindExpanded(m.files.Open).
		OnToggle(func(it fileItem, open bool) {
			if open {
				m.loadDir(it.Node, it.Path, false)
			}
		}).
		BindSelected(m.files.Sel).
		OnSelect(func(it fileItem) {
			switch {
			case it.Stub:
			case it.Dir:
				m.toggleDir(it)
			case isImage(it.Name):
				m.openPreview(it)
			default:
				m.openFile(it)
			}
		}).
		Name(T("Files"))

	name, where := projectName(r.Path), prettyPath(r.Path)
	if p.multi() {
		name, where = p.Name, Tf("%d folders", len(p.Roots))
	}
	header := panelHeader(
		icon("folder-open").Size(15),
		ggui.Expanded(ggui.Row(
			ggui.Text(name).Size(13).Weight(ggui.WeightMedium).NoWrap(),
			ggui.Expanded(ggui.Text(where).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis()),
		).Gap(8).Align(ggui.AlignBaseline)),
		ggui.Row(
			hiddenFilesToggle(m),
			newInRoot(m, r),
			iconButton("refresh-cw", T("Refresh"), m.refreshFiles),
		).Gap(2),
	)
	// A failed delete says why under the header.
	header = ggui.Column(header, ggui.If(ggui.Map(m.files.Note, func(s string) bool { return s != "" }), func() ggui.Widget {
		return ggui.Box(ggui.Row(
			ggui.Expanded(ggui.TextOf(m.files.Note).Size(12).Color(skin.DotDown)),
			iconButton("x", T("Dismiss"), func() { m.files.Note.Set("") }),
		).Gap(6)).Pad(4, 6, 4, 12)
	})).Stretch()
	hasPreview := ggui.Map(m.files.Preview, func(p filePreview) bool { return p.Key != "" })
	// Files dropped below the folders go into the project, from the
	// desktop and from the tab; a right click there makes or pastes there.
	root := dirItem(r.Node, r.Path)
	var list ggui.Widget = ggui.Pointer(ggui.Scroll(ggui.Padding(tree, 4, 4)).Bar(skin.Surface3)).OnDrop(func(ev ggui.DropEvent) {
		m.copyInto(r.Node, r.Path, ev.Paths())
	})
	list = ggui.DropZone(list, func(d ggui.Dropped[pathDrag]) { m.dropInto(d.Value, root) })
	list = ui.ContextMenuFunc(list, func() []ggui.Widget {
		return append(folderItems(m, root, m.askFile), ui.MenuDivider(), ui.MenuItem(T("Refresh"), m.refreshFiles))
	}).Name(T("File actions"))
	return treeOverPane(header, list,
		hasPreview, 0.55, func() ggui.Widget { return previewPane(m) }, T("Preview divider"))
}

func previewPane(m *model) ggui.Widget {
	head := ggui.View(m.files.Preview, func(p filePreview) ggui.Widget {
		path := p.Path
		info := ""
		switch {
		case p.Loading:
			info = T("Reading…")
		case p.Img != nil:
			b := p.Img.Bounds()
			info = fmt.Sprintf("%d × %d · %s", b.Dx(), b.Dy(), byteSize(p.File.GetSize()))
		case p.Err == "":
			info = byteSize(p.File.GetSize())
		}
		var enlarge ggui.Widget = ggui.Box()
		if p.Img != nil {
			enlarge = iconButton("maximize-2", T("Enlarge"), func() { m.files.ImageOpen.Set(true) })
		}
		return panelHeader(
			fileTypeIcon(p.Name, false, false).Size(14),
			ggui.Expanded(ggui.Row(
				ggui.Box(ggui.Text(p.Name).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()).MaxWidth(170),
				ggui.Text(info).Size(11.5).Color(skin.ExtraMuted).NoWrap(),
			).Gap(8).Align(ggui.AlignBaseline)),
			ggui.Row(
				enlarge,
				iconButton("terminal", T("Insert path in the message"), func() { m.insertPath("", path) }),
				iconButton("copy", T("Copy path"), func() { ggui.CurrentClipboard().Write(path) }),
				iconButton("x", T("Close preview"), func() { m.files.Preview.Set(filePreview{}); m.files.Sel.Set("") }),
			).Gap(2),
		)
	})
	content := ggui.View(m.files.Preview, func(p filePreview) ggui.Widget {
		var note string
		switch {
		case p.Loading:
			return ggui.Center(ui.Spinner())
		case p.Err != "":
			note = p.Err
		case p.Img != nil:
			// Clicked, it shows as large as the window allows.
			img := ggui.Pointer(ggui.Image(p.Img).Alt(p.Name)).OnTap(func() { m.files.ImageOpen.Set(true) })
			return ggui.Padding(ggui.Center(img), 12)
		case p.File.GetTruncated():
			note = Tf("An image of %s, too large to show.", byteSize(p.File.GetSize()))
		default:
			note = T("Not an image this can show.")
		}
		return paneNote(note)
	})
	return lowerPane(head, content)
}

// imageViewer shows the previewed image as large as the window allows,
// at most its own size, until a click on it or Escape closes it.
func imageViewer(m *model) ggui.Widget {
	img := ggui.Map(m.files.Preview, func(p filePreview) *ggfx.Image { return p.Img })
	return ggui.View(img, func(*ggfx.Image) ggui.Widget {
		p := ggui.Peek(m.files.Preview)
		if p.Img == nil {
			return ggui.Box()
		}
		b := p.Img.Bounds()
		content := ggui.Column(
			ggui.Pointer(ggui.Image(p.Img).Alt(p.Name)).OnTap(func() { m.files.ImageOpen.Set(false) }),
			muted(ggui.Text(fmt.Sprintf("%s · %d × %d", p.Name, b.Dx(), b.Dy())).Size(12)),
		).Gap(10).Align(ggui.AlignCenter)
		// The panel hugs the image, which shrinks to fit the window.
		return ui.Dialog(m.files.ImageOpen, ggui.Padding(content, 12)).Name(T("Image")).Compact().Width(float64(max(b.Dx(), 240) + 24))
	})
}

// panelHeader is a side panel's top line, as tall as the panes' tab bars
// so the line under it runs on from theirs.
func panelHeader(children ...ggui.Widget) ggui.Widget {
	return ggui.Box(ggui.Row(children...).Gap(8)).Height(tabBarHeight).Pad(0, 6, 0, 12)
}

// treeOverPane is a panel's header over its tree, and once open turns
// true, the pane the tree opens under it, at ratio of the height to start.
func treeOverPane(header, tree ggui.Widget, open ggui.Readable[bool], ratio float64, pane func() ggui.Widget, divider string) ggui.Widget {
	split := ggui.State(ratio)
	return ggui.Column(
		header,
		hline(),
		ggui.Expanded(ggui.If(open, func() ggui.Widget {
			return ui.Resizable(split, tree, pane()).Vertical().MinSizes(80, 80).Hairline().Name(divider)
		}).Else(func() ggui.Widget { return tree })),
	).Stretch()
}

// lowerPane frames the pane under a tree: its head line, then its content
// on the sunken surface.
func lowerPane(head, content ggui.Widget) ggui.Widget {
	return ggui.Column(hline(), head, hline(), ggui.Expanded(ggui.Box(content).Fill(skin.Surface1))).Stretch()
}

// paneNote stands in a lower pane for content it has none of.
func paneNote(text string) ggui.Widget { return ggui.Box(muted(ggui.Text(text).Size(12.5))).Pad(12) }

// byteSize writes a size as people read one.
func byteSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}
