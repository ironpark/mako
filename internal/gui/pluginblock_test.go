package gui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

// ciPlugin is a plugin as the daemon lists it.
func ciPlugin(state string) *protocol.PluginInfo {
	return &protocol.PluginInfo{Id: "ci-status", Version: "0.1.0", Publisher: "ironpark", State: state,
		Permissions: []string{"net:api.github.com", "storage"}, Asked: []string{"net:api.github.com", "storage"},
		Blocks:   []*protocol.PluginBlock{{Name: "plugin:ci-status/ci", Title: "CI"}},
		Commands: []*protocol.PluginCommand{{Id: "rerun", Title: "Re-run the latest failed CI run"}},
		Settings: []*protocol.PluginSetting{{Key: "repo", Type: "string", Label: "Repository"}, {Key: "token", Type: "secret", Label: "GitHub token", Set: true}}}
}

// A document naming a plugin's block draws it missing until the daemon
// lists the plugin, framed as the plugin's once it does, and missing again
// once it goes.
func TestPluginBlocksComeAndGo(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m, p := windowProbe(t, windowView, withLayout(t, `{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[{"panes":"main","grow":1},{"id":"ci","block":"plugin:ci-status/ci","size":200}],"grow":1}]}}`))
	shows := func(text string) bool { return strings.Contains(p.Semantics().String(), text) }
	if !shows("Missing block") {
		t.Fatalf("no missing block:\n%s", p.Semantics().String())
	}
	m.setPlugins([]*protocol.PluginInfo{ciPlugin(protocol.PluginReady)})
	for range 3 {
		p.Frame()
	}
	if shows("Missing block") || !shows("CI") || !shows("ci-status") || !shows("Plugin") {
		t.Fatalf("the plugin's block is not framed as its:\n%s", p.Semantics().String())
	}
	m.setPlugins(nil)
	for range 3 {
		p.Frame()
	}
	if !shows("Missing block") {
		t.Fatalf("the block stayed:\n%s", p.Semantics().String())
	}
}

// A tree of every kind is drawn with the window's components, an icon
// not bundled as a square, and the frame names the plugin.
func TestPluginTreeIsDrawn(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	tree := &protocol.UiNode{Kind: "column", Children: []*protocol.UiNode{
		{Id: "/h", Kind: "row", Children: []*protocol.UiNode{
			{Id: "/h/i", Kind: "icon", Icon: "no-such-icon"},
			{Id: "/h/t", Kind: "text", Text: "Latest runs", Weight: "medium", Grow: 1},
			{Id: "/h/b", Kind: "button", Text: "Refresh", Icon: "refresh-cw", Tap: true},
		}},
		{Id: "/l", Kind: "list", Children: []*protocol.UiNode{
			{Id: "/l/1", Kind: "row", Tap: true, Children: []*protocol.UiNode{
				{Id: "/l/1/p", Kind: "pill", Text: "failure", Tone: "danger"},
				{Id: "/l/1/c", Kind: "chip", Text: "main"},
			}},
		}},
		{Id: "/q", Kind: "input", Text: "Filter", Change: true},
		{Id: "/o", Kind: "toggle", Text: "Only failures", Checked: true, Change: true},
		{Id: "/s", Kind: "select", Value: "main", Options: []string{"main", "dev"}, Change: true},
		{Id: "/m", Kind: "markdown", Text: "**bold** words"},
		{Id: "/c", Kind: "code", Text: "go test ./..."},
		{Id: "/g", Kind: "progress", Progress: 0.4},
		{Id: "/d", Kind: "divider"},
		{Id: "/x", Kind: "stack", Children: []*protocol.UiNode{{Id: "/x/a", Kind: "text", Text: "under"}}},
		{Id: "/ch", Kind: "chart", Chart: "bar", Text: "Builds a day", Labels: []string{"mon", "tue"},
			Series: []*protocol.UiSeries{{Name: "passed", Tone: "success", Values: []float64{3, 5}}, {Name: "failed", Values: []float64{1, 0}}}},
		{Id: "/cl", Kind: "chart", Chart: "line", Series: []*protocol.UiSeries{{Values: []float64{1, 4, 2}}}},
		{Id: "/cs", Kind: "chart", Chart: "sparkline", Text: "Load", Series: []*protocol.UiSeries{{Tone: "accent", Values: []float64{1, 4, 2, 8}}}},
		{Id: "/tb", Kind: "tabs", Value: "Overview", Options: []string{"Overview", "Logs"}, Change: true},
		{Id: "/sc", Kind: "scroll", Direction: "vertical", MaxHeight: 160, Children: []*protocol.UiNode{
			{Id: "/sc/card", Kind: "card", Children: []*protocol.UiNode{
				{Id: "/sc/card/t", Kind: "text", Text: "Card body"},
				{Id: "/sc/card/ta", Kind: "textarea", Value: "Notes", Text: "Add notes", Change: true},
			}},
		}},
		{Id: "/tbl", Kind: "table", Labels: []string{"Job", "Result"}, Children: []*protocol.UiNode{
			{Id: "/tbl/r1", Kind: "row", Children: []*protocol.UiNode{
				{Id: "/tbl/r1/a", Kind: "text", Text: "unit-tests", Grow: 1},
				{Id: "/tbl/r1/b", Kind: "pill", Text: "passed", Tone: "success"},
			}},
		}},
	}}
	m := newModel(config.Default(), "")
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return pluginFrame(m, "plugin:ci-status/ci", "ci-status", "CI", &protocol.BlockUpdate{Title: "CI", State: protocol.PluginRunning, Tree: tree})
	}, ggui.Sz(420, 1500))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	for _, want := range []string{"CI", "ci-status", "Latest runs", "Refresh", "failure", "main", "Only failures", "bold", "go test", "Builds a day", "Load", "Overview", "Card body", "unit-tests"} {
		if !strings.Contains(sem, want) {
			t.Errorf("no %q in:\n%s", want, sem)
		}
	}
}

// What a plugin asks for is drawn by the window in its block's place, and
// over the window.
func TestPluginAskIsTheWindows(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.setPlugins([]*protocol.PluginInfo{ciPlugin(protocol.PluginWaiting)})
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Provide(ggui.ReducedMotionKey, true, pluginAskBar(m))
	}, ggui.Sz(1000, 300))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	for _, want := range []string{"ci-status", "Connect to api.github.com", "Keep data on this computer", "Allow", "Deny", "Later"} {
		if !strings.Contains(sem, want) {
			t.Errorf("no %q in:\n%s", want, sem)
		}
	}
	m.plugins.Later.Set(map[string]bool{"ci-status": true})
	for range 3 {
		p.Frame()
	}
	if strings.Contains(p.Semantics().String(), "Allow") {
		t.Fatal("the bar stayed once put off")
	}
}

// The plugins' commands are in the palette under their plugin's name,
// and Settings lists Plugins only while there are any.
func TestPluginCommandsAndSettings(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	count := func(groups []settingsGroup) int {
		n := 0
		for _, g := range groups {
			n += len(g.sections)
		}
		return n
	}
	if count(settingsGroups(false)) != count(settingsNav) {
		t.Fatal("Plugins is listed with no plugins")
	}
	if count(settingsGroups(true)) != count(settingsNav)+1 {
		t.Fatal("Plugins is not listed with plugins")
	}
	before := len(m.commands())
	m.setPlugins([]*protocol.PluginInfo{ciPlugin(protocol.PluginRunning)})
	cmds := m.commands()
	if len(cmds) != before+2 {
		t.Fatalf("%d commands, %d before (expected +1 plugin command +1 block open)", len(cmds), before)
	}
	foundCmd, foundOpen := false, false
	for _, c := range cmds {
		if c.Category == "ci-status" && c.ID == "Re-run the latest failed CI run" && !c.Disabled {
			foundCmd = true
		}
		if c.Category == "ci-status" && strings.Contains(c.ID, "CI") && !c.Disabled {
			foundOpen = true
		}
	}
	if !foundCmd || !foundOpen {
		t.Fatalf("missing palette entries: cmd=%v open=%v", foundCmd, foundOpen)
	}
	m.setPlugins([]*protocol.PluginInfo{ciPlugin(protocol.PluginWaiting)})
	for _, c := range m.commands() {
		if c.Category == "ci-status" && c.ID == "Re-run the latest failed CI run" && !c.Disabled {
			t.Fatal("a waiting plugin's command can be run")
		}
	}
}

// A plugin's Markdown fetches no picture: it would reach a host the
// plugin was not allowed, with what the plugin put in the address.
// Local Mermaid diagrams are rendered in-process (zero network), and onLink
// intercepts non-http(s) links.
func TestPluginMarkdownFetchesNothing(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.post = func(fn func()) { fn() }
	var clicked []string
	host := pluginPage{
		pageHost: pageHost{m: m},
		onLink:   func(dest string) { clicked = append(clicked, dest) },
	}
	for _, src := range []string{"https://evil.example/leak?d=secret", "/etc/hosts", "../x.png"} {
		if host.Image(src) != nil {
			t.Errorf("%s was fetched", src)
		}
	}
	if host.Diagram("graph TD; a-->b", "mermaid") == nil {
		t.Error("local mermaid diagram was not rendered")
	}
	if host.Diagram("print(1)", "python") != nil {
		t.Error("non-diagram code block returned a diagram image")
	}
	host.Open("okf:architecture/plugin-system.md")
	host.Open("docs/guide.md")
	if len(clicked) != 2 || clicked[0] != "okf:architecture/plugin-system.md" || clicked[1] != "docs/guide.md" {
		t.Fatalf("onLink clicks = %v", clicked)
	}
}

// An op that does not apply to the tree held closes the block's stream
// and opens it again at once, for a whole tree, rather than going on
// with a stream that will send only ops.
func TestABlockStreamStartsAgainAfterAnOpThatDoesNotApply(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opened := make(chan int, 4)
	n := 0
	watch := func(ctx context.Context, each func(*protocol.BlockUpdate)) error {
		n++
		opened <- n
		switch n {
		case 1:
			each(&protocol.BlockUpdate{Tree: &protocol.UiNode{Kind: "column", Children: []*protocol.UiNode{{Id: "/a", Kind: "text", Text: "one"}}}})
			each(&protocol.BlockUpdate{Ops: []*protocol.UiOp{{Op: protocol.UiRemove, Id: "/missing"}}})
			each(&protocol.BlockUpdate{Ops: []*protocol.UiOp{{Op: protocol.UiRemove, Id: "/a"}}})
		case 2:
			each(&protocol.BlockUpdate{Tree: &protocol.UiNode{Kind: "column", Children: []*protocol.UiNode{{Id: "/b", Kind: "text", Text: "two"}}}})
		}
		<-ctx.Done() // a stream sends until it is closed
		return ctx.Err()
	}
	var trees []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		followStream(ctx, watch, time.Hour, func(u *protocol.BlockUpdate) {
			trees = append(trees, u.GetTree().GetChildren()[0].GetText())
			if len(trees) == 2 {
				cancel()
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the stream was not opened again; opened %d times", len(opened))
	}
	if strings.Join(trees, ",") != "one,two" || len(opened) != 2 {
		t.Fatalf("trees %v, opened %d times", trees, len(opened))
	}
}

// Dragging a draggable node onto a drop zone of the same block fires a drop
// BlockEvent carrying the dragged node's payload, while clicking still fires
// its tap event.
func TestPluginBlockDragAndDrop(t *testing.T) {
	tree := &protocol.UiNode{Kind: "row", Gap: 12, Children: []*protocol.UiNode{
		{Id: "/todo", Kind: "column", Grow: 1, Drop: true, Children: []*protocol.UiNode{
			{Id: "/todo/h", Kind: "text", Text: "Todo"},
			{Id: "/todo/c1", Kind: "column", Drag: "c1", Tap: true, Drop: true, Children: []*protocol.UiNode{
				{Id: "/todo/c1/t", Kind: "text", Text: "Card One"},
			}},
		}},
		{Id: "/done", Kind: "column", Grow: 1, Drop: true, Children: []*protocol.UiNode{
			{Id: "/done/h", Kind: "text", Text: "Done"},
			{Id: "/done/empty", Kind: "text", Text: "Drop here"},
		}},
	}}
	type ev struct{ block, node, kind, value string }
	var got []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:kanban/board",
			form:  form,
			onEvent: func(block, node, kind, value string, _ map[string]string) {
				got = append(got, ev{block, node, kind, value})
			},
		}
		return v.node(tree, false)
	}, ggui.Sz(600, 300))
	defer p.Close()
	p.Frame()

	card, ok := p.Semantics().Find(ggui.RoleButton, "Card One")
	if !ok {
		t.Fatalf("no Card One:\n%s", p.Semantics().String())
	}
	target, ok := p.Semantics().Find(ggui.RoleText, "Drop here")
	if !ok {
		t.Fatalf("no Drop here:\n%s", p.Semantics().String())
	}
	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}
	// Click still fires tap.
	p.Click(center(card.Rect))
	p.Frame()
	if len(got) != 1 || got[0] != (ev{"plugin:kanban/board", "/todo/c1", protocol.UiTap, ""}) {
		t.Fatalf("tap events = %+v", got)
	}
	// Dragging from Card One to the Done column fires a drop event.
	got = nil
	from := center(card.Rect)
	to := center(target.Rect)
	p.Press(from)
	p.Frame()
	p.Move(ggui.Pt((from.X+to.X)/2, (from.Y+to.Y)/2))
	p.Frame()
	p.Move(to)
	p.Frame()
	p.Release(to)
	p.Frame()
	if len(got) != 1 || got[0] != (ev{"plugin:kanban/board", "/done", protocol.UiDrop, "c1"}) {
		t.Fatalf("drop events = %+v, want drop of c1 on /done", got)
	}
}

// Drag groups filter which drop zones accept a dragged item: a node tagged
// dragGroup="card" is rejected by dropAccept=["lane"] and accepted by
// dropAccept=["card"].
func TestPluginBlockDragGroupFiltering(t *testing.T) {
	tree := &protocol.UiNode{Kind: "row", Gap: 12, Children: []*protocol.UiNode{
		{Id: "/src", Kind: "column", Grow: 1, Children: []*protocol.UiNode{
			{Id: "/src/c1", Kind: "card", Drag: "c1", DragGroup: "card", Children: []*protocol.UiNode{
				{Id: "/src/c1/t", Kind: "text", Text: "Card One"},
			}},
		}},
		{Id: "/wrong", Kind: "column", Grow: 1, Drop: true, DropAccept: []string{"lane"}, Children: []*protocol.UiNode{
			{Id: "/wrong/t", Kind: "text", Text: "Lane only"},
		}},
		{Id: "/right", Kind: "column", Grow: 1, Drop: true, DropAccept: []string{"card"}, Children: []*protocol.UiNode{
			{Id: "/right/t", Kind: "text", Text: "Cards ok"},
		}},
	}}
	type ev struct{ node, kind, value string }
	var got []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:kanban/board",
			form:  form,
			onEvent: func(_, node, kind, value string, _ map[string]string) {
				got = append(got, ev{node, kind, value})
			},
		}
		return v.node(tree, false)
	}, ggui.Sz(600, 260))
	defer p.Close()
	p.Frame()

	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}
	drag := func(from, to ggui.Point) {
		p.Press(from)
		p.Frame()
		p.Move(ggui.Pt((from.X+to.X)/2, (from.Y+to.Y)/2))
		p.Frame()
		p.Move(to)
		p.Frame()
		p.Release(to)
		p.Frame()
	}

	card, ok := p.Semantics().Find(ggui.RoleText, "Card One")
	if !ok {
		t.Fatalf("no Card One:\n%s", p.Semantics().String())
	}
	wrong, ok := p.Semantics().Find(ggui.RoleText, "Lane only")
	if !ok {
		t.Fatalf("no Lane only:\n%s", p.Semantics().String())
	}
	right, ok := p.Semantics().Find(ggui.RoleText, "Cards ok")
	if !ok {
		t.Fatalf("no Cards ok:\n%s", p.Semantics().String())
	}

	drag(center(card.Rect), center(wrong.Rect))
	if len(got) != 0 {
		t.Fatalf("drop onto non-matching group fired: %+v", got)
	}
	drag(center(card.Rect), center(right.Rect))
	if len(got) != 1 || got[0] != (ev{"/right", protocol.UiDrop, "c1"}) {
		t.Fatalf("drop onto matching group = %+v", got)
	}
}

// Scoped blocks with no active target render an empty-scope card (with a quick
// Pin selector when targets exist) instead of starting a stream.
func TestScopedBlockEmptyPlaceholder(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")
	m := newModel(config.Default(), "")
	m.Spaces.Set(spaceList{
		Spaces: []space{{ID: "sp1", Name: "Work"}},
		Projects: []project{
			{ID: "alpha", Name: "Alpha", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/src/alpha"}}}}},
		},
	})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s1", Agent: "claude", Project: "/src/alpha", NodeId: "n1"}})

	cases := []struct {
		scope    string
		wantText string
		wantChip string
		wantPin  string
	}{
		{protocol.ScopeProject, "Select a project to view this block", "No project", "Pin to a project"},
		{protocol.ScopeSpace, "Select a space to view this block", "No space", "Pin to a space"},
		{protocol.ScopeSession, "Select a session to view this block", "No session", "Pin to a session"},
	}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			p := ggui.ProbeBuilder(func() ggui.Widget {
				return pluginBlock(m, "plugin:kanban/board", "kanban", "Board", tc.scope, nil)
			}, ggui.Sz(420, 320))
			defer p.Close()
			for range 3 {
				p.Frame()
			}
			sem := p.Semantics().String()
			for _, want := range []string{tc.wantText, tc.wantChip, tc.wantPin, "Block scope"} {
				if !strings.Contains(sem, want) {
					t.Errorf("missing %q in:\n%s", want, sem)
				}
			}
			if strings.Contains(sem, "Starting…") {
				t.Errorf("empty-scope block unexpectedly rendered Starting…:\n%s", sem)
			}
		})
	}
}

// Scoped blocks resolve Follow vs Pin targets across active space, project,
// and session, display the active target name in the header chip, and persist
// pinned scope props into the active layout document via layoutdoc.Apply.
func TestScopedBlockHeaderChipFollowAndPin(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")
	layoutJSON := `{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[{"panes":"main","grow":1},{"id":"board","block":"plugin:kanban/board","size":220}],"grow":1}]}}`
	m, p := windowProbe(t, windowView, withLayout(t, layoutJSON))

	// Register a project-scoped plugin block and two projects.
	m.Spaces.Update(func(l spaceList) spaceList {
		l.Spaces = []space{{ID: "sp1", Name: "Work"}}
		l.Projects = []project{
			{ID: "alpha", Name: "Alpha", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/src/alpha"}}}}},
			{ID: "beta", Name: "Beta", Roots: []projectRoot{{Locations: []fileRoot{{Node: "n1", Path: "/src/beta"}}}}},
		}
		return l
	})
	m.setPlugins([]*protocol.PluginInfo{{
		Id: "kanban", Version: "0.3.0", Publisher: "ironpark", State: protocol.PluginRunning,
		Blocks: []*protocol.PluginBlock{{Name: "plugin:kanban/board", Title: "Board", Scope: protocol.ScopeProject}},
	}})
	for range 3 {
		p.Frame()
	}

	// Initially no active project -> empty-scope placeholder & "No project" chip.
	sem := p.Semantics().String()
	if !strings.Contains(sem, "Select a project to view this block") || !strings.Contains(sem, "No project") {
		t.Fatalf("expected empty project scope state:\n%s", sem)
	}

	// Focus a session belonging to project Alpha -> Follow mode resolves to project:alpha.
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-alpha", Agent: "claude", NodeId: "n1", Project: "/src/alpha"},
		{Id: "s-beta", Agent: "claude", NodeId: "n1", Project: "/src/beta"},
	})
	m.Selected.Set("s-alpha")
	for range 3 {
		p.Frame()
	}
	sem = p.Semantics().String()
	if strings.Contains(sem, "Select a project to view this block") {
		t.Fatalf("expected active project to leave empty-scope state:\n%s", sem)
	}
	if !strings.Contains(sem, "Alpha") {
		t.Fatalf("expected header scope chip to show Alpha:\n%s", sem)
	}

	// Verify active space and session resolution helpers too.
	m.setSpace(1)
	if got := m.resolveActiveScope(protocol.ScopeSpace); got != "space:sp1" {
		t.Fatalf("resolveActiveScope(space) = %q, want space:sp1", got)
	}
	if got := m.resolveActiveScope(protocol.ScopeSession); got != "session:s-alpha" {
		t.Fatalf("resolveActiveScope(session) = %q, want session:s-alpha", got)
	}

	// Selecting project Beta in the sidebar updates Follow mode to project:beta.
	m.Scope.Set(sidebarScope{Space: "sp1", scopeKey: scopeKey{Section: "beta"}})
	if got := m.resolveActiveScope(protocol.ScopeProject); got != "project:beta" {
		t.Fatalf("resolveActiveScope(project) with sidebar scope = %q, want project:beta", got)
	}
	for range 3 {
		p.Frame()
	}
	if sem = p.Semantics().String(); !strings.Contains(sem, "Beta") {
		t.Fatalf("expected header scope chip to follow Beta:\n%s", sem)
	}

	// Pinning the block to project:alpha persists props["scope"] via layoutdoc.Apply + setWindowLayout.
	m.setPluginBlockScope("plugin:kanban/board", "", "project:alpha")
	raw := ggui.Peek(m.Spaces).WindowLayouts[layoutdoc.DefaultName]
	doc, err := layoutdoc.Parse(raw, m.blocks)
	if err != nil {
		t.Fatalf("pinned layout failed to parse: %v\n%s", err, string(raw))
	}
	node := doc.Node("board")
	if node == nil || blockPropScope(node.Props) != "project:alpha" {
		t.Fatalf("pinned node props = %#v, want scope=project:alpha", node)
	}

	// Applying the updated layout keeps the block pinned to Alpha even while Beta is active.
	m.showWindowLayout(winSource{body: string(raw)})
	for range 3 {
		p.Frame()
	}
	if sem = p.Semantics().String(); !strings.Contains(sem, "Block scope: Alpha (pinned)") {
		t.Fatalf("expected pinned block header chip to show Alpha (pinned):\n%s", sem)
	}

	// Unpinning clears props["scope"] so the block follows Beta again.
	m.setPluginBlockScope("plugin:kanban/board", "project:alpha", "")
	raw = ggui.Peek(m.Spaces).WindowLayouts[layoutdoc.DefaultName]
	doc, err = layoutdoc.Parse(raw, m.blocks)
	if err != nil {
		t.Fatalf("unpinned layout failed to parse: %v", err)
	}
	if node := doc.Node("board"); node == nil || blockPropScope(node.Props) != "" {
		t.Fatalf("unpinned node still has scope prop: %#v", node)
	}
}

// Scoped settings render a scope selector on the plugin card, display
// global defaults vs scoped overrides (including secrets), and show a
// Reset to default button only when overridden.
func TestPluginScopedSettingsUI(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")
	m := newModel(config.Default(), "")
	m.Spaces.Set(spaceList{
		Projects: []project{
			{ID: "alpha", Name: "Alpha"},
			{ID: "beta", Name: "Beta"},
		},
	})
	scopedPlugin := &protocol.PluginInfo{
		Id: "ci-status", Version: "0.2.0", Publisher: "ironpark", State: protocol.PluginRunning,
		Settings: []*protocol.PluginSetting{
			{
				Key: "repo", Type: "string", Label: "Repository", Value: "default/repo", Scope: protocol.ScopeProject,
				ScopedValues: map[string]string{"project:alpha": "acme/alpha"},
			},
			{
				Key: "branch", Type: "string", Label: "Branch", Value: "main", Scope: protocol.ScopeProject,
			},
			{
				Key: "token", Type: "secret", Label: "GitHub token", Set: true, Scope: protocol.ScopeProject,
				ScopedSet: []string{"project:alpha"},
			},
			{
				Key: "interval", Type: "number", Label: "Poll seconds", Value: "60", Scope: protocol.ScopeGlobal,
			},
		},
	}
	globalPlugin := ciPlugin(protocol.PluginRunning)

	// Global-only plugin does not show the Settings scope selector.
	gp := ggui.ProbeBuilder(func() ggui.Widget {
		return pluginCardBody(m, globalPlugin)
	}, ggui.Sz(720, 600))
	defer gp.Close()
	for range 3 {
		gp.Frame()
	}
	if strings.Contains(gp.Semantics().String(), "Settings scope") {
		t.Fatalf("global-only plugin unexpectedly showed Settings scope:\n%s", gp.Semantics().String())
	}

	// Plugin with scoped settings shows scope selector and switches cleanly between scopes.
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return pluginCardBody(m, scopedPlugin)
	}, ggui.Sz(760, 820))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	for _, want := range []string{"Settings scope", "Global default", "Repository", "default/repo", "GitHub token"} {
		if !strings.Contains(sem, want) {
			t.Errorf("global view missing %q:\n%s", want, sem)
		}
	}
	if strings.Contains(sem, "Reset to default") {
		t.Errorf("global view should not show Reset to default:\n%s", sem)
	}

	// Switch to project:alpha (has repo & token overrides, inherits branch, interval is global).
	m.setPluginSettingScope("ci-status", "project:alpha")
	for range 3 {
		p.Frame()
	}
	sem = p.Semantics().String()
	for _, want := range []string{"Alpha", "acme/alpha", "Overridden", "Inherits default", "Applies globally", "Reset to default"} {
		if !strings.Contains(sem, want) {
			t.Errorf("project:alpha view missing %q:\n%s", want, sem)
		}
	}

	// Switch to project:beta (no overrides -> inherits defaults, no Reset to default).
	m.setPluginSettingScope("ci-status", "project:beta")
	for range 3 {
		p.Frame()
	}
	sem = p.Semantics().String()
	for _, want := range []string{"Beta", "default/repo", "Inherits default", "Applies globally"} {
		if !strings.Contains(sem, want) {
			t.Errorf("project:beta view missing %q:\n%s", want, sem)
		}
	}
	if strings.Contains(sem, "Reset to default") || strings.Contains(sem, "acme/alpha") {
		t.Errorf("project:beta view leaked alpha override:\n%s", sem)
	}
}

// Regression test: horizontal ScrollWidgets inside ui.tabs and horizontal
// ui.scroll must stay at their intrinsic height inside a fill:true root column
// instead of expanding to the full panel height and hiding the scrolling body.
func TestPluginTabsAndHorizontalScrollInsideFillRootKeepCompactHeight(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")
	m := newModel(config.Default(), "")
	tree := &protocol.UiNode{
		Id:   "",
		Kind: "column",
		Fill: true,
		Gap:  8,
		Children: []*protocol.UiNode{
			{
				Id:   "/topbar",
				Kind: "row",
				Gap:  6,
				Children: []*protocol.UiNode{
					{
						Id:      "/topbar/tabs",
						Kind:    "tabs",
						Value:   "Browse",
						Options: []string{"Browse", "Tree", "Graph", "Health"},
						Change:  true,
					},
					{Id: "/topbar/summary", Kind: "text", Text: "7 docs", Grow: 1},
					{Id: "/topbar/new", Kind: "button", Text: "New", Size: "small", Variant: "ghost", Tap: true},
				},
			},
			{
				Id:        "/hstrip",
				Kind:      "scroll",
				Direction: "horizontal",
				Children: []*protocol.UiNode{
					{Id: "/hstrip/c1", Kind: "chip", Text: "#okf"},
					{Id: "/hstrip/c2", Kind: "chip", Text: "#arch"},
				},
			},
			{
				Id:   "/body",
				Kind: "scroll",
				Grow: 1,
				Children: []*protocol.UiNode{
					{Id: "/body/item", Kind: "text", Text: "Scrollable Body Content"},
				},
			},
		},
	}
	u := &protocol.BlockUpdate{
		Block: "plugin:okf/vault",
		Title: "OKF Knowledge",
		State: protocol.PluginRunning,
		Tree:  tree,
	}
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return pluginFrame(m, "plugin:okf/vault", "okf", "OKF Knowledge", u)
	}, ggui.Sz(480, 700))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	browseBtn, ok := p.Semantics().Find(ggui.RoleButton, "Browse")
	if !ok {
		t.Fatalf("missing Browse tab button:\n%s", p.Semantics().String())
	}
	bodyText, ok := p.Semantics().Find(ggui.RoleText, "Scrollable Body Content")
	if !ok {
		t.Fatalf("missing Scrollable Body Content:\n%s", p.Semantics().String())
	}
	if browseBtn.Rect.Size.H > 60 {
		t.Fatalf("Browse tab stretched vertically to %.1fpx (rect=%v)", browseBtn.Rect.Size.H, browseBtn.Rect)
	}
	if bodyText.Rect.Size.H <= 0 || bodyText.Rect.Origin.Y > 220 {
		t.Fatalf("body content pushed down or hidden by topbar/hscroll: body=%v browse=%v", bodyText.Rect, browseBtn.Rect)
	}
}
