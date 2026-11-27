package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	wasmtime "github.com/ironpark/wasmtime-pure-go"

	"github.com/ironpark/mako/internal/protocol"
)

func findByKind(n *protocol.UiNode, kind string) *protocol.UiNode {
	if n == nil {
		return nil
	}
	if n.GetKind() == kind {
		return n
	}
	for _, c := range n.GetChildren() {
		if got := findByKind(c, kind); got != nil {
			return got
		}
	}
	return nil
}

func findBySuffix(n *protocol.UiNode, suffix string) *protocol.UiNode {
	if n == nil {
		return nil
	}
	if strings.HasSuffix(n.GetId(), "/"+suffix) {
		return n
	}
	for _, c := range n.GetChildren() {
		if got := findBySuffix(c, suffix); got != nil {
			return got
		}
	}
	return nil
}

// viewWatcher applies both full trees and incremental UiOp patches like a real front-end.
type viewWatcher struct {
	t       *testing.T
	mu      sync.Mutex
	tree    *protocol.UiNode
	errText string
	changed chan struct{}
}

func watchViewForTest(t *testing.T, ctx context.Context, m *Manager, block, scope, locale string, view View) *viewWatcher {
	t.Helper()
	w := &viewWatcher{t: t, changed: make(chan struct{}, 1)}
	wctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		_ = m.WatchScopedView(wctx, block, scope, locale, view, func(u *protocol.BlockUpdate) error {
			w.mu.Lock()
			if u.GetError() != "" {
				w.errText = u.GetError()
			} else {
				w.errText = ""
			}
			switch {
			case u.GetTree() != nil:
				w.tree = u.GetTree()
			case len(u.GetOps()) > 0:
				next, err := protocol.ApplyUi(w.tree, u.GetOps())
				if err != nil {
					w.errText = "apply ops: " + err.Error()
				} else {
					w.tree = next
				}
			}
			w.mu.Unlock()
			select {
			case w.changed <- struct{}{}:
			default:
			}
			return nil
		})
	}()
	return w
}

func (w *viewWatcher) until(desc string, ok func(*protocol.UiNode) bool) *protocol.UiNode {
	w.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		w.mu.Lock()
		tree, errText := w.tree, w.errText
		w.mu.Unlock()
		if errText != "" {
			w.t.Fatalf("%s: block error: %s", desc, errText)
		}
		if tree != nil && ok(tree) {
			return tree
		}
		select {
		case <-w.changed:
		case <-deadline:
			w.t.Fatalf("timed out waiting for %s (last texts: %v)", desc, texts(tree))
		}
	}
}

// TestUIWorkbenchExamplePlugin verifies examples/plugins/ui-workbench end-to-end:
// left sidebar page, search + empty state, preview & kept custom workspace tabs,
// unified/split diff + line comments, all new & enhanced Rev 49 components, and KO i18n.
func TestUIWorkbenchExamplePlugin(t *testing.T) {
	lim := testLimits
	lim.Events = 100
	r := newRig(t, lim, nil)
	files := map[string]string{}
	for _, name := range []string{ManifestName, "main.js"} {
		b, err := os.ReadFile(filepath.Join("../../examples/plugins/ui-workbench", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	r.write("ui-workbench", files)
	r.state("ui-workbench", protocol.PluginWaiting)
	r.grant("ui-workbench")

	// Verify manifest slots and icon.
	var info *protocol.PluginInfo
	for _, p := range r.m.List() {
		if p.GetId() == "ui-workbench" {
			info = p
			break
		}
	}
	if info == nil || len(info.GetBlocks()) != 2 {
		t.Fatalf("expected 2 blocks on ui-workbench, got %+v", info)
	}
	if !slices.Contains(info.GetBlocks()[0].GetSlots(), "sidebar") || info.GetBlocks()[0].GetIcon() != "flask-conical" {
		t.Fatalf("explorer block = %+v", info.GetBlocks()[0])
	}
	if !slices.Contains(info.GetBlocks()[1].GetSlots(), "tab") || info.GetBlocks()[1].GetIcon() != "file-diff" {
		t.Fatalf("viewer block = %+v", info.GetBlocks()[1])
	}

	// 1. Sidebar page renders toolbar, stats, ring progress, and presets.
	sb := r.watch("plugin:ui-workbench/explorer")
	sbTree := sb.until("sidebar presets", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Auth Token Diff Review")
	})
	if findByKind(sbTree, "stat") == nil || findByKind(sbTree, "progress") == nil {
		t.Fatalf("expected stat and progress ring in sidebar tree")
	}

	// 2. Search filtering to zero results shows ui.empty.
	searchNode := findBySuffix(sbTree, "search")
	if searchNode == nil {
		t.Fatal("missing search input node")
	}
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/explorer", Node: searchNode.GetId(), Kind: protocol.UiChange, Value: "zz-no-match",
	}); err != nil {
		t.Fatalf("search change: %v", err)
	}
	emptyTree := sb.until("empty state", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findByKind(n, "empty") != nil
	})
	clearBtn := findBySuffix(emptyTree, "clear-search")
	if clearBtn == nil {
		t.Fatal("missing clear-search button in ui.empty")
	}
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/explorer", Node: clearBtn.GetId(), Kind: protocol.UiTap,
	}); err != nil {
		t.Fatalf("clear search: %v", err)
	}
	sbTree = sb.until("presets restored", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Auth Token Diff Review")
	})

	// 3. Clicking a preset emits mako.openTab intent for viewer custom tab.
	cardNode := findBySuffix(sbTree, "item-diff-auth")
	if cardNode == nil {
		t.Fatal("missing preset card")
	}
	evRes, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/explorer", Node: cardNode.GetId(), Kind: protocol.UiTap,
	})
	if err != nil {
		t.Fatalf("open preset tap: %v", err)
	}
	if len(evRes.GetOpen()) != 1 || evRes.GetOpen()[0].GetKind() != "tab" ||
		evRes.GetOpen()[0].GetBlock() != "plugin:ui-workbench/viewer" ||
		evRes.GetOpen()[0].GetKey() != "diff-auth" || !evRes.GetOpen()[0].GetPreview() {
		t.Fatalf("unexpected openTab intent: %+v", evRes.GetOpen())
	}

	// 4. Watch multi-instance custom workspace tab with ctx.tab params in Korean.
	tabW := watchViewForTest(t, t.Context(), r.m, "plugin:ui-workbench/viewer", "", "ko", View{
		Instance: "diff-auth",
		Params:   `{"preset":"diff-auth","section":"diff"}`,
	})

	// Verify KO localized Diff Review tab renders ui.diff + ui.code + ui.alert + ui.kbd.
	diffTree := tabW.until("diff tab", func(n *protocol.UiNode) bool {
		return findByKind(n, "diff") != nil && hasText(n, "인증 토큰 Diff 리뷰")
	})
	if diffNode := findByKind(diffTree, "diff"); diffNode.GetVariant() != "unified" {
		t.Fatalf("initial diff variant = %q, want unified", diffNode.GetVariant())
	}
	if findByKind(diffTree, "code") == nil || findByKind(diffTree, "alert") == nil || findByKind(diffTree, "kbd") == nil {
		t.Fatal("expected code, alert, and kbd nodes in diff tab")
	}

	// Switch to Split diff via ui.radio.
	modeRadio := findBySuffix(diffTree, "diff-mode")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: modeRadio.GetId(), Kind: protocol.UiChange, Value: "Split",
	}); err != nil {
		t.Fatalf("switch diff mode: %v", err)
	}
	tabW.until("split diff", func(n *protocol.UiNode) bool {
		d := findByKind(n, "diff")
		return d != nil && d.GetVariant() == "split"
	})

	// Switch to Component Studio tab & verify slider, stat, sortable table, skeleton, alerts.
	mainTabs := findBySuffix(diffTree, "main-tabs")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: mainTabs.GetId(), Kind: protocol.UiChange, Value: "컴포넌트 스튜디오",
	}); err != nil {
		t.Fatalf("switch to studio: %v", err)
	}
	studioTree := tabW.until("component studio", func(n *protocol.UiNode) bool {
		return findByKind(n, "slider") != nil && findByKind(n, "table") != nil
	})
	sliderNode := findBySuffix(studioTree, "demo-slider")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: sliderNode.GetId(), Kind: protocol.UiChange, Value: "85",
	}); err != nil {
		t.Fatalf("slider change: %v", err)
	}
	studioTree = tabW.until("slider updated", func(n *protocol.UiNode) bool {
		return hasText(n, "85%")
	})

	// Switch studio sub-tab to Skeleton & Empty and enable skeletons.
	pills := findBySuffix(studioTree, "studio-pills")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: pills.GetId(), Kind: protocol.UiChange, Value: "스켈레톤 & 빈 상태",
	}); err != nil {
		t.Fatalf("switch to skeleton pill: %v", err)
	}
	skTree := tabW.until("empty state in studio", func(n *protocol.UiNode) bool {
		return findByKind(n, "empty") != nil
	})
	skToggle := findBySuffix(skTree, "sk-toggle")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: skToggle.GetId(), Kind: protocol.UiChange, Value: "true",
	}); err != nil {
		t.Fatalf("enable skeletons: %v", err)
	}
	tabW.until("skeletons visible", func(n *protocol.UiNode) bool {
		return findByKind(n, "skeleton") != nil
	})

	// Switch to Canvas tab (KO) & verify ui.canvas ops, pan/zoom, scene switch, and hitId selection.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: mainTabs.GetId(), Kind: protocol.UiChange, Value: "캔버스",
	}); err != nil {
		t.Fatalf("switch to canvas: %v", err)
	}
	canvasTree := tabW.until("canvas tab", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "demo-canvas") != nil
	})
	cvNode := findBySuffix(canvasTree, "demo-canvas")
	if len(cvNode.GetCanvasCommands()) < 6 || !cvNode.GetCanvasPan() || !cvNode.GetCanvasZoom() {
		t.Fatalf("unexpected demo-canvas node: cmds=%d pan=%v zoom=%v", len(cvNode.GetCanvasCommands()), cvNode.GetCanvasPan(), cvNode.GetCanvasZoom())
	}
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: cvNode.GetId(), Kind: protocol.UiChange, Value: "node-gateway",
	}); err != nil {
		t.Fatalf("canvas hitId change: %v", err)
	}
	tabW.until("canvas inspector updated", func(n *protocol.UiNode) bool {
		insp := findBySuffix(n, "canvas-inspector")
		return insp != nil && hasText(insp, "node-gateway")
	})
	sceneRadio := findBySuffix(canvasTree, "canvas-scene")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: sceneRadio.GetId(), Kind: protocol.UiChange, Value: "pipeline",
	}); err != nil {
		t.Fatalf("canvas scene change: %v", err)
	}
	canvasTree = tabW.until("canvas pipeline scene", func(n *protocol.UiNode) bool {
		cv := findBySuffix(n, "demo-canvas")
		if cv == nil {
			return false
		}
		for _, cmd := range cv.GetCanvasCommands() {
			if cmd.GetHitId() == "stage-plan" {
				return true
			}
		}
		return false
	})
	canvasReset := findBySuffix(canvasTree, "canvas-reset")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: canvasReset.GetId(), Kind: protocol.UiTap,
	}); err != nil {
		t.Fatalf("canvas reset tap: %v", err)
	}
	tabW.until("canvas inspector cleared", func(n *protocol.UiNode) bool {
		insp := findBySuffix(n, "canvas-inspector")
		return insp != nil && !hasText(insp, "node-gateway")
	})

	// Switch to Timeline tab (KO) & verify ui.timeline lanes/spans and span selection.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: mainTabs.GetId(), Kind: protocol.UiChange, Value: "타임라인",
	}); err != nil {
		t.Fatalf("switch to timeline: %v", err)
	}
	timelineTree := tabW.until("timeline tab", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "demo-timeline") != nil
	})
	tlNode := findBySuffix(timelineTree, "demo-timeline")
	if len(tlNode.GetTimelineLanes()) < 2 || tlNode.GetMaxHeight() <= 0 || tlNode.GetGrow() <= 0 {
		t.Fatalf("unexpected demo-timeline node: lanes=%d maxH=%v grow=%v", len(tlNode.GetTimelineLanes()), tlNode.GetMaxHeight(), tlNode.GetGrow())
	}
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: tlNode.GetId(), Kind: protocol.UiChange, Value: "sp-explore-1",
	}); err != nil {
		t.Fatalf("timeline span change: %v", err)
	}
	tabW.until("timeline inspector updated", func(n *protocol.UiNode) bool {
		insp := findBySuffix(n, "timeline-inspector")
		return insp != nil && hasText(insp, "sp-explore-1")
	})

	// Switch to Charts tab (KO) & verify area, stacked-bar, and heatmap charts + onChange payloads.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: mainTabs.GetId(), Kind: protocol.UiChange, Value: "차트",
	}); err != nil {
		t.Fatalf("switch to charts: %v", err)
	}
	chartsTree := tabW.until("charts tab", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "chart-area") != nil && findBySuffix(n, "chart-stacked") != nil && findBySuffix(n, "chart-heatmap") != nil
	})
	chArea := findBySuffix(chartsTree, "chart-area")
	chStacked := findBySuffix(chartsTree, "chart-stacked")
	chHeatmap := findBySuffix(chartsTree, "chart-heatmap")
	for _, tc := range []struct {
		node *protocol.UiNode
		want string
	}{
		{chArea, "area"},
		{chStacked, "stacked-bar"},
		{chHeatmap, "heatmap"},
	} {
		if tc.node.GetChart() != tc.want || tc.node.GetMaxHeight() <= 0 || tc.node.GetGrow() <= 0 {
			t.Fatalf("chart %s = {chart:%q maxH:%v grow:%v}, want %q with maxH>0 & grow>0", tc.node.GetId(), tc.node.GetChart(), tc.node.GetMaxHeight(), tc.node.GetGrow(), tc.want)
		}
	}
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: chArea.GetId(), Kind: protocol.UiChange, Value: `{"index":2,"label":"T3"}`,
	}); err != nil {
		t.Fatalf("chart area change: %v", err)
	}
	tabW.until("chart inspector area updated", func(n *protocol.UiNode) bool {
		insp := findBySuffix(n, "chart-inspector")
		return insp != nil && hasText(insp, "area: index #2 (T3)")
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: chHeatmap.GetId(), Kind: protocol.UiChange, Value: `{"row":1,"col":2,"series":"Wasm","label":"Wed","value":74}`,
	}); err != nil {
		t.Fatalf("chart heatmap change: %v", err)
	}
	tabW.until("chart inspector heatmap updated", func(n *protocol.UiNode) bool {
		insp := findBySuffix(n, "chart-inspector")
		return insp != nil && hasText(insp, "heatmap: Wasm / Wed [1,2] = 74")
	})

	// Switch to Graph tab (KO) & verify cards variant, minimap, layout switcher, doubleTap, and drop persistence.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: mainTabs.GetId(), Kind: protocol.UiChange, Value: "그래프",
	}); err != nil {
		t.Fatalf("switch to graph: %v", err)
	}
	graphTree := tabW.until("graph tab", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "demo-graph") != nil
	})
	gNode := findBySuffix(graphTree, "demo-graph")
	if gNode.GetVariant() != "cards" || !gNode.GetMinimap() || !gNode.GetDoubleTap() || !gNode.GetDrop() {
		t.Fatalf("unexpected demo-graph flags: variant=%q minimap=%v doubleTap=%v drop=%v", gNode.GetVariant(), gNode.GetMinimap(), gNode.GetDoubleTap(), gNode.GetDrop())
	}
	layoutRadio := findBySuffix(graphTree, "graph-layout")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: layoutRadio.GetId(), Kind: protocol.UiChange, Value: "hierarchy",
	}); err != nil {
		t.Fatalf("graph layout change: %v", err)
	}
	tabW.until("graph hierarchy layout", func(n *protocol.UiNode) bool {
		g := findBySuffix(n, "demo-graph")
		return g != nil && g.GetGraphLayout() == "hierarchy"
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: gNode.GetId(), Kind: protocol.UiDoubleTap, Value: "wb-lead",
	}); err != nil {
		t.Fatalf("graph doubleTap: %v", err)
	}
	tabW.until("graph double-tap recorded", func(n *protocol.UiNode) bool {
		insp := findBySuffix(n, "graph-inspector")
		return insp != nil && hasText(insp, "wb-lead")
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: gNode.GetId(), Kind: protocol.UiDrop, Value: `{"id":"wb-lead","x":180,"y":90}`,
	}); err != nil {
		t.Fatalf("graph drop: %v", err)
	}
	tabW.until("graph position pinned and double-tap recorded", func(n *protocol.UiNode) bool {
		g := findBySuffix(n, "demo-graph")
		insp := findBySuffix(n, "graph-inspector")
		if g == nil || insp == nil || !hasText(insp, "wb-lead") {
			return false
		}
		for _, gn := range g.GetGraphNodes() {
			if gn.GetId() == "wb-lead" && gn.GetHasPos() && gn.GetX() == 180 && gn.GetY() == 90 {
				return true
			}
		}
		return false
	})

	// Reset pinned graph positions and verify hasPos clears.
	graphReset := findBySuffix(graphTree, "graph-reset-pos")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: graphReset.GetId(), Kind: protocol.UiTap,
	}); err != nil {
		t.Fatalf("graph reset-pos tap: %v", err)
	}
	tabW.until("graph pins cleared", func(n *protocol.UiNode) bool {
		g := findBySuffix(n, "demo-graph")
		if g == nil {
			return false
		}
		for _, gn := range g.GetGraphNodes() {
			if gn.GetId() == "wb-lead" && gn.GetHasPos() {
				return false
			}
		}
		return true
	})

	// Unknown preset/section fallback recovers cleanly to default diff section.
	fallbackW := watchViewForTest(t, t.Context(), r.m, "plugin:ui-workbench/viewer", "", "en", View{
		Instance: "fallback-inst",
		Params:   `{"preset":"nonexistent-preset","section":"unknown-section"}`,
	})
	fallbackW.until("unknown section fallback", func(n *protocol.UiNode) bool {
		mt := findBySuffix(n, "main-tabs")
		return mt != nil && mt.GetValue() == "Diff Review" && findByKind(n, "diff") != nil
	})

	// Tap Close button -> verifies close_tab intent with matching key.
	closeBtn := findBySuffix(diffTree, "btn-close")
	closeRes, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/viewer", Instance: "diff-auth", Node: closeBtn.GetId(), Kind: protocol.UiTap,
	})
	if err != nil {
		t.Fatalf("close tab tap: %v", err)
	}
	if len(closeRes.GetOpen()) != 1 || closeRes.GetOpen()[0].GetKind() != "close_tab" || closeRes.GetOpen()[0].GetKey() != "diff-auth" {
		t.Fatalf("unexpected closeTab intent: %+v", closeRes.GetOpen())
	}
}

// TestUIWorkbenchStorageAndReloadResilience verifies that corrupted or
// out-of-bounds persisted storage entries (graph-pos:*, comments:*, scratch:*)
// recover cleanly without JS errors or uiblock rejections, and that saving a
// new comment after reloading persisted comments advances commentSeq past
// existing IDs (no duplicate key collision on "c1").
func TestUIWorkbenchStorageAndReloadResilience(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "plugin-data", "ui-workbench")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seeded := map[string]string{
		"graph-pos:huge":               `{"wb-lead":{"x":1e9,"y":1e9},"wb-router":{"x":1e308,"y":-1e308},"wb-explorer":{"x":null,"y":null},"wb-impl":{"x":[],"y":true},"wb-verify":{"x":120,"y":80}}`,
		"comments:bad-elem:diff-auth":  `[null,"bad",123,[],{"id":"c1","line":1,"text":null},{"id":"c2","line":"bad","text":"nope"},{"id":"c3","line":0,"text":"zero"},{"id":"c4","line":2,"text":"valid\u001b\u202e note"},{"id":"c/bad#id","line":3,"text":"slash hash"},{"id":"c/bad#id","line":4,"text":"dup id"}]`,
		"comments:only-bad:diff-auth":  `[null,"bad",{"id":"c1","line":1,"text":null}]`,
		"comments:seq-inst:diff-auth":  `[{"id":"c1","line":1,"text":"existing comment"}]`,
		"scratch:scratch-inst":         "line1\n\tline2\x00\u202e",
	}
	rawSeed, err := json.Marshal(seeded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "storage.json"), rawSeed, 0o600); err != nil {
		t.Fatal(err)
	}

	lim := testLimits
	lim.Events = 100
	r := startRigIn(t, base, Config{Limits: lim})
	files := map[string]string{}
	for _, name := range []string{ManifestName, "main.js"} {
		b, err := os.ReadFile(filepath.Join("../../examples/plugins/ui-workbench", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	r.write("ui-workbench", files)
	r.state("ui-workbench", protocol.PluginWaiting)
	r.grant("ui-workbench")

	// 1. Explorer countAllNotes counts only valid normalized comments (3 + 1 = 4).
	expW := watchViewForTest(t, t.Context(), r.m, "plugin:ui-workbench/explorer", "", "en", View{})
	expTree := expW.until("explorer note count", func(n *protocol.UiNode) bool {
		st := findBySuffix(n, "st-notes")
		return st != nil && st.GetValue() == "4"
	})
	_ = expTree

	// 2. Bug 1: Out-of-bounds / invalid-type graph-pos:* entries are discarded while valid ones survive.
	graphW := watchViewForTest(t, t.Context(), r.m, "plugin:ui-workbench/viewer", "", "en", View{
		Instance: "huge",
		Params:   `{"preset":"graph-lab","section":"graph"}`,
	})
	graphTree := graphW.until("graph tab recovers from out-of-bounds coords", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "demo-graph") != nil
	})
	gNode := findBySuffix(graphTree, "demo-graph")
	for _, gn := range gNode.GetGraphNodes() {
		switch gn.GetId() {
		case "wb-lead", "wb-router", "wb-explorer", "wb-impl":
			if gn.GetHasPos() {
				t.Fatalf("node %q should have rejected invalid persisted position, got (%v, %v)", gn.GetId(), gn.GetX(), gn.GetY())
			}
		case "wb-verify":
			if !gn.GetHasPos() || gn.GetX() != 120 || gn.GetY() != 80 {
				t.Fatalf("wb-verify expected valid persisted pos (120, 80), got hasPos=%v (%v, %v)", gn.GetHasPos(), gn.GetX(), gn.GetY())
			}
		}
	}

	// 3. Bug 2: Malformed comments:* elements and spoofing chars recover cleanly; scratch:* sanitizes cleanly.
	badDiffW := watchViewForTest(t, t.Context(), r.m, "plugin:ui-workbench/viewer", "", "en", View{
		Instance: "bad-elem",
		Params:   `{"preset":"diff-auth","section":"diff"}`,
	})
	badDiffTree := badDiffW.until("diff tab recovers from malformed comment elements", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "diff-comments-card") != nil && hasText(n, "slash hash")
	})
	if findBySuffix(badDiffTree, "c4") == nil || findBySuffix(badDiffTree, "cbadid") == nil || findBySuffix(badDiffTree, "cbadid-2") == nil {
		t.Fatalf("expected sanitized deduplicated comment keys c4, cbadid, cbadid-2")
	}

	onlyBadW := watchViewForTest(t, t.Context(), r.m, "plugin:ui-workbench/viewer", "", "en", View{
		Instance: "only-bad",
		Params:   `{"preset":"diff-auth","section":"diff"}`,
	})
	onlyBadTree := onlyBadW.until("diff tab recovers from all-invalid comment array", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "diff-comments-card") != nil && findByKind(n, "diff") != nil
	})
	if findBySuffix(onlyBadTree, "btn-clear-comments") != nil {
		t.Fatalf("expected zero surviving comments on only-bad")
	}

	instW := watchViewForTest(t, t.Context(), r.m, "plugin:ui-workbench/viewer", "", "en", View{
		Instance: "scratch-inst",
		Params:   `{"preset":"instance-notes","section":"instance"}`,
	})
	instW.until("instance scratchpad recovers from control/spoofing chars", func(n *protocol.UiNode) bool {
		sc := findBySuffix(n, "scratch-input")
		return sc != nil && strings.Contains(sc.GetValue(), "line1\n\tline2")
	})

	// 4. Bug 3: Reloading persisted comments (c1) and saving a new comment generates c2 without colliding on c1.
	seqW := watchViewForTest(t, t.Context(), r.m, "plugin:ui-workbench/viewer", "", "en", View{
		Instance: "seq-inst",
		Params:   `{"preset":"diff-auth","section":"diff"}`,
	})
	seqTree := seqW.until("reloaded c1 comment rendered", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "c1") != nil && hasText(n, "existing comment")
	})
	draftNode := findBySuffix(seqTree, "comment-draft")
	saveBtn := findBySuffix(seqTree, "btn-save-comment")
	if draftNode == nil || saveBtn == nil {
		t.Fatal("missing comment-draft or btn-save-comment")
	}
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block:    "plugin:ui-workbench/viewer",
		Instance: "seq-inst",
		Node:     saveBtn.GetId(),
		Kind:     protocol.UiTap,
		Values:   map[string]string{draftNode.GetId(): "second comment"},
	}); err != nil {
		t.Fatalf("save comment after reload: %v", err)
	}
	seqTree = seqW.until("new comment saved as c2 without key collision", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "c1") != nil && findBySuffix(n, "c2") != nil && hasText(n, "second comment")
	})
	if !hasText(seqTree, "existing comment") {
		t.Fatalf("expected existing comment c1 to remain visible after saving c2")
	}
}

// TestWasmInspectorExamplePlugin loads the real compiled Go plugin.wasm in
// examples/plugins/wasm-inspector, runs prime+SHA-256 compute inside wasmtime-pure-go,
// persists results via storage host_call, and opens a multi-instance custom tab.
func TestWasmInspectorExamplePlugin(t *testing.T) {
	if err := wasmtime.LoadError(); err != nil {
		t.Skipf("wasmtime unavailable: %v", err)
	}
	srcDir := "../../examples/plugins/wasm-inspector"
	manifestBytes, err := os.ReadFile(filepath.Join(srcDir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	wasmBytes, err := os.ReadFile(filepath.Join(srcDir, "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	pdir := filepath.Join(dir, "plugins", "wasm-inspector")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, ManifestName), manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "plugin.wasm"), wasmBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(Config{
		Dir:     filepath.Join(dir, "plugins"),
		DataDir: filepath.Join(dir, "data"),
		Poll:    50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Wait for discovery & grant storage.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		found := false
		for _, p := range m.List() {
			if p.GetId() == "wasm-inspector" {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := m.Act(ctx, &protocol.PluginAction{PluginId: "wasm-inspector", Op: protocol.PluginGrant}); err != nil {
		t.Fatalf("grant wasm-inspector: %v", err)
	}

	// Watch sidebar block.
	panelW := watchViewForTest(t, ctx, m, "plugin:wasm-inspector/panel", "", "en", View{})
	tree := panelW.until("initial wasm panel", func(n *protocol.UiNode) bool {
		return findByKind(n, "slider") != nil && findByKind(n, "empty") != nil
	})

	// Run real prime sieve (<= 10000 -> 1229 primes, largest 9973) + SHA-256 inside Wasm!
	runBtn := findBySuffix(tree, "btn-run")
	if runBtn == nil {
		t.Fatal("missing btn-run button")
	}
	runRes, err := m.Event(ctx, &protocol.BlockEvent{
		Block: "plugin:wasm-inspector/panel", Node: runBtn.GetId(), Kind: protocol.UiTap,
	})
	if err != nil {
		t.Fatalf("runCompute tap: %v", err)
	}
	if !strings.Contains(runRes.GetToast(), "1229 primes") {
		t.Fatalf("unexpected toast = %q, want 1229 primes", runRes.GetToast())
	}

	tree = panelW.until("compute results rendered", func(n *protocol.UiNode) bool {
		stNode := findBySuffix(n, "st-primes")
		return findByKind(n, "code") != nil &&
			stNode != nil && stNode.GetValue() == "1229" && stNode.GetPlaceholder() == "max 9973" &&
			hasText(n, `"primes": 1229`)
	})

	// Open Wasm Report custom workspace tab from Wasm via mako.openTab host_call.
	openBtn := findBySuffix(tree, "btn-open-tab")
	openRes, err := m.Event(ctx, &protocol.BlockEvent{
		Block: "plugin:wasm-inspector/panel", Node: openBtn.GetId(), Kind: protocol.UiTap,
	})
	if err != nil {
		t.Fatalf("openDetail tap: %v", err)
	}
	if len(openRes.GetOpen()) != 1 || openRes.GetOpen()[0].GetKind() != "tab" ||
		openRes.GetOpen()[0].GetBlock() != "plugin:wasm-inspector/detail" ||
		openRes.GetOpen()[0].GetKey() != "run-1" {
		t.Fatalf("unexpected openTab intent from wasm: %+v", openRes.GetOpen())
	}

	// Watch the custom workspace tab in Korean and verify table history renders.
	detW := watchViewForTest(t, ctx, m, "plugin:wasm-inspector/detail", "", "ko", View{
		Instance: "run-1",
		Params:   openRes.GetOpen()[0].GetParams(),
	})
	detW.until("wasm detail tab in Korean", func(n *protocol.UiNode) bool {
		return findByKind(n, "table") != nil && hasText(n, "9973") && hasText(n, "실행 기록")
	})
}

// TestAgentMapExamplePlugin verifies examples/plugins/agent-map end-to-end:
// multi-machine -> project -> agent -> subagent topology on ui.graph cards,
// minimap, branch/profile/approval badges, drag-drop position persistence,
// double-tap and context menu session open intents, inspector, and KO i18n.
func TestAgentMapExamplePlugin(t *testing.T) {
	sessions := []*protocol.SessionInfo{
		{Id: "s-mako-1", Name: "Plugin Architect", Agent: "agy", Project: "/work/oss/mako", State: protocol.StateRunning, Status: protocol.AgentWorking, NodeId: "mac-studio", Branch: "feat/graph-v2", Profile: "architect"},
		{Id: "s-mako-2", Title: "◐ Claude Code", Agent: "claude", Project: "/work/oss/mako", State: protocol.StateRunning, Status: protocol.AgentWaiting, NodeId: "mac-studio", ParentSessionId: "s-mako-1", Approval: &protocol.Approval{Id: "ap-1", Tool: "Bash"}},
		{Id: "s-game-1", Name: "Engine Refactor", Agent: "codex", Project: "/work/stone-remaster", State: protocol.StateStopped, Status: protocol.AgentIdle, NodeId: "mac-studio"},
	}
	r := newRig(t, testLimits, nil, sessions...)
	files := map[string]string{}
	for _, name := range []string{ManifestName, "main.js"} {
		b, err := os.ReadFile(filepath.Join("../../examples/plugins/agent-map", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	r.write("agent-map", files)
	r.state("agent-map", protocol.PluginWaiting)
	r.grant("agent-map")

	w := watchViewForTest(t, t.Context(), r.m, "plugin:agent-map/map", "", "ko", View{})
	tree := w.until("initial agent map graph", func(n *protocol.UiNode) bool {
		g := findByKind(n, "graph")
		return g != nil && len(g.GetGraphNodes()) == 6 && len(g.GetGraphEdges()) == 5
	})

	g := findByKind(tree, "graph")
	if g.GetVariant() != "cards" || !g.GetMinimap() {
		t.Fatalf("expected variant=cards & minimap=true, got variant=%q minimap=%v", g.GetVariant(), g.GetMinimap())
	}
	var sawClaudeTitle, sawSubagentEdge, sawBadges bool
	for _, nd := range g.GetGraphNodes() {
		if nd.GetId() == "sess:s-mako-2" {
			if strings.Contains(nd.GetLabel(), "Claude Code") {
				sawClaudeTitle = true
			}
			if nd.GetTone() != "warning" || nd.GetStatus() != "waiting" {
				t.Fatalf("waiting approval node = %+v", nd)
			}
			for _, b := range nd.GetBadges() {
				if b.GetText() == "Bash" {
					sawBadges = true
				}
			}
		}
		if nd.GetShape() != "card" {
			t.Fatalf("expected card shape on node %s, got %q", nd.GetId(), nd.GetShape())
		}
	}
	for _, ge := range g.GetGraphEdges() {
		if ge.GetFrom() == "sess:s-mako-1" && ge.GetTo() == "sess:s-mako-2" && ge.GetKind() == "parent" {
			sawSubagentEdge = true
		}
	}
	if !sawClaudeTitle || !sawSubagentEdge || !sawBadges {
		t.Fatalf("missing expected topology features: title=%v subagentEdge=%v badges=%v", sawClaudeTitle, sawSubagentEdge, sawBadges)
	}

	// Drag-drop position persistence.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiDrop, Value: `{"id":"sess:s-mako-1","x":240,"y":120}`,
	}); err != nil {
		t.Fatalf("drop graph node: %v", err)
	}
	w.until("saved node coordinates", func(n *protocol.UiNode) bool {
		ng := findByKind(n, "graph")
		if ng == nil {
			return false
		}
		for _, nd := range ng.GetGraphNodes() {
			if nd.GetId() == "sess:s-mako-1" {
				return nd.GetHasPos() && nd.GetX() == 240 && nd.GetY() == 120
			}
		}
		return false
	})

	// Double-tap session node -> emits PluginOpen{Kind: "session", Key: "s-mako-1"}.
	dtapRes, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiDoubleTap, Value: "sess:s-mako-1",
	})
	if err != nil {
		t.Fatalf("double tap graph node: %v", err)
	}
	if len(dtapRes.GetOpen()) != 1 || dtapRes.GetOpen()[0].GetKey() != "s-mako-1" {
		t.Fatalf("unexpected double-tap openSession intent: %+v", dtapRes.GetOpen())
	}

	// Context menu open-session item -> emits PluginOpen{Kind: "session", Key: "s-mako-2"}.
	ctxOpen := findBySuffix(tree, "ctx-open")
	if ctxOpen == nil {
		t.Fatal("missing ctx-open menu item in tree")
	}
	ctxRes, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: ctxOpen.GetId(), Kind: protocol.UiTap, Value: "sess:s-mako-2",
	})
	if err != nil {
		t.Fatalf("context menu tap: %v", err)
	}
	if len(ctxRes.GetOpen()) != 1 || ctxRes.GetOpen()[0].GetKey() != "s-mako-2" {
		t.Fatalf("unexpected context menu openSession intent: %+v", ctxRes.GetOpen())
	}

	// Select agent node s-mako-1 -> inspector renders enriched metadata & Jump to Session button.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiChange, Value: "sess:s-mako-1",
	}); err != nil {
		t.Fatalf("select graph node: %v", err)
	}
	inspTree := w.until("agent inspector", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "btn-jump-session") != nil && hasText(n, "Plugin Architect") && hasText(n, "feat/graph-v2")
	})

	// Tap Jump to session -> emits PluginOpen{Kind: "session", Key: "s-mako-1"}.
	jumpBtn := findBySuffix(inspTree, "btn-jump-session")
	jumpRes, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: jumpBtn.GetId(), Kind: protocol.UiTap,
	})
	if err != nil {
		t.Fatalf("jump tap: %v", err)
	}
	if len(jumpRes.GetOpen()) != 1 || jumpRes.GetOpen()[0].GetKind() != "session" || jumpRes.GetOpen()[0].GetKey() != "s-mako-1" {
		t.Fatalf("unexpected openSession intent: %+v", jumpRes.GetOpen())
	}

	// Switch to Sessions table view in Korean.
	viewTabs := findBySuffix(inspTree, "view-tabs")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: viewTabs.GetId(), Kind: protocol.UiChange, Value: "세션 목록",
	}); err != nil {
		t.Fatalf("switch to table view: %v", err)
	}
	w.until("sessions table view", func(n *protocol.UiNode) bool {
		return findByKind(n, "table") != nil && hasText(n, "Engine Refactor")
	})
}

func loadAgentMapFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{ManifestName, "main.js"} {
		b, err := os.ReadFile(filepath.Join("../../examples/plugins/agent-map", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	return files
}

// TestAgentMapSubagentContextMenu verifies that agent-map declares "fs:project",
// resolves project + root via mako.files.listProjects() (including subdirectory
// sessions and fallback), and spawns a subagent with ParentSessionID via the
// ctx-subagent -> dlg-input -> dlg-submit flow.
func TestAgentMapSubagentContextMenu(t *testing.T) {
	agents := &fakeAgents{}
	sessions := []*protocol.SessionInfo{
		{Id: "s-parent", Name: "Parent Agent", Agent: "claude", Project: "/work/oss/mako/sub", State: protocol.StateRunning, Status: protocol.AgentIdle, NodeId: "mac-studio"},
		{Id: "s-unmatched", Name: "External Session", Agent: "codex", Project: "/other/unregistered/dir", State: protocol.StateRunning, Status: protocol.AgentIdle, NodeId: "mac-studio"},
	}
	r := startRig(t, Config{
		Limits:   testLimits,
		Sessions: func() []*protocol.SessionInfo { return sessions },
		Projects: func() []ProjectRoots {
			return []ProjectRoots{{ID: "proj-mako", Name: "Mako", Roots: []string{"/work/oss/mako"}}}
		},
		Agents: agents,
	})
	r.write("agent-map", loadAgentMapFiles(t))
	info := r.state("agent-map", protocol.PluginWaiting)
	if !slices.Contains(info.GetPermissions(), "fs:project") {
		t.Fatalf("agent-map permissions = %v, want fs:project included", info.GetPermissions())
	}
	r.grant("agent-map")

	w := watchViewForTest(t, t.Context(), r.m, "plugin:agent-map/map", "", "en", View{})
	tree := w.until("initial graph", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "ctx-subagent") != nil
	})

	// 1. Trigger ctx-subagent on subdirectory session s-parent -> dlg-input -> dlg-submit.
	ctxSub := findBySuffix(tree, "ctx-subagent")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: ctxSub.GetId(), Kind: protocol.UiTap, Value: "sess:s-parent",
	}); err != nil {
		t.Fatalf("ctx-subagent tap: %v", err)
	}
	dlgTree := w.until("subagent dialog", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "dlg-input") != nil && findBySuffix(n, "dlg-submit") != nil
	})
	dlgInput := findBySuffix(dlgTree, "dlg-input")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: dlgInput.GetId(), Kind: protocol.UiChange, Value: "audit mesh endpoints",
	}); err != nil {
		t.Fatalf("dlg-input change: %v", err)
	}
	dlgSubmit := findBySuffix(dlgTree, "dlg-submit")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: dlgSubmit.GetId(), Kind: protocol.UiTap,
	}); err != nil {
		t.Fatalf("dlg-submit tap: %v", err)
	}

	ask := r.ask("agent-map")
	if _, err := r.act(&protocol.PluginAction{
		PluginId: "agent-map",
		Op:       protocol.PluginAllowAgent,
		Key:      ask.GetId(),
		Value:    protocol.AgentAlways,
	}); err != nil {
		t.Fatalf("allow agent: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for agents.starts() == 0 && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
	}
	agents.mu.Lock()
	if len(agents.started) != 1 {
		agents.mu.Unlock()
		t.Fatalf("started count = %d, want 1", len(agents.started))
	}
	got := agents.started[0]
	agents.mu.Unlock()
	if got.ParentSessionID != "s-parent" || got.Project != "proj-mako" || got.Root != "/work/oss/mako" {
		t.Fatalf("first AgentStart = %+v, want ParentSessionID=s-parent Project=proj-mako Root=/work/oss/mako", got)
	}

	// 2. Fallback on s-unmatched (/other/unregistered/dir): must set BOTH project and root from p.roots[0].
	w.until("dialog closed", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "dlg-input") == nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: ctxSub.GetId(), Kind: protocol.UiTap, Value: "sess:s-unmatched",
	}); err != nil {
		t.Fatalf("ctx-subagent fallback tap: %v", err)
	}
	dlgTree2 := w.until("subagent dialog 2", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "dlg-input") != nil && findBySuffix(n, "dlg-submit") != nil
	})
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: findBySuffix(dlgTree2, "dlg-input").GetId(), Kind: protocol.UiChange, Value: "fallback root task",
	}); err != nil {
		t.Fatalf("dlg-input 2 change: %v", err)
	}
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: findBySuffix(dlgTree2, "dlg-submit").GetId(), Kind: protocol.UiTap,
	}); err != nil {
		t.Fatalf("dlg-submit 2 tap: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for agents.starts() < 2 && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
	}
	agents.mu.Lock()
	defer agents.mu.Unlock()
	if len(agents.started) != 2 {
		t.Fatalf("started count = %d, want 2", len(agents.started))
	}
	if agents.started[1].ParentSessionID != "s-unmatched" || agents.started[1].Project != "proj-mako" || agents.started[1].Root != "/work/oss/mako" {
		t.Fatalf("fallback AgentStart = %+v, want ParentSessionID=s-unmatched Project=proj-mako Root=/work/oss/mako", agents.started[1])
	}
}

// TestAgentMapUTF8TruncationAndControlFallback verifies that long Korean/CJK
// machine, project, and session names (>= 30 Korean characters) stay within
// uiblock UTF-8 byte limits and control-char-only names fall back cleanly.
func TestAgentMapUTF8TruncationAndControlFallback(t *testing.T) {
	longMachine := strings.Repeat("가", 30) + strings.Repeat("🚀", 5)
	longProject := "/work/" + strings.Repeat("🚀", 8) + strings.Repeat("프", 30)
	longSession := strings.Repeat("한", 35)
	longBranch := strings.Repeat("브", 30)

	nodes := []*protocol.Node{
		{Id: "node-kr", Name: longMachine, Os: "darwin", Arch: "arm64", Source: "self", Link: protocol.LinkUp, Trusted: true},
		{Id: "node-ctrl", Name: "\x00\x01\u202e\x1f  ", Os: "linux", Arch: "amd64", Link: protocol.LinkUp, Trusted: true},
	}
	sessions := []*protocol.SessionInfo{
		{Id: "s-kr", Name: longSession, Agent: "claude", Project: longProject, State: protocol.StateRunning, Status: protocol.AgentWorking, NodeId: "node-kr", Branch: longBranch},
		{Id: "s-ctrl", Name: "\x00\u202e", Agent: "codex", Project: "/work/app", State: protocol.StateRunning, Status: protocol.AgentIdle, NodeId: "node-ctrl"},
	}

	r := startRig(t, Config{
		Limits:   testLimits,
		NodeID:   "node-kr",
		Sessions: func() []*protocol.SessionInfo { return sessions },
		Nodes:    func() []*protocol.Node { return nodes },
	})
	r.write("agent-map", loadAgentMapFiles(t))
	r.state("agent-map", protocol.PluginWaiting)
	r.grant("agent-map")

	w := watchViewForTest(t, t.Context(), r.m, "plugin:agent-map/map", "", "ko", View{})
	tree := w.until("UTF-8 Korean and control-fallback graph", func(n *protocol.UiNode) bool {
		g := findByKind(n, "graph")
		return g != nil && len(g.GetGraphNodes()) == 6
	})

	g := findByKind(tree, "graph")
	var sawCtrlMachine, sawCtrlSession bool
	for _, nd := range g.GetGraphNodes() {
		if nd.GetLabel() == "" || len(nd.GetLabel()) > 128 || !utf8.ValidString(nd.GetLabel()) {
			t.Fatalf("node %q invalid label: %q (bytes=%d)", nd.GetId(), nd.GetLabel(), len(nd.GetLabel()))
		}
		if len(nd.GetGroup()) > 64 || !utf8.ValidString(nd.GetGroup()) {
			t.Fatalf("node %q invalid group: %q (bytes=%d)", nd.GetId(), nd.GetGroup(), len(nd.GetGroup()))
		}
		if len(nd.GetSubtitle()) > 160 || !utf8.ValidString(nd.GetSubtitle()) {
			t.Fatalf("node %q invalid subtitle: %q (bytes=%d)", nd.GetId(), nd.GetSubtitle(), len(nd.GetSubtitle()))
		}
		if nd.GetId() == "mach:node-ctrl" {
			sawCtrlMachine = true
			if nd.GetLabel() != "node-ctrl" {
				t.Fatalf("control-char machine label = %q, want %q", nd.GetLabel(), "node-ctrl")
			}
		}
		if nd.GetId() == "sess:s-ctrl" {
			sawCtrlSession = true
			if !strings.Contains(nd.GetLabel(), "codex") {
				t.Fatalf("control-char session label = %q, want fallback containing codex", nd.GetLabel())
			}
		}
	}
	if !sawCtrlMachine || !sawCtrlSession {
		t.Fatalf("missing expected fallback nodes: mach=%v sess=%v", sawCtrlMachine, sawCtrlSession)
	}
}

func assertNoSpoofingInTree(t *testing.T, n *protocol.UiNode) {
	t.Helper()
	if n == nil {
		return
	}
	check := func(field, s string) {
		if strings.ContainsFunc(s, protocol.Spoofing) {
			t.Fatalf("node %q (%s) %s contains spoofing rune: %q", n.GetId(), n.GetKind(), field, s)
		}
		if !utf8.ValidString(s) {
			t.Fatalf("node %q (%s) %s has invalid UTF-8: %q", n.GetId(), n.GetKind(), field, s)
		}
	}
	check("text", n.GetText())
	check("value", n.GetValue())
	check("placeholder", n.GetPlaceholder())
	for _, b := range n.GetBadges() {
		if b.GetText() == "" && b.GetIcon() == "" {
			t.Fatalf("node %q has empty badge", n.GetId())
		}
		check("badge", b.GetText())
	}
	for _, gn := range n.GetGraphNodes() {
		check("graph.id", gn.GetId())
		check("graph.label", gn.GetLabel())
		check("graph.group", gn.GetGroup())
		check("graph.subtitle", gn.GetSubtitle())
		for _, b := range gn.GetBadges() {
			if b.GetText() == "" && b.GetIcon() == "" {
				t.Fatalf("graph node %q has empty badge", gn.GetId())
			}
			check("graph.badge", b.GetText())
		}
	}
	for _, ge := range n.GetGraphEdges() {
		check("edge.label", ge.GetLabel())
	}
	for _, c := range n.GetChildren() {
		assertNoSpoofingInTree(t, c)
	}
}

// TestAgentMapZWJAndSpoofingChars verifies that ZWJ emoji ("🧑‍💻", "👨‍👩‍👧‍👦")
// and zero-width / BOM / directional marks (\u061c, \u200b-\u200f, \ufeff)
// in machine, session, and project metadata render cleanly through uiblock.Read
// across map, inspector, projects, and table views, and that control-only Os/Link
// fields do not emit empty badges.
func TestAgentMapZWJAndSpoofingChars(t *testing.T) {
	nodes := []*protocol.Node{
		{Id: "node-zwj-1", Name: "🧑‍💻 Studio-\u061c\u200b\u200c\u200d\u200e\u200f\ufeffHost", Os: "\x00\u202e", Arch: "arm64", Source: "self", Link: "\u200b\x1f", Trusted: true},
		{Id: "node-zwj-2", Name: "👨‍👩‍👧‍👦 Family-Node", Os: "linux", Arch: "amd64", Link: "up", Trusted: true},
	}
	sessions := []*protocol.SessionInfo{
		{
			Id: "s-zwj-1", Name: "🧑‍💻 Architect \u200b\ufeffLead", Agent: "claude",
			Project: "/work/\u061c🧑‍💻mako\u200f", State: protocol.StateRunning, Status: protocol.AgentWorking,
			NodeId: "node-zwj-1", Branch: "feat/\u200bzwj-\u200demoji", Profile: "\u200epro\ufeff",
			Approval: &protocol.Approval{Id: "ap-zwj", Tool: "\u061cBash\u200f"},
		},
		{
			Id: "s-zwj-2", Name: "👨‍👩‍👧‍👦 Subagent", Agent: "codex",
			Project: "/work/\u200b\u200c\u200d\ufeff", State: protocol.StateRunning, Status: protocol.AgentIdle,
			NodeId: "node-zwj-2",
		},
	}

	r := startRig(t, Config{
		Limits:   testLimits,
		NodeID:   "node-zwj-1",
		Sessions: func() []*protocol.SessionInfo { return sessions },
		Nodes:    func() []*protocol.Node { return nodes },
	})
	r.write("agent-map", loadAgentMapFiles(t))
	r.state("agent-map", protocol.PluginWaiting)
	r.grant("agent-map")

	w := watchViewForTest(t, t.Context(), r.m, "plugin:agent-map/map", "", "en", View{})
	tree := w.until("ZWJ emoji initial map", func(n *protocol.UiNode) bool {
		g := findByKind(n, "graph")
		return g != nil && len(g.GetGraphNodes()) == 6 && findBySuffix(n, "insp-overview") != nil
	})
	assertNoSpoofingInTree(t, tree)

	g := findByKind(tree, "graph")
	var sawZWJSession, sawCleanMach bool
	for _, nd := range g.GetGraphNodes() {
		if nd.GetId() == "sess:s-zwj-1" {
			sawZWJSession = true
			// ZWJ stripped with "" so adjacent emoji glyphs stay joined without an inserted space.
			if !strings.Contains(nd.GetLabel(), "🧑💻") {
				t.Fatalf("ZWJ session label = %q, want adjacent glyph sequence %q", nd.GetLabel(), "🧑💻")
			}
		}
		if nd.GetId() == "mach:node-zwj-1" {
			sawCleanMach = true
			if !strings.Contains(nd.GetLabel(), "🧑💻") {
				t.Fatalf("ZWJ machine label = %q, want adjacent glyph sequence %q", nd.GetLabel(), "🧑💻")
			}
			for _, b := range nd.GetBadges() {
				if b.GetText() == "" {
					t.Fatalf("mach:node-zwj-1 emitted empty badge for control-only Os/Link: %+v", b)
				}
			}
		}
	}
	if !sawZWJSession || !sawCleanMach {
		t.Fatalf("missing expected nodes: sess=%v mach=%v", sawZWJSession, sawCleanMach)
	}

	// Select session, project, and machine nodes in inspector.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiChange, Value: "sess:s-zwj-1",
	}); err != nil {
		t.Fatalf("select s-zwj-1: %v", err)
	}
	sessInsp := w.until("ZWJ session inspector", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "insp-session") != nil && hasText(n, "Bash")
	})
	assertNoSpoofingInTree(t, sessInsp)

	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiChange, Value: "proj:0:0",
	}); err != nil {
		t.Fatalf("select proj:0:0: %v", err)
	}
	projInsp := w.until("ZWJ project inspector", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "insp-project") != nil
	})
	assertNoSpoofingInTree(t, projInsp)

	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiChange, Value: "mach:node-zwj-1",
	}); err != nil {
		t.Fatalf("select mach:node-zwj-1: %v", err)
	}
	machInsp := w.until("ZWJ machine inspector", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "insp-machine") != nil
	})
	assertNoSpoofingInTree(t, machInsp)

	// Switch to Projects and Sessions (table) views.
	viewTabs := findBySuffix(machInsp, "view-tabs")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: viewTabs.GetId(), Kind: protocol.UiChange, Value: "Projects",
	}); err != nil {
		t.Fatalf("switch to projects view: %v", err)
	}
	projTree := w.until("ZWJ projects view", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "projects-grid") != nil
	})
	assertNoSpoofingInTree(t, projTree)

	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: viewTabs.GetId(), Kind: protocol.UiChange, Value: "Sessions",
	}); err != nil {
		t.Fatalf("switch to table view: %v", err)
	}
	tblTree := w.until("ZWJ table view", func(n *protocol.UiNode) bool {
		return findByKind(n, "table") != nil
	})
	assertNoSpoofingInTree(t, tblTree)
}

// TestAgentMapAdversarialControlCharsAcrossAllViews verifies that control and
// directional reordering characters in Session.Project, Branch, Profile,
// Approval.Tool, Agent, and NodeId render cleanly across map, insp-overview,
// insp-session, insp-project, insp-machine, projects, and table views.
func TestAgentMapAdversarialControlCharsAcrossAllViews(t *testing.T) {
	nodes := []*protocol.Node{
		{Id: "node-clean", Name: "Clean\x00\u202eStudio", Os: "darwin\x07", Arch: "arm64\u2066", Source: "self", Link: "up\u200f", Trusted: true},
	}
	sessions := []*protocol.SessionInfo{
		{
			Id:              "s-adv-1",
			Name:            "SafeName",
			Agent:           "\u202eclaude\x1f",
			Project:         "/work/\x00\u202eadv-proj\u2066",
			State:           protocol.StateRunning,
			Status:          protocol.AgentWaiting,
			NodeId:          "node-clean",
			Branch:          "\x1b[31mmain\u202e",
			Profile:         "\u202aarch\x07",
			ParentSessionId: "s-adv-2",
			Approval:        &protocol.Approval{Id: "ap-adv", Tool: "\x00\u202eBash\u2069"},
		},
		{
			Id:       "s-adv-2",
			Name:     "\x00\u202e\u200b\ufeff",
			Agent:    "\x00\u202e",
			Project:  "\x00\x1b\u202e\u2066",
			State:    protocol.StateStopped,
			Status:   protocol.AgentIdle,
			NodeId:   "\x00\u202e",
			Branch:   "\x00\u202e",
			Profile:  "\u200b\u202e",
			Approval: &protocol.Approval{Id: "ap-ctrl", Tool: "\x00\u202e"},
		},
	}

	r := startRig(t, Config{
		Limits:   testLimits,
		NodeID:   "node-clean",
		Sessions: func() []*protocol.SessionInfo { return sessions },
		Nodes:    func() []*protocol.Node { return nodes },
	})
	r.write("agent-map", loadAgentMapFiles(t))
	r.state("agent-map", protocol.PluginWaiting)
	r.grant("agent-map")

	w := watchViewForTest(t, t.Context(), r.m, "plugin:agent-map/map", "", "en", View{})
	tree := w.until("adversarial map + insp-overview", func(n *protocol.UiNode) bool {
		g := findByKind(n, "graph")
		return g != nil && findBySuffix(n, "insp-overview") != nil && hasText(n, "adv-proj")
	})
	assertNoSpoofingInTree(t, tree)

	g := findByKind(tree, "graph")
	// Select s-adv-1 -> insp-session.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiChange, Value: "sess:s-adv-1",
	}); err != nil {
		t.Fatalf("select s-adv-1: %v", err)
	}
	s1Tree := w.until("insp-session s-adv-1", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "insp-session") != nil && hasText(n, "SafeName") && hasText(n, "Bash") && hasText(n, "arch")
	})
	assertNoSpoofingInTree(t, s1Tree)

	// Select s-adv-2 (control-char-only metadata) -> insp-session with non-empty fallbacks.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiChange, Value: "sess:s-adv-2",
	}); err != nil {
		t.Fatalf("select s-adv-2: %v", err)
	}
	s2Tree := w.until("insp-session s-adv-2", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "insp-session") != nil && hasText(n, "s-adv-2") && hasText(n, "(No folder)")
	})
	assertNoSpoofingInTree(t, s2Tree)

	// Select proj:0:0 -> insp-project.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiChange, Value: "proj:0:0",
	}); err != nil {
		t.Fatalf("select proj:0:0: %v", err)
	}
	pTree := w.until("insp-project", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "insp-project") != nil
	})
	assertNoSpoofingInTree(t, pTree)

	// Select mach:node-clean -> insp-machine.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiChange, Value: "mach:node-clean",
	}); err != nil {
		t.Fatalf("select mach:node-clean: %v", err)
	}
	mTree := w.until("insp-machine", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "insp-machine") != nil
	})
	assertNoSpoofingInTree(t, mTree)

	// Switch view-tabs -> Projects -> Sessions (table) -> Topology Map.
	viewTabs := findBySuffix(mTree, "view-tabs")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: viewTabs.GetId(), Kind: protocol.UiChange, Value: "Projects",
	}); err != nil {
		t.Fatalf("switch to Projects: %v", err)
	}
	projectsTree := w.until("projects view", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "projects-grid") != nil && hasText(n, "adv-proj")
	})
	assertNoSpoofingInTree(t, projectsTree)

	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: viewTabs.GetId(), Kind: protocol.UiChange, Value: "Sessions",
	}); err != nil {
		t.Fatalf("switch to Sessions: %v", err)
	}
	tableTree := w.until("table view", func(n *protocol.UiNode) bool {
		return findByKind(n, "table") != nil && hasText(n, "SafeName")
	})
	assertNoSpoofingInTree(t, tableTree)

	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: viewTabs.GetId(), Kind: protocol.UiChange, Value: "Topology Map",
	}); err != nil {
		t.Fatalf("switch back to Topology Map: %v", err)
	}
	backTree := w.until("back to map view", func(n *protocol.UiNode) bool {
		return findByKind(n, "graph") != nil
	})
	assertNoSpoofingInTree(t, backTree)
}
