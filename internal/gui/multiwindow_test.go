package gui

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// TestDetachedPanePreservesPersistedLayout verifies the core invariant:
// detaching and returning split panes never mutates m.Layout or workspaceDocNow.
func TestDetachedPanePreservesPersistedLayout(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.ws.layoutKey = layoutKey(allSpaceID, "")
	m.showTab("s1")
	m.splitTab("s2", 1, dropRight)
	m.setRatio(ggui.Peek(m.Layout).ID, 0.35)

	groups := ggui.Peek(m.Layout).groups()
	if len(groups) != 2 {
		t.Fatalf("got %d panes, want 2", len(groups))
	}
	p1 := paneRef{Key: m.ws.layoutKey, GroupID: groups[0].ID}
	p2 := paneRef{Key: m.ws.layoutKey, GroupID: groups[1].ID}

	wantDoc, _ := json.Marshal(m.workspaceDocNow())
	wantLayout, _ := json.Marshal(ggui.Peek(m.Layout))

	// Pop out pane 2: persisted data must stay byte-for-byte identical,
	// while the view-only filter collapses the split to pane 1.
	m.popoutPane(p2)
	if !m.isPaneDetached(p2) {
		t.Fatal("pane 2 should be marked detached")
	}
	if got, _ := json.Marshal(m.workspaceDocNow()); !bytes.Equal(got, wantDoc) {
		t.Fatalf("workspaceDocNow mutated on popoutPane:\ngot:  %s\nwant: %s", got, wantDoc)
	}
	if got, _ := json.Marshal(ggui.Peek(m.Layout)); !bytes.Equal(got, wantLayout) {
		t.Fatalf("m.Layout mutated on popoutPane:\ngot:  %s\nwant: %s", got, wantLayout)
	}
	vis := visibleLayoutNode(ggui.Peek(m.Layout), ggui.Peek(m.DetachedPanes), m.ws.layoutKey)
	if vis == nil || vis.Group == nil || vis.Group.ID != p1.GroupID {
		t.Fatalf("visibleLayoutNode after detaching p2 = %+v, want single leaf p1 (%d)", vis, p1.GroupID)
	}

	// Pop out pane 1 as well: visible tree becomes nil, persisted data still untouched.
	m.popoutPane(p1)
	if visAll := visibleLayoutNode(ggui.Peek(m.Layout), ggui.Peek(m.DetachedPanes), m.ws.layoutKey); visAll != nil {
		t.Fatalf("visibleLayoutNode when all panes detached = %+v, want nil", visAll)
	}
	if got, _ := json.Marshal(m.workspaceDocNow()); !bytes.Equal(got, wantDoc) {
		t.Fatalf("workspaceDocNow mutated when all panes detached:\ngot:  %s\nwant: %s", got, wantDoc)
	}

	// Return both panes: exact original split & divider ratio come back.
	m.returnPane(p2)
	m.returnPane(p1)
	visRestored := visibleLayoutNode(ggui.Peek(m.Layout), ggui.Peek(m.DetachedPanes), m.ws.layoutKey)
	if visRestored == nil || visRestored.Group != nil || visRestored.Ratio != 0.35 || len(visRestored.groups()) != 2 {
		t.Fatalf("restored visibleLayoutNode = %+v, want original 2-pane split at ratio 0.35", visRestored)
	}
	if got, _ := json.Marshal(m.workspaceDocNow()); !bytes.Equal(got, wantDoc) {
		t.Fatalf("workspaceDocNow changed after returning panes:\ngot:  %s\nwant: %s", got, wantDoc)
	}
}

// TestDetachedPaneViewAndReturn verifies rendering of the main window,
// detached pane window (even after the main window switches layout keys),
// the all-detached placeholder, and restoration on dock/close.
func TestDetachedPaneViewAndReturn(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.ws.layoutKey = layoutKey(allSpaceID, "")
	m.SidebarOpen.Set(false)
	m.DetailsOpen.Set(false)
	m.Titles.Set(map[string]string{"s1": "alpha", "s2": "beta", "s3": "gamma"})
	m.showTab("s1")
	m.splitTab("s2", 1, dropRight)
	m.moveTab("s3", ggui.Peek(m.Layout).groups()[1].ID, 1)

	groups := ggui.Peek(m.Layout).groups()
	key := m.ws.layoutKey
	p1 := paneRef{Key: key, GroupID: groups[0].ID}
	p2 := paneRef{Key: key, GroupID: groups[1].ID}

	mainProbe := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 600))
	defer mainProbe.Close()
	if _, ok := mainProbe.Find("alpha"); !ok {
		t.Fatal("expected alpha in main window")
	}
	if _, ok := mainProbe.Find("beta"); !ok {
		t.Fatal("expected beta in main window")
	}

	// Pop out pane 2: main window hides beta/gamma while detached pane window shows them.
	m.popoutPane(p2)
	mainProbe.Frame()
	if _, ok := mainProbe.Find("alpha"); !ok {
		t.Fatal("alpha should remain in main window")
	}
	if _, ok := mainProbe.Find("beta"); ok {
		t.Fatal("beta should be hidden from main window while pane 2 is detached")
	}

	paneProbe := ggui.ProbeBuilder(func() ggui.Widget {
		return detachedPaneWindow(m, p2, func() { m.dockPane(p2) })
	}, ggui.Sz(800, 500))
	defer paneProbe.Close()
	if _, ok := paneProbe.Find("beta"); !ok {
		t.Fatal("expected beta in detached pane window")
	}
	if _, ok := paneProbe.Find(T("Return to main window")); !ok {
		t.Fatal("expected Return to main window button in detached pane window")
	}

	// Switch the main window to a different layout key; the detached pane window
	// must continue rendering and editing its own layout key independently.
	otherKey := layoutKey("other-space", "")
	m.showLayout(otherKey)
	paneProbe.Frame()
	if _, ok := paneProbe.Find("beta"); !ok {
		t.Fatal("detached pane window lost beta after main window switched layouts")
	}
	m.activateTabInKey(p2.Key, p2.GroupID, "s2")
	if got := m.peekLayoutForKey(p2.Key).group(p2.GroupID).Active; got != "s2" {
		t.Fatalf("detached pane active tab = %q, want s2", got)
	}
	if m.ws.layoutKey != otherKey {
		t.Fatalf("activateTabInKey hijacked main window layoutKey to %q", m.ws.layoutKey)
	}

	// Switch main window back and detach pane 1 too: placeholder should appear.
	m.showLayout(key)
	m.popoutPane(p1)
	mainProbe.Frame()
	if _, ok := mainProbe.Find(T("Return all panes")); !ok {
		t.Fatal("expected Return all panes button when every pane is detached")
	}

	// Docking all panes restores both alpha and beta to the main window.
	m.returnAllPanes(key)
	mainProbe.Frame()
	if _, ok := mainProbe.Find("alpha"); !ok {
		t.Fatal("alpha did not return to main window")
	}
	if _, ok := mainProbe.Find("beta"); !ok {
		t.Fatal("beta did not return to main window")
	}
}

// TestSpaceWindowIndependentFromMainWindow verifies opening a Space in its own
// window renders that Space's projects & sessions and edits its layout without
// altering the main window's active space or selection.
func TestSpaceWindowIndependentFromMainWindow(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Spaces.Set(spaceList{
		Spaces: []space{
			{ID: "sp-work", Name: "Work", Color: "blue", Owners: []string{"github.com/acme"}},
			{ID: "sp-oss", Name: "OSS", Color: "teal", Owners: []string{"github.com/oss"}},
		},
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-work", NodeId: "n1", Agent: "claude", Project: "/work/app", Owner: "github.com/acme", State: protocol.StateRunning},
		{Id: "s-oss", NodeId: "n1", Agent: "codex", Project: "/oss/mako", Owner: "github.com/oss", State: protocol.StateRunning},
	})
	m.Titles.Set(map[string]string{"s-work": "Work agent", "s-oss": "OSS agent"})
	m.Prefs.Update(func(p prefs) prefs { p.ShownSpace = "sp-work"; return p })
	m.ws.layoutKey = layoutKey("sp-work", "")
	m.showTab("s-work")

	sw := m.openSpaceWindow("sp-oss")
	if sw == nil {
		t.Fatal("openSpaceWindow returned nil")
	}
	if !ggui.Peek(m.SpaceWindows)["sp-oss"] {
		t.Fatal("sp-oss should be marked open in SpaceWindows")
	}
	// Main window must stay on sp-work.
	if got := m.spacesNow().spaceID(m.spacesNow().Space); got != "sp-work" {
		t.Fatalf("main window space changed to %q, want sp-work", got)
	}
	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("main window selection changed to %q, want s-work", got)
	}

	probe := ggui.ProbeBuilder(func() ggui.Widget { return spaceWindowView(m, sw) }, ggui.Sz(1000, 640))
	defer probe.Close()
	if _, ok := probe.Find("OSS agent"); !ok {
		t.Fatal("expected OSS agent in OSS space window")
	}
	if _, ok := probe.Find("Work agent"); ok {
		t.Fatal("Work agent should not appear in OSS space window")
	}

	// Selecting a session inside the Space window opens it in the Space's layout
	// without disturbing the main window.
	m.spaceWindowSelectSession(sw, "s-oss")
	probe.Frame()
	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("main window selection changed to %q after space window click, want s-work", got)
	}
	if got := m.spaceWindowActiveTab(sw); got != "s-oss" {
		t.Fatalf("space window active tab = %q, want s-oss", got)
	}
	ossKey := sw.peekLayoutKey()
	doc := m.workspaceDocNow()
	if root := doc.Layouts[ossKey]; root == nil || root.groupOf("s-oss") == nil {
		t.Fatalf("workspaceDocNow missing s-oss under key %q: %+v", ossKey, doc.Layouts)
	}

	// Closing the Space window removes it from SpaceWindows while preserving its layout.
	m.closeSpaceWindow("sp-oss")
	if ggui.Peek(m.SpaceWindows)["sp-oss"] {
		t.Fatal("sp-oss should be cleared from SpaceWindows after close")
	}
	if root := m.workspaceDocNow().Layouts[ossKey]; root == nil || root.groupOf("s-oss") == nil {
		t.Fatal("closing space window should not discard its saved layout")
	}
}
