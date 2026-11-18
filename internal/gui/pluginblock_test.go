package gui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
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
	}}
	m := newModel(config.Default(), "")
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return pluginFrame(m, "plugin:ci-status/ci", "ci-status", "CI", &protocol.BlockUpdate{Title: "CI", State: protocol.PluginRunning, Tree: tree})
	}, ggui.Sz(420, 700))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	for _, want := range []string{"CI", "ci-status", "Latest runs", "Refresh", "failure", "main", "Only failures", "bold", "go test"} {
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
	if len(cmds) != before+1 {
		t.Fatalf("%d commands, %d before", len(cmds), before)
	}
	found := false
	for _, c := range cmds {
		if c.Category == "ci-status" && c.ID == "Re-run the latest failed CI run" && !c.Disabled {
			found = true
		}
	}
	if !found {
		t.Fatal("the plugin's command is not in the palette under its name")
	}
	m.setPlugins([]*protocol.PluginInfo{ciPlugin(protocol.PluginWaiting)})
	for _, c := range m.commands() {
		if c.Category == "ci-status" && !c.Disabled {
			t.Fatal("a waiting plugin's command can be run")
		}
	}
}

// A plugin's Markdown fetches no picture: it would reach a host the
// plugin was not allowed, with what the plugin put in the address.
func TestPluginMarkdownFetchesNothing(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	host := pluginPage{pageHost{m: newModel(config.Default(), "")}}
	for _, src := range []string{"https://evil.example/leak?d=secret", "/etc/hosts", "../x.png"} {
		if host.Image(src) != nil {
			t.Errorf("%s was fetched", src)
		}
	}
	if host.Diagram("graph TD; a-->b", "mermaid") != nil {
		t.Error("a diagram was drawn")
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
