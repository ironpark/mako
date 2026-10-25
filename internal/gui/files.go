package gui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif" // the formats the preview shows
	_ "image/jpeg"
	_ "image/png"
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
)

// The right panel's Files tab browses the selected session's project on
// the node it runs on, through that node's daemon: a tree whose folders are
// read as they open, over a preview of the file chosen.

// fileRoot is the directory the panel browses: a session's project.
type fileRoot struct{ Node, Path string }

// dirKey names a directory, or a file, on a node: the tree's keys.
func dirKey(node, path string) string { return node + "\x00" + path }

func splitKey(key string) (node, path string) {
	node, path, _ = strings.Cut(key, "\x00")
	return node, path
}

// dirListing is what is known of a directory: its entries once read, and
// while it is read again, the ones from before.
type dirListing struct {
	Entries []protocol.DirEntry
	Err     string
	Loading bool
}

// fileItem is one row of the tree. A stub stands under a folder not yet
// read, so it shows a chevron, and says it is loading or why it failed.
type fileItem struct {
	Key, Node, Path, Name string
	Dir, Stub             bool
	Ignored               bool // left out of the repository, and shown dimmed
}

// filePreview is the file shown under the tree.
type filePreview struct {
	fileItem
	File    protocol.File
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
	return reflect.DeepEqual(p, o)
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
// goroutine.
type decodedImage struct {
	File protocol.File
	Img  image.Image
}

// previewMax is how much of a file the preview reads.
const previewMax = 256 << 10

// fetch makes a daemon call off the window's goroutine and hands done its
// result back on it.
func fetch[T any](m *model, timeout time.Duration, call func(context.Context, *client.Client) (T, error), done func(T, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var v T
		c, err := m.client(ctx)
		if err == nil {
			v, err = call(ctx, c)
		}
		m.post(func() { done(v, err) })
	}()
}

// remoteID is the node a request names: "" for the daemon the window talks
// through, which serves its own node.
func (m *model) remoteID(node string) string {
	if node == ggui.Peek(m.Conn).Self.ID {
		return ""
	}
	return node
}

// filesRoot is the selected session's project.
func (m *model) filesRoot() fileRoot {
	s, ok := sessionIn(m.Sessions.Get(), m.Selected.Get())
	if !ok {
		return fileRoot{}
	}
	return fileRoot{Node: s.NodeID, Path: s.Project}
}

// loadDir reads a directory unless it has been, or always when force is
// set, keeping what was read before on show until the new entries come.
func (m *model) loadDir(node, path string, force bool) {
	key := dirKey(node, path)
	dirs := ggui.Peek(m.Dirs)
	if l, ok := dirs[key]; ok && (l.Loading || !force) {
		return
	}
	next := maps.Clone(dirs)
	l := next[key]
	l.Loading = true
	next[key] = l
	m.Dirs.Set(next)

	req := protocol.ListDir{NodeID: m.remoteID(node), Path: path}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (protocol.Dir, error) {
		return c.ListDir(ctx, m.key(), req)
	}, func(dir protocol.Dir, err error) {
		next := maps.Clone(ggui.Peek(m.Dirs))
		if err != nil {
			next[key] = dirListing{Err: err.Error()}
		} else {
			next[key] = dirListing{Entries: dir.Entries}
		}
		m.Dirs.Set(next)
	})
}

// refreshFiles reads the project again, and every folder open in it.
func (m *model) refreshFiles() {
	root := ggui.Untrack(m.filesRoot)
	if root.Path == "" {
		return
	}
	m.loadDir(root.Node, root.Path, true)
	for key, open := range ggui.Peek(m.FilesOpen) {
		if node, path := splitKey(key); open && node == root.Node && strings.HasPrefix(path, root.Path) {
			m.loadDir(node, path, true)
		}
	}
	if p := ggui.Peek(m.Preview); p.Key != "" {
		m.openPreview(p.fileItem)
	}
}

// children are the rows under a folder: its entries once read, else a stub.
// Dotfiles are left out unless the settings show them.
func (m *model) children(it fileItem) []fileItem {
	if !it.Dir || it.Stub {
		return nil
	}
	l, ok := m.Dirs.Get()[it.Key]
	switch {
	case l.Err != "":
		return []fileItem{{Key: it.Key + "\x00stub", Name: l.Err, Stub: true}}
	case !ok || (l.Loading && l.Entries == nil):
		return []fileItem{{Key: it.Key + "\x00stub", Name: T("Loading…"), Stub: true}}
	}
	hidden := m.Prefs.Get().ShowHidden
	items := make([]fileItem, 0, len(l.Entries))
	for _, e := range l.Entries {
		if !hidden && strings.HasPrefix(e.Name, ".") {
			continue
		}
		path := joinPath(it.Path, e.Name)
		items = append(items, fileItem{Key: dirKey(it.Node, path), Node: it.Node, Path: path, Name: e.Name, Dir: e.Dir, Ignored: e.Ignored})
	}
	return items
}

// joinPath joins a name onto a directory of the node it is on, which may
// not share this machine's separator.
func joinPath(dir, name string) string {
	sep := "/"
	if strings.Contains(dir, `\`) && !strings.Contains(dir, "/") {
		sep = `\`
	}
	return strings.TrimSuffix(dir, sep) + sep + name
}

// toggleHidden shows or hides dotfiles in the Files tab.
func (m *model) toggleHidden() {
	m.Prefs.Update(func(p prefs) prefs { p.ShowHidden = !p.ShowHidden; return p })
}

// toggleDir opens a closed folder, reading it, and closes an open one.
func (m *model) toggleDir(it fileItem) {
	open := maps.Clone(ggui.Peek(m.FilesOpen))
	open[it.Key] = !open[it.Key]
	if open[it.Key] {
		m.loadDir(it.Node, it.Path, false)
	}
	m.FilesOpen.Set(open)
}

// openPreview reads the start of a file into the preview, or an image
// whole.
func (m *model) openPreview(it fileItem) {
	m.Preview.Set(filePreview{fileItem: it, Loading: true})
	if isImage(it.Name) {
		m.openImage(it)
		return
	}
	req := protocol.ReadFile{NodeID: m.remoteID(it.Node), Path: it.Path, Max: previewMax}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (protocol.File, error) {
		return c.ReadFile(ctx, m.key(), req)
	}, func(f protocol.File, err error) {
		if ggui.Peek(m.Preview).Key != it.Key {
			return // another file was chosen meanwhile
		}
		p := filePreview{fileItem: it, File: f}
		if err != nil {
			p.Err = err.Error()
		}
		m.Preview.Set(p)
	})
}

func (m *model) openImage(it fileItem) {
	req := protocol.ReadFile{NodeID: m.remoteID(it.Node), Path: it.Path, Image: true}
	fetch(m, 30*time.Second, func(ctx context.Context, c *client.Client) (decodedImage, error) {
		f, err := c.ReadFile(ctx, m.key(), req)
		if err != nil || f.Data == nil {
			return decodedImage{File: f}, err
		}
		img, _, err := image.Decode(bytes.NewReader(f.Data))
		f.Data = nil
		return decodedImage{File: f, Img: img}, err
	}, func(d decodedImage, err error) {
		if ggui.Peek(m.Preview).Key != it.Key {
			return // another file was chosen meanwhile
		}
		p := filePreview{fileItem: it, File: d.File}
		switch {
		case err != nil:
			p.Err = err.Error()
		case d.Img != nil:
			p.Img = ggfx.NewImageFromImage(d.Img)
		}
		m.Preview.Set(p)
	})
}

// insertPath types a path, relative to the project, into the composer.
func (m *model) insertPath(path string) {
	root := ggui.Untrack(m.filesRoot)
	rel := path
	if r := strings.TrimPrefix(path, root.Path); r != path && r != "" {
		rel = strings.TrimLeft(r, `/\`)
	}
	text := ggui.Peek(m.Composer)
	if text != "" && !strings.HasSuffix(text, " ") {
		text += " "
	}
	m.Composer.Set(text + rel)
}

// Panel -------------------------------------------------------------------------

func filesPanel(m *model) ggui.Widget {
	root := ggui.Derived(m.filesRoot)
	// The project is read when it is shown, and a new one clears the
	// preview of the last.
	ggui.Effect(func() ggui.Cleanup {
		r := root.Get()
		if r.Path == "" {
			return nil
		}
		m.loadDir(r.Node, r.Path, false)
		if p := ggui.Peek(m.Preview); p.Key != "" && (p.Node != r.Node || !strings.HasPrefix(p.Path, r.Path)) {
			m.Preview.Set(filePreview{})
		}
		return nil
	})
	return ggui.Key(root, func(r fileRoot) ggui.Widget {
		if r.Path == "" {
			return ggui.Box(muted(ggui.Text(T("Open a session to browse its project.")).Size(13))).Pad(16)
		}
		return filesView(m, r)
	})
}

func filesView(m *model, r fileRoot) ggui.Widget {
	rootItem := fileItem{Key: dirKey(r.Node, r.Path), Node: r.Node, Path: r.Path, Dir: true}
	roots := ggui.Derived(func() []fileItem { return m.children(rootItem) })
	tree := ui.Tree(roots, func(it fileItem) string { return it.Key }, m.children,
		func(item ggui.Readable[fileItem]) ggui.Widget {
			it := ggui.Untrack(item.Get)
			if it.Stub {
				return muted(ggui.Text(it.Name).Size(12.5).NoWrap().Ellipsis())
			}
			name := "file"
			if it.Dir {
				name = "folder"
			}
			label := ggui.Text(it.Name).Size(12.5).NoWrap().Ellipsis()
			lead := icon(name).Size(14)
			if it.Ignored {
				label, lead = label.Color(tokExtraMuted), lead.Color(tokExtraMuted)
			}
			row := ggui.Row(lead, ggui.Expanded(label)).Gap(7)
			// Dragged to a terminal or the message box, it is typed as
			// its path.
			return ggui.DragSource(row, pathDrag{Path: it.Path})
		}).
		RowName(func(it fileItem) string { return it.Name }).
		RowHeight(26).Indent(14).
		BindExpanded(m.FilesOpen).
		OnToggle(func(it fileItem, open bool) {
			if open {
				m.loadDir(it.Node, it.Path, false)
			}
		}).
		BindSelected(m.FileSel).
		OnSelect(func(it fileItem) {
			switch {
			case it.Stub:
			case it.Dir:
				m.toggleDir(it)
			default:
				m.openPreview(it)
			}
		}).
		Name(T("Files"))

	header := panelHeader(
		icon("folder-open").Size(15),
		ggui.Expanded(ggui.Row(
			ggui.Text(projectName(r.Path)).Size(13).Weight(ggui.WeightMedium).NoWrap(),
			ggui.Expanded(ggui.Text(prettyPath(r.Path)).Size(11.5).Color(tokExtraMuted).NoWrap().Ellipsis()),
		).Gap(8).Align(ggui.AlignBaseline)),
		ggui.Row(
			ggui.View(ggui.Map(m.Prefs, func(p prefs) bool { return p.ShowHidden }), func(shown bool) ggui.Widget {
				if shown {
					return iconButton("eye", T("Hide hidden files"), m.toggleHidden)
				}
				return iconButton("eye-off", T("Show hidden files"), m.toggleHidden)
			}),
			iconButton("refresh-cw", T("Refresh"), m.refreshFiles),
		).Gap(2),
	)
	hasPreview := ggui.Map(m.Preview, func(p filePreview) bool { return p.Key != "" })
	return treeOverPane(header, ggui.Scroll(ggui.Padding(tree, 4, 4)).Bar(tokSurface3),
		hasPreview, 0.55, func() ggui.Widget { return previewPane(m) }, T("Preview divider"))
}

func previewPane(m *model) ggui.Widget {
	head := ggui.View(m.Preview, func(p filePreview) ggui.Widget {
		path := p.Path
		info := ""
		switch {
		case p.Loading:
			info = T("Reading…")
		case p.Img != nil:
			b := p.Img.Bounds()
			info = fmt.Sprintf("%d × %d · %s", b.Dx(), b.Dy(), byteSize(p.File.Size))
		case p.Err == "":
			info = byteSize(p.File.Size)
		}
		var enlarge ggui.Widget = ggui.Box()
		if p.Img != nil {
			enlarge = iconButton("maximize-2", T("Enlarge"), func() { m.ImageOpen.Set(true) })
		}
		return panelHeader(
			icon("file").Size(14),
			ggui.Expanded(ggui.Row(
				ggui.Box(ggui.Text(p.Name).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()).MaxWidth(170),
				ggui.Text(info).Size(11.5).Color(tokExtraMuted).NoWrap(),
			).Gap(8).Align(ggui.AlignBaseline)),
			ggui.Row(
				enlarge,
				iconButton("terminal", T("Insert path in the message"), func() { m.insertPath(path) }),
				iconButton("copy", T("Copy path"), func() { ggui.CurrentClipboard().Write(path) }),
				iconButton("x", T("Close preview"), func() { m.Preview.Set(filePreview{}); m.FileSel.Set("") }),
			).Gap(2),
		)
	})
	content := ggui.View(m.Preview, func(p filePreview) ggui.Widget {
		var note string
		switch {
		case p.Loading:
			return ggui.Center(ui.Spinner())
		case p.Err != "":
			note = p.Err
		case p.Img != nil:
			// Clicked, it shows as large as the window allows.
			img := ggui.Pointer(ggui.Image(p.Img).Alt(p.Name)).OnTap(func() { m.ImageOpen.Set(true) })
			return ggui.Padding(ggui.Center(img), 12)
		case isImage(p.Name) && p.File.Truncated:
			note = Tf("An image of %s, too large to show.", byteSize(p.File.Size))
		case p.File.Binary:
			note = Tf("A binary file, %s.", byteSize(p.File.Size))
		case len(p.File.Data) == 0:
			note = T("Empty.")
		}
		if note != "" {
			return paneNote(note)
		}
		text := strings.ReplaceAll(string(p.File.Data), "\t", "    ")
		rows := []ggui.Widget{ggui.Text(text).Style(ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 11.5}).Selectable()}
		if p.File.Truncated {
			rows = append(rows, muted(ggui.Text(Tf("Showing the first %s of %s.", byteSize(int64(len(p.File.Data))), byteSize(p.File.Size))).Size(11.5)))
		}
		return ggui.Scroll(ggui.Padding(ggui.Column(rows...).Gap(10).Stretch(), 10, 12)).Bar(tokSurface3)
	})
	return lowerPane(head, content)
}

// imageViewer shows the previewed image as large as the window allows,
// at most its own size, until a click on it or Escape closes it.
func imageViewer(m *model) ggui.Widget {
	img := ggui.Map(m.Preview, func(p filePreview) *ggfx.Image { return p.Img })
	return ggui.View(img, func(*ggfx.Image) ggui.Widget {
		p := ggui.Peek(m.Preview)
		if p.Img == nil {
			return ggui.Box()
		}
		b := p.Img.Bounds()
		content := ggui.Column(
			ggui.Pointer(ggui.Image(p.Img).Alt(p.Name)).OnTap(func() { m.ImageOpen.Set(false) }),
			muted(ggui.Text(fmt.Sprintf("%s · %d × %d", p.Name, b.Dx(), b.Dy())).Size(12)),
		).Gap(10).Align(ggui.AlignCenter)
		// The panel hugs the image, which shrinks to fit the window.
		return ui.Dialog(m.ImageOpen, ggui.Padding(content, 12)).Name(T("Image")).Compact().Width(float64(max(b.Dx(), 240) + 24))
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
			return ui.Resizable(split, tree, pane()).Vertical().MinSizes(80, 80).Name(divider)
		}).Else(func() ggui.Widget { return tree })),
	).Stretch()
}

// lowerPane frames the pane under a tree: its head line, then its content
// on the sunken surface.
func lowerPane(head, content ggui.Widget) ggui.Widget {
	return ggui.Column(hline(), head, hline(), ggui.Expanded(ggui.Box(content).Fill(tokSurface1))).Stretch()
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
