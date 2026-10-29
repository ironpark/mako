package gui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
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
	Version int64        `json:"version"` // Unix nanoseconds
	NodeID  string       `json:"node_id"` // where it was made
	Doc     workspaceDoc `json:"doc"`
	// Contexts are the window's own, not shared: by space, the owner or
	// project whose tabs it showed last.
	Contexts map[string]string `json:"contexts,omitempty"`
}

// allSpaceID is the ID of the space of every session, whose layout is kept
// as the others' are.
const allSpaceID = "all"

// How a space keeps its tabs and panes: one set for the whole space, or a
// set for each owner, or each project, of the sessions in it, the one
// shown following the session last picked in the sidebar.
const (
	layoutBySpace   = ""
	layoutByOwner   = "owner"
	layoutByProject = "project"
)

// layoutBys are the ways, in the order the settings offer them, and their
// labels, in English for T.
var (
	layoutBys      = []string{layoutBySpace, layoutByOwner, layoutByProject}
	layoutByLabels = map[string]string{layoutBySpace: "Whole space", layoutByOwner: "By owner", layoutByProject: "By project"}
)

// layoutKey is what a set of tabs and panes is kept by: its space's ID, and
// within the space the owner or project it is for, "" for the whole space.
func layoutKey(spaceID, context string) string { return spaceID + "|" + context }

// keySpace is the space a layout key is in.
func keySpace(key string) string { s, _, _ := strings.Cut(key, "|"); return s }

// sessionContext is the set of tabs a session is in within a space that
// keeps them by: its owner's, or its project's.
func sessionContext(by string, s *protocol.SessionInfo) string {
	switch by {
	case layoutByOwner:
		return "owner:" + s.GetOwner()
	case layoutByProject:
		return "project:" + dirKey(s.GetNodeId(), projectOf(s))
	}
	return ""
}

// fits reports whether context is one a space keeping tabs by has.
func fits(by, context string) bool {
	if by == layoutBySpace {
		return context == ""
	}
	return strings.HasPrefix(context, by+":")
}

// shownKey is the layout key of what the window shows: the space shown's,
// and in it the owner's or project's last picked, while it keeps its tabs
// that way.
func (m *model) shownKey() string {
	p := m.spacesNow()
	id := p.spaceID(p.Space)
	context := m.contexts[id]
	if !fits(p.LayoutBy, context) {
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
			m.wsVersion, m.wsNode = f.Version, f.NodeID
			m.layouts = keyedLayouts(f.Doc.Layouts)
			maps.Copy(m.tabNodes, f.Doc.TabNodes)
			if f.Contexts != nil {
				m.contexts = f.Contexts
			}
		}
	}
	m.Spaces.Set(f.Doc.spaceList.upgraded())
	moveOldSpaces(m)
	m.layoutKey = m.shownKey()
	m.setLayout(m.takeLayout(m.layoutKey))
	m.wsLast, _ = json.Marshal(m.workspaceDocNow())
	// Another space, or another way of keeping its tabs, shows its own.
	shown := ggui.Derived(func() string { p := m.spaces(); return layoutKey(p.spaceID(p.Space), p.LayoutBy) })
	ggui.Watch(shown, func(string) { m.showLayout(m.shownKey()) })
	ggui.Watch(m.Spaces, func(spaceList) { m.workspaceChanged() })
	ggui.Watch(m.Layout, func(*layoutNode) { m.workspaceChanged() })
	sync := ggui.Map(m.Prefs, func(p prefs) bool { return p.SyncWorkspace })
	ggui.Watch(sync, func(on bool) {
		m.syncing.Store(on)
		if on {
			m.wsStale.Store(true)
			m.pollNow()
		}
	})
}

// moveOldSpaces moves the spaces the settings kept, before the workspace
// kept them, into the workspace, the one shown with them.
func moveOldSpaces(m *model) {
	p := ggui.Peek(m.Prefs)
	if len(p.OldSpaces) == 0 && p.OldAllLayoutBy == "" {
		return
	}
	if len(ggui.Peek(m.Spaces).Spaces) == 0 {
		old := slices.Clone(p.OldSpaces)
		for i := range old {
			if old[i].ID == "" {
				old[i].ID = newSpaceID()
			}
		}
		m.Spaces.Set(spaceList{Spaces: old, OldAllLayoutBy: p.OldAllLayoutBy}.upgraded())
		if p.OldSpace > 0 && p.OldSpace <= len(old) {
			p.ShownSpace = old[p.OldSpace-1].ID
		}
	}
	m.Prefs.Update(func(q prefs) prefs {
		q.ShownSpace = p.ShownSpace
		q.OldSpaces, q.OldSpace, q.OldAllLayoutBy = nil, 0, ""
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

// showContext shows, in the space shown, the tabs and panes of the owner or
// project of session s, when the space keeps them that way.
func (m *model) showContext(s *protocol.SessionInfo) {
	p := m.spacesNow()
	by := p.LayoutBy
	if by == layoutBySpace {
		return
	}
	id := p.spaceID(p.Space)
	if c := sessionContext(by, s); c != m.contexts[id] {
		m.contexts[id] = c
		m.contextsMoved = true
	}
	m.showLayout(layoutKey(id, m.contexts[id]))
}

// takeLayout takes key's panes out of those put by, or one empty pane for
// a key with none yet.
func (m *model) takeLayout(key string) *layoutNode {
	root := m.layouts[key]
	delete(m.layouts, key)
	if root == nil {
		root = newLayout()
	}
	return root
}

// showLayout puts the panes shown by, under the key they belong to, and
// brings out key's.
func (m *model) showLayout(key string) {
	if key == m.layoutKey {
		return
	}
	m.layouts[m.layoutKey] = ggui.Peek(m.Layout)
	m.layoutKey = key
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
	m.attachVisible()
}

// workspaceDocNow is the workspace as it stands: every space's panes, with
// the diffs left out, which are not written down.
func (m *model) workspaceDocNow() workspaceDoc {
	layouts := maps.Clone(m.layouts)
	layouts[m.layoutKey] = ggui.Peek(m.Layout)
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
	if m.wsTimer != nil {
		m.wsTimer.Stop()
	}
	m.wsTimer = time.AfterFunc(workspaceSave, func() { post(m.saveWorkspace) })
}

// saveWorkspace writes the workspace down and gives it to the daemon, as a
// new edit, if it is not as it was. UI goroutine.
func (m *model) saveWorkspace() {
	doc, err := json.Marshal(m.workspaceDocNow())
	if err != nil {
		return
	}
	if bytes.Equal(doc, m.wsLast) {
		if m.contextsMoved {
			m.writeWorkspace() // the window's own, and no edit to share
		}
		return
	}
	m.wsLast = doc
	m.wsVersion, m.wsNode = time.Now().UnixNano(), ggui.Peek(m.Conn).Self.GetId()
	m.giveWorkspace(m.writeWorkspace())
}

// writeWorkspace writes the workspace down as the edit it is, and returns
// what it wrote.
func (m *model) writeWorkspace() []byte {
	m.contextsMoved = false
	body, err := json.Marshal(workspaceFile{Version: m.wsVersion, NodeID: m.wsNode, Doc: m.workspaceDocNow(), Contexts: m.contexts})
	if err != nil {
		return nil
	}
	// The file is found now, not in the goroutine writing it: MAKO_HOME
	// may have changed by then, as it does when a test ends.
	path, err := configFile("workspace.json")
	if err != nil {
		return body
	}
	m.wsWrites.Add(1)
	go func() {
		defer m.wsWrites.Done()
		if err := writeWindowFile(path, body); err != nil {
			log.Printf("mako: save workspace: %v", err)
		}
	}()
	return body
}

// giveWorkspace gives the daemon the workspace written down as body, while
// the window follows the other nodes, and takes up a newer one it holds.
func (m *model) giveWorkspace(body []byte) {
	if body == nil || !ggui.Peek(m.Prefs).SyncWorkspace || m.post == nil {
		return
	}
	ws := &protocol.Workspace{Doc: body, Version: m.wsVersion, NodeId: m.wsNode}
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
	case ws.GetVersion() < m.wsVersion:
		// The window's is newer, or the daemon holds none: it has the
		// window's next.
		m.giveWorkspace(m.writeWorkspace())
		return
	case !protocol.Newer(ws.GetVersion(), ws.GetNodeId(), m.wsVersion, m.wsNode):
		return
	}
	var f workspaceFile
	if json.Unmarshal(ws.GetDoc(), &f) != nil {
		return
	}
	m.wsVersion, m.wsNode = ws.GetVersion(), ws.GetNodeId()
	// The space shown is kept by its ID, and stays shown if the other node
	// kept it.
	m.Spaces.Set(f.Doc.spaceList.upgraded())
	m.layouts = keyedLayouts(f.Doc.Layouts)
	maps.Copy(m.tabNodes, f.Doc.TabNodes)
	m.layoutKey = m.shownKey()
	root := m.takeLayout(m.layoutKey)
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
	m.wsLast, _ = json.Marshal(m.workspaceDocNow())
	m.writeWorkspace()
}
