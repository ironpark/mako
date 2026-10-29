package gui

import (
	"maps"
	"slices"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/protocol"
)

// Leftover tabs: a layout written down, or another node's, can hold tabs
// of sessions that are gone, killed while the window was closed or by
// another window. Such a tab has nothing to attach to; it leaves once its
// session has been missing from the mesh's list for leftoverAfter while
// the session's node was live. A tab whose node is not live stays, as its
// session may run on, out of reach. The node of each session tab is
// written down with the workspace for this, as a tab's ID does not say it;
// a tab whose node was never written down is taken for gone as one whose
// node is live.

// leftoverAfter is how long a tab's session is missing before the tab
// leaves: longer than a peer's state stays fresh (stateFresh in the
// daemon), so that a node just come back has sent its sessions.
const leftoverAfter = 30 * time.Second

// liveWithin is how recently a node was seen to be taken for live.
const liveWithin = 20 * time.Second

// liveNodes are the nodes whose sessions the mesh's list would hold: this
// daemon's, and those the window can drive and has seen of late.
func liveNodes(self *protocol.Node, nodes []*protocol.Node, now time.Time) map[string]bool {
	live := map[string]bool{self.GetId(): true}
	for _, n := range nodes {
		if reachable(n) && now.Sub(protocol.TimeOf(n.GetLastSeen())) <= liveWithin {
			live[n.GetId()] = true
		}
	}
	return live
}

// allTabs are the tabs of every layout, the one shown and those of the
// spaces not shown.
func (m *model) allTabs() []string {
	tabs := slices.Clone(ggui.Peek(m.Tabs))
	for _, root := range m.layouts {
		for _, g := range root.groups() {
			tabs = append(tabs, g.Tabs...)
		}
	}
	return tabs
}

// dropLeftoverTabs notes the node of each session tab the list has, and
// closes, in every layout, the tabs whose sessions have been gone long
// enough. UI goroutine.
func (m *model) dropLeftoverTabs(sessions []*protocol.SessionInfo, live map[string]bool, now time.Time) {
	tabs := slices.DeleteFunc(m.allTabs(), func(id string) bool { return !isSessionTab(id) })
	maps.DeleteFunc(m.missing, func(id string, _ time.Time) bool { return !slices.Contains(tabs, id) })
	gone := map[string]bool{}
	for _, id := range tabs {
		if s, ok := sessionIn(sessions, id); ok {
			delete(m.missing, id)
			if m.tabNodes[id] != s.GetNodeId() {
				m.tabNodes[id] = s.GetNodeId()
				m.workspaceChanged()
			}
			continue
		}
		if node, known := m.tabNodes[id]; known && !live[node] {
			delete(m.missing, id) // out of reach, not gone
			continue
		}
		since, ok := m.missing[id]
		if !ok {
			m.missing[id] = now
			continue
		}
		if now.Sub(since) >= leftoverAfter {
			gone[id] = true
		}
	}
	if len(gone) == 0 {
		return
	}
	for id := range gone {
		delete(m.missing, id)
		delete(m.tabNodes, id)
		m.closeTab(id)
	}
	for key, root := range m.layouts {
		root = root.clone()
		root.removeTabs(func(id string) bool { return gone[id] })
		m.layouts[key] = root
	}
	m.workspaceChanged()
}

// keptTabNodes are the nodes of the session tabs among layouts, as the
// workspace writes them down.
func (m *model) keptTabNodes(layouts map[string]*layoutNode) map[string]string {
	kept := map[string]string{}
	for _, root := range layouts {
		for _, g := range root.groups() {
			for _, id := range g.Tabs {
				if node, ok := m.tabNodes[id]; ok {
					kept[id] = node
				}
			}
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}
