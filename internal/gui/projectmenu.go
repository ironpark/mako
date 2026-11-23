package gui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/protocol"
)

// A project section's menu, from its heading's ⋯ or a right click on it:
// naming it, adding a folder to it, merging it into another, taking its
// folders apart again, and the spaces it shows in. Its dialogs ask for a
// name, for a folder on a node, and for the project to merge into.

// projectAsk is the state of the project dialogs: the section each is
// about, and what is typed in them.
type projectAsk struct {
	section sessionGroup // the section the dialog open is about

	NameOpen *ggui.StateValue[bool]
	Name     *ggui.StateValue[string]
	NameErr  *ggui.StateValue[string]

	AddOpen *ggui.StateValue[bool]
	AddNode *ggui.StateValue[string]
	AddPath *ggui.StateValue[string]
	AddErr  *ggui.StateValue[string]

	MergeOpen  *ggui.StateValue[bool]
	MergeQuery *ggui.StateValue[string]
	merge      *picker
	targets    []sessionGroup // what the section can merge into, as the picker opened

	// SettingsOpen edits a copy of the project, Draft, until it is saved.
	SettingsOpen *ggui.StateValue[bool]
	Draft        *ggui.StateValue[project]

	// Import scans a folder for git repositories to add to the sidebar.
	Import *importAsk
}

func newProjectAsk() *projectAsk {
	return &projectAsk{
		NameOpen: ggui.State(false), Name: ggui.State(""), NameErr: ggui.State(""),
		AddOpen: ggui.State(false), AddNode: ggui.State(""), AddPath: ggui.State(""), AddErr: ggui.State(""),
		MergeOpen: ggui.State(false), MergeQuery: ggui.State(""),
		SettingsOpen: ggui.State(false),
		Draft:        ggui.State(project{}).WithEqual(deepEqual[project]),
		Import:       newImportAsk(),
	}
}

// projectMenu is a project section's menu entries.
func projectMenu(m *model, g sessionGroup, name string) []ggui.Widget {
	own := g.Def != nil
	items := []ggui.Widget{
		ui.MenuItem(T("Project settings…"), func() { m.openProjectSettings(g) }),
		ui.MenuItem(T("Rename project…"), func() { m.askProjectName(g) }),
		ui.MenuItem(T("Add folder…"), func() { m.askAddFolder(g) }),
		ui.MenuItem(T("Merge into another project…"), func() { m.askMerge(g) }),
	}
	if own && len(g.Def.Roots) > 1 {
		items = append(items, ui.MenuDivider())
		for i, r := range g.Def.Roots {
			items = append(items, ui.MenuItem(Tf("Take %s out of the project", r.label()), func() { m.removeRoot(g, i) }))
		}
	}
	if own {
		items = append(items, ui.MenuItem(T("Ungroup folders"), func() { m.ungroup(g) }))
	}
	// A section with no sessions has nothing to bring it back once it
	// leaves, so its menu can drop it.
	if len(g.Sessions) == 0 {
		roots := g.Roots
		items = append(items, ui.MenuItem(T("Remove from sidebar"), func() {
			if own {
				m.ungroup(g)
			}
			m.forgetProjects(roots)
		}))
	}
	return append(items, projectSpaceItems(m, g, name)...)
}

// startItems are the folders a section can start a session in, each on
// its node, for its heading's chevron: a project's by root, a
// repository's clones by node. A folder on a node that is not reachable
// is offered still, and says so.
func startItems(m *model, g sessionGroup, nodes []*protocol.Node) []ggui.Widget {
	var items []ggui.Widget
	add := func(label string, at fileRoot) {
		if n, up := nodeIn(nodes, at.Node); !up || !reachable(n) {
			label = Tf("%s (not reachable)", label)
		}
		items = append(items, ui.MenuItem(label, func() { m.openNew(at.Node, at.Path) }).Checked(at.Node == g.NodeID && at.Path == g.Project))
	}
	if g.Def == nil {
		for _, r := range g.Roots {
			add(Tf("On %s, in %s", nodeName(nodes, r.Node), tail(r.Path, 2)), r)
		}
		return items
	}
	for _, r := range g.Def.Roots {
		for _, l := range r.Locations {
			add(Tf("%s on %s", r.label(), nodeName(nodes, l.Node)), l)
		}
	}
	return items
}

// Spaces ----------------------------------------------------------------------

// A section is in one space: the first that it was moved to, else the
// first holding an owner of its repositories, as a new project is placed,
// else the default space. The space of every session shows every one.

// sectionReason is why a section is in the space it is.
type sectionReason int

const (
	byDefault    sectionReason = iota // no other space places it
	byProject                         // it was moved there
	shownByOwner                      // an owner of its repositories is the space's
)

// defaultSpace is the index of the space a section goes to that nothing
// else places: the one chosen, else the first; allSpace with none.
func (p spaceSet) defaultSpace() int {
	if len(p.Spaces) == 0 {
		return allSpace
	}
	return max(slices.IndexFunc(p.Spaces, func(sp space) bool { return sp.ID == p.Default }), 0) + 1
}

// placeOf is the index of the space the section of ID key, whose
// repositories owners hold, is in, and why.
func (p spaceSet) placeOf(key string, owners []string) (int, sectionReason) {
	for i, sp := range p.Spaces {
		if slices.Contains(sp.Projects, key) {
			return i + 1, byProject
		}
	}
	for i, sp := range p.Spaces {
		// Hidden is what older windows left out of a space by its owners.
		if !slices.Contains(sp.Hidden, key) && slices.ContainsFunc(owners, func(o string) bool { return slices.Contains(sp.Owners, o) }) {
			return i + 1, shownByOwner
		}
	}
	return p.defaultSpace(), byDefault
}

// showsGroup reports whether space i shows section g: the space of every
// session shows every one, the others those in them.
func (p spaceSet) showsGroup(i int, g sessionGroup) bool {
	return p.showsSection(i, g.Key, groupOwners(g))
}

// showsSection is showsGroup for the section of ID key whose repositories
// owners hold.
func (p spaceSet) showsSection(i int, key string, owners []string) bool {
	if _, ok := p.spaceOf(i); !ok {
		return true
	}
	at, _ := p.placeOf(key, owners)
	return at == i
}

// spaceGroups keeps the sections space i shows.
func (p spaceSet) spaceGroups(i int, groups []sessionGroup) []sessionGroup {
	if _, ok := p.spaceOf(i); !ok {
		return groups
	}
	return slices.DeleteFunc(slices.Clone(groups), func(g sessionGroup) bool { return !p.showsGroup(i, g) })
}

// placeSections moves the sections of IDs keys into the space of ID to,
// out of any other, whatever their owners say.
func placeSections(spaces []space, to string, keys ...string) []space {
	for i := range spaces {
		sp := &spaces[i]
		sp.Projects = slices.DeleteFunc(slices.Clone(sp.Projects), func(id string) bool { return slices.Contains(keys, id) })
		sp.Hidden = slices.DeleteFunc(slices.Clone(sp.Hidden), func(id string) bool { return slices.Contains(keys, id) })
		if sp.ID == to {
			sp.Projects = append(sp.Projects, keys...)
		}
	}
	return spaces
}

// moveToSpace moves the sections of IDs keys into the space of ID to.
func (m *model) moveToSpace(to string, keys ...string) {
	m.updateSpaces(func(spaces []space) []space { return placeSections(spaces, to, keys...) })
}

// moveGroupsTo moves sections to space i, an index into the spaces after
// the first, from the sidebar, with a notice that names where they went
// and offers to put them back.
func (m *model) moveGroupsTo(i int, name string, groups ...sessionGroup) {
	before := ggui.Peek(m.Spaces).Spaces
	if i < 0 || i >= len(before) {
		return
	}
	keys := make([]string, len(groups))
	for j, g := range groups {
		keys[j] = g.Key
	}
	m.moveToSpace(before[i].ID, keys...)
	undo := func() {
		m.updateSpaces(func(spaces []space) []space {
			// Only the moved sections, as they were; the rest as it is now.
			for j := range spaces {
				spaces[j].Projects = slices.DeleteFunc(spaces[j].Projects, func(id string) bool { return slices.Contains(keys, id) })
				if k := slices.IndexFunc(before, func(sp space) bool { return sp.ID == spaces[j].ID }); k >= 0 {
					for _, id := range before[k].Projects {
						if slices.Contains(keys, id) {
							spaces[j].Projects = append(spaces[j].Projects, id)
						}
					}
					for _, id := range before[k].Hidden {
						if slices.Contains(keys, id) && !slices.Contains(spaces[j].Hidden, id) {
							spaces[j].Hidden = append(spaces[j].Hidden, id)
						}
					}
				}
			}
			return spaces
		})
	}
	m.toaster.Push(ui.Toast(Tf("Moved %s to %s", name, spaceLabel(before[i], i+1)), "").Action(T("Undo"), undo))
}

// spaceLabel is how a space at index i is named: its name, else its place.
func spaceLabel(sp space, i int) string { return cmp.Or(sp.Name, Tf("Space %d", i)) }

// moveItems are a menu's entries moving groups to each space, the one they
// are in checked: groups, from a project's menu, or an owner's.
func moveItems(m *model, name string, groups ...sessionGroup) []ggui.Widget {
	set := m.spacesNow()
	var items []ggui.Widget
	for i, sp := range set.Spaces {
		in := len(groups) > 0 && !slices.ContainsFunc(groups, func(g sessionGroup) bool { return !set.showsGroup(i+1, g) })
		items = append(items, ui.MenuItem(spaceLabel(sp, i+1), func() { m.moveGroupsTo(i, name, groups...) }).
			Icon(spaceMark(sp)).Checked(in))
	}
	return items
}

// projectSpaceItems are a project section's menu entries for the spaces:
// moving it to another, and a new space of it.
func projectSpaceItems(m *model, g sessionGroup, name string) []ggui.Widget {
	items := []ggui.Widget{ui.MenuDivider()}
	if to := moveItems(m, name, g); len(to) > 0 {
		items = append(items, ui.MenuSub(T("Move to space"), to...))
	}
	return append(items, ui.MenuItem(T("New space with this project"), func() { m.newSpaceOf(name, nil, g) }))
}

// projectLabel is how a space's settings name a section it holds by ID:
// its project's name, else its repository's or folder's.
func projectLabel(defs []project, id string) string {
	if i := projectByID(defs, id); i >= 0 {
		p := defs[i]
		if p.Name != "" {
			return p.Name
		}
		if len(p.Roots) > 0 {
			return p.Roots[0].label()
		}
	}
	id = strings.TrimPrefix(id, freePrefix)
	if remote, ok := strings.CutPrefix(id, "repo:"); ok {
		return projectName(remote)
	}
	_, path := splitKey(id)
	return projectName(path)
}

// Dialogs ---------------------------------------------------------------------

// askProjectName opens the dialog naming section g.
func (m *model) askProjectName(g sessionGroup) {
	a := m.Proj
	a.section = g
	a.Name.Set(groupName(g))
	a.NameErr.Set("")
	a.NameOpen.Set(true)
}

func (m *model) submitProjectName() {
	a := m.Proj
	g := a.section
	name := strings.TrimSpace(ggui.Peek(a.Name))
	if name == projectName(g.Project) && g.Name == "" {
		name = "" // as it was
	}
	m.renameProject(g, name)
	a.NameOpen.Set(false)
}

// askAddFolder opens the dialog adding a folder to section g, on the node
// it starts in.
func (m *model) askAddFolder(g sessionGroup) {
	a := m.Proj
	a.section = g
	a.AddNode.Set(cmp.Or(g.NodeID, ggui.Peek(m.Conn).Self.GetId()))
	a.AddPath.Set("")
	a.AddErr.Set("")
	a.AddOpen.Set(true)
}

func (m *model) submitAddFolder() {
	a := m.Proj
	node, dir := ggui.Peek(a.AddNode), strings.TrimRight(strings.TrimSpace(ggui.Peek(a.AddPath)), `/\`)
	if !isAbsPath(dir) {
		a.AddErr.Set(T("Choose a folder, by its whole path."))
		return
	}
	for _, r := range a.section.Roots {
		if r.Node == node && pathscope.Within(dir, r.Path) {
			a.AddErr.Set(Tf("%s is in the project already.", projectName(dir)))
			return
		}
	}
	m.addFolder(a.section, node, dir)
	a.AddOpen.Set(false)
}

// sections are every project section there is, whatever space shows it.
func (m *model) sections() []sessionGroup {
	l := ggui.Peek(m.Spaces)
	return groupProjects(ggui.Peek(m.Sessions), ggui.Peek(m.Nodes), ggui.Peek(m.Prefs).Projects, l.Projects, l.Names)
}

// askMerge opens the picker of the section to merge section g into.
func (m *model) askMerge(g sessionGroup) {
	a := m.Proj
	a.section = g
	a.targets = slices.DeleteFunc(m.sections(), func(o sessionGroup) bool { return o.Key == g.Key })
	a.MergeQuery.Set("")
	if a.merge != nil {
		a.merge.refresh()
	}
	a.MergeOpen.Set(true)
}

// mergeRows are the sections the one asked about can merge into, every
// one but itself, whatever space shows them, matching what is typed.
func (m *model) mergeRows(q string) []pickRow {
	from := m.Proj.section
	nodes := ggui.Peek(m.Nodes)
	q = strings.ToLower(strings.TrimSpace(q))
	var rows []pickRow
	for _, g := range m.Proj.targets {
		name := groupName(g)
		if q != "" && !strings.Contains(strings.ToLower(name), q) {
			continue
		}
		detail := nodeName(nodes, g.NodeID) + " · " + prettyPath(g.Project)
		if g.multiRoot() {
			detail = Tf("%d folders", g.rootCount())
		}
		rows = append(rows, pickRow{Label: name, Detail: detail, Icon: icon("folder").Size(15), Run: func() { m.confirmMerge(from, g) }})
	}
	return rows
}

// confirmMerge says what merging from into into makes, and does it once
// agreed.
func (m *model) confirmMerge(from, into sessionGroup) {
	folders := into.rootCount() + from.rootCount()
	sessions := len(from.Sessions) + len(into.Sessions)
	m.askConfirm(confirmation{
		Title:  Tf("Merge %s into %s?", groupName(from), groupName(into)),
		Body:   Tf("%s then holds %d folders and %d sessions, in one section of the sidebar. Its sessions say which folder they are in, and its agents on one node see and message each other as one project's. Its menu takes a folder out again.", groupName(into), folders, sessions),
		Action: T("Merge"), Safe: true,
		Run: func() { m.mergeProjects(from, into) },
	})
}

// projectDialogs are the project dialogs, over the window.
func projectDialogs(m *model) ggui.Widget {
	a := m.Proj
	a.merge = newPicker(a.MergeOpen, a.MergeQuery, m.mergeRows)
	a.merge.Placeholder = T("Merge into…")
	a.merge.Name = T("Projects")
	a.merge.Empty = T("No other projects")
	a.merge.Action = T("Merge")
	name := nameDialog(nameAsk{
		Open: a.NameOpen, Text: a.Name, Err: a.NameErr,
		Title: T("Rename project"), Field: T("Project name"), Action: T("Rename"),
		Placeholder: T("Its folder's name"), Submit: m.submitProjectName,
	})
	return widget.Stack(name, addFolderDialog(m), a.merge.dialog(), projectSettingsDialog(m), importProjectsDialog(m))
}

// addFolderDialog asks for a folder to add to a project: the node it is
// on, and its path, browsed for in this machine's folder dialog or, on
// another node, in mako's.
func addFolderDialog(m *model) ggui.Widget {
	a := m.Proj
	nodeIDs := reachableNodeIDs(m)
	browse := browseButton(a.AddPath, func(d runtime.Dialogs) (string, error) {
		return d.PickFolder(runtime.FileDialog{Title: T("Add folder"), Directory: ggui.Peek(a.AddPath)})
	})
	browseRemote := ui.Button(T("Browse…"), func() {
		m.pickFolderFor(a.AddOpen, ggui.Peek(a.AddNode), ggui.Peek(a.AddPath), a.AddErr.Set, func(dir string) {
			if dir != "" {
				a.AddPath.Set(dir)
			}
		})
	}).Outline()
	local := ggui.Derived(func() bool { return a.AddNode.Get() == m.Conn.Get().Self.GetId() })
	hasErr := ggui.Map(a.AddErr, func(s string) bool { return s != "" })
	form := widget.Column(
		muted(widget.Text(T("The folder becomes one of the project's: its sessions show in the project's section, marked with the folder's name. A repository's clones on other nodes join it as they show up.")).Size(12)),
		ui.Field(T("Node"), ui.Select(a.AddNode).BindOptions(nodeIDs).Format(func(id string) string {
			return nodeName(ggui.Peek(m.Nodes), id)
		}).Name(T("Node"))),
		ui.Field(T("Folder"), widget.Row(
			widget.Expanded(ui.TextField(a.AddPath).Placeholder(T("Its whole path")).Name(T("Folder")).OnSubmit(func(string) { m.submitAddFolder() })),
			ggui.If(local, func() ggui.Widget { return browse }).Else(func() ggui.Widget { return browseRemote }),
		).Gap(8)),
		ggui.If(hasErr, func() ggui.Widget { return widget.TextOf(a.AddErr).Size(12).Color(skin.DotDown) }),
		widget.Row(
			ui.Button(T("Cancel"), func() { a.AddOpen.Set(false) }).Outline(),
			ui.Button(T("Add folder"), m.submitAddFolder),
		).Gap(8).Justify(widget.JustifyEnd),
	).Gap(16).Stretch()
	return keyedDialog(a.AddOpen, form).Title(T("Add a folder to the project")).Width(460)
}

// Settings --------------------------------------------------------------------

// openProjectSettings opens the settings of section g, on a copy of it.
func (m *model) openProjectSettings(g sessionGroup) {
	a := m.Proj
	a.section = g
	a.Draft.Set(asProject(g))
	a.SettingsOpen.Set(true)
}

// saveProjectSettings keeps the draft: a section the sidebar grouped
// itself whose folders and ways the draft left as they were keeps
// grouping so, named as the draft says.
func (m *model) saveProjectSettings() {
	a := m.Proj
	g, p := a.section, ggui.Peek(a.Draft)
	a.SettingsOpen.Set(false)
	p.Name = strings.TrimSpace(p.Name)
	if g.Def == nil {
		was := asProject(g)
		was.Name = p.Name
		if deepEqual(was, p) {
			m.renameProject(g, p.Name) // as it was, but for its name
			return
		}
	}
	m.saveProject(Tf("Saved %s", cmp.Or(p.Name, groupName(g))), g, p)
}

// draftField binds a field of the draft.
func draftField[T any](a *projectAsk, field func(*project) *T) ggui.Binding[T] {
	return ggui.Bind(func() T { p := a.Draft.Get(); return *field(&p) }, func(v T) {
		a.Draft.Update(func(p project) project { p = p.clone(); *field(&p) = v; return p })
	})
}

// projectSettingsDialog edits a project: its name, its folders, which
// starts sessions by default, and whether new clones join it and its
// agents are given its other folders.
func projectSettingsDialog(m *model) ggui.Widget {
	a := m.Proj
	// The folders' rows are made again as folders come, go or move, not
	// as a label is typed, which would take the field's focus.
	shape := ggui.Map(a.Draft, func(p project) string {
		ids := make([]string, len(p.Roots))
		for i, r := range p.Roots {
			ids[i] = r.Remote + "\x00" + fmt.Sprint(r.Locations)
		}
		return strings.Join(ids, "\x01")
	})
	folders := ggui.View(shape, func(string) ggui.Widget {
		p := ggui.Peek(a.Draft)
		nodes := ggui.Peek(m.Nodes)
		var rows []ggui.Widget
		for i, r := range p.Roots {
			rows = append(rows, rootSettingsRow(a, i, r, len(p.Roots), nodes))
		}
		return card(rows...)
	})
	join := ggui.Bind(func() bool { return !a.Draft.Get().NoJoin }, func(on bool) {
		a.Draft.Update(func(p project) project { p = p.clone(); p.NoJoin = !on; return p })
	})
	give := draftField(a, func(p *project) *bool { return &p.GiveDirs })
	form := widget.Column(
		ui.Field(T("Name"), ui.TextField(draftField(a, func(p *project) *string { return &p.Name })).
			Placeholder(T("Its folder's name")).Name(T("Project name"))),
		section(T("Folders"), folders,
			muted(widget.Text(T("The first folder is where its plus starts a session. Agents in any of its folders on one node see and message each other as one project's.")).Size(12))),
		card(
			row(T("Connect new clones"), T("A clone of one of its repositories, on another node or in another folder, joins the project as it shows up."),
				ui.Switch(join, "").Name(T("Connect new clones"))),
			row(T("Give agents its other folders"), T("An agent that works in folders besides its own, as Claude Code and Codex do, starts with the project's other folders on its node. A running agent has them once it starts again."),
				ui.Switch(give, "").Name(T("Give agents its other folders"))),
		),
		widget.Row(
			ui.Button(T("Cancel"), func() { a.SettingsOpen.Set(false) }).Outline(),
			ui.Button(T("Save"), m.saveProjectSettings),
		).Gap(8).Justify(widget.JustifyEnd),
	).Gap(16).Stretch()
	return keyedDialog(a.SettingsOpen, widget.Scroll(form)).Title(T("Project settings")).Width(520)
}

// rootSettingsRow is one of the draft's folders: its label, where it is on
// each node, and buttons that make it the first and take it out.
func rootSettingsRow(a *projectAsk, i int, r projectRoot, n int, nodes []*protocol.Node) ggui.Widget {
	label := ggui.Bind(func() string {
		p := a.Draft.Get()
		if i < len(p.Roots) {
			return p.Roots[i].Label
		}
		return ""
	}, func(v string) {
		a.Draft.Update(func(p project) project {
			p = p.clone()
			if i < len(p.Roots) {
				p.Roots[i].Label = strings.TrimSpace(v)
			}
			return p
		})
	})
	where := make([]ggui.Widget, 0, len(r.Locations))
	for _, l := range r.Locations {
		where = append(where, muted(widget.Text(nodeName(nodes, l.Node)+" · "+prettyPath(l.Path)).Size(11.5).NoWrap().Ellipsis()))
	}
	if r.Remote != "" {
		where = append(where, widget.Text(r.Remote).Size(11).Color(skin.ExtraMuted).NoWrap().Ellipsis())
	}
	buttons := []ggui.Widget{}
	if i > 0 {
		buttons = append(buttons, iconButton("arrow-up", T("Start sessions here by default"), func() {
			a.Draft.Update(func(p project) project {
				p = p.clone()
				root := p.Roots[i]
				p.Roots = append([]projectRoot{root}, slices.Delete(p.Roots, i, i+1)...)
				return p
			})
		}))
	}
	if n > 1 {
		buttons = append(buttons, iconButton("x", Tf("Take %s out of the project", r.label()), func() {
			a.Draft.Update(func(p project) project {
				p = p.clone()
				p.Roots = slices.Delete(p.Roots, i, i+1)
				return p
			})
		}))
	}
	field := ui.TextField(label).Placeholder(r.folderName()).Name(T("Folder label"))
	return widget.Box(widget.Row(
		widget.Expanded(widget.Column(append([]ggui.Widget{field}, where...)...).Gap(4).Stretch()),
		widget.Row(buttons...).Gap(2),
	).Gap(8).Align(widget.AlignStart)).Pad(10, 14)
}

// Dragging --------------------------------------------------------------------

// projectDrag is a project section's heading, dragged onto another's to
// merge into it.
type projectDrag struct{ Key, Name string }

// projectDragged makes a project section's heading one to drag onto
// another's, which offers to merge it there: the merge is asked about as
// from the menu, and the heading dropped on says beforehand what it
// would do. A session dropped on it starts there again, as before.
func projectDragged(m *model, head ggui.Widget, g sessionGroup) ggui.Widget {
	over := ggui.State("")
	into := groupName(g)
	marked := ggui.View(over, func(from string) ggui.Widget {
		if from == "" {
			return head
		}
		hint := widget.Box(widget.Text(Tf("Drop to merge %s into %s", from, into)).Size(11.5).Color(skin.AccentBright).NoWrap().Ellipsis()).Pad(0, 8, 4, 8)
		return widget.Box(widget.Column(head, hint).Stretch()).Border(1.5, skin.AccentBright).Radius(7)
	})
	zone := widget.DropZone(marked, func(d widget.Dropped[projectDrag]) {
		over.Set("")
		if d.Value.Key == g.Key {
			return
		}
		groups := m.sections()
		if i := slices.IndexFunc(groups, func(o sessionGroup) bool { return o.Key == d.Value.Key }); i >= 0 {
			m.confirmMerge(groups[i], g)
		}
	}).OnHover(func(on bool) {
		if !on {
			over.Set("")
		}
	})
	zone = zone.OnOver(func(d widget.Dropped[projectDrag]) {
		if d.Value.Key != g.Key {
			over.Set(d.Value.Name)
		}
	})
	return widget.DragSource(zone, projectDrag{Key: g.Key, Name: into})
}

// reachableNodeIDs are the nodes a session can start on, or a folder be
// picked on: those this one reaches.
func reachableNodeIDs(m *model) ggui.Readable[[]string] {
	return ggui.Map(m.Nodes, func(nodes []*protocol.Node) []string {
		var ids []string
		for _, n := range nodes {
			if reachable(n) {
				ids = append(ids, n.GetId())
			}
		}
		return ids
	})
}
