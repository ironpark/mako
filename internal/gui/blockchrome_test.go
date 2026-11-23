package gui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

func TestSoloDockHeaderForOneChildStackAndBareBlock(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	for _, tc := range []struct {
		name   string
		layout string
	}{
		{
			name: "one-child stack",
			layout: `{"version":1,"root":{"id":"window","row":[
				{"id":"workspace","column":[{"panes":"main","grow":1}],"grow":1},
				{"id":"details","stack":["mako.files"],"size":320,"collapsible":true}]}}`,
		},
		{
			name: "bare chromePanel block",
			layout: `{"version":1,"root":{"id":"window","row":[
				{"id":"workspace","column":[{"panes":"main","grow":1}],"grow":1},
				{"id":"solo-files","block":"mako.files","size":320,"collapsible":true}]}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, p := windowProbe(t, windowView, withLayout(t, tc.layout))
			sem := p.Semantics().String()

			// Solo dock header shows the panel title as text (not a tab button) and the ⋯ menu.
			titleNode, ok := p.Semantics().Find(ggui.RoleText, "Files")
			if !ok {
				t.Fatalf("expected solo dock header title \"Files\":\n%s", sem)
			}
			if titleNode.Rect.Origin.Y >= headerHeight {
				t.Fatalf("dock header title Y=%v, expected inside headerHeight (%v)", titleNode.Rect.Origin.Y, headerHeight)
			}
			menuNode, ok := p.Semantics().Find(ggui.RoleMenu, "Menu")
			if !ok {
				t.Fatalf("expected solo dock header overflow menu:\n%s", sem)
			}
			if menuNode.Rect.Origin.Y >= headerHeight {
				t.Fatalf("dock header menu Y=%v, expected inside headerHeight (%v)", menuNode.Rect.Origin.Y, headerHeight)
			}
			// No tab strip button named "Files" in solo placement.
			if _, ok := p.Semantics().Find(ggui.RoleButton, "Files"); ok {
				t.Fatalf("unexpected tab button \"Files\" in solo placement:\n%s", sem)
			}

			// Double-clicking the dock header handle toggles the collapsible drawer closed.
			center := ggui.Pt(titleNode.Rect.Origin.X+titleNode.Rect.Size.W/2, titleNode.Rect.Origin.Y+titleNode.Rect.Size.H/2)
			p.Click(center)
			p.Frame()
			p.Click(center)
			p.Frame()

			doc := ggui.Peek(m.WinDoc)
			var panelNode *layoutdoc.Node
			if n := doc.Node(layoutdoc.DetailsNode); n != nil {
				panelNode = n
			} else {
				panelNode = doc.Node("solo-files")
			}
			if ggui.Peek(m.nodeState(panelNode).open) {
				t.Fatalf("expected double-clicking solo dock header to collapse %s", panelNode.ID)
			}
		})
	}
}

func TestWrapInStackDragToTabbedAndCloseLayoutBlock(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	// Start with a bare panel block at the bottom of workspace and a multi-tab details stack.
	layoutJSON := `{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[
			{"panes":"main","grow":1},
			{"id":"bottom-files","block":"mako.files","size":220,"collapsible":true}],"grow":1},
		{"id":"details","stack":["mako.agents","mako.forge"],"size":320,"collapsible":true}]}}`

	m, p := windowProbe(t, windowView, func(m *model) {
		withLayout(t, layoutJSON)(m)
	})
	dispose := ggui.Root(func() { followWindowLayout(m) })
	t.Cleanup(dispose)

	// Verify wrapInStack transfers Size, Grow, and Collapsible onto the materialized stack.
	cloned := ggui.Peek(m.WinDoc).Clone()
	st := wrapInStack(cloned, "bottom-files")
	if st == nil || st.Kind != layoutdoc.KindStack || st.Size != 220 || !st.Collapsible {
		t.Fatalf("wrapInStack returned unexpected stack: %+v", st)
	}
	if len(st.Children) != 1 || st.Children[0].ID != "bottom-files" || st.Children[0].Size != 0 || st.Children[0].Collapsible {
		t.Fatalf("wrapped child kept layout sizing/collapsible: %+v", st.Children)
	}

	// Dropping mako.agents from details onto the bare bottom-files panel turns it into a tabbed stack.
	m.moveStackBlock(layoutdoc.DetailsNode, agentsTab, "bottom-files", -1)
	for range 3 {
		p.Frame()
	}
	doc := ggui.Peek(m.WinDoc)
	bottomPath := doc.BlockPath(filesTab)
	if len(bottomPath) < 2 {
		t.Fatalf("files block missing after move: %v", bottomPath)
	}
	bottomStack := bottomPath[len(bottomPath)-2]
	if bottomStack.Kind != layoutdoc.KindStack || len(bottomStack.Children) != 2 {
		t.Fatalf("expected bare panel to become 2-tab stack, got %+v", bottomStack)
	}
	// Details stack now has 1 child (mako.forge) so it automatically renders as solo (no tab button "GitHub").
	if det := doc.Node(layoutdoc.DetailsNode); det == nil || len(det.Children) != 1 {
		t.Fatalf("expected details stack to have 1 child, got %+v", det)
	}
	if _, ok := p.Semantics().Find(ggui.RoleButton, "GitHub"); ok {
		t.Fatalf("1-child stack should render solo dock header, not tab button:\n%s", p.Semantics().String())
	}
	// Bottom stack now has 2 children so it renders tab buttons for Files and Agents.
	if _, ok := p.Semantics().Find(ggui.RoleButton, "Files"); !ok {
		t.Fatalf("expected Files tab button in newly tabbed bottom stack:\n%s", p.Semantics().String())
	}
	if _, ok := p.Semantics().Find(ggui.RoleButton, "Agents"); !ok {
		t.Fatalf("expected Agents tab button in newly tabbed bottom stack:\n%s", p.Semantics().String())
	}

	// Dragging a bare panel block (StackID == "") into a stack works via lazy wrapInStack.
	m.placePluginBlock("plugin:kanban/board", "bottom")
	doc = ggui.Peek(m.WinDoc)
	boardPath := doc.BlockPath("plugin:kanban/board")
	if len(boardPath) == 0 {
		t.Fatal("failed to place bare plugin block")
	}
	boardID := boardPath[len(boardPath)-1].ID
	m.moveStackBlock("", boardID, layoutdoc.DetailsNode, -1)
	doc = ggui.Peek(m.WinDoc)
	det := doc.Node(layoutdoc.DetailsNode)
	if det == nil || len(det.Children) != 2 || det.Children[1].Block != "plugin:kanban/board" {
		t.Fatalf("expected bare plugin block moved into details stack, got %+v", det)
	}

	// closeLayoutBlock removes a built-in block cleanly.
	m.closeLayoutBlock(forgeTab)
	doc = ggui.Peek(m.WinDoc)
	if doc.BlockPath(forgeTab) != nil {
		t.Fatal("expected forgeTab to be removed by closeLayoutBlock")
	}
}

func TestThirdPartyProvenanceBadgeSoloAndTabbed(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	kanban := &protocol.PluginInfo{
		Id: "kanban", Version: "1.0.0", Publisher: "acme", State: protocol.PluginRunning,
		Blocks: []*protocol.PluginBlock{{Name: "plugin:kanban/board", Title: "Board", Scope: protocol.ScopeProject}},
	}
	bundled := &protocol.PluginInfo{
		Id: "mako-git", Version: "1.0.0", Publisher: "mako", State: protocol.PluginRunning, Bundled: true,
		Blocks: []*protocol.PluginBlock{{Name: gitPluginBlock, Title: "Git", Scope: protocol.ScopeFolder}},
	}

	// 1. Solo third-party plugin shows non-droppable provenance badge ("Plugin · kanban" + chip "kanban").
	soloLayout := `{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[{"panes":"main","grow":1}],"grow":1},
		{"id":"board","block":"plugin:kanban/board","size":360}]}}`
	m, p := windowProbe(t, windowView, func(m *model) {
		m.setPlugins([]*protocol.PluginInfo{kanban, bundled})
		withLayout(t, soloLayout)(m)
	})
	sem := p.Semantics().String()
	if !strings.Contains(sem, "Plugin · kanban") || !strings.Contains(sem, `text "kanban"`) {
		t.Fatalf("solo third-party plugin missing provenance badge:\n%s", sem)
	}
	if !isThirdPartyPluginBlock(m, "plugin:kanban/board") {
		t.Fatal("expected kanban to be identified as third-party plugin")
	}
	if isThirdPartyPluginBlock(m, gitPluginBlock) {
		t.Fatal("expected bundled mako-git not to be identified as third-party plugin")
	}

	// 2. Tabbed stack with third-party plugin shows provenance badge in page bar and context menu items.
	tabbedLayout := `{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[{"panes":"main","grow":1}],"grow":1},
		{"id":"details","stack":["mako.files","plugin:kanban/board"],"active":1,"size":360}]}}`
	m2, p2 := windowProbe(t, windowView, func(m *model) {
		m.setPlugins([]*protocol.PluginInfo{kanban, bundled})
		withLayout(t, tabbedLayout)(m)
	})
	sem2 := p2.Semantics().String()
	if !strings.Contains(sem2, "Plugin · kanban") || !strings.Contains(sem2, `text "kanban"`) {
		t.Fatalf("tabbed third-party plugin missing provenance badge:\n%s", sem2)
	}
	if _, ok := p2.Semantics().Find(ggui.RoleButton, "Board"); !ok {
		t.Fatalf("expected Board tab button in tabbed stack:\n%s", sem2)
	}

	// Verify tab context menu items for third-party scoped dev plugin include Reload block and Pin scope.
	def, ok := m2.blocks.lookup("plugin:kanban/board")
	if !ok {
		t.Fatal("kanban block not registered")
	}
	bv := def.Build(m2, nil, blockPlace{Solo: false})
	doc2 := ggui.Peek(m2.WinDoc)
	det2 := doc2.Node(layoutdoc.DetailsNode)
	items := tabContextMenuItems(m2, det2, det2.Children[1], bv.Menu)
	menuProbe := ggui.NewProbe(widget.Column(items...), ggui.Sz(260, 400))
	defer menuProbe.Close()
	menuProbe.Frame()
	menuSem := menuProbe.Semantics().String()
	for _, want := range []string{"Move to split above", "Move to split below", "Reload block", "Pin scope", "Close panel"} {
		if !strings.Contains(menuSem, want) {
			t.Errorf("tab context menu missing %q:\n%s", want, menuSem)
		}
	}

	// 3. Bundled mako.git solo shows no third-party provenance badge.
	gitSoloLayout := `{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[{"panes":"main","grow":1}],"grow":1},
		{"id":"git-solo","block":"mako.git","size":360}]}}`
	_, p3 := windowProbe(t, windowView, func(m *model) {
		m.setPlugins([]*protocol.PluginInfo{bundled})
		withLayout(t, gitSoloLayout)(m)
	})
	if strings.Contains(p3.Semantics().String(), "Plugin ·") {
		t.Fatalf("bundled mako.git unexpectedly showed third-party provenance badge:\n%s", p3.Semantics().String())
	}
}

func TestLiftToolbarInSoloVsTabbedChromeAndWatchBlockStandalone(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	type watchCall struct {
		block      string
		scope      string
		standalone bool
	}
	var mu sync.Mutex
	var calls []watchCall

	toolbarTree := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Fill: true,
		Children: []*protocol.UiNode{
			{
				Id:      "/tb",
				Kind:    "row",
				Variant: "toolbar",
				Children: []*protocol.UiNode{
					{Id: "/tb/branch", Kind: "button", Text: "main", Tap: true},
					{Id: "/tb/push", Kind: "button", Text: "Push", Tap: true},
				},
			},
			{
				Id:   "/body",
				Kind: "text",
				Text: "Working tree clean",
			},
		},
	}

	prevHook := watchBlockFunc
	watchBlockFunc = func(ctx context.Context, block, scope, locale string, standalone bool, fn func(*protocol.BlockUpdate)) error {
		mu.Lock()
		calls = append(calls, watchCall{block: block, scope: scope, standalone: standalone})
		mu.Unlock()
		fn(&protocol.BlockUpdate{
			Block:  block,
			Scope:  scope,
			Title:  "Git",
			State:  protocol.PluginRunning,
			Chrome: "toolbar",
			Tree:   toolbarTree,
		})
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { watchBlockFunc = prevHook })

	var postMu sync.Mutex
	var posted []func()
	enqueuePost := func(fn func()) {
		postMu.Lock()
		posted = append(posted, fn)
		postMu.Unlock()
	}
	drainPosts := func() {
		for {
			postMu.Lock()
			if len(posted) == 0 {
				postMu.Unlock()
				return
			}
			batch := posted
			posted = nil
			postMu.Unlock()
			for _, fn := range batch {
				fn()
			}
		}
	}

	waitForUpdate := func(t *testing.T, m *model, p *ggui.Probe, n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			drainPosts()
			p.Frame()
			mu.Lock()
			got := len(calls)
			mu.Unlock()
			if got >= n && m.blockTreeFor(gitPluginBlock) != nil {
				for range 3 {
					drainPosts()
					p.Frame()
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d watch calls and tree update", n)
	}

	// 1. Solo mako.git: WatchBlock.Standalone == true, toolbar lifted into dock header,
	// title "Git" omitted due to Chrome=="toolbar", toolbar buttons appear once (not duplicated in Body).
	soloLayout := `{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[{"panes":"main","grow":1}],"grow":1},
		{"id":"details","stack":["mako.git"],"size":400}]}}`
	m, p := windowProbe(t, windowView, func(m *model) {
		m.post = enqueuePost
		m.Sessions.Set([]*protocol.SessionInfo{{Id: "s1", Agent: "claude", NodeId: "n1", Project: "/repo/app"}})
		m.Selected.Set("s1")
		withLayout(t, soloLayout)(m)
	})
	waitForUpdate(t, m, p, 1)

	mu.Lock()
	first := calls[0]
	mu.Unlock()
	if first.block != gitPluginBlock || !first.standalone {
		t.Fatalf("solo watch call = %+v, want block=%q standalone=true", first, gitPluginBlock)
	}
	wantScope := protocol.FolderScope("n1", "/repo/app")
	if first.scope != wantScope {
		t.Fatalf("solo watch scope = %q, want %q", first.scope, wantScope)
	}

	sem := p.Semantics().String()
	// Title "Git" is omitted in dock header when Chrome=="toolbar".
	if _, ok := p.Semantics().Find(ggui.RoleText, "Git"); ok {
		t.Fatalf("expected title \"Git\" omitted when Chrome==\"toolbar\":\n%s", sem)
	}
	// Toolbar buttons appear inside headerHeight and only once (stripped from Body).
	branchBtn, ok := p.Semantics().Find(ggui.RoleButton, "main")
	if !ok {
		t.Fatalf("missing lifted toolbar button \"main\":\n%s", sem)
	}
	if branchBtn.Rect.Origin.Y >= headerHeight {
		t.Fatalf("lifted toolbar button Y=%v, expected inside dock header (%v)", branchBtn.Rect.Origin.Y, headerHeight)
	}
	if n := strings.Count(sem, `button "Push"`); n != 1 {
		t.Fatalf("expected Push button once (lifted, not duplicated in body), got %d:\n%s", n, sem)
	}
	if !strings.Contains(sem, "Working tree clean") {
		t.Fatalf("missing body text:\n%s", sem)
	}
	// Test hook blockTreeFor returns the active full tree (both without and with explicit scope).
	if got := m.blockTreeFor(gitPluginBlock); got != toolbarTree {
		t.Fatalf("blockTreeFor(%q) = %v, want toolbarTree", gitPluginBlock, got)
	}
	if got := m.blockTreeFor(gitPluginBlock, wantScope); got != toolbarTree {
		t.Fatalf("blockTreeFor(%q, %q) = %v, want toolbarTree", gitPluginBlock, wantScope, got)
	}

	// 2. Tabbed mako.git: WatchBlock.Standalone == false, tab strip on top, lifted toolbar in slim bar below tabs.
	tabbedLayout := `{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[{"panes":"main","grow":1}],"grow":1},
		{"id":"details","stack":["mako.files","mako.git"],"active":1,"size":400}]}}`
	m2, p2 := windowProbe(t, windowView, func(m *model) {
		m.post = enqueuePost
		m.Sessions.Set([]*protocol.SessionInfo{{Id: "s1", Agent: "claude", NodeId: "n1", Project: "/repo/app"}})
		m.Selected.Set("s1")
		withLayout(t, tabbedLayout)(m)
	})
	waitForUpdate(t, m2, p2, 2)

	mu.Lock()
	second := calls[1]
	mu.Unlock()
	if second.block != gitPluginBlock || second.standalone {
		t.Fatalf("tabbed watch call = %+v, want block=%q standalone=false", second, gitPluginBlock)
	}
	sem2 := p2.Semantics().String()
	tabBtn, ok := p2.Semantics().Find(ggui.RoleButton, "Git")
	if !ok {
		t.Fatalf("missing Git tab button in tabbed stack:\n%s", sem2)
	}
	branchBtn2, ok := p2.Semantics().Find(ggui.RoleButton, "main")
	if !ok {
		t.Fatalf("missing lifted toolbar button \"main\" in tabbed mode:\n%s", sem2)
	}
	if branchBtn2.Rect.Origin.Y <= tabBtn.Rect.Origin.Y {
		t.Fatalf("expected lifted toolbar (Y=%v) below tab strip (Y=%v)", branchBtn2.Rect.Origin.Y, tabBtn.Rect.Origin.Y)
	}
	if n := strings.Count(sem2, `button "Push"`); n != 1 {
		t.Fatalf("expected Push button once in tabbed mode, got %d:\n%s", n, sem2)
	}
}

func TestFolderScopeGUIResolutionOptionsAndLabels(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(config.Default(), "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "local-node"}})
	m.Nodes.Set([]*protocol.Node{{Id: "local-node", Name: "Mac", Source: "self"}, {Id: "peer-1", Name: "Linux", Trusted: true, TrustsUs: true}})
	m.Spaces.Set(spaceList{
		Projects: []project{{
			ID: "mono", Name: "Mono",
			Roots: []projectRoot{
				{Label: "web", Locations: []fileRoot{{Node: "local-node", Path: "/src/web"}}},
				{Label: "api", Locations: []fileRoot{{Node: "peer-1", Path: "/srv/api"}}},
			},
		}},
	})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s1", Agent: "claude", NodeId: "local-node", Project: "/src/web"}})

	// No session selected -> empty active folder scope.
	if got := m.resolveActiveScope(protocol.ScopeFolder); got != "" {
		t.Fatalf("resolveActiveScope(folder) with no selection = %q, want empty", got)
	}

	// Select local session -> resolves to local folder scope key.
	m.Selected.Set("s1")
	localKey := protocol.FolderScope("", "/src/web")
	peerKey := protocol.FolderScope("peer-1", "/srv/api")
	if got := m.resolveActiveScope(protocol.ScopeFolder); got != localKey {
		t.Fatalf("resolveActiveScope(folder) = %q, want %q", got, localKey)
	}

	// Switching RepoPick to the peer root updates resolveActiveScope(folder).
	m.RepoPick.Set(repoPick{For: fileRoot{Node: "local-node", Path: "/src/web"}, At: fileRoot{Node: "peer-1", Path: "/srv/api"}})
	if got := m.resolveActiveScope(protocol.ScopeFolder); got != peerKey {
		t.Fatalf("resolveActiveScope(folder) after RepoPick = %q, want %q", got, peerKey)
	}

	// scopeOptions lists both project roots with canonical folder keys and labels.
	opts := m.scopeOptions(protocol.ScopeFolder)
	if len(opts) != 2 {
		t.Fatalf("scopeOptions(folder) = %+v, want 2 roots", opts)
	}
	if opts[0].Key != localKey || opts[0].Label != "web" {
		t.Fatalf("opts[0] = %+v, want key=%q label=web", opts[0], localKey)
	}
	if opts[1].Key != peerKey || !strings.Contains(opts[1].Label, "api") {
		t.Fatalf("opts[1] = %+v, want key=%q label containing api", opts[1], peerKey)
	}

	// scopeLabel resolves known roots by label and unknown paths by projectName(path).
	if got := m.scopeLabel(localKey); got != "web" {
		t.Fatalf("scopeLabel(localKey) = %q, want web", got)
	}
	otherKey := protocol.FolderScope("", "/tmp/other-repo")
	if got := m.scopeLabel(otherKey); got != "other-repo" {
		t.Fatalf("scopeLabel(otherKey) = %q, want other-repo", got)
	}
}

func TestStaticTitleInToolsHidesHeaderTitle(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	m := newModel(config.Default(), "")
	def := &blockDef{Name: "mako.custom", Title: "CustomTitle", Chrome: chromePanel, Icon: "git-branch"}
	child := &layoutdoc.Node{Kind: layoutdoc.KindBlock, ID: "c1", Block: "mako.custom"}
	bv := blockView{
		Body:         widget.Text("Panel body"),
		Tools:        ui.Button("BranchPicker", func() {}).Ghost(),
		TitleInTools: true,
	}
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return soloPanel(m, nil, child, def, bv)
	}, ggui.Sz(360, 240))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	if strings.Contains(sem, "CustomTitle") {
		t.Fatalf("expected title hidden when TitleInTools is true:\n%s", sem)
	}
	if _, ok := p.Semantics().Find(ggui.RoleButton, "BranchPicker"); !ok {
		t.Fatalf("expected Tools widget in dock header:\n%s", sem)
	}
}

func TestBundledPluginSettingsCard(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	bundled := &protocol.PluginInfo{
		Id:          "mako-git",
		Version:     "1.0.0",
		Publisher:   "mako",
		State:       protocol.PluginRunning,
		Bundled:     true,
		Permissions: []string{"git:read", "git:write", "forge:read"},
		Granted:     []string{"git:read", "git:write", "forge:read"},
	}

	for _, storeOpen := range []bool{false, true} {
		name := "store-closed"
		if storeOpen {
			name = "store-open"
		}
		t.Run(name, func(t *testing.T) {
			m := newModel(config.Default(), "")
			if storeOpen {
				m.store.Registries.Set([]*protocol.RegistryInfo{{Url: "https://example.test/reg.git"}})
			}
			p := ggui.ProbeBuilder(func() ggui.Widget {
				return pluginCardBody(m, bundled)
			}, ggui.Sz(720, 600))
			defer p.Close()
			for range 3 {
				p.Frame()
			}
			sem := p.Semantics().String()
			for _, want := range []string{"mako-git", "Built-in", "comes with mako", "Included with Mako."} {
				if !strings.Contains(sem, want) {
					t.Errorf("bundled card missing %q:\n%s", want, sem)
				}
			}
			for _, forbid := range []string{"Revoke", "Revoking them stops it.", "Remove", "Reload", "in development"} {
				if strings.Contains(sem, forbid) {
					t.Errorf("bundled card should not contain %q:\n%s", forbid, sem)
				}
			}
		})
	}
}

func TestTreeBlockViewPreservesFeedAndDraftAcrossSoloTabbedTransition(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	treeWithDraft := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Fill: true,
		Children: []*protocol.UiNode{
			{
				Id:      "/tb",
				Kind:    "row",
				Variant: "toolbar",
				Children: []*protocol.UiNode{
					{Id: "/tb/branch", Kind: "button", Text: "main", Tap: true},
				},
			},
			{Id: "/status", Kind: "text", Text: "Changes ready"},
			{Id: "/commit-msg", Kind: "textarea", Value: "", Placeholder: "Commit message", Change: true},
		},
	}

	var mu sync.Mutex
	watchCount := 0
	holdSecond := make(chan struct{})
	prevHook := watchBlockFunc
	watchBlockFunc = func(ctx context.Context, block, scope, locale string, standalone bool, fn func(*protocol.BlockUpdate)) error {
		mu.Lock()
		watchCount++
		n := watchCount
		mu.Unlock()
		if n == 2 {
			// Delay the second stream's first update so we observe the immediate
			// post-remount frame before the daemon answers.
			select {
			case <-holdSecond:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		fn(&protocol.BlockUpdate{
			Block:  block,
			Scope:  scope,
			Title:  "Git",
			State:  protocol.PluginRunning,
			Chrome: "toolbar",
			Tree:   treeWithDraft,
		})
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() {
		watchBlockFunc = prevHook
	})

	var postMu sync.Mutex
	var posted []func()
	enqueuePost := func(fn func()) {
		postMu.Lock()
		posted = append(posted, fn)
		postMu.Unlock()
	}
	drainPosts := func() {
		for {
			postMu.Lock()
			if len(posted) == 0 {
				postMu.Unlock()
				return
			}
			batch := posted
			posted = nil
			postMu.Unlock()
			for _, fn := range batch {
				fn()
			}
		}
	}

	soloLayout := `{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[{"panes":"main","grow":1}],"grow":1},
		{"id":"details","stack":["mako.git"],"size":400}]}}`
	m, p := windowProbe(t, windowView, func(m *model) {
		m.post = enqueuePost
		m.Sessions.Set([]*protocol.SessionInfo{{Id: "s1", Agent: "claude", NodeId: "n1", Project: "/repo/app"}})
		m.Selected.Set("s1")
		withLayout(t, soloLayout)(m)
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		drainPosts()
		p.Frame()
		if strings.Contains(p.Semantics().String(), "Changes ready") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(p.Semantics().String(), "Changes ready") {
		t.Fatalf("initial solo tree never rendered:\n%s", p.Semantics().String())
	}

	// Simulate user typing a commit message draft into the cached form.
	scopeKey := protocol.FolderScope("n1", "/repo/app")
	cached := m.cachedBlock(gitPluginBlock, scopeKey)
	if cached == nil || cached.form == nil {
		t.Fatalf("expected cached block state for (%q, %q)", gitPluginBlock, scopeKey)
	}
	cached.form.field("/commit-msg", "").Set("feat: keep my draft")
	p.Frame()
	if !strings.Contains(p.Semantics().String(), "feat: keep my draft") {
		t.Fatalf("typed draft not visible before transition:\n%s", p.Semantics().String())
	}

	// Transition solo -> tabbed by mutating WinDoc (second watch is held back).
	tabbedDoc, err := layoutdoc.Parse([]byte(`{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[{"panes":"main","grow":1}],"grow":1},
		{"id":"details","stack":["mako.files","mako.git"],"active":1,"size":400}]}}`), m.blocks)
	if err != nil {
		t.Fatalf("parse tabbed layout: %v", err)
	}
	m.WinDoc.Set(tabbedDoc)
	for range 3 {
		drainPosts()
		p.Frame()
	}

	// Immediately after remount (while stream 2 has NOT delivered an update yet):
	// zero "Starting…" flash, tree still rendered, and commit draft preserved!
	semMid := p.Semantics().String()
	if strings.Contains(semMid, "Starting…") {
		t.Fatalf("unexpected Starting… flash during solo->tabbed remount:\n%s", semMid)
	}
	if !strings.Contains(semMid, "Changes ready") {
		t.Fatalf("cached tree lost during solo->tabbed remount:\n%s", semMid)
	}
	if !strings.Contains(semMid, "feat: keep my draft") {
		t.Fatalf("commit draft lost during solo->tabbed remount:\n%s", semMid)
	}

	// Release stream 2 and verify draft survives the fresh BlockUpdate too.
	close(holdSecond)
	for range 5 {
		drainPosts()
		p.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	semAfter := p.Semantics().String()
	if !strings.Contains(semAfter, "feat: keep my draft") {
		t.Fatalf("commit draft overwritten after fresh BlockUpdate:\n%s", semAfter)
	}
}
