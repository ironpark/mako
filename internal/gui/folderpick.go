package gui

import (
	"context"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// The New session dialog picks a working directory on the node the
// session will run on: this machine's in its own folder dialog, another
// node's in one of mako's own laid out as the desktop's is, over the form,
// browsing that node's folders through the daemons. The folders the
// node's sessions have run in are offered in the form at a tap, and in
// the picker's sidebar.

// folderPick is the folder picker, while it shows.
type folderPick struct {
	On   *ggui.StateValue[bool]
	Node string
	Home string // the node's home folder, as it writes it
	*navigator
	Sel     *ggui.StateValue[string] // the folder chosen, by key
	Address *ggui.StateValue[string] // the path typed in the address field
	Err     *ggui.StateValue[string]
	Naming  *ggui.StateValue[bool]   // a new folder's name is being typed
	NewName *ggui.StateValue[string] // and the name
	taps    taps
}

func newFolderPick(m *model) *folderPick {
	p := &folderPick{
		On:      ggui.State(false),
		Sel:     ggui.State(""),
		Address: ggui.State(""),
		Err:     ggui.State(""),
		Naming:  ggui.State(false),
		NewName: ggui.State(""),
	}
	p.navigator = newNavigator("", func(dir string) {
		p.Sel.Set("")
		p.Err.Set("")
		p.Address.Set(dir)
		m.loadDir(p.Node, dir, true)
	})
	return p
}

// pickFolder shows the picker over the New session dialog, for the node
// the form names, at the folder it names, else the node's home.
func (m *model) pickFolder() {
	f := ggui.Peek(m.Form)
	if err := m.canTransfer(f.NodeID); err != nil { // its ListDir does not read ~
		m.FormErr.Set(nodeErr(err))
		return
	}
	m.resolveHome(f.NodeID, "~", func(home string) {
		p := m.Pick
		p.Node, p.Home = f.NodeID, home
		p.Naming.Set(false)
		m.loadDir(p.Node, home, false) // for the sidebar's places in it
		start := strings.TrimSpace(f.Project)
		if !isAbsPath(start) {
			start = home
		}
		p.reset(start)
		m.NewOpen.Set(false)
		p.On.Set(true)
	}, m.FormErr.Set)
}

// isAbsPath reports whether p is absolute on a node of either kind.
func isAbsPath(p string) bool {
	return strings.HasPrefix(p, "/") || len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// closePicker goes back to the form, with dir in it unless it is "".
func (m *model) closePicker(dir string) {
	if dir != "" {
		m.Form.Update(func(f newSession) newSession { f.Project = dir; return f })
	}
	m.Pick.On.Set(false)
}

// picked is the folder the picker would choose: the one selected, else
// the one shown.
func (p *folderPick) picked() string {
	if sel := p.Sel.Get(); sel != "" {
		_, dir := splitKey(sel)
		return dir
	}
	return p.Path.Get()
}

// makeFolder makes the folder named in the picker's field, in the folder
// shown, and goes into it.
func (m *model) makeFolder() {
	p := m.Pick
	name := strings.TrimSpace(ggui.Peek(p.NewName))
	if why := validName(name); why != "" {
		p.Err.Set(why)
		return
	}
	dir := joinPath(ggui.Peek(p.Path), name)
	req := &protocol.FileOp{NodeId: m.remoteID(p.Node), Op: protocol.FileMkdir, Path: dir}
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.File, error) {
		return c.FileOp(ctx, req)
	}, func(_ *protocol.File, err error) {
		if err != nil {
			p.Err.Set(nodeErr(err))
			return
		}
		p.Naming.Set(false)
		p.goTo(dir)
	})
}

// place is a folder the picker's sidebar offers.
type place struct {
	Icon, Name, Path string
}

// places are the sidebar's sections: the home folder and the usual ones
// in it, the folders sessions ran in, and the root of the node's disk.
func (m *model) places(p *folderPick) (favorites, recent, locations []place) {
	favorites = []place{{"house", baseName(p.Home), p.Home}}
	have := map[string]bool{}
	for _, e := range m.Dirs.Get()[dirKey(p.Node, p.Home)].Entries {
		have[e.GetName()] = e.GetDir()
	}
	for _, name := range []string{"Desktop", "Documents", "Downloads", "Projects"} {
		if have[name] {
			favorites = append(favorites, place{"folder", name, joinPath(p.Home, name)})
		}
	}
	for _, dir := range m.recentFolders(p.Node, 5) {
		recent = append(recent, place{"folder", baseName(dir), dir})
	}
	root := "/"
	if len(p.Home) >= 3 && p.Home[1] == ':' {
		root = p.Home[:3]
	}
	locations = []place{{"hard-drive", nodeName(m.Nodes.Get(), p.Node), root}}
	return favorites, recent, locations
}

// folderPickerDialog is the picker, over the window.
func folderPickerDialog(m *model) ggui.Widget {
	p := m.Pick
	// Closed by its buttons, Escape or a tap outside, it goes back to the
	// form.
	was := false
	ggui.Watch(p.On, func(on bool) {
		if was && !on {
			m.NewOpen.Set(true)
		}
		was = on
	})
	return ggui.If(p.On, func() ggui.Widget {
		return ui.Dialog(p.On, folderPicker(m)).
			Title(Tf("Choose a folder on %s", nodeName(ggui.Peek(m.Nodes), p.Node))).Width(760)
	})
}

func folderPicker(m *model) ggui.Widget {
	p := m.Pick
	toolbar := ggui.Row(
		navButtons(p.navigator),
		ggui.Expanded(ui.TextField(p.Address).Name(T("Folder")).OnSubmit(func(dir string) {
			p.goTo(strings.TrimSpace(dir))
		})),
		hiddenFilesToggle(m),
	).Gap(6)
	body := ggui.Row(
		ggui.Box(ggui.Scroll(pickerSidebar(m))).Width(170),
		vline(),
		ggui.Expanded(pickerList(m)),
	).Stretch()
	hasErr := ggui.Map(p.Err, func(s string) bool { return s != "" })
	return ggui.Column(
		toolbar,
		ggui.Box(body).Height(340).Border(1, tokBorder).Radius(8),
		ggui.If(hasErr, func() ggui.Widget { return ggui.TextOf(p.Err).Size(12).Color(tokDotDown) }),
		pickerFooter(m),
	).Gap(12).Stretch()
}

// pickerSidebar is the places, in sections, the folder shown lit.
func pickerSidebar(m *model) ggui.Widget {
	p := m.Pick
	return ggui.Reactive(func() ggui.Widget {
		favorites, recent, locations := m.places(p)
		cur := p.Path.Get()
		var rows []ggui.Widget
		section := func(title string, list []place) {
			if len(list) == 0 {
				return
			}
			rows = append(rows, ggui.Box(ggui.Text(title).Size(11).Weight(ggui.WeightMedium).Color(tokExtraMuted)).Pad(10, 10, 4, 10))
			for _, pl := range list {
				lead := icon(pl.Icon).Size(14).Color(tokMuted)
				var row ggui.Widget = ggui.Row(lead, ggui.Expanded(ggui.Text(pl.Name).Size(12.5).NoWrap().Ellipsis())).Gap(8)
				btn := ui.ButtonOf(row, func() { p.goTo(pl.Path) }).Ghost().Pad(5, 10).Radius(6).Name(pl.Path)
				if pl.Path == cur {
					rows = append(rows, ggui.Box(btn).Fill(tokSurface3).Radius(6))
					continue
				}
				rows = append(rows, btn)
			}
		}
		section(T("Favorites"), favorites)
		section(T("Recent"), recent)
		section(T("Locations"), locations)
		return ggui.Box(ggui.Column(rows...).Gap(1).Stretch()).Pad(4, 6, 8, 6)
	})
}

// pickerList is the folder shown: its folders, which open on a second
// tap, and its files, dimmed, which cannot be chosen.
func pickerList(m *model) ggui.Widget {
	p := m.Pick
	rows := ggui.Derived(func() []fileItem { return m.children(dirItem(p.Node, p.Path.Get())) })
	tree := ui.Tree(rows, func(it fileItem) string { return it.Key }, func(fileItem) []fileItem { return nil },
		func(item ggui.Readable[fileItem]) ggui.Widget {
			it := ggui.Untrack(item.Get)
			if it.Stub {
				return muted(ggui.Text(it.Name).Size(13).NoWrap().Ellipsis())
			}
			name := ggui.Text(it.Name).Size(13).NoWrap().Ellipsis()
			lead := fileTypeIcon(it.Name, it.Dir, false).Size(15)
			mod := ""
			if !it.Mod.IsZero() {
				mod = it.Mod.Local().Format("2006-01-02 15:04")
			}
			modText := ggui.Text(mod).Size(12).Color(tokMuted).NoWrap()
			if !it.Dir {
				name, modText = name.Color(tokExtraMuted), modText.Color(tokExtraMuted)
				lead = lead.Color(tokExtraMuted)
			}
			return ggui.Row(
				ggui.Expanded(ggui.Row(lead, ggui.Expanded(name)).Gap(8)),
				ggui.Box(modText).Width(modColumn),
			).Gap(12)
		}).
		RowName(func(it fileItem) string { return it.Name }).
		RowHeight(26).Chevrons(false).
		BindSelected(p.Sel).
		OnSelect(func(it fileItem) {
			switch {
			case it.Stub:
			case !it.Dir: // a file is not a working directory
				p.Sel.Set("")
			case p.taps.again(it.Key):
				p.goTo(it.Path)
			}
		}).
		Name(T("Folders"))
	head := ggui.Box(ggui.Row(
		ggui.Expanded(columnHead(T("Name"), 0)),
		ggui.Box(columnHead(T("Modified"), 0)).Width(modColumn),
	).Gap(12)).Pad(6, 14)
	return ggui.Column(head, hline(), ggui.Expanded(ggui.Scroll(ggui.Padding(tree, 4, 6)).Bar(tokSurface3))).Stretch()
}

// pickerFooter is New Folder on the left, and Cancel and Choose on the
// right, as the desktop's dialogs have them; while a new folder is named,
// its field takes the left.
func pickerFooter(m *model) ggui.Widget {
	p := m.Pick
	left := ggui.View(p.Naming, func(naming bool) ggui.Widget {
		if !naming {
			return ui.Button(T("New folder"), func() { p.NewName.Set(""); p.Err.Set(""); p.Naming.Set(true) }).Outline()
		}
		return ggui.Row(
			ggui.Box(ui.TextField(p.NewName).Placeholder(T("Folder name")).Name(T("Folder name")).
				OnSubmit(func(string) { m.makeFolder() })).Width(200),
			ui.Button(T("Create"), m.makeFolder).Outline(),
			iconButton("x", T("Cancel"), func() { p.Naming.Set(false) }),
		).Gap(6)
	})
	chosen := ggui.Derived(func() string { return baseName(p.picked()) })
	return ggui.Row(
		left,
		ggui.Spacer(),
		ui.Button(T("Cancel"), func() { m.closePicker("") }).Outline(),
		ui.ButtonOf(ggui.TextOf(ggui.Map(chosen, func(name string) string { return Tf("Choose %s", name) })).Size(13).NoWrap(),
			func() { m.closePicker(ggui.Untrack(p.picked)) }).Name(T("Choose")),
	).Gap(8)
}

// recentFolders are the folders the node's sessions have run in, most
// recent first, at most max of them.
func (m *model) recentFolders(node string, max int) []string {
	var out []string
	projects := m.Prefs.Get().Projects
	for i := len(projects) - 1; i >= 0 && len(out) < max; i-- {
		if r := projects[i]; r.Node == node && !slices.Contains(out, r.Path) {
			out = append(out, r.Path)
		}
	}
	return out
}

// recentChips offers the folders the form's node has run sessions in.
func recentChips(m *model) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		f := m.Form.Get()
		recent := m.recentFolders(f.NodeID, 6)
		if len(recent) == 0 {
			return ggui.Box()
		}
		chips := make([]ggui.Widget, 0, len(recent))
		for _, dir := range recent {
			text := ggui.Text(path.Base(strings.ReplaceAll(dir, `\`, "/"))).Size(12).NoWrap()
			if dir == f.Project {
				text = text.Weight(ggui.WeightMedium)
			}
			chips = append(chips, ui.ButtonOf(text, func() {
				m.Form.Update(func(f newSession) newSession { f.Project = dir; return f })
			}).Secondary().Pad(3, 9).Radius(12).Name(dir))
		}
		return ggui.Wrap(chips...).Gap(6)
	})
}
