package gui

import (
	"context"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/ui/icons"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// A file opened from the Files tab shows as a tab of its own, beside the
// terminals and arranged into panes as they are: the whole file, read
// through its node's daemon, in an editor that saves it back with ⌘S.
//
// A file tab's ID is docPrefix, the node and the path; anything else in
// m.Tabs is a session.

const docPrefix = "file\x00"

// docTabID is the tab of a file on a node.
func docTabID(node, path string) string { return docPrefix + dirKey(node, path) }

// isDocTab reports whether a tab shows a file rather than a session.
func isDocTab(id string) bool { return strings.HasPrefix(id, docPrefix) }

// docEditMax is the largest file opened for editing; a larger one shows
// its start, read-only.
const docEditMax = 4 << 20

// docInfo is what the window shows of an open file: the tab, the header
// and the banner over the editor.
type docInfo struct {
	Node, Path string
	Root       fileRoot // the project it was opened from, which the panels keep showing
	Loading    bool
	Err        string // why it could not be read
	SaveErr    string // why the last save failed
	Changed    bool   // the last save found it changed on disk since it was read
	Binary     bool
	Truncated  bool
	Size       int64
	Dirty      bool // edited since it was read or saved
	Saving     bool
}

// Name is the file's name.
func (d docInfo) Name() string { return baseName(d.Path) }

// ReadOnly reports whether the file is shown but not edited.
func (d docInfo) ReadOnly() bool { return d.Truncated || d.Binary }

// document is an open file's text, kept on the window's goroutine.
type document struct {
	text  *ggui.StateValue[string]
	saved string    // the text as last read or saved
	mod   time.Time // the file's ModTime then
	// editor is kept across the tab being hidden and shown, so its caret,
	// selection, scroll and undo stay.
	editor *codeEditor
	crlf   bool // its lines end in CR LF, as read
	// preview shows a Markdown file rendered rather than its source.
	preview *ggui.StateValue[bool]
	images  map[string]*ggui.StateValue[mdImage] // what its preview shows, by path
	line    int                                  // the line to put the caret on once read, 0 for none
}

func (m *model) doc(id string) (docInfo, bool) {
	d, ok := ggui.Peek(m.Docs)[id]
	return d, ok
}

func (m *model) updateDoc(id string, fn func(*docInfo)) {
	docs := ggui.Peek(m.Docs)
	d, ok := docs[id]
	if !ok {
		return
	}
	fn(&d)
	next := maps.Clone(docs)
	next[id] = d
	m.Docs.Set(next)
}

// openFile shows a file in a tab, reading it the first time.
func (m *model) openFile(it fileItem) {
	m.showTab(m.ensureDoc(it, ggui.Untrack(m.filesRoot)))
}

// ensureDoc opens a file for a tab, under the project root, reading it
// unless it is open already, edits and all, and returns the tab's ID.
func (m *model) ensureDoc(it fileItem, root fileRoot) string {
	id := docTabID(it.Node, it.Path)
	if _, ok := m.doc(id); ok {
		return id
	}
	next := maps.Clone(ggui.Peek(m.Docs))
	next[id] = docInfo{Node: it.Node, Path: it.Path, Root: root, Loading: true}
	m.Docs.Set(next)
	m.docs[id] = &document{text: ggui.State(""), preview: ggui.State(false)}
	m.readDoc(id)
	return id
}

// openFileAt opens a file with its caret at a line, from 1.
func (m *model) openFileAt(it fileItem, line int) {
	m.openFile(it)
	if doc := m.docs[docTabID(it.Node, it.Path)]; doc != nil {
		doc.line = line
		m.goToLine(docTabID(it.Node, it.Path))
	}
}

// goToLine moves a file's caret to the line asked for, once its text is
// read and its editor made.
func (m *model) goToLine(id string) {
	doc := m.docs[id]
	if doc == nil || doc.line == 0 || doc.editor == nil || ggui.Peek(m.Docs)[id].Loading {
		return
	}
	doc.editor.goToLine(doc.line)
	doc.line = 0
}

// readDoc reads an open file from its node, replacing what the editor
// holds.
func (m *model) readDoc(id string) {
	d, ok := m.doc(id)
	if !ok {
		return
	}
	req := &protocol.ReadFile{NodeId: m.remoteID(d.Node), Path: d.Path, Max: docEditMax}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (*protocol.File, error) {
		return c.ReadFile(ctx, req)
	}, func(f *protocol.File, err error) {
		doc := m.docs[id]
		if doc == nil {
			return // closed meanwhile
		}
		if err == nil && !f.GetBinary() {
			doc.saved, doc.mod = string(f.GetData()), protocol.TimeOf(f.GetModTime())
			doc.crlf = strings.Contains(doc.saved, "\r\n")
			doc.text.Set(doc.saved)
			if doc.editor != nil {
				m.syncEditor(id)
			}
		}
		m.updateDoc(id, func(d *docInfo) {
			*d = docInfo{Node: d.Node, Path: d.Path, Root: d.Root, Binary: f.GetBinary(), Truncated: f.GetTruncated(), Size: f.GetSize()}
			if err != nil {
				d.Err = err.Error()
			}
		})
		m.goToLine(id)
	})
}

// docEdited follows an edit, marking the file dirty or clean again.
func (m *model) docEdited(id, text string) {
	doc := m.docs[id]
	if doc == nil {
		return
	}
	dirty := text != doc.saved
	if d, ok := m.doc(id); ok && d.Dirty != dirty {
		m.updateDoc(id, func(d *docInfo) { d.Dirty = dirty })
	}
}

// saveDoc writes the editor's text back to the file. Unless force is set,
// a file changed on disk since it was read is left alone and the banner
// asks whether to overwrite it.
func (m *model) saveDoc(id string, force bool) {
	d, ok := m.doc(id)
	doc := m.docs[id]
	if !ok || doc == nil || d.Saving || d.Loading || d.ReadOnly() || d.Err != "" {
		return
	}
	text := ggui.Peek(doc.text)
	req := &protocol.WriteFile{NodeId: m.remoteID(d.Node), Path: d.Path, Data: []byte(text), ModTime: protocol.Time(doc.mod)}
	if force {
		req.ModTime = nil
	}
	m.updateDoc(id, func(d *docInfo) { d.Saving = true })
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (*protocol.File, error) {
		return c.WriteFile(ctx, req)
	}, func(f *protocol.File, err error) {
		doc := m.docs[id]
		if doc == nil {
			return
		}
		if err == nil {
			doc.saved, doc.mod = text, protocol.TimeOf(f.GetModTime())
		}
		dirty := ggui.Peek(doc.text) != doc.saved
		m.updateDoc(id, func(d *docInfo) {
			d.Saving, d.Dirty, d.SaveErr, d.Changed = false, dirty, "", false
			d.Size = f.GetSize()
			switch {
			case protocol.HasCode(err, protocol.CodeChanged):
				d.Changed = true
			case err != nil:
				d.SaveErr = err.Error()
			}
		})
	})
}

// askCloseDoc closes a file's tab, asking first when it has unsaved
// changes.
func (m *model) askCloseDoc(id string) {
	if d, ok := m.doc(id); ok && d.Dirty {
		m.confirmDiscard = id
		m.DiscardOpen.Set(true)
		return
	}
	m.closeDoc(id)
}

func (m *model) confirmedDiscard() {
	if id := m.confirmDiscard; id != "" {
		m.confirmDiscard = ""
		m.closeDoc(id)
	}
}

// closeDoc closes a file's tab and forgets its text.
func (m *model) closeDoc(id string) {
	m.closeTab(id)
	delete(m.docs, id)
	next := maps.Clone(ggui.Peek(m.Docs))
	delete(next, id)
	m.Docs.Set(next)
}

// saveSelected saves the file the current pane shows, as ⌘S does.
func (m *model) saveSelected() {
	if id := ggui.Peek(m.Selected); isDocTab(id) {
		m.saveDoc(id, false)
	}
}

// dirtyDocs are the open files with unsaved changes.
func (m *model) dirtyDocs() []string {
	var out []string
	for id, d := range ggui.Peek(m.Docs) {
		if d.Dirty {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// View --------------------------------------------------------------------------

// docTab is a file's tab: its icon, its name, a dot while it has unsaved
// changes, which the close button becomes on hover, as editors show it.
func docTab(m *model, id string, d docInfo, selected, focused bool) ggui.Widget {
	fg := tokMuted
	if selected {
		fg = tokFg
	}
	name := d.Name()
	label := name
	if d.Dirty {
		label = name + " ●"
	}
	content := ggui.Box(ggui.Row(
		icon(fileIconName(name)).Size(14).Color(fg),
		ggui.Box(ggui.Text(label).Size(13).Color(fg).NoWrap().Ellipsis()).MaxWidth(170),
		ui.ButtonOf(icon("x").Size(13), func() { m.askCloseDoc(id) }).Ghost().Pad(3).Radius(4).Name(Tf("Close %s", name)),
	).Gap(8)).Height(tabBarHeight)
	btn := ui.ButtonOf(content, func() { m.showTab(id) }).Ghost().Pad(0, 8, 0, 12).Radius(0).Name(name)
	return tabCell(btn, selected, focused)
}

// docView is a file's tab content: a line about the file and the caret,
// a banner while it reads, or when it cannot be edited or saved, and the
// editor, or for Markdown the page it renders.
func docView(m *model, id string, focus bool, onPress func()) ggui.Widget {
	doc := m.docs[id]
	info := ggui.Derived(func() docInfo { return m.Docs.Get()[id] })
	state := ggui.Derived(func() string {
		d := info.Get()
		switch {
		case d.Loading:
			return "loading"
		case d.Err != "":
			return "error"
		case d.Binary:
			return "binary"
		case doc.preview.Get() && isMarkdown(d.Name()):
			return "preview"
		}
		return "text"
	})
	body := ggui.Key(state, func(s string) ggui.Widget {
		d := ggui.Peek(m.Docs)[id]
		switch s {
		case "loading":
			return ggui.Center(ui.Spinner())
		case "error":
			return paneNote(d.Err)
		case "binary":
			return paneNote(Tf("A binary file, %s.", byteSize(d.Size)))
		case "preview":
			return ggui.View(doc.text, func(text string) ggui.Widget { return markdownView(m, id, text) })
		}
		e := m.docEditor(id)
		e.ReadOnly(d.ReadOnly()).AutoFocus(focus).OnPress(onPress)
		return e
	})
	banner := ggui.Reactive(func() ggui.Widget {
		d := info.Get()
		switch {
		case d.Changed:
			return docBanner(T("The file changed on disk since it was opened."),
				ui.Button(T("Overwrite"), func() { m.saveDoc(id, true) }).Outline(),
				ui.Button(T("Reload"), func() { m.readDoc(id) }).Outline())
		case d.SaveErr != "":
			return docBanner(Tf("Could not save: %s", d.SaveErr), ui.Button(T("Retry"), func() { m.saveDoc(id, false) }).Outline())
		case d.Truncated:
			return docBanner(Tf("Showing the first %s of %s, read-only.", byteSize(int64(len(doc.saved))), byteSize(d.Size)))
		}
		return ggui.Box()
	})
	return ggui.Column(docStatus(m, id, info, state), hline(), banner, ggui.Expanded(body)).Stretch()
}

// docEditor is a file's editor, made the first time it shows and kept.
func (m *model) docEditor(id string) *codeEditor {
	doc := m.docs[id]
	if doc.editor == nil {
		doc.editor = newCodeEditor(doc.text, func() prefs { return ggui.Peek(m.Prefs) }).
			Colors(m.docColors(id)).
			OnChange(func(text string) { m.docEdited(id, text) })
		m.syncEditor(id)
		m.goToLine(id)
	}
	return doc.editor
}

// syncEditor brings a file's editor up to the text read: its language's
// comment and tabs, and the caret's line for the status line.
func (m *model) syncEditor(id string) {
	doc := m.docs[id]
	if l, ok := m.hl.languageFor(ggui.Peek(m.Docs)[id].Path, ggui.Peek(doc.text)); ok {
		doc.editor.Comment(l.Comment).TabSize(l.TabSize)
	}
	doc.editor.publish()
}

// docStatus is the line under a file's tab: its size and lines, and its
// language, line endings and where the caret is; for Markdown, the
// switch between its source and its page. What does not fit a narrow
// pane is left out, the line endings first and the caret last.
func docStatus(m *model, id string, info ggui.Readable[docInfo], state ggui.Readable[string]) ggui.Widget {
	doc := m.docs[id]
	label := func(s string) ggui.Widget { return muted(ggui.Text(s).Size(12).NoWrap()) }
	// shown is an item for the states it shows in.
	shown := func(fn func() string, states ...string) ggui.Widget {
		return ggui.Reactive(func() ggui.Widget {
			if !slices.Contains(states, state.Get()) {
				return ggui.Box()
			}
			return label(fn())
		})
	}
	size := shown(func() string {
		if d := info.Get(); d.Binary {
			return byteSize(d.Size)
		}
		return byteSize(int64(len(doc.text.Get())))
	}, "text", "preview", "binary")
	editor := m.docEditor(id)
	lines := shown(func() string { return Tf("%d lines", editor.Caret.Get().Lines) }, "text", "preview")
	lang := shown(func() string {
		m.Extensions.Get() // the language may come from one installed later
		if l, ok := m.hl.languageFor(info.Get().Path, ggui.Peek(doc.text)); ok {
			return l.Name
		}
		return ""
	}, "text")
	ending := shown(func() string {
		info.Get() // read again with the file
		if doc.crlf {
			return "CRLF"
		}
		return "LF"
	}, "text")
	caret := shown(func() string {
		c := editor.Caret.Get()
		pos := Tf("Ln %d, Col %d", c.Line, c.Col)
		if c.Selected > 0 {
			pos += " " + Tf("(%d selected)", c.Selected)
		}
		return pos
	}, "text")
	row := &statusRow{gap: 14, split: 2, items: []ggui.Widget{size, lines, lang, ending, caret}, drop: []int{3, 2, 1, 0, 4}}
	if isMarkdown(ggui.Peek(m.Docs)[id].Name()) {
		row.items = append(row.items, ui.ToggleGroup[bool](doc.preview).Options([]bool{false, true}).
			Format(func(on bool) string { return T(pick(on, "Preview", "Edit")) }).
			Icon(func(on bool) *icons.Widget { return icon(pick(on, "eye", "pencil")).Size(13) }).
			Name(T("Markdown view")))
	}
	return ggui.Box(row).Pad(0, 8, 0, 14).Height(statusHeight).Fill(tokSurface0)
}

// statusRow lays out a status line: its items before split at the left,
// the rest at the right, centred in its height, and while they do not
// fit, leaves out items in the order drop gives.
type statusRow struct {
	items []ggui.Widget
	split int
	drop  []int
	gap   float64

	sizes []ggui.Size
	shown []bool
}

func (r *statusRow) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	r.sizes = make([]ggui.Size, len(r.items))
	r.shown = make([]bool, len(r.items))
	total := 0.0
	count := 0
	for i, it := range r.items {
		r.sizes[i] = it.Layout(ggui.Loose(ggui.Sz(math.Inf(1), c.MaxH)), env)
		if r.sizes[i].W > 0 {
			r.shown[i] = true
			total += r.sizes[i].W
			count++
		}
	}
	width := func() float64 { return total + r.gap*float64(max(count-1, 0)) + r.gap }
	for _, i := range r.drop {
		if width() <= c.MaxW {
			break
		}
		if r.shown[i] {
			r.shown[i] = false
			total -= r.sizes[i].W
			count--
		}
	}
	return c.Constrain(ggui.Sz(c.MaxW, c.MaxH))
}

func (r *statusRow) Paint(dst *ggui.Canvas, rect ggui.Rect) {
	place := func(i int, x float64) {
		s := r.sizes[i]
		dst.Paint(r.items[i], ggui.Rct(ggui.Pt(x, rect.Origin.Y+(rect.Size.H-s.H)/2), s))
	}
	x := rect.Origin.X
	for i := 0; i < r.split; i++ {
		if r.shown[i] {
			place(i, x)
			x += r.sizes[i].W + r.gap
		}
	}
	x = rect.Origin.X + rect.Size.W
	for i := len(r.items) - 1; i >= r.split; i-- {
		if r.shown[i] {
			x -= r.sizes[i].W
			place(i, x)
			x -= r.gap
		}
	}
}

// statusHeight is the height of the line under a file's tab.
const statusHeight = 30

// togglePreview flips a Markdown file between its source and its page.
func (m *model) togglePreview(id string) {
	if doc := m.docs[id]; doc != nil && isMarkdown(ggui.Peek(m.Docs)[id].Name()) {
		ggui.Toggle(doc.preview)
	}
}

// toggleSelectedPreview is togglePreview for the tab shown.
func (m *model) toggleSelectedPreview() { m.togglePreview(ggui.Peek(m.Selected)) }

// previewLabel names what togglePreview does to a file now.
func previewLabel(doc *document) string {
	if ggui.Peek(doc.preview) {
		return "Edit Markdown source"
	}
	return "Show Markdown preview"
}

func docBanner(msg string, actions ...ggui.Widget) ggui.Widget {
	row := append([]ggui.Widget{icon("info").Size(14), ggui.Expanded(ggui.Text(msg).Size(12.5))}, actions...)
	return ggui.Column(
		ggui.Box(ggui.Row(row...).Gap(10)).Pad(6, 12).Fill(tokSurface2),
		hline(),
	).Stretch()
}

// docHeader is the workspace header over a file: its name and where it
// is, and its actions.
func docHeader(m *model, id string, d docInfo) ggui.Widget {
	where := nodeName(ggui.Peek(m.Nodes), d.Node) + ":" + prettyPath(d.Path)
	state := ""
	switch {
	case d.Saving:
		state = T("Saving…")
	case d.Dirty:
		state = T("Edited")
	}
	items := []ggui.Widget{ui.MenuItem(T("Save"), func() { m.saveDoc(id, false) }).Shortcut("cmd+s")}
	if doc := m.docs[id]; doc != nil && isMarkdown(d.Name()) {
		items = append(items, ui.MenuItem(T(previewLabel(doc)), func() { m.togglePreview(id) }).Shortcut("cmd+shift+v"))
	}
	items = append(items,
		ui.MenuItem(T("Reload from disk"), func() { m.readDoc(id) }),
		ui.MenuItem(T("Copy path"), func() { ggui.CurrentClipboard().Write(d.Path) }),
		ui.MenuItem(T("Insert path in the message"), func() { m.insertPath("", d.Path) }),
	)
	items = append(items, m.editorItems(d.Node, d.Path, func() int { return m.docLine(id) }, nil)...)
	items = append(items, ui.MenuDivider(), ui.MenuItem(T("Close file"), func() { m.askCloseDoc(id) }))
	return ggui.Row(
		ggui.Text(d.Name()).Size(14).Weight(ggui.WeightMedium).NoWrap(),
		ggui.Expanded(muted(ggui.Text(where).Size(13).NoWrap().Ellipsis())),
		muted(ggui.Text(state).Size(12.5).NoWrap()),
		iconMenu(ui.MenuOf(icon("ellipsis"), items...).Name(T("File actions")), 6),
	).Gap(10)
}

// docLine is the line the caret is on in a file's tab, 0 before its
// editor is made.
func (m *model) docLine(id string) int {
	if doc := m.docs[id]; doc != nil && doc.editor != nil {
		return ggui.Peek(doc.editor.Caret).Line
	}
	return 0
}

// docColors is what colours a file's text: its language's highlighting,
// found again when the extensions installed change.
func (m *model) docColors(id string) func(string, palette) []styleSpan {
	var ds *docSyntax
	gen := -1
	return func(text string, pal palette) []styleSpan {
		if gen != m.langGen {
			gen = m.langGen
			d, _ := m.doc(id)
			l, ok := m.hl.languageFor(d.Path, text)
			switch {
			case !ok:
				ds = nil
			case ds == nil || ds.lang.Key != l.Key:
				ds = m.syntaxFor(l)
			}
		}
		if ds == nil {
			return nil
		}
		return ds.colors(text, pal)
	}
}
