package plugin

import (
	"context"
	"fmt"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestGraphM1PreludeAndEvents(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("graph-m1", map[string]string{
		ManifestName: `id = "graph-m1"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["storage"]

[[contributes.block]]
id = "canvas"
title = "Canvas"
slots = ["sidebar"]
`,
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("canvas", {
      load(s) {
        s.jumped = "";
        s.dropped = null;
      },
      render(s, ui) {
        return ui.column([
          ui.graph(
            [
              {
                id: "sess:s1",
                label: "Coordinator",
                group: "core",
                tone: "accent",
                subtitle: "claude · main",
                shape: "card",
                icon: "bot",
                status: "working",
                badges: ["main", { text: "lead", tone: "success", variant: "soft" }],
                x: 10,
                y: 20,
                hasPos: true,
                pinned: true,
                contextMenu: "node-menu",
                contextArg: "arg-s1",
              },
              {
                id: "sess:s2",
                label: "Worker",
                group: "core",
                shape: "circle",
                status: "waiting",
              },
            ],
            [
              { from: "sess:s1", to: "sess:s2", label: "msg", animated: true, kind: "agentMessage" },
            ],
            {
              key: "g",
              variant: "cards",
              layout: "radial",
              minimap: true,
              fill: true,
              onChange: ["pick"],
              onDoubleTap: ["jump"],
              onDrop: ["savePos"],
            }
          ),
          ui.contextMenu("node-menu", [
            ui.menuItem("Inspect", { key: "insp", onTap: ["inspect"] }),
          ]),
          ui.text("jumped=" + s.jumped + "|dropped=" + (s.dropped ? s.dropped.id + "@" + s.dropped.x + "," + s.dropped.y : ""), { key: "status" }),
        ], { fill: true });
      },
      on: {
        pick(s, id) { s.picked = id; },
        jump(s, id) { s.jumped = id; },
        savePos(s, pos) { s.dropped = pos; },
        inspect(s, arg) { s.jumped = "menu:" + arg; },
      },
    });
  },
};`,
	})
	r.state("graph-m1", protocol.PluginWaiting)
	r.grant("graph-m1")

	w := r.watch("plugin:graph-m1/canvas")
	tree := w.until("initial graph", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKind(n, "graph") != nil
	})
	g := findByKind(tree, "graph")
	if g.GetVariant() != "cards" || g.GetGraphLayout() != "radial" || !g.GetMinimap() || !g.GetFill() || !g.GetDoubleTap() || !g.GetDrop() {
		t.Fatalf("graph node = %+v", g)
	}
	if len(g.GetGraphNodes()) != 2 || g.GetGraphNodes()[0].GetShape() != "card" || g.GetGraphNodes()[0].GetIcon() != "bot" || g.GetGraphNodes()[0].GetStatus() != "working" || len(g.GetGraphNodes()[0].GetBadges()) != 2 {
		t.Fatalf("graphNodes = %+v", g.GetGraphNodes())
	}
	if len(g.GetGraphEdges()) != 1 || !g.GetGraphEdges()[0].GetActive() || g.GetGraphEdges()[0].GetKind() != "agentMessage" {
		t.Fatalf("graphEdges = %+v", g.GetGraphEdges())
	}

	ctx := context.Background()
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{
		Block: "plugin:graph-m1/canvas",
		Node:  g.GetId(),
		Kind:  protocol.UiDoubleTap,
		Value: "sess:s1",
	}); err != nil {
		t.Fatalf("UiDoubleTap event failed: err=%v", err)
	}
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{
		Block: "plugin:graph-m1/canvas",
		Node:  g.GetId(),
		Kind:  protocol.UiDrop,
		Value: `{"id":"sess:s1","x":42,"y":84}`,
	}); err != nil {
		t.Fatalf("UiDrop event failed: err=%v", err)
	}
	w.until("jumped and dropped updated", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		st := findBySuffix(n, "status")
		return st != nil && st.GetText() == fmt.Sprintf("jumped=sess:s1|dropped=sess:s1@42,84")
	})
}
