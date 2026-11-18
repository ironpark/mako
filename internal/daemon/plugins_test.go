package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

// TestPluginsOverTheSocket puts a plugin in a daemon's plugins folder and
// drives it as a front-end does: listed, told of, allowed, watched, acted
// on; and its block in the catalog agents read.
func TestPluginsOverTheSocket(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	changed := make(chan *protocol.Changed, 16)
	go func() { _ = c.Watch(ctx, func(ch *protocol.Changed) { changed <- ch }) }()
	dir := filepath.Join(d.dir, "plugins", "hello")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plugin.toml": `id = "hello"
version = "1.0.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["storage"]

[[contributes.block]]
id = "hi"
title = "Hello"
`,
		"main.js": `export default { activate(m) { m.blocks.register("hi", { state: { n: 0 },
			render: (s, ui) => ui.column([ui.button("Taps " + s.n, { key: "b", onTap: ["tap"] })]),
			on: { tap(s) { s.n++ } } }) } }`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The front-end is told, and lists it, waiting for the user.
	for told := false; !told; {
		select {
		case ch := <-changed:
			told = ch.GetPlugins()
		case <-ctx.Done():
			t.Fatal("no Changed.plugins came")
		}
	}
	for {
		list, err := c.Plugins(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(list.GetPlugins()) == 1 && list.GetPlugins()[0].GetState() == protocol.PluginWaiting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := d.blockCatalog().BlockProps("plugin:hello/hi"); !ok {
		t.Fatal("the block is not in the daemon's catalog")
	}
	if !strings.Contains(blockNames(d.blocks()), "plugin:hello/hi") {
		t.Fatalf("list_blocks: %s", blockNames(d.blocks()))
	}
	if _, err := c.PluginAction(ctx, &protocol.PluginAction{PluginId: "hello", Op: protocol.PluginGrant}); err != nil {
		t.Fatal(err)
	}
	updates := make(chan *protocol.BlockUpdate, 16)
	go func() { _ = c.WatchBlock(ctx, "plugin:hello/hi", func(u *protocol.BlockUpdate) { updates <- u }) }()
	var tree *protocol.UiNode
	wait := func(text string) {
		t.Helper()
		for {
			select {
			case u := <-updates:
				if u.GetTree() != nil {
					tree = u.GetTree()
				} else if len(u.GetOps()) > 0 {
					next, err := protocol.ApplyUi(tree, u.GetOps())
					if err != nil {
						t.Fatal(err)
					}
					tree = next
				}
				if tree != nil && tree.GetChildren()[0].GetText() == text {
					return
				}
			case <-ctx.Done():
				t.Fatalf("never %q; tree %v", text, tree)
			}
		}
	}
	wait("Taps 0")
	if _, err := c.BlockEvent(ctx, &protocol.BlockEvent{Block: "plugin:hello/hi", Node: "/b", Kind: protocol.UiTap}); err != nil {
		t.Fatal(err)
	}
	wait("Taps 1")
}

func blockNames(bs []layoutdoc.Block) string {
	var names []string
	for _, b := range bs {
		names = append(names, b.Name)
	}
	return strings.Join(names, " ")
}

// TestPeersReachNoPlugins: a node's plugins are its own front-ends'.
func TestPeersReachNoPlugins(t *testing.T) {
	d := meshDaemon(t, "a")
	env, err := protocol.Encode(protocol.TypeListPlugins, "1", &protocol.ListPlugins{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.dispatch(context.Background(), nil, env, true); !errors.Is(err, errPluginPeer) {
		t.Fatalf("a peer's listing: %v", err)
	}
	for _, msg := range []struct {
		typ  protocol.Type
		body proto.Message
	}{
		{protocol.TypePluginAction, &protocol.PluginAction{PluginId: "x", Op: protocol.PluginGrant}},
		{protocol.TypeWatchBlock, &protocol.WatchBlock{Block: "plugin:x/y"}},
		{protocol.TypeBlockEvent, &protocol.BlockEvent{Block: "plugin:x/y"}},
	} {
		env, err := protocol.Encode(msg.typ, "1", msg.body)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.dispatch(context.Background(), nil, env, true); !errors.Is(err, errPluginPeer) && !errors.Is(err, errBlocksPeer) {
			t.Fatalf("a peer's %s: %v", msg.typ, err)
		}
	}
}

// One watch_block serves both kinds of block: an agent's, here, as a
// plugin's above, and its event goes to the agent, not to a plugin.
func TestWatchBlockServesAgentsBlocks(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := d.agentUI.Show("s1", "claude", "b", "Board", []byte(`{"kind":"column","children":[{"kind":"button","key":"go","text":"Go","onTap":["go"]}]}`)); err != nil {
		t.Fatal(err)
	}
	list, err := c.AgentBlocks(ctx)
	if err != nil || len(list.GetBlocks()) != 1 || list.GetBlocks()[0].GetName() != "agent:s1/b" {
		t.Fatalf("%v %v", list, err)
	}
	got := make(chan *protocol.BlockUpdate, 4)
	go func() { _ = c.WatchBlock(ctx, "agent:s1/b", func(u *protocol.BlockUpdate) { got <- u }) }()
	select {
	case u := <-got:
		if u.GetTree() == nil || u.GetSessionId() != "s1" || u.GetAgent() != "claude" || u.GetTitle() != "Board" || u.GetPlugin() != "" {
			t.Fatalf("%v", u)
		}
	case <-ctx.Done():
		t.Fatal("no update")
	}
	_, err = c.BlockEvent(ctx, &protocol.BlockEvent{Block: "agent:s1/b", Node: "/go", Kind: protocol.UiTap})
	if err == nil || !strings.Contains(err.Error(), "session is gone") {
		t.Fatalf("an event of a session not running: %v", err)
	}
	if _, err := c.BlockEvent(ctx, &protocol.BlockEvent{Block: "mako.sidebar", Node: "/", Kind: protocol.UiTap}); err == nil {
		t.Fatal("an event of the window's own block went through")
	}
}
