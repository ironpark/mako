package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// TestPluginDBSyncAcrossMesh verifies end-to-end P2P SQLite CRDT sync between
// two mutually trusted daemons over libp2p: writing a synced row on node A
// propagates automatically to node B and updates node B's live watched block.
func TestPluginDBSyncAcrossMesh(t *testing.T) {
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, a, b)
	letIn(t, b, a)

	ca := dialDaemon(t, a)
	cb := dialDaemon(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	files := map[string]string{
		"plugin.toml": `id = "syncboard"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["db", "db:sync"]

[[contributes.block]]
id = "board"
title = "Sync Board"
scope = "project"
`,
		"main.js": `let mako;
async function listItems(ctx) {
  await ctx.db.syncEnable("cards");
  const q = await ctx.db.query("SELECT id, json_extract(doc, '$.title') AS title FROM cards WHERE deleted = 0 ORDER BY id");
  return q.rows;
}
export default {
  activate(m) {
    mako = m;
    m.blocks.register("board", {
      state: { cards: [] },
      async load(s, ctx) {
        s.cards = await listItems(ctx);
      },
      render: (s, ui) => ui.column([
        ui.button("Add", { key: "add", onTap: ["add"] }),
        ...s.cards.map((c) => ui.text(c.id + ":" + c.title, { key: c.id })),
      ]),
      on: {
        async add(s) {
          const ctx = mako.blocks.context("board");
          await ctx.db.syncEnable("cards");
          await ctx.db.syncPut("cards", "card-1", { title: "Synced over libp2p" });
          s.cards = await listItems(ctx);
        },
      },
    });
  },
};`,
	}

	writePlugin(t, a, "syncboard", files)
	writePlugin(t, b, "syncboard", files)

	for _, c := range []*struct {
		name string
		cli  interface {
			Plugins(context.Context) (*protocol.Plugins, error)
			PluginAction(context.Context, *protocol.PluginAction) (*protocol.PluginDone, error)
		}
	}{{"a", ca}, {"b", cb}} {
		waitFor(t, "plugin listed on "+c.name, func() bool {
			list, err := c.cli.Plugins(ctx)
			return err == nil && findPlugin(list.GetPlugins(), "syncboard") != nil
		})
		if _, err := c.cli.PluginAction(ctx, &protocol.PluginAction{PluginId: "syncboard", Op: protocol.PluginGrant}); err != nil {
			t.Fatal(err)
		}
	}

	scope := "project:repo:github.com/ironpark/mako"
	updatesA := make(chan *protocol.BlockUpdate, 16)
	updatesB := make(chan *protocol.BlockUpdate, 16)
	go func() {
		_ = ca.WatchBlockScoped(ctx, "plugin:syncboard/board", scope, func(u *protocol.BlockUpdate) { updatesA <- u })
	}()
	go func() {
		_ = cb.WatchBlockScoped(ctx, "plugin:syncboard/board", scope, func(u *protocol.BlockUpdate) { updatesB <- u })
	}()

	waitText := func(ch <-chan *protocol.BlockUpdate, tree **protocol.UiNode, want string) {
		t.Helper()
		if *tree != nil && uiHasText(*tree, want) {
			return
		}
		for {
			select {
			case u := <-ch:
				if u.GetTree() != nil {
					*tree = u.GetTree()
				} else if len(u.GetOps()) > 0 && *tree != nil {
					next, err := protocol.ApplyUi(*tree, u.GetOps())
					if err != nil {
						t.Fatal(err)
					}
					*tree = next
				}
				if *tree != nil && uiHasText(*tree, want) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %q; last tree = %v", want, *tree)
			}
		}
	}

	var treeA, treeB *protocol.UiNode
	waitText(updatesA, &treeA, "Add")
	waitText(updatesB, &treeB, "Add")

	// Tap Add on node A -> writes CRDT row to SQLite and replicates to node B!
	if _, err := ca.BlockEvent(ctx, &protocol.BlockEvent{
		Block: "plugin:syncboard/board",
		Scope: scope,
		Node:  "/add",
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	waitText(updatesA, &treeA, "card-1:Synced over libp2p")
	waitText(updatesB, &treeB, "card-1:Synced over libp2p")

	// Local front-end calling TypeSyncPluginDB directly must be refused.
	env, err := protocol.Encode(protocol.TypeSyncPluginDB, "1", &protocol.SyncPluginDB{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(ctx, nil, env, false); err == nil || !strings.Contains(err.Error(), "mutually trusted peer") {
		t.Fatalf("non-peer sync call: got %v, want peer-only refusal", err)
	}
}

func uiHasText(n *protocol.UiNode, want string) bool {
	if n == nil {
		return false
	}
	if strings.Contains(n.GetText(), want) {
		return true
	}
	for _, c := range n.GetChildren() {
		if uiHasText(c, want) {
			return true
		}
	}
	return false
}
