package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestPrimitivesM3PreludeAndEvents(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("prim-m3", map[string]string{
		ManifestName: `id = "prim-m3"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = "main"
title = "M3"
slots = ["sidebar"]
`,
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("main", {
      load(s) {
        s.hit = "";
        s.span = "";
        s.chart = "";
      },
      render(s, ui) {
        return ui.column([
          ui.canvas([
            { op: "roundRect", x: 10, y: 10, w: 120, h: 60, r: 8, fillTone: "surfaceAlt", strokeTone: "accent", hitId: "worker" },
            { op: "circle", x: 180, y: 40, r: 20, fillTone: "success", hitId: "worker" },
            { op: "text", x: 24, y: 40, text: "Node", size: "medium", tone: "ink" },
            { op: "icon", x: 80, y: 26, icon: "cpu", size: 16, tone: "accent" },
            { op: "roundRect", x: 210, y: 10, w: 30, h: 30, fillTone: "muted", alpha: 0 },
          ], { key: "cv", caption: "Arch", width: 400, height: 180, onChange: ["onCanvas"] }),
          ui.timeline([
            { id: "lane-a", label: "Turns", spans: [{ id: "sp-1", startMs: 0, endMs: 1200, label: "Plan", tone: "accent", kind: "turn" }] },
          ], { key: "tl-a", maxHeight: 160, onChange: ["onTimeline"] }),
          ui.timeline([
            { id: "sp-2", lane: "Worker", kind: "subagent", label: "Build", subtitle: "2s", tone: "success", startMs: 100, endMs: 2100 },
            { id: "sp-3", lane: "Worker", kind: "ci", label: "Test", tone: "warning", startMs: 2200, endMs: 4200 },
          ], { key: "tl-b", maxHeight: 160, onChange: ["onTimeline"] }),
          ui.chart("area", [{ name: "TPS", tone: "accent", values: [10, 20, 15] }], { key: "ch-area", labels: ["A", "B", "C"], maxHeight: 140, onChange: ["onChart"] }),
          ui.chart("stacked-bar", [{ name: "Ok", tone: "success", values: [2, 3] }, { name: "Err", tone: "danger", values: [1, 0] }], { key: "ch-sb", labels: ["W1", "W2"] }),
          ui.chart("heatmap", [{ name: "R1", values: [1, 4] }, { name: "R2", values: [3, 2] }], { key: "ch-hm", labels: ["C1", "C2"], onChange: ["onChart"] }),
          ui.text(s.hit + "|" + s.span + "|" + s.chart, { key: "status" }),
        ], { fill: true });
      },
      on: {
        onCanvas(s, id) { s.hit = String(id || ""); },
        onTimeline(s, id) { s.span = String(id || ""); },
        onChart(s, v) { s.chart = String(v || ""); },
      },
    });
  },
};`,
	})
	r.state("prim-m3", protocol.PluginReady)
	r.grant("prim-m3")

	w := r.watch("plugin:prim-m3/main")
	tree := w.until("initial m3 tree", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findBySuffix(n, "cv") != nil && findBySuffix(n, "tl-b") != nil && findBySuffix(n, "ch-hm") != nil
	})
	cv := findBySuffix(tree, "cv")
	if !cv.GetCanvasPan() || !cv.GetCanvasZoom() || cv.GetCanvasWidth() != 400 || cv.GetCanvasHeight() != 180 || len(cv.GetCanvasCommands()) != 5 {
		t.Fatalf("unexpected canvas node: %+v", cv)
	}
	if cv.GetCanvasCommands()[0].GetAlpha() != 1.0 {
		t.Fatalf("expected omitted alpha in QuickJS to default to 1.0, got %v", cv.GetCanvasCommands()[0].GetAlpha())
	}
	if cv.GetCanvasCommands()[4].Alpha == nil || *cv.GetCanvasCommands()[4].Alpha != 0 {
		t.Fatalf("expected explicit alpha:0 in QuickJS to preserve 0, got %v", cv.GetCanvasCommands()[4].Alpha)
	}
	tlA := findBySuffix(tree, "tl-a")
	tlB := findBySuffix(tree, "tl-b")
	if len(tlA.GetTimelineLanes()) != 1 || len(tlB.GetTimelineLanes()) != 1 || len(tlB.GetTimelineLanes()[0].GetSpans()) != 2 {
		t.Fatalf("unexpected timeline lanes: tlA=%+v tlB=%+v", tlA.GetTimelineLanes(), tlB.GetTimelineLanes())
	}
	chArea := findBySuffix(tree, "ch-area")
	chHM := findBySuffix(tree, "ch-hm")

	ctx := context.Background()
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{Block: "plugin:prim-m3/main", Node: cv.GetId(), Kind: protocol.UiChange, Value: "worker"}); err != nil {
		t.Fatalf("canvas event: %v", err)
	}
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{Block: "plugin:prim-m3/main", Node: tlB.GetId(), Kind: protocol.UiChange, Value: "sp-2"}); err != nil {
		t.Fatalf("timeline event: %v", err)
	}
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{Block: "plugin:prim-m3/main", Node: chArea.GetId(), Kind: protocol.UiChange, Value: `{"index":1,"label":"B"}`}); err != nil {
		t.Fatalf("chart area event: %v", err)
	}
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{Block: "plugin:prim-m3/main", Node: chHM.GetId(), Kind: protocol.UiChange, Value: `{"row":1,"col":0}`}); err != nil {
		t.Fatalf("chart heatmap event: %v", err)
	}

	w.until("m3 status updated", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		st := findBySuffix(n, "status")
		return st != nil && strings.HasPrefix(st.GetText(), "worker|sp-2|")
	})
}

func TestPrimitivesM3CanvasRejectsRawHexInRuntime(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("prim-m3-bad", map[string]string{
		ManifestName: `id = "prim-m3-bad"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = "main"
title = "Bad"
slots = ["sidebar"]
`,
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(_s, ui) {
        return ui.canvas([{ op: "roundRect", x: 0, y: 0, w: 40, h: 40, fillTone: "#ff0000" }]);
      },
    });
  },
};`,
	})
	r.state("prim-m3-bad", protocol.PluginReady)
	r.grant("prim-m3-bad")

	w := r.watch("plugin:prim-m3-bad/main")
	w.until("sandbox rejection", func(_ *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u != nil && u.GetError() != ""
	})
}
