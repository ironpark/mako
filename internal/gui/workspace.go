package gui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	shared "github.com/ironpark/mako/internal/workspace"
)

// The workspace: the window's tabs and panes in each space, and the spaces,
// projects, names, looks and pins. The panes shown are the space shown's;
// moving to another space puts them by and brings out that space's.
//
// The daemon holds the workspace, as a CRDT it shares with the nodes that
// trust it both ways (internal/workspace). The window holds it as its
// document, the view of the daemon's state it last took, and edits that,
// as it always has. Once its edits pause, the edit from the document it
// last took or gave to the one it has goes to the daemon as a delta
// (workspace.Diff), and the daemon's answer, the workspace after, is taken
// up. A state the daemon has meanwhile, from another window or node, is
// taken up as it comes, but never over an edit of the window's: an edit not
// yet given goes first, and the answer to it, which holds both, is the one
// taken up. The window writes its document down in workspace.json, with
// what is the window's own, to show before the daemon answers.

// workspaceDoc is the workspace as the window holds it: its document.
type workspaceDoc struct {
	spaceList
	Layouts map[string]*layoutNode `json:"layouts,omitempty"` // by layoutKey
	// TabNodes are the nodes of the session tabs, by tab; see leftover.go.
	TabNodes map[string]string `json:"tab_nodes,omitempty"`
}

// workspaceFile is workspace.json: the window's document, as it last had
// it, and what is the window's own.
type workspaceFile struct {
	// Version and NodeID are those of a workspace kept before it was a
	// CRDT, the edit it was, in Unix nanoseconds or counted, and where it
	// was made: the daemon takes it in, stamped so, once (giveEdit). A
	// file written since has none.
	Version int64        `json:"version,omitempty"`
	NodeID  string       `json:"node_id,omitempty"`
	Doc     workspaceDoc `json:"doc"`
	// Base is the document as the window last took it from its daemon or
	// gave it the edit to it, while Doc holds edits the daemon has not
	// had: the window started again gives it the edit from Base to Doc.
	// Left out when there are none.
	Base json.RawMessage `json:"base,omitempty"`
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
			m.ws.layouts = keyedLayouts(f.Doc.Layouts)
			maps.Copy(m.tabNodes, f.Doc.TabNodes)
			if f.Contexts != nil {
				m.ws.contexts = f.Contexts
			}
			if f.Version != 0 {
				m.ws.old = &workspaceFile{Version: f.Version, NodeID: f.NodeID, Doc: f.Doc}
			}
		}
	}
	if m.ws.give == nil {
		m.ws.give = m.giveDaemon
	}
	m.Spaces.Set(f.Doc.spaceList)
	m.ws.layoutKey = m.shownKey()
	m.setLayout(m.takeLayout(m.ws.layoutKey))
	// What the window had is what its edits are of, but for edits it had
	// not given; what settling it in changes below is an edit like any.
	m.ws.base, _ = json.Marshal(m.workspaceDocNow())
	if len(f.Base) > 0 {
		m.ws.base = slices.Clone([]byte(f.Base))
	}
	moveOldSpaces(m)
	m.migrateProjectKeys()
	m.showLayout(m.shownKey())
	// Another space shows its own, and the sidebar follows its active tab.
	shown := ggui.Derived(func() string { p := m.spaces(); return p.spaceID(p.Space) })
	ggui.Watch(shown, func(string) {
		m.showLayout(m.shownKey())
		m.syncScopeToTab(ggui.Peek(m.Selected), false)
	})
	ggui.Watch(m.Sessions, func([]*protocol.SessionInfo) {
		m.syncScopeToTab(ggui.Peek(m.Selected), false)
	})
	ggui.Watch(m.Spaces, func(spaceList) { m.workspaceChanged() })
	// The daemons hear of the projects as they change, not at the next poll.
	projects := ggui.Map(m.Spaces, func(l spaceList) []project { return l.Projects }).WithEqual(slicesEqual)
	ggui.Watch(projects, func([]project) { m.pushProjects(ggui.Peek(m.Nodes)) })
	ggui.Watch(m.Layout, func(*layoutNode) { m.workspaceChanged() })
	followWindowLayout(m)
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
	m.ws.contexts[id] = projectContext(section)
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
	m.bumpWorkspaceRev()
}

// setLayout shows root's panes, bringing back what each tab shows, as
// its kind does (bringBack); a tab that cannot come back leaves.
func (m *model) setLayout(root *layoutNode) {
	m.setLayoutFocus(root, 0)
}

// setLayoutFocus is setLayout, keeping focus on pane focus when it is
// non-zero and present in root rather than resetting to the first pane.
func (m *model) setLayoutFocus(root *layoutNode, focus int) {
	root = root.clone()
	root.removeTabs(func(id string) bool { return !m.bringBack(id) })
	m.editLayout(func(r *layoutNode) int {
		*r = *root
		if focus != 0 && root.group(focus) != nil {
			return focus
		}
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
// pause, it is written down and given to the daemon.
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

// saveWorkspace writes the workspace down and gives the daemon the edit it
// is. UI goroutine.
func (m *model) saveWorkspace() {
	m.writeWorkspace()
	m.giveEdit()
}

// docNow is the window's document as it stands, as JSON.
func (m *model) docNow() []byte {
	body, _ := json.Marshal(m.workspaceDocNow())
	return body
}

// giveEdit gives the daemon the edit the window's document is of the one
// it last took or gave, if it is one, unless an edit is on its way, whose
// answer gives the next. The workspace the window kept before it was a
// CRDT goes first, whole. UI goroutine.
func (m *model) giveEdit() {
	if m.ws.giving || m.ws.give == nil {
		return
	}
	if old := m.ws.old; old != nil {
		body, err := json.Marshal(old)
		if err != nil {
			return
		}
		m.ws.giving = true
		gen := m.ws.gen
		m.ws.give(&protocol.Workspace{Doc: body}, func(ws *protocol.Workspace, err error) {
			if gen != m.ws.gen {
				return // an answer of the daemon the window left
			}
			m.ws.giving = false
			if err == nil {
				m.ws.old = nil
				m.takeWorkspace(ws)
				m.writeWorkspace()
			}
		})
		return
	}
	now := m.docNow()
	if bytes.Equal(now, m.ws.base) {
		return
	}
	delta, err := shared.Diff(m.ws.state, m.ws.base, now)
	if err != nil {
		return
	}
	prev := m.ws.base
	m.ws.base = now
	if shared.Empty(delta) {
		return
	}
	m.ws.giving = true
	gen := m.ws.gen
	m.ws.give(&protocol.Workspace{State: delta}, func(ws *protocol.Workspace, err error) {
		if gen != m.ws.gen {
			return // an answer of the daemon the window left
		}
		m.ws.giving = false
		if err != nil {
			// Given again with the next, as an edit of what the daemon
			// last had; and so written down, should the window close
			// first.
			m.ws.base = prev
			m.writeWorkspace()
			return
		}
		m.takeWorkspace(ws)
		m.writeWorkspace()
	})
}

// giveDaemon gives the window's daemon ws, and hands its answer to done on
// the UI goroutine. A daemon from before the workspace was a CRDT takes
// nothing.
func (m *model) giveDaemon(ws *protocol.Workspace, done func(*protocol.Workspace, error)) {
	if ggui.Peek(m.Conn).Self.GetRevision() < protocol.RevisionWorkspaceCRDT {
		done(nil, errors.New("the daemon is too old to keep the workspace"))
		return
	}
	fetch(m, 5*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Workspace, error) {
		return c.SetWorkspace(ctx, ws)
	}, done)
}

// writeWorkspace writes the window's document down, with its contexts,
// which are its own, and the document its edits not given are of.
func (m *model) writeWorkspace() {
	f := workspaceFile{Doc: m.workspaceDocNow(), Contexts: m.ws.contexts}
	if doc, _ := json.Marshal(f.Doc); !bytes.Equal(doc, m.ws.base) {
		f.Base = m.ws.base
	}
	if old := m.ws.old; old != nil {
		// Not yet taken in: still the workspace from before.
		f.Version, f.NodeID = old.Version, old.NodeID
	}
	body, err := json.Marshal(f)
	if err != nil {
		return
	}
	// The file is found now, not in the goroutine writing it: MAKO_HOME
	// may have changed by then, as it does when a test ends.
	if path, err := configFile("workspace.json"); err == nil {
		m.ws.writer.write(path, body)
	}
}

// takeWorkspace takes up the workspace the daemon holds, as it answered an
// edit or the poll. A state that comes while an edit is on its way is left
// for the edit's answer, which is newer; and an edit of the window's not
// yet given is given first, the daemon's answer, holding both, taken up in
// its place. Taking one up is no edit of the window's. UI goroutine.
func (m *model) takeWorkspace(ws *protocol.Workspace) {
	st := ws.GetState()
	if st == nil {
		return // a daemon from before the workspace was a CRDT
	}
	// A change staged is the daemon's to say, whatever the window edits.
	m.LayoutStage.Set(ws.GetStage())
	if m.ws.giving {
		m.link.wsStale.Store(true) // asked for again once the answer is in
		return
	}
	// A daemon's workspace only grows, as states merge into it: joined
	// with the last the window took, one the poll asked for before the
	// answer to an edit, and brings after it, changes nothing.
	joined := proto.CloneOf(m.ws.state)
	if joined == nil {
		joined = &protocol.WorkspaceState{}
	}
	shared.Merge(joined, st, nil, math.MaxInt64)
	m.ws.state = joined
	m.importLayoutFile(joined)
	if m.ws.old == nil && shared.Empty(st) && !bytes.Equal(m.ws.base, emptyDoc) {
		// A daemon that holds nothing has lost what the window has, or
		// never had it: it is given whole, counted, so that whatever the
		// other nodes have of it wins.
		var doc workspaceDoc
		if json.Unmarshal(m.ws.base, &doc) == nil {
			m.ws.old = &workspaceFile{Version: 1, Doc: doc}
		}
	}
	if m.ws.old != nil || !bytes.Equal(m.docNow(), m.ws.base) {
		m.giveEdit()
		return
	}
	view := shared.View(joined)
	if bytes.Equal(view, m.ws.applied) {
		return
	}
	var doc workspaceDoc
	if json.Unmarshal(view, &doc) != nil {
		return
	}
	m.ws.applied = view
	m.showDoc(doc)
	m.ws.base = m.docNow()
	m.writeWorkspace()
}

// leaveWorkspace forgets the workspace of the daemon the window leaves for
// another: the window brings its workspace along to none, but takes up the
// next daemon's, or gives it its own when it holds nothing. Edits not yet
// given to the daemon left are dropped, as is its answer to one on its
// way. A window that has taken nothing yet, as one just started, leaves
// nothing, and gives its next daemon what it holds. UI goroutine.
func (m *model) leaveWorkspace() {
	if m.ws.state == nil && !m.ws.giving {
		return
	}
	m.ws.gen++
	m.ws.giving = false
	m.LayoutStage.Set(nil) // the daemon left holds it
	m.ws.state, m.ws.applied = nil, nil
	m.ws.base = m.docNow()
	m.link.wsStale.Store(true)
}

// emptyDoc is an empty document, as the window writes one.
var emptyDoc, _ = json.Marshal(workspaceDoc{})

// showDoc shows the document doc in place of the window's: the space shown
// stays shown, by its ID, if doc has it, and the panes shown are doc's for
// it, keeping open window-local tabs (new-session launch tabs, open diff
// tabs, and files with unsaved edits). UI goroutine.
func (m *model) showDoc(doc workspaceDoc) {
	shownKey, shown := m.ws.layoutKey, ggui.Peek(m.Layout).clone()
	prevFocus := ggui.Peek(m.FocusGroup)
	prevLayouts := m.ws.layouts
	m.Spaces.Set(doc.spaceList)
	m.ws.layouts = keyedLayouts(doc.Layouts)
	maps.Copy(m.tabNodes, doc.TabNodes)
	m.migrateProjectKeys()
	m.followSections()
	// Keep window-local transient tabs (launch tabs and open diff tabs) in
	// background layouts and secondary windows.
	for key, prev := range prevLayouts {
		if prev == nil || key == shownKey || !m.hasOpenTransient(prev) {
			continue
		}
		cur := m.ws.layouts[key]
		if sameLayout(nonNilLayout(cur), keptLayout(prev)) {
			m.ws.layouts[key] = prev
			continue
		}
		m.ws.layouts[key] = m.restoreTransientTabs(cur, prev, 0)
	}
	m.ws.layoutKey = m.shownKey()
	root := m.takeLayout(m.ws.layoutKey)
	if m.ws.layoutKey == shownKey {
		// If the persisted (kept) layout for the current view did not change,
		// keep the live layout intact so transient tabs, splits and focus are
		// untouched.
		if sameLayout(root, shown) || (m.hasOpenTransient(shown) && sameLayout(root, keptLayout(shown))) {
			m.bumpWorkspaceRev()
			return
		}
		if m.hasOpenTransient(shown) {
			root = m.restoreTransientTabs(root, shown, prevFocus)
			if sameLayout(root, shown) {
				m.bumpWorkspaceRev()
				return
			}
		}
	} else if m.hasOpenTransient(shown) {
		cur := m.ws.layouts[shownKey]
		if sameLayout(nonNilLayout(cur), keptLayout(shown)) {
			m.ws.layouts[shownKey] = shown
		} else {
			m.ws.layouts[shownKey] = m.restoreTransientTabs(cur, shown, prevFocus)
		}
	}
	// A file with edits not saved stays open, whatever the other node had.
	for _, id := range m.dirtyDocs() {
		if root.groupOf(id) == nil {
			g := root.groups()[0]
			g.insert(id, len(g.Tabs))
		}
	}
	focus := 0
	if m.ws.layoutKey == shownKey && root.group(prevFocus) != nil {
		focus = prevFocus
	}
	m.setLayoutFocus(root, focus)
}

// isOpenTransient reports whether id is an open window-local tab (a new
// session's launch tab or an open diff tab) not written down with the
// workspace.
func (m *model) isOpenTransient(id string) bool {
	switch {
	case isLaunchTab(id):
		return m.launches[id] != nil
	case isDiffTab(id):
		_, open := ggui.Peek(m.Diffs)[id]
		return open
	case isPluginTab(id):
		_, open := ggui.Peek(m.PluginTabs)[id]
		return open
	}
	return false
}

// hasOpenTransient reports whether root holds any open window-local tab.
func (m *model) hasOpenTransient(root *layoutNode) bool {
	if root == nil {
		return false
	}
	for _, g := range root.groups() {
		if slices.ContainsFunc(g.Tabs, m.isOpenTransient) {
			return true
		}
	}
	return false
}

// nonNilLayout returns root, or a fresh empty layout when root is nil.
func nonNilLayout(root *layoutNode) *layoutNode {
	if root == nil {
		return newLayout()
	}
	return root
}

// keptLayout returns root stripped of transient tabs and pruned, matching
// what workspaceDocNow writes down for a layout.
func keptLayout(root *layoutNode) *layoutNode {
	if root == nil {
		return newLayout()
	}
	c := root.clone()
	c.removeTabs(func(id string) bool { return !keptTab(id) })
	if c = c.prune(); c == nil {
		return newLayout()
	}
	return c
}

// restoreTransientTabs re-inserts open window-local tabs from prev into a
// copy of dst at their relative positions, keeping active transient tabs
// selected in their pane.
func (m *model) restoreTransientTabs(dst, prev *layoutNode, prevFocus int) *layoutNode {
	if prev == nil || !m.hasOpenTransient(prev) {
		return dst
	}
	out := nonNilLayout(dst).clone()
	for _, pg := range prev.groups() {
		if !slices.ContainsFunc(pg.Tabs, m.isOpenTransient) {
			continue
		}
		tg := out.group(pg.ID)
		if tg == nil {
			for _, id := range pg.Tabs {
				if keptTab(id) {
					if g := out.groupOf(id); g != nil {
						tg = g
						break
					}
				}
			}
		}
		if tg == nil {
			tg = out.groups()[0]
		}
		for i, id := range pg.Tabs {
			if !m.isOpenTransient(id) || out.groupOf(id) != nil {
				continue
			}
			at := len(tg.Tabs)
			found := false
			for j := i - 1; j >= 0; j-- {
				if k := slices.Index(tg.Tabs, pg.Tabs[j]); k >= 0 {
					at, found = k+1, true
					break
				}
			}
			if !found {
				for j := i + 1; j < len(pg.Tabs); j++ {
					if k := slices.Index(tg.Tabs, pg.Tabs[j]); k >= 0 {
						at = k
						break
					}
				}
			}
			tg.Tabs = slices.Insert(tg.Tabs, at, id)
			if tg.Active == "" {
				tg.Active = id
			}
		}
		if m.isOpenTransient(pg.Active) && (tg.ID == pg.ID || pg.ID == prevFocus) && slices.Contains(tg.Tabs, pg.Active) {
			tg.Active = pg.Active
		}
	}
	return out
}

// sameLayout reports whether two pane trees are the same, as written down.
func sameLayout(a, b *layoutNode) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
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

// workspaceSyncs is whether the node shares its workspace, as its
// configuration says, or, saying nothing, as the window's own setting
// from before did.
func workspaceSyncs(cfg *config.Config) bool {
	dir, err := config.Dir()
	if err != nil {
		return true
	}
	return cfg.Workspace.Syncs(dir)
}

// setWorkspaceSync has the node share its workspace or not: config.json
// says so, which the daemon follows as it changes. The window gives its
// daemon its edits and takes its workspace either way.
func (m *model) setWorkspaceSync(on bool) {
	cfg, err := config.Load()
	if err == nil {
		cfg.Workspace.Sync = &on
		err = config.Save(cfg)
	}
	if err != nil {
		if m.toaster != nil {
			m.toaster.Push(ui.Toast(Tf("Could not save: %v", err), "").Destructive())
		}
		return
	}
	m.cfg.Workspace = cfg.Workspace
	m.WorkspaceSync.Set(on)
}

// setPluginSync toggles peer-to-peer plugin config sync in config.json.
func (m *model) setPluginSync(on bool) {
	cfg, err := config.Load()
	if err == nil {
		cfg.Workspace.SyncPlugins = &on
		err = config.Save(cfg)
	}
	if err != nil {
		if m.toaster != nil {
			m.toaster.Push(ui.Toast(Tf("Could not save: %v", err), "").Destructive())
		}
		return
	}
	m.cfg.Workspace = cfg.Workspace
	m.PluginSync.Set(on)
}

// setAgentSync toggles peer-to-peer agent config sync in config.json.
func (m *model) setAgentSync(on bool) {
	cfg, err := config.Load()
	if err == nil {
		cfg.Workspace.SyncAgents = &on
		err = config.Save(cfg)
	}
	if err != nil {
		if m.toaster != nil {
			m.toaster.Push(ui.Toast(Tf("Could not save: %v", err), "").Destructive())
		}
		return
	}
	m.cfg.Workspace = cfg.Workspace
	m.AgentSync.Set(on)
}

// setProjectDirs persists this node's default project storage locations in config.json.
func (m *model) setProjectDirs(dirs []string) {
	norm := config.NormalizeProjectDirs(dirs)
	cfg, err := config.Load()
	if err == nil {
		cfg.ProjectDirs = norm
		err = config.Save(cfg)
	}
	if err != nil {
		if m.toaster != nil {
			m.toaster.Push(ui.Toast(Tf("Could not save: %v", err), "").Destructive())
		}
		return
	}
	m.cfg.ProjectDirs = slices.Clone(norm)
	m.ProjectDirs.Set(norm)
}

// reloadSyncedConfig reloads config.json after the daemon applies peer-synced
// agents, MCP servers, skills, or prompt templates, preserving any unsaved
// local edits in Settings.
func (m *model) reloadSyncedConfig() {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	hostDirty := ggui.Peek(m.Host) != draftFrom(m.cfg)
	agentsDirty := !sameAgents(ggui.Peek(m.AgentDraft), draftAgents(m.cfg.Agents))
	*m.cfg = *cfg
	m.Agents.Set(agentspec.Targets(cfg.Agents))
	m.WorkspaceSync.Set(workspaceSyncs(cfg))
	m.PluginSync.Set(cfg.Workspace.PluginsEnabled())
	m.AgentSync.Set(cfg.Workspace.AgentsEnabled())
	m.ProjectDirs.Set(config.NormalizeProjectDirs(cfg.ProjectDirs))
	m.SSHHosts.Set(config.NormalizeSSHHosts(cfg.SSHHosts))
	if !hostDirty {
		m.Host.Set(draftFrom(cfg))
	}
	if !agentsDirty {
		m.AgentDraft.Set(draftAgents(cfg.Agents))
	}
	if m.ext != nil {
		m.ext.MCPServers.Set(maps.Clone(cfg.ContextServers))
	}
	m.invalidateSessionTemplates()
}
