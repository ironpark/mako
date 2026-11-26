package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func sidebarAndTabPlugin(state string) *protocol.PluginInfo {
	return &protocol.PluginInfo{
		Id:        "acme-tools",
		Version:   "1.0.0",
		Publisher: "acme",
		State:     state,
		Blocks: []*protocol.PluginBlock{
			{
				Name:  "plugin:acme-tools/explorer",
				Title: "Explorer",
				Icon:  "compass",
				Slots: []string{"sidebar"},
			},
			{
				Name:  "plugin:acme-tools/inspector",
				Title: "Inspector",
				Icon:  "microscope",
				Slots: []string{"tab"},
				Scope: protocol.ScopeProject,
			},
		},
	}
}

// Left sidebar page switcher stays invisible when no active plugin contributes
// a "sidebar" block, appears once one does, switches cleanly to the plugin
// page, and falls back to Projects when the plugin is disabled or removed.
func TestSidebarPluginPagesLifecycle(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	active := m.SidebarPage

	// 0 sidebar plugins -> switcher renders empty.
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return sidebarPageBar(m, active)
	}, ggui.Sz(272, 60))
	defer p.Close()
	p.Frame()
	if strings.Contains(p.Semantics().String(), "Projects") {
		t.Fatalf("switcher bar rendered with zero sidebar plugins:\n%s", p.Semantics().String())
	}

	// Enable plugin with a "sidebar" block -> switcher shows Projects + Explorer.
	m.setPlugins([]*protocol.PluginInfo{sidebarAndTabPlugin(protocol.PluginRunning)})
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	if !strings.Contains(sem, "Projects") || !strings.Contains(sem, "Explorer") {
		t.Fatalf("switcher bar missing Projects or Explorer:\n%s", sem)
	}

	// Opening the sidebar page sets SidebarOpen and SidebarPage.
	m.SidebarOpen.Set(false)
	m.openSidebarPage("plugin:acme-tools/explorer")
	if !ggui.Peek(m.SidebarOpen) || ggui.Peek(m.SidebarPage) != "plugin:acme-tools/explorer" {
		t.Fatalf("openSidebarPage state: open=%v page=%q", ggui.Peek(m.SidebarOpen), ggui.Peek(m.SidebarPage))
	}

	// Render the sidebar plugin page itself and verify provenance badge.
	pages := sidebarPluginBlocks(ggui.Peek(m.plugins.List))
	if len(pages) != 1 || pages[0].Icon != "compass" {
		t.Fatalf("sidebarPluginBlocks = %+v", pages)
	}
	pPage := ggui.ProbeBuilder(func() ggui.Widget {
		return sidebarPluginPageView(m, pages[0])
	}, ggui.Sz(272, 500))
	defer pPage.Close()
	for range 3 {
		pPage.Frame()
	}
	pageSem := pPage.Semantics().String()
	if !strings.Contains(pageSem, "Explorer") || !strings.Contains(pageSem, "acme-tools") {
		t.Fatalf("sidebar page missing title or provenance:\n%s", pageSem)
	}

	// Disabling the plugin removes it from sidebarPluginBlocks and hides the bar.
	m.setPlugins([]*protocol.PluginInfo{sidebarAndTabPlugin(protocol.PluginDisabled)})
	if got := sidebarPluginBlocks(ggui.Peek(m.plugins.List)); len(got) != 0 {
		t.Fatalf("disabled plugin still listed in sidebarPluginBlocks: %+v", got)
	}
	for range 3 {
		p.Frame()
	}
	if strings.Contains(p.Semantics().String(), "Explorer") {
		t.Fatalf("switcher kept disabled plugin:\n%s", p.Semantics().String())
	}
}

// Command palette exposes sidebar and custom tab actions for blocks declaring
// "sidebar" and "tab" slots.
func TestPluginPaletteIncludesSidebarAndTabBlocks(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.setPlugins([]*protocol.PluginInfo{sidebarAndTabPlugin(protocol.PluginRunning)})
	cmds := m.commands()
	var hasSidebar, hasTab bool
	for _, c := range cmds {
		if c.Category != "acme-tools" || c.Disabled {
			continue
		}
		if strings.Contains(c.ID, "Explorer") && strings.Contains(c.ID, "sidebar") {
			hasSidebar = true
		}
		if strings.Contains(c.ID, "Inspector") && strings.Contains(c.ID, "tab") {
			hasTab = true
		}
	}
	if !hasSidebar || !hasTab {
		t.Fatalf("palette missing sidebar=%v tab=%v", hasSidebar, hasTab)
	}
}

// Custom plugin tabs support multi-instance keys, preview replacement vs. keep,
// close_tab intents, project grouping via folder scope, and tab classification.
func TestCustomPluginTabLifecycleAndIntents(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.setPlugins([]*protocol.PluginInfo{sidebarAndTabPlugin(protocol.PluginRunning)})

	// Open two non-preview instances via plugin open intent.
	m.dispatchPluginOpen(&protocol.PluginOpen{
		Kind:   "tab",
		Block:  "plugin:acme-tools/inspector",
		Key:    "item-1",
		Title:  "Item 1",
		Icon:   "microscope",
		Params: `{"id":1}`,
		Scope:  protocol.FolderScope("local", "/repo/app"),
	})
	m.dispatchPluginOpen(&protocol.PluginOpen{
		Kind:   "tab",
		Block:  "plugin:acme-tools/inspector",
		Key:    "item-2",
		Title:  "Item 2",
		Params: `{"id":2}`,
	})

	id1 := pluginTabID("plugin:acme-tools/inspector", "item-1")
	id2 := pluginTabID("plugin:acme-tools/inspector", "item-2")
	tabs := ggui.Peek(m.Tabs)
	ptabs := ggui.Peek(m.PluginTabs)
	if len(tabs) != 2 || tabs[0] != id1 || tabs[1] != id2 {
		t.Fatalf("tabs = %v, want [%s %s]", tabs, id1, id2)
	}
	if ptabs[id1].Params != `{"id":1}` || ptabs[id2].Params != `{"id":2}` {
		t.Fatalf("params mismatch: %+v", ptabs)
	}
	if ptabs[id1].Root.Path != "/repo/app" {
		t.Fatalf("folder scope did not populate Root.Path: %+v", ptabs[id1].Root)
	}
	if isSessionTab(id1) || keptTab(id1) || !m.isOpenTransient(id1) {
		t.Fatalf("tab classification wrong for %s", id1)
	}
	if ptabs[id1].Preview {
		t.Fatal("non-preview plugin tab should not have Preview=true")
	}
	groups := []sessionGroup{
		{Key: "proj-a", Roots: []fileRoot{{Path: "/repo/app"}}},
		{Key: "proj-b", Roots: []fileRoot{{Path: "/repo/other"}}},
	}
	if sec, _ := tabPlace(id1, groups, nil, nil, ptabs); sec != 0 {
		t.Fatalf("tabPlace(folder-scoped) = %d, want 0", sec)
	}
	projTab := pluginTabID("plugin:acme-tools/inspector", "proj-b-item")
	ptabsWithProj := map[string]pluginTab{
		projTab: {Block: "plugin:acme-tools/inspector", Key: "proj-b-item", Scope: protocol.ScopeProject + ":proj-b"},
	}
	if sec, _ := tabPlace(projTab, groups, nil, nil, ptabsWithProj); sec != 1 {
		t.Fatalf("tabPlace(project-scoped) = %d, want 1", sec)
	}

	// Open a preview tab, then open another preview tab: the second replaces the first.
	m.dispatchPluginOpen(&protocol.PluginOpen{
		Kind:    "tab",
		Block:   "plugin:acme-tools/inspector",
		Key:     "prev-a",
		Title:   "Preview A",
		Preview: true,
	})
	idA := pluginTabID("plugin:acme-tools/inspector", "prev-a")
	if !ggui.Peek(m.PluginTabs)[idA].Preview {
		t.Fatal("preview plugin tab should have Preview=true")
	}
	m.dispatchPluginOpen(&protocol.PluginOpen{
		Kind:    "tab",
		Block:   "plugin:acme-tools/inspector",
		Key:     "prev-b",
		Title:   "Preview B",
		Preview: true,
	})
	idB := pluginTabID("plugin:acme-tools/inspector", "prev-b")
	ptabs = ggui.Peek(m.PluginTabs)
	if _, stillA := ptabs[idA]; stillA {
		t.Fatal("preview A was not replaced by preview B")
	}
	if _, hasB := ptabs[idB]; !hasB {
		t.Fatal("preview B missing")
	}

	// Pin preview B -> next preview C opens beside it instead of replacing.
	m.keepPluginTab(idB)
	m.dispatchPluginOpen(&protocol.PluginOpen{
		Kind:    "tab",
		Block:   "plugin:acme-tools/inspector",
		Key:     "prev-c",
		Title:   "Preview C",
		Preview: true,
	})
	idC := pluginTabID("plugin:acme-tools/inspector", "prev-c")
	ptabs = ggui.Peek(m.PluginTabs)
	if _, hasB := ptabs[idB]; !hasB {
		t.Fatal("kept tab B was replaced")
	}
	if _, hasC := ptabs[idC]; !hasC {
		t.Fatal("preview tab C missing")
	}

	// Close via close_tab intent.
	m.dispatchPluginOpen(&protocol.PluginOpen{
		Kind:  "close_tab",
		Block: "plugin:acme-tools/inspector",
		Key:   "item-1",
	})
	if _, still1 := ggui.Peek(m.PluginTabs)[id1]; still1 {
		t.Fatal("close_tab intent did not close item-1")
	}
}

// Smoke & interaction test for all 9 new revision-49 UI components and
// upgraded existing components (code syntax/copy, search/password inputs,
// card header, sortable striped table, progress ring, underline tabs).
func TestPluginTreeDrawsRev49Components(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	tree := &protocol.UiNode{Kind: "column", Gap: 8, Children: []*protocol.UiNode{
		{Id: "/h", Kind: "text", Text: "Deploy Overview", Variant: "heading"},
		{Id: "/m", Kind: "text", Text: "sha256:abcdef", Variant: "mono"},
		{Id: "/cap", Kind: "text", Text: "Synced just now", Variant: "caption"},
		{Id: "/al", Kind: "alert", Text: "Heads up", Placeholder: "Canary rollout active", Tone: "warning", Variant: "soft", Icon: "alert-triangle", Change: true},
		{Id: "/crd", Kind: "card", Text: "Metrics", Icon: "activity", Badges: []*protocol.UiBadge{{Text: "live", Tone: "success"}}, Children: []*protocol.UiNode{
			{Id: "/st", Kind: "stat", Text: "Latency", Value: "42 ms", Placeholder: "-8% vs baseline", Icon: "gauge", Tone: "success"},
		}},
		{Id: "/em", Kind: "empty", Text: "Nothing queued", Placeholder: "All background jobs finished", Icon: "check-circle"},
		{Id: "/df", Kind: "diff", DiffFrom: "old.go", DiffTo: "new.go", Language: "go", Variant: "unified", Change: true, DiffLines: []*protocol.UiDiffLine{
			{Op: "@", Text: "@@ -1,2 +1,2 @@"},
			{Op: " ", Text: "package main", OldLine: 1, NewLine: 1},
			{Op: "-", Text: "x := 1", OldLine: 2},
			{Op: "+", Text: "x := 2", NewLine: 2},
		}},
		{Id: "/rd", Kind: "radio", Value: "fast", Options: []string{"fast", "safe"}, Direction: "horizontal", Change: true},
		{Id: "/sl", Kind: "slider", Text: "Workers", Value: "4", Min: 1, Max: 16, Step: 1, Change: true},
		{Id: "/kb", Kind: "kbd", Options: []string{"Cmd", "Shift", "P"}},
		{Id: "/sk", Kind: "skeleton", Variant: "text"},
		{Id: "/spRow", Kind: "row", Children: []*protocol.UiNode{
			{Id: "/spL", Kind: "text", Text: "LeftSide"},
			{Id: "/sp", Kind: "spacer"},
			{Id: "/spR", Kind: "text", Text: "RightSide"},
		}},
		{Id: "/srch", Kind: "input", Value: "query", Variant: "search", Icon: "search", Placeholder: "Find…", Change: true},
		{Id: "/pw", Kind: "input", Value: "secret", Variant: "password", Placeholder: "API key", Change: true},
		{Id: "/ta", Kind: "textarea", Value: "SELECT 1", Variant: "mono", Change: true},
		{Id: "/tb", Kind: "tabs", Value: "Summary", Options: []string{"Summary", "Events"}, Variant: "underline", Change: true},
		{Id: "/pr", Kind: "progress", Progress: 0.65, Variant: "ring", Text: "65%"},
		{Id: "/cd", Kind: "code", Text: "package main\nfunc main() {}", Language: "go", MaxHeight: 160},
		{Id: "/tbl", Kind: "table", Labels: []string{"Service", "Latency"}, Value: "Latency:asc", Variant: "striped", Change: true, Children: []*protocol.UiNode{
			{Id: "/tbl/r1", Kind: "row", Children: []*protocol.UiNode{
				{Id: "/tbl/r1/a", Kind: "text", Text: "gateway", Grow: 1},
				{Id: "/tbl/r1/b", Kind: "text", Text: "18ms"},
			}},
		}},
	}}

	m := newModel(config.Default(), "")
	type ev struct{ node, kind, value string }
	var events []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			m:     m,
			block: "plugin:acme-tools/inspector",
			form:  form,
			onEvent: func(_, node, kind, value string, _ map[string]string) {
				events = append(events, ev{node, kind, value})
			},
		}
		return v.node(tree, false)
	}, ggui.Sz(560, 1800))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	sem := p.Semantics().String()
	for _, want := range []string{
		"Deploy Overview", "sha256:abcdef", "Synced just now",
		"Heads up", "Canary rollout active",
		"Metrics", "Latency", "42 ms",
		"Nothing queued", "All background jobs finished",
		"old.go", "new.go", "x := 2",
		"fast", "safe", "Workers",
		"LeftSide", "RightSide",
		"Summary", "Events", "65%",
		"go", "Service", "gateway",
	} {
		if !strings.Contains(sem, want) {
			t.Errorf("rendered tree missing %q in:\n%s", want, sem)
		}
	}

	// Clicking a sortable table column header emits a change event toggling sort direction.
	if btn, ok := p.Semantics().Find(ggui.RoleButton, "Sort by Service"); ok {
		p.Click(ggui.Pt(btn.Rect.Origin.X+btn.Rect.Size.W/2, btn.Rect.Origin.Y+btn.Rect.Size.H/2))
		p.Frame()
		foundSort := false
		for _, e := range events {
			if e.node == "/tbl" && e.kind == protocol.UiChange && e.value == "Service" {
				foundSort = true
			}
		}
		if !foundSort {
			t.Errorf("clicking Service header did not fire sort event; got %+v", events)
		}
	} else {
		t.Errorf("sortable table header button 'Sort by Service' not found in semantics")
	}
}
