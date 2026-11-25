package gui

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/inspect"
	"github.com/ironpark/ggui/ui/icons"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
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
		{protocol.ScopeFolder, "Select a folder to view this block", "No folder", "Pin to a folder"},
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

func TestPluginAssistVariantsAndAskBar(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")
	m := newModel(config.Default(), "")

	tree := &protocol.UiNode{
		Kind: "column",
		Gap:  6,
		Children: []*protocol.UiNode{
			{Id: "/c1", Kind: "card", Variant: "soft", Tone: "accent", Children: []*protocol.UiNode{
				{Id: "/c1/t", Kind: "text", Text: "Soft Accent Card"},
			}},
			{Id: "/c2", Kind: "card", Variant: "soft", Children: []*protocol.UiNode{
				{Id: "/c2/t", Kind: "text", Text: "Soft Neutral Card"},
			}},
			{Id: "/r", Kind: "row", Children: []*protocol.UiNode{
				{Id: "/r/p1", Kind: "pill", Text: "Soft Pill", Variant: "soft", Tone: "success", Icon: "sparkles"},
				{Id: "/r/p2", Kind: "pill", Text: "Solid Pill", Variant: "solid", Tone: "accent"},
				{Id: "/r/p3", Kind: "pill", Text: "Outline Pill", Variant: "outline", Tone: "warning"},
			}},
			{Id: "/w", Kind: "wrap", Children: []*protocol.UiNode{
				{Id: "/w/c1", Kind: "chip", Text: "Soft Chip", Variant: "soft", Icon: "tag", Tap: true},
				{Id: "/w/c2", Kind: "chip", Text: "Solid Chip", Variant: "solid", Tone: "accent"},
				{Id: "/w/c3", Kind: "chip", Text: "Outline Chip", Variant: "outline"},
			}},
			{Id: "/sp", Kind: "progress", Variant: "spinner", Text: "Selecting candidates..."},
			{Id: "/sp2", Kind: "progress", Variant: "spinner"},
			{Id: "/tr", Kind: "tree", Children: []*protocol.UiNode{
				{Id: "/tr/d", Kind: "tree_item", Text: "decisions", Value: "3", Variant: "badge", Icon: "folder", Tone: "warning"},
			}},
			{Id: "/sc", Kind: "scroll", Variant: "follow", MaxHeight: 120, Children: []*protocol.UiNode{
				{Id: "/sc/m", Kind: "text", Text: "Follow message"},
			}},
		},
	}
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return pluginFrame(m, "plugin:okf/vault", "okf", "OKF", &protocol.BlockUpdate{Title: "OKF", State: protocol.PluginRunning, Tree: tree})
	}, ggui.Sz(460, 800))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	for _, want := range []string{"Soft Accent Card", "Soft Neutral Card", "Soft Pill", "Solid Pill", "Outline Pill", "Soft Chip", "Solid Chip", "Outline Chip", "Selecting candidates...", "decisions", "3", "Follow message"} {
		if !strings.Contains(sem, want) {
			t.Errorf("missing %q in:\n%s", want, sem)
		}
	}

	// Verify permission labels.
	if got := permissionText("decision"); !strings.Contains(got, "decision model") {
		t.Errorf("permissionText(decision) = %q", got)
	}
	if got := permissionText("agents:ask"); !strings.Contains(got, "ask them questions") {
		t.Errorf("permissionText(agents:ask) = %q", got)
	}

	// Verify ask bar kind="ask".
	m.setPlugins([]*protocol.PluginInfo{{
		Id: "okf", Version: "0.3.0", State: protocol.PluginRunning,
		AgentAsk: &protocol.PluginAgentAsk{
			Id: "q1", Kind: "ask", Agent: "claude", Profile: "explorer",
			Project: "Alpha", Root: "/alpha", Name: "OKF: Why storage?", Prompt: "OKF: Why storage?",
		},
	}})
	ap := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Provide(ggui.ReducedMotionKey, true, pluginAgentBar(m))
	}, ggui.Sz(900, 300))
	defer ap.Close()
	for range 3 {
		ap.Frame()
	}
	asem := ap.Semantics().String()
	for _, want := range []string{"The plugin okf asks an agent a question", "Always allow this agent", "without YOLO", "Allow once", "Deny"} {
		if !strings.Contains(asem, want) {
			t.Errorf("ask bar missing %q in:\n%s", want, asem)
		}
	}
}

// Regression test [QA-2]: table header cells and body row cells (including
// tappable rows and rows where column 0 has Grow > 0) must share identical X
// coordinates at narrow (420px) and wide (720px) viewports.
func TestPluginTableColumnAlignment(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	tree := &protocol.UiNode{
		Id:     "/tbl",
		Kind:   "table",
		Labels: []string{"Doc", "Local", "Decision", "Final"},
		Children: []*protocol.UiNode{
			{Id: "/tbl/r1", Kind: "row", Gap: 6, Children: []*protocol.UiNode{
				{Id: "/tbl/r1/c0", Kind: "text", Text: "Doc Alpha", Grow: 1},
				{Id: "/tbl/r1/c1", Kind: "text", Text: "0.84"},
				{Id: "/tbl/r1/c2", Kind: "text", Text: "High"},
				{Id: "/tbl/r1/c3", Kind: "text", Text: "0.91"},
			}},
			{Id: "/tbl/r2", Kind: "row", Gap: 6, Tap: true, Children: []*protocol.UiNode{
				{Id: "/tbl/r2/c0", Kind: "text", Text: "Doc Beta", Grow: 1},
				{Id: "/tbl/r2/c1", Kind: "text", Text: "0.62"},
				{Id: "/tbl/r2/c2", Kind: "text", Text: "Medium"},
				{Id: "/tbl/r2/c3", Kind: "text", Text: "0.70"},
			}},
		},
	}

	headers := []string{"Doc", "Local", "Decision", "Final"}
	rows := [][]string{
		{"Doc Alpha", "0.84", "High", "0.91"},
		{"Doc Beta", "0.62", "Medium", "0.70"},
	}

	for _, width := range []float64{420, 720} {
		form := newBlockForm()
		p := ggui.ProbeBuilder(func() ggui.Widget {
			form.redraw()
			v := &treeView{block: "plugin:okf/vault", form: form}
			return v.node(tree, false)
		}, ggui.Sz(width, 300))
		for range 3 {
			p.Frame()
		}

		hdrX := make([]float64, len(headers))
		for i, h := range headers {
			nd, ok := p.Semantics().Find(ggui.RoleText, h)
			if !ok {
				p.Close()
				t.Fatalf("width=%.0f: missing header %q:\n%s", width, h, p.Semantics().String())
			}
			hdrX[i] = nd.Rect.Origin.X
		}
		for rIdx, cells := range rows {
			for cIdx, label := range cells {
				nd, ok := p.Semantics().Find(ggui.RoleText, label)
				if !ok {
					p.Close()
					t.Fatalf("width=%.0f row=%d: missing cell %q:\n%s", width, rIdx, label, p.Semantics().String())
				}
				if diff := math.Abs(nd.Rect.Origin.X - hdrX[cIdx]); diff > 1.0 {
					p.Close()
					t.Fatalf("width=%.0f row=%d col=%d (%s): cell X=%.1f != header %q X=%.1f (diff=%.1f)",
						width, rIdx, cIdx, label, nd.Rect.Origin.X, headers[cIdx], hdrX[cIdx], diff)
				}
			}
		}
		p.Close()
	}
}

// Regression test [QA-3]: long pill/chip labels inside a narrow (300px) ui.row
// must truncate cleanly within their bounds without collapsing a trailing button
// to 0x0.
func TestPluginLongPillChipInRowKeepsTrailingButtonVisible(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	longLabel := "Very long status badge label that definitely exceeds three hundred pixels in a narrow row"
	cases := []struct {
		kind    string
		variant string
	}{
		{"pill", "soft"},
		{"pill", "solid"},
		{"pill", "outline"},
		{"chip", ""},
		{"chip", "soft"},
		{"chip", "solid"},
		{"chip", "outline"},
	}

	for _, tc := range cases {
		t.Run(tc.kind+"-"+tc.variant, func(t *testing.T) {
			tree := &protocol.UiNode{
				Id:   "/r",
				Kind: "row",
				Gap:  8,
				Children: []*protocol.UiNode{
					{Id: "/r/badge", Kind: tc.kind, Variant: tc.variant, Tone: "accent", Icon: "sparkles", Text: longLabel},
					{Id: "/r/btn", Kind: "button", Text: "Open", Size: "small", Tap: true},
				},
			}
			form := newBlockForm()
			p := ggui.ProbeBuilder(func() ggui.Widget {
				form.redraw()
				v := &treeView{block: "plugin:test/ui", form: form}
				return v.node(tree, false)
			}, ggui.Sz(300, 120))
			defer p.Close()
			for range 3 {
				p.Frame()
			}

			btn, ok := p.Semantics().Find(ggui.RoleButton, "Open")
			if !ok {
				t.Fatalf("missing Open button:\n%s", p.Semantics().String())
			}
			if btn.Rect.Size.W <= 20 || btn.Rect.Size.H <= 10 {
				t.Fatalf("trailing button collapsed: rect=%v", btn.Rect)
			}
			if btn.Rect.Origin.X+btn.Rect.Size.W > 300.5 {
				t.Fatalf("trailing button overflowed 300px viewport: rect=%v", btn.Rect)
			}

			lbl, ok := p.Semantics().Find(ggui.RoleText, longLabel)
			if !ok {
				t.Fatalf("missing badge text:\n%s", p.Semantics().String())
			}
			if lbl.Rect.Size.W > 200 {
				t.Fatalf("badge label exceeded 200px max width: rect=%v", lbl.Rect)
			}
			if lbl.Rect.Origin.X+lbl.Rect.Size.W > btn.Rect.Origin.X+0.5 {
				t.Fatalf("badge label %v overlapped trailing button %v", lbl.Rect, btn.Rect)
			}
		})
	}
}

// Regression test [QA-4]: every tone of solid pill/chip badges must achieve
// WCAG AA contrast (>= 4.5:1) under both skin.Dark and skin.Light themes.
func TestSolidPillChipWCAGContrast(t *testing.T) {
	tones := []string{"", "accent", "success", "warning", "danger", "muted", "faint"}
	palettes := []skin.Palette{skin.Dark, skin.Light}
	for _, pal := range palettes {
		env := skin.Theme(pal).Apply(ggui.Env{})
		for _, tone := range tones {
			fillTok, fgTok := solidToneColors(tone)
			fill := ggui.ResolveColor(fillTok, env)
			fg := ggui.ResolveColor(fgTok, env)
			ratio := wcagContrast(fill, fg)
			if ratio < 4.5 {
				t.Errorf("palette=%s tone=%q: solid contrast %.2f:1 < 4.5:1 (fill=%v fg=%v)",
					pal.Name, tone, ratio, fill, fg)
			}
		}
	}
}

func TestFollowStreamBacksOffOnRepeatedLost(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var attempts atomic.Int32
	watch := func(streamCtx context.Context, each func(*protocol.BlockUpdate)) error {
		attempts.Add(1)
		// Send an op without a tree so ApplyUi immediately triggers lost=true.
		each(&protocol.BlockUpdate{Ops: []*protocol.UiOp{{Op: protocol.UiRemove, Id: "/missing"}}})
		<-streamCtx.Done()
		return streamCtx.Err()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		followStream(ctx, watch, time.Hour, func(*protocol.BlockUpdate) {})
	}()

	// Over 120ms, exponential backoff (0ms, 50ms, 100ms, ...) must keep attempts tightly bounded.
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("followStream did not exit promptly on context cancel")
	}
	got := attempts.Load()
	if got < 2 || got > 6 {
		t.Fatalf("attempts during 120ms backoff window = %d, want between 2 and 6", got)
	}
}

func TestListTappableRowAvoidsCloningAndKeepsChildInteractive(t *testing.T) {
	rowChild := &protocol.UiNode{
		Id:   "/row1",
		Kind: "row",
		Tap:  true,
		Drag: "drag-row1",
		Drop: true,
		Children: []*protocol.UiNode{
			{Id: "/row1/lbl", Kind: "text", Text: "Item One"},
			{Id: "/row1/btn", Kind: "button", Text: "Action", Tap: true},
		},
	}
	tree := &protocol.UiNode{
		Kind:     "list",
		Children: []*protocol.UiNode{rowChild},
	}

	type ev struct{ node, kind string }
	var events []ev
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := &treeView{
			block: "plugin:test/ui",
			form:  form,
			onEvent: func(_ string, node, kind, _ string, _ map[string]string) {
				events = append(events, ev{node, kind})
			},
		}
		return v.node(tree, false)
	}, ggui.Sz(420, 160))
	defer p.Close()
	p.Frame()

	// Input node must remain unmutated.
	if !rowChild.Tap || rowChild.Drag != "drag-row1" || !rowChild.Drop {
		t.Fatalf("input UiNode was mutated: Tap=%v Drag=%q Drop=%v", rowChild.Tap, rowChild.Drag, rowChild.Drop)
	}

	// Clicking the nested child button invokes the button's tap handler.
	p.Tap("Action")
	p.Frame()
	if len(events) != 1 || events[0] != (ev{"/row1/btn", protocol.UiTap}) {
		t.Fatalf("tapping nested button produced %+v", events)
	}

	// Clicking the outer listRow invokes /row1's tap once (not duplicated).
	events = nil
	rowBtn, ok := p.Semantics().Find(ggui.RoleButton, "Item One Action")
	if !ok {
		t.Fatalf("missing outer listRow button:\n%s", p.Semantics().String())
	}
	// Click near the left edge of the row, clear of the trailing Action button.
	p.Click(ggui.Pt(rowBtn.Rect.Origin.X+12, rowBtn.Rect.Origin.Y+rowBtn.Rect.Size.H/2))
	p.Frame()
	if len(events) != 1 || events[0] != (ev{"/row1", protocol.UiTap}) {
		t.Fatalf("tapping list row produced %+v", events)
	}

	// Verify bare rendering emits zero duplicate inner tap buttons on the row itself.
	bareForm := newBlockForm()
	v := &treeView{block: "plugin:test/ui", form: bareForm}
	_ = v.nodeWith(rowChild, false, true)
	if !rowChild.Tap || rowChild.Drag != "drag-row1" || !rowChild.Drop {
		t.Fatal("nodeWith(..., bare=true) mutated input node")
	}
}

// Regression test: git plugin section headers (with and without count badges),
// forge bar buttons, branch list rows, and commit-detail file rows must align
// cleanly along shared left/right axes without badge-induced horizontal shifts
// or unintended centering.
func TestGitPluginLayoutAlignment(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	tree := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Fill: true,
		Children: []*protocol.UiNode{
			// Forge bar wrapped in ghost card padding (matching mako-git lines.js).
			{
				Id:   "/root/forge-line",
				Kind: "column",
				Children: []*protocol.UiNode{
					{
						Id:      "/root/forge-line/forge-pad",
						Kind:    "card",
						Variant: "ghost",
						Children: []*protocol.UiNode{
							{
								Id:   "/root/forge-line/forge-pad/forge-row",
								Kind: "row",
								Gap:  4,
								Children: []*protocol.UiNode{
									{
										Id: "/root/forge-line/forge-pad/forge-row/forge-create-pr", Kind: "button",
										Text: "Create pull request", Icon: "git-pull-request", Variant: "ghost", Size: "small", Grow: 1, Tap: true,
									},
									{
										Id: "/root/forge-line/forge-pad/forge-row/forge-runs", Kind: "button",
										Text: "Passed", Icon: "circle-check", Tone: "success", Variant: "ghost", Size: "small", Tap: true,
									},
								},
							},
						},
					},
					{Id: "/root/forge-line/forge-div", Kind: "divider"},
				},
			},
			// Worktree sections wrapped in ghost card padding (matching mako-git layout.js).
			{
				Id:   "/root/worktree-scroll",
				Kind: "scroll",
				Grow: 1,
				Children: []*protocol.UiNode{
					{
						Id:      "/root/worktree-scroll/worktree-inner",
						Kind:    "card",
						Variant: "ghost",
						Gap:     4,
						Children: []*protocol.UiNode{
							// Changes section (count=3 -> has soft badge)
							{
								Id: "/root/worktree-scroll/worktree-inner/sec-changes-head", Kind: "row", Gap: 2,
								Children: []*protocol.UiNode{
									{
										Id: "/root/worktree-scroll/worktree-inner/sec-changes-head/sec-changes-toggle", Kind: "button",
										Text: "Changes", Icon: "chevron-down", Variant: "ghost", Size: "small", Weight: "semibold", Tone: "muted", Grow: 1, Tap: true,
										Badges: []*protocol.UiBadge{{Text: "3", Tone: "faint", Variant: "soft"}},
									},
								},
							},
							// Stashes section (count=0 -> NO badge)
							{
								Id: "/root/worktree-scroll/worktree-inner/sec-stashes-head", Kind: "row", Gap: 2,
								Children: []*protocol.UiNode{
									{
										Id: "/root/worktree-scroll/worktree-inner/sec-stashes-head/sec-stashes-toggle", Kind: "button",
										Text: "Stashes", Icon: "chevron-right", Variant: "ghost", Size: "small", Weight: "semibold", Tone: "muted", Grow: 1, Tap: true,
									},
									{
										Id: "/root/worktree-scroll/worktree-inner/sec-stashes-head/stashes-add", Kind: "button",
										Icon: "plus", Variant: "ghost", Size: "small", Placeholder: "Stash changes…", Tap: true,
									},
								},
							},
							// Branches section (count=1 -> has soft badge + expanded rows)
							{
								Id: "/root/worktree-scroll/worktree-inner/branches-section", Kind: "column", Gap: 2,
								Children: []*protocol.UiNode{
									{
										Id: "/root/worktree-scroll/worktree-inner/branches-section/sec-branches-head", Kind: "row", Gap: 2,
										Children: []*protocol.UiNode{
											{
												Id: "/root/worktree-scroll/worktree-inner/branches-section/sec-branches-head/sec-branches-toggle", Kind: "button",
												Text: "Branches", Icon: "chevron-down", Variant: "ghost", Size: "small", Weight: "semibold", Tone: "muted", Grow: 1, Tap: true,
												Badges: []*protocol.UiBadge{{Text: "1", Tone: "faint", Variant: "soft"}},
											},
											{
												Id: "/root/worktree-scroll/worktree-inner/branches-section/sec-branches-head/branches-add", Kind: "button",
												Icon: "plus", Variant: "ghost", Size: "small", Placeholder: "New branch…", Tap: true,
											},
										},
									},
									{
										Id: "/root/worktree-scroll/worktree-inner/branches-section/branches-list", Kind: "column", Gap: 1,
										Children: []*protocol.UiNode{
											{
												Id: "/root/worktree-scroll/worktree-inner/branches-section/branches-list/bl-main", Kind: "button",
												Text: "main", Icon: "check", Tone: "accent", Weight: "medium", Variant: "ghost", Size: "small", Tap: true,
											},
											{
												Id: "/root/worktree-scroll/worktree-inner/branches-section/branches-list/cf-file", Kind: "button",
												Text: "feature.go", Icon: "square-plus", Tone: "success", Variant: "ghost", Size: "small", Tap: true,
												Badges: []*protocol.UiBadge{
													{Text: "+5", Tone: "success", Variant: "mono"},
													{Text: "-2", Tone: "danger", Variant: "mono"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			// History folded footer header wrapped in matching ghost card padding.
			{Id: "/root/hist-div", Kind: "divider"},
			{
				Id: "/root/hist-head-pad", Kind: "card", Variant: "ghost",
				Children: []*protocol.UiNode{
					{
						Id: "/root/hist-head-pad/sec-history-head", Kind: "row", Gap: 2,
						Children: []*protocol.UiNode{
							{
								Id: "/root/hist-head-pad/sec-history-head/sec-history-toggle", Kind: "button",
								Text: "History", Icon: "chevron-right", Variant: "ghost", Size: "small", Weight: "semibold", Tone: "muted", Grow: 1, Tap: true,
							},
							{
								Id: "/root/hist-head-pad/sec-history-head/hist-refresh", Kind: "button",
								Icon: "refresh-cw", Variant: "ghost", Size: "small", Placeholder: "Refresh", Tap: true,
							},
						},
					},
				},
			},
		},
	}

	for _, width := range []float64{340, 560} {
		form := newBlockForm()
		var inspected []inspect.Node
		p := ggui.ProbeBuilder(func() ggui.Widget {
			form.redraw()
			v := &treeView{block: gitPluginBlock, form: form}
			return uitheme.With(skin.Theme(skin.Dark), widget.Box(v.node(tree, false)).Fill(skin.Surface0))
		}, ggui.Sz(width, 500))
		p.OnInspect(func(f *inspect.Frame) {
			f.DescribeAll()
			inspected = append([]inspect.Node(nil), f.Nodes...)
		})
		for range 3 {
			p.Frame()
		}

		findPaintedText := func(label string) inspect.Rect {
			t.Helper()
			for _, nd := range inspected {
				if nd.Name == "Text" && nd.Label == label {
					return nd.Rect
				}
			}
			t.Fatalf("width=%.0f: missing painted Text %q", width, label)
			return inspect.Rect{}
		}
		findPaintedIconIn := func(container ggui.Rect) inspect.Rect {
			t.Helper()
			for _, nd := range inspected {
				if _, ok := nd.Instance.(*icons.Widget); ok &&
					nd.Rect.Origin.X >= container.Origin.X-0.5 &&
					nd.Rect.Origin.Y >= container.Origin.Y-0.5 &&
					nd.Rect.Origin.X+nd.Rect.Size.W <= container.Origin.X+container.Size.W+0.5 &&
					nd.Rect.Origin.Y+nd.Rect.Size.H <= container.Origin.Y+container.Size.H+0.5 {
					return nd.Rect
				}
			}
			t.Fatalf("width=%.0f: missing painted icon inside %v", width, container)
			return inspect.Rect{}
		}
		findBtn := func(label string) ggui.Rect {
			t.Helper()
			nd, ok := p.Semantics().Find(ggui.RoleButton, label)
			if !ok {
				t.Fatalf("width=%.0f: missing button %q:\n%s", width, label, p.Semantics().String())
			}
			return nd.Rect
		}

		// 1. All section headers (with and without badges, including History) share identical
		// button X, button height, chevron icon X, and label text X.
		changesBtn := findBtn("Changes")
		stashesBtn := findBtn("Stashes")
		branchesBtn := findBtn("Branches")
		historyBtn := findBtn("History")
		changesIcon := findPaintedIconIn(changesBtn)
		changesLbl := findPaintedText("Changes")
		for name, btn := range map[string]ggui.Rect{"Stashes": stashesBtn, "Branches": branchesBtn, "History": historyBtn} {
			if diff := math.Abs(btn.Origin.X - changesBtn.Origin.X); diff > 0.1 {
				t.Fatalf("width=%.0f: %s button X=%.2f != Changes button X=%.2f", width, name, btn.Origin.X, changesBtn.Origin.X)
			}
			if diff := math.Abs(btn.Size.H - changesBtn.Size.H); diff > 0.5 {
				t.Fatalf("width=%.0f: %s button H=%.2f != Changes button H=%.2f", width, name, btn.Size.H, changesBtn.Size.H)
			}
			ic := findPaintedIconIn(btn)
			if diff := math.Abs(ic.Origin.X - changesIcon.Origin.X); diff > 0.1 {
				t.Fatalf("width=%.0f: %s chevron X=%.2f != Changes chevron X=%.2f", width, name, ic.Origin.X, changesIcon.Origin.X)
			}
			lbl := findPaintedText(name)
			if diff := math.Abs(lbl.Origin.X - changesLbl.Origin.X); diff > 0.1 {
				t.Fatalf("width=%.0f: %s text X=%.2f != Changes text X=%.2f", width, name, lbl.Origin.X, changesLbl.Origin.X)
			}
		}

		// 2. Forge "Create pull request" is left-aligned (not centered) and "Passed" has right inset.
		createPRBtn := findBtn("Create pull request")
		createPRIcon := findPaintedIconIn(createPRBtn)
		createPRLbl := findPaintedText("Create pull request")
		if diff := math.Abs(createPRIcon.Origin.X - changesIcon.Origin.X); diff > 0.1 {
			t.Fatalf("width=%.0f: Create pull request icon X=%.2f != Changes chevron X=%.2f (centered?)", width, createPRIcon.Origin.X, changesIcon.Origin.X)
		}
		if diff := math.Abs(createPRLbl.Origin.X - changesLbl.Origin.X); diff > 0.1 {
			t.Fatalf("width=%.0f: Create pull request text X=%.2f != Changes text X=%.2f", width, createPRLbl.Origin.X, changesLbl.Origin.X)
		}
		passedBtn := findBtn("Passed")
		if rightMargin := width - (passedBtn.Origin.X + passedBtn.Size.W); rightMargin < 8 {
			t.Fatalf("width=%.0f: Passed button touches right edge (margin=%.1f, rect=%v)", width, rightMargin, passedBtn)
		}

		// 3. Branch list rows and commit-detail file rows are left-aligned, with mono diff badges right-aligned.
		mainBtn := findBtn("main")
		mainIcon := findPaintedIconIn(mainBtn)
		mainLbl := findPaintedText("main")
		if diff := math.Abs(mainIcon.Origin.X - changesIcon.Origin.X); diff > 0.1 {
			t.Fatalf("width=%.0f: branch 'main' icon X=%.2f != Changes chevron X=%.2f (centered?)", width, mainIcon.Origin.X, changesIcon.Origin.X)
		}
		if diff := math.Abs(mainLbl.Origin.X - changesLbl.Origin.X); diff > 0.1 {
			t.Fatalf("width=%.0f: branch 'main' text X=%.2f != Changes text X=%.2f", width, mainLbl.Origin.X, changesLbl.Origin.X)
		}
		fileBtn := findBtn("feature.go")
		fileIcon := findPaintedIconIn(fileBtn)
		fileLbl := findPaintedText("feature.go")
		if diff := math.Abs(fileIcon.Origin.X - changesIcon.Origin.X); diff > 0.1 {
			t.Fatalf("width=%.0f: commit file 'feature.go' icon X=%.2f != Changes chevron X=%.2f", width, fileIcon.Origin.X, changesIcon.Origin.X)
		}
		if diff := math.Abs(fileLbl.Origin.X - changesLbl.Origin.X); diff > 0.1 {
			t.Fatalf("width=%.0f: commit file 'feature.go' text X=%.2f != Changes text X=%.2f", width, fileLbl.Origin.X, changesLbl.Origin.X)
		}
		minusLbl := findPaintedText("-2")
		if minusLbl.Origin.X < width-50 {
			t.Fatalf("width=%.0f: commit file mono diff badge '-2' not right-aligned (rect=%v)", width, minusLbl)
		}

		// 4. Right-side action buttons align along the same right edge.
		stashAdd := findBtn("Stash changes…")
		branchAdd := findBtn("New branch…")
		histRef := findBtn("Refresh")
		rightEdge := func(r ggui.Rect) float64 { return r.Origin.X + r.Size.W }
		for name, r := range map[string]ggui.Rect{"New branch…": branchAdd, "Refresh": histRef, "Passed": passedBtn} {
			if diff := math.Abs(rightEdge(r) - rightEdge(stashAdd)); diff > 0.5 {
				t.Fatalf("width=%.0f: %s right edge=%.2f != Stash + right edge=%.2f", width, name, rightEdge(r), rightEdge(stashAdd))
			}
		}

		p.Close()
	}
}
