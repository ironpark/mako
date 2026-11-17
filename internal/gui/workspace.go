package gui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// The workspace: the window's tabs and panes in each space, and the spaces
// themselves. The panes shown are the space shown's; moving to another
// space puts them by and brings out that space's. The window writes the
// workspace down in workspace.json, and, while the setting has it follow
// the other nodes, gives it to its daemon, which shares it with the nodes
// that trust it both ways and tells the window of a newer one from them.
// The later edit wins, whole.

// workspaceDoc is the workspace as it is written down and shared.
type workspaceDoc struct {
	spaceList
	Layouts map[string]*layoutNode `json:"layouts,omitempty"` // by layoutKey
	// TabNodes are the nodes of the session tabs, by tab; see leftover.go.
	TabNodes map[string]string `json:"tab_nodes,omitempty"`
}

// workspaceFile is workspace.json: the workspace, and the edit it is.
type workspaceFile struct {
	Version int64        `json:"version"` // Unix nanoseconds, or counted; see editVersion
	NodeID  string       `json:"node_id"` // where it was made
	Doc     workspaceDoc `json:"doc"`
	// Contexts are the window's own, not shared: by space, the owner or
	// project whose tabs it showed last.
	Contexts map[string]string `json:"contexts,omitempty"`
}

// allSpaceID is the ID of the space of every session, whose layout is kept
// as the others' are.
const allSpaceID = "all"

// A space keeps tabs and panes for each project, the one shown following
// the project last picked in the sidebar, or the session last picked;
// those of a space before any project was picked are its own.

// layoutKey is what a set of tabs and panes is kept by: its space's ID, and
// within the space the project it is for, "" for none.
func layoutKey(spaceID, context string) string { return spaceID + "|" + context }

// keySpace is the space a layout key is in.
func keySpace(key string) string { s, _, _ := strings.Cut(key, "|"); return s }

// projectContext is the set of tabs a project section is kept by in a
// space.
func projectContext(section string) string { return "project:" + section }

// fits reports whether context is a project's, as a space keeps tabs by;
// one kept by an owner, as spaces once could, is not.
func fits(context string) bool { return context == "" || strings.HasPrefix(context, "project:") }

// shownKey is the layout key of what the window shows: the space shown's,
// and in it the project's last picked.
func (m *model) shownKey() string {
	p := m.spacesNow()
	id := p.spaceID(p.Space)
	context := m.ws.contexts[id]
	if !fits(context) {
		context = ""
	}
	return layoutKey(id, context)
}

// newSpaceID is a fresh space's ID.
func newSpaceID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// spaceID is the ID of space i, as its layout is kept by.
func (p spaceSet) spaceID(i int) string {
	if sp, ok := p.spaceOf(i); ok {
		return sp.ID
	}
	return allSpaceID
}

// workspaceSave is how long the workspace waits, after a change, to be
// written down and given to the daemon: a divider dragged is one change.
const workspaceSave = 400 * time.Millisecond

// setupWorkspace brings back the workspace written down, and from then on
// keeps the panes by space, writes changes down and follows the daemon's.
// It runs under the app's owner.
func setupWorkspace(m *model) {
	var f workspaceFile
	if path, err := configFile("workspace.json"); err == nil {
		if body, err := os.ReadFile(path); err == nil && json.Unmarshal(body, &f) == nil {
			m.ws.version, m.ws.node = f.Version, f.NodeID
			m.ws.layouts = keyedLayouts(f.Doc.Layouts)
			maps.Copy(m.tabNodes, f.Doc.TabNodes)
			if f.Contexts != nil {
				m.ws.contexts = f.Contexts
			}
		}
	}
	m.Spaces.Set(f.Doc.spaceList)
	moveOldSpaces(m)
	m.migrateProjectKeys()
	m.ws.layoutKey = m.shownKey()
	m.setLayout(m.takeLayout(m.ws.layoutKey))
	m.ws.last, _ = json.Marshal(m.workspaceDocNow())
	// Another space shows its own.
	shown := ggui.Derived(func() string { p := m.spaces(); return p.spaceID(p.Space) })
	ggui.Watch(shown, func(string) { m.showLayout(m.shownKey()) })
	ggui.Watch(m.Spaces, func(spaceList) { m.workspaceChanged() })
	// The daemons hear of the projects as they change, not at the next poll.
	projects := ggui.Map(m.Spaces, func(l spaceList) []project { return l.Projects }).WithEqual(slicesEqual)
	ggui.Watch(projects, func([]project) { m.pushProjects(ggui.Peek(m.Nodes)) })
	ggui.Watch(m.Layout, func(*layoutNode) { m.workspaceChanged() })
	sync := ggui.Map(m.Prefs, func(p prefs) bool { return p.SyncWorkspace })
	ggui.Watch(sync, func(on bool) {
		m.link.syncing.Store(on)
		if on {
			m.link.wsStale.Store(true)
			m.link.pollNow()
		}
	})
}

// moveOldSpaces moves the spaces the settings kept, before the workspace
// kept them, into the workspace, the one shown with them.
func moveOldSpaces(m *model) {
	p := ggui.Peek(m.Prefs)
	if len(p.OldPinned) > 0 || len(p.OldPinnedProjects) > 0 {
		m.Spaces.Update(func(l spaceList) spaceList {
			if len(l.Pinned) == 0 && len(l.PinnedProjects) == 0 {
				l.Pinned, l.PinnedProjects = p.OldPinned, p.OldPinnedProjects
			}
			return l
		})
		m.Prefs.Update(func(q prefs) prefs { q.OldPinned, q.OldPinnedProjects = nil, nil; return q })
	}
	if len(p.OldSpaces) == 0 {
		return
	}
	if len(ggui.Peek(m.Spaces).Spaces) == 0 {
		old := slices.Clone(p.OldSpaces)
		for i := range old {
			if old[i].ID == "" {
				old[i].ID = newSpaceID()
			}
		}
		m.Spaces.Set(spaceList{Spaces: old})
		if p.OldSpace > 0 && p.OldSpace <= len(old) {
			p.ShownSpace = old[p.OldSpace-1].ID
		}
	}
	m.Prefs.Update(func(q prefs) prefs {
		q.ShownSpace = p.ShownSpace
		q.OldSpaces, q.OldSpace = nil, 0
		return q
	})
}

// slicesEqual compares slices of anything by value.
func slicesEqual[T any](a, b []T) bool { return reflect.DeepEqual(a, b) }

// keyedLayouts is layouts written down by space alone, as before spaces
// kept them by owner or project, keyed as the whole space's.
func keyedLayouts(layouts map[string]*layoutNode) map[string]*layoutNode {
	out := make(map[string]*layoutNode, len(layouts))
	for key, root := range layouts {
		if !strings.Contains(key, "|") {
			key = layoutKey(key, "")
		}
		out[key] = root
	}
	return out
}

// showContext shows, in the space shown, the tabs and panes of the project
// of session s.
func (m *model) showContext(s *protocol.SessionInfo) {
	m.showProjectTabs(sectionID(m.projectsNow(), s))
}

// showProjectTabs shows, in the space shown, the tabs and panes of the
// project section with ID section.
func (m *model) showProjectTabs(section string) {
	id := m.spacesNow().spaceID(m.spacesNow().Space)
	if c := projectContext(section); c != m.ws.contexts[id] {
		m.ws.contexts[id] = c
		m.ws.contextsMoved = true
	}
	m.showLayout(layoutKey(id, m.ws.contexts[id]))
}

// takeLayout takes key's panes out of those put by, or one empty pane for
// a key with none yet.
func (m *model) takeLayout(key string) *layoutNode {
	root := m.ws.layouts[key]
	delete(m.ws.layouts, key)
	if root == nil {
		root = newLayout()
	}
	return root
}

// showLayout puts the panes shown by, under the key they belong to, and
// brings out key's.
func (m *model) showLayout(key string) {
	if key == m.ws.layoutKey {
		return
	}
	m.ws.layouts[m.ws.layoutKey] = ggui.Peek(m.Layout)
	m.ws.layoutKey = key
	m.setLayout(m.takeLayout(key))
}

// setLayout shows root's panes, bringing back what each tab shows, as
// its kind does (bringBack); a tab that cannot come back leaves.
func (m *model) setLayout(root *layoutNode) {
	root = root.clone()
	root.removeTabs(func(id string) bool { return !m.bringBack(id) })
	m.editLayout(func(r *layoutNode) int {
		*r = *root
		return root.groups()[0].ID
	})
	m.forgetClosedTabs()
	m.attachVisible()
}

// workspaceDocNow is the workspace as it stands: every space's panes, with
// the diffs left out, which are not written down.
func (m *model) workspaceDocNow() workspaceDoc {
	layouts := maps.Clone(m.ws.layouts)
	layouts[m.ws.layoutKey] = ggui.Peek(m.Layout)
	p := ggui.Peek(m.Spaces)
	for id, root := range layouts {
		if s := keySpace(id); s != allSpaceID && !slices.ContainsFunc(p.Spaces, func(sp space) bool { return sp.ID == s }) {
			delete(layouts, id) // a deleted space's
			continue
		}
		root = root.clone()
		root.removeTabs(func(id string) bool { return !keptTab(id) })
		if root = root.prune(); root == nil || len(root.groups()) == 1 && len(root.groups()[0].Tabs) == 0 {
			delete(layouts, id)
			continue
		}
		layouts[id] = root
	}
	return workspaceDoc{spaceList: p, Layouts: layouts, TabNodes: m.keptTabNodes(layouts)}
}

// workspaceChanged notes that the workspace may have changed: once changes
// pause, it is written down and given to the daemon if it differs from
// what was, which moving to another space or taking up another node's
// does not make it.
func (m *model) workspaceChanged() {
	post := m.post
	if post == nil {
		return // not started, as in a test
	}
	if m.ws.timer != nil {
		m.ws.timer.Stop()
	}
	m.ws.timer = time.AfterFunc(workspaceSave, func() { post(m.saveWorkspace) })
}

// saveWorkspace writes the workspace down and gives it to the daemon, as a
// new edit, if it is not as it was. UI goroutine.
func (m *model) saveWorkspace() {
	doc, err := json.Marshal(m.workspaceDocNow())
	if err != nil {
		return
	}
	if bytes.Equal(doc, m.ws.last) {
		if m.ws.contextsMoved {
			m.writeWorkspace() // the window's own, and no edit to share
		}
		return
	}
	m.ws.last = doc
	m.ws.version, m.ws.node = editVersion(m.ws.version, time.Now()), ggui.Peek(m.Conn).Self.GetId()
	m.giveWorkspace(m.writeWorkspace())
}

// editVersion is the version of an edit made at now of the workspace at
// version: now, or just after version when the node that made that one
// keeps a clock ahead of this one's. An edit is newer than what it
// edited, so the nodes take it up, however their clocks differ; else one
// made here would lose, and be undone, to the very workspace it changed.
//
// A workspace begun from nothing, as a new node's window begins one, its
// edits are counted, not timed, until it takes up one from the clock: any
// workspace made on another node is newer, so the tab or two a window
// opens before its node is let into the mesh does not, once it is, take
// the place of the spaces and projects the other nodes have.
func editVersion(version int64, now time.Time) int64 {
	if version < countedEdits {
		return version + 1
	}
	return max(now.UnixNano(), version+1)
}

// countedEdits is where versions counted from nothing end and those from
// the clock begin: 2001, in Unix nanoseconds.
const countedEdits = 1_000_000_000 * 1_000_000_000

// writeWorkspace writes the workspace down as the edit it is, and returns
// it as the nodes share it: without the contexts, which are the window's
// own.
func (m *model) writeWorkspace() []byte {
	m.ws.contextsMoved = false
	f := workspaceFile{Version: m.ws.version, NodeID: m.ws.node, Doc: m.workspaceDocNow()}
	shared, err := json.Marshal(f)
	if err != nil {
		return nil
	}
	f.Contexts = m.ws.contexts
	body, err := json.Marshal(f)
	if err != nil {
		return shared
	}
	// The file is found now, not in the goroutine writing it: MAKO_HOME
	// may have changed by then, as it does when a test ends.
	if path, err := configFile("workspace.json"); err == nil {
		m.ws.writer.write(path, body)
	}
	return shared
}

// giveWorkspace gives the daemon the workspace written down as body, while
// the window follows the other nodes, and takes up a newer one it holds.
func (m *model) giveWorkspace(body []byte) {
	if body == nil || !ggui.Peek(m.Prefs).SyncWorkspace || m.post == nil {
		return
	}
	ws := &protocol.Workspace{Doc: body, Version: m.ws.version, NodeId: m.ws.node}
	fetch(m, 5*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Workspace, error) {
		return c.SetWorkspace(ctx, ws)
	}, func(ws *protocol.Workspace, err error) {
		if err == nil {
			m.takeWorkspace(ws)
		}
	})
}

// takeWorkspace takes up a workspace from the daemon, if the window follows
// the other nodes and it is newer than the window's own. UI goroutine.
func (m *model) takeWorkspace(ws *protocol.Workspace) {
	if !ggui.Peek(m.Prefs).SyncWorkspace {
		return
	}
	switch {
	case ws.GetVersion() < m.ws.version:
		// The window's is newer, or the daemon holds none: it has the
		// window's next.
		m.giveWorkspace(m.writeWorkspace())
		return
	case !protocol.Newer(ws.GetVersion(), ws.GetNodeId(), m.ws.version, m.ws.node):
		return
	}
	var f workspaceFile
	if json.Unmarshal(ws.GetDoc(), &f) != nil {
		return
	}
	m.ws.version, m.ws.node = ws.GetVersion(), ws.GetNodeId()
	// The space shown is kept by its ID, and stays shown if the other node
	// kept it.
	m.Spaces.Set(f.Doc.spaceList)
	m.ws.layouts = keyedLayouts(f.Doc.Layouts)
	maps.Copy(m.tabNodes, f.Doc.TabNodes)
	m.migrateProjectKeys()
	m.followSections()
	m.ws.layoutKey = m.shownKey()
	root := m.takeLayout(m.ws.layoutKey)
	// A file with edits not saved stays open, whatever the other node had.
	for _, id := range m.dirtyDocs() {
		if root.groupOf(id) == nil {
			g := root.groups()[0]
			g.insert(id, len(g.Tabs))
		}
	}
	m.setLayout(root)
	// Written down as it came, under its own version, and no edit to give
	// back.
	m.ws.last, _ = json.Marshal(m.workspaceDocNow())
	m.writeWorkspace()
}

// followSections points each space's context at the section it is now:
// the projects another node's workspace brings may have taken a section
// in, as one merged or made there does, its tabs moving with it, or done
// away with it.
func (m *model) followSections() {
	l := ggui.Peek(m.Spaces)
	kept := ggui.Peek(m.Prefs).Projects
	groups := groupProjects(ggui.Peek(m.Sessions), ggui.Peek(m.Nodes), kept, l.Projects, l.Names)
	for space, ctx := range m.ws.contexts {
		section, ok := strings.CutPrefix(ctx, "project:")
		if !ok {
			continue
		}
		if to := sectionNow(section, groups, l.Projects, kept); to != section {
			m.ws.contexts[space] = ""
			if to != "" {
				m.ws.contexts[space] = projectContext(to)
			}
			m.ws.contextsMoved = true
		}
	}
}

// sectionNow is the section with ID id as the sidebar has it now: itself,
// while it has it; else the project that holds the folder it was for;
// else "" for none.
func sectionNow(id string, groups []sessionGroup, defs []project, kept []keptProject) string {
	if slices.ContainsFunc(groups, func(g sessionGroup) bool { return g.Key == id }) {
		return id
	}
	key := strings.TrimPrefix(id, freePrefix)
	for _, r := range kept {
		if implicitKey(r.Node, r.Path, r.Remote) != key {
			continue
		}
		if pl := placeKept(defs, r); pl.At >= 0 {
			return defs[pl.At].ID
		}
	}
	return ""
}
