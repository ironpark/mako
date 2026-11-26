package plugin

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	r := newRig(t, testLimits, nil)
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
// live sessions grouped into project hubs & agent nodes on ui.graph, real-time
// agentMessage communication edges, node inspection, and mako.openSession intent.
func TestAgentMapExamplePlugin(t *testing.T) {
	sessions := []*protocol.SessionInfo{
		{Id: "s-mako-1", Name: "Plugin Architect", Agent: "agy", Project: "/work/oss/mako", State: protocol.StateRunning, Status: protocol.AgentWorking, NodeId: "mac-studio"},
		{Id: "s-mako-2", Title: "◐ Claude Code", Agent: "claude", Project: "/work/oss/mako", State: protocol.StateRunning, Status: protocol.AgentWaiting, NodeId: "mac-studio"},
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
		return g != nil && len(g.GetGraphNodes()) == 5 && len(g.GetGraphEdges()) == 3
	})

	g := findByKind(tree, "graph")
	var sawClaudeTitle bool
	for _, nd := range g.GetGraphNodes() {
		if nd.GetId() == "sess:s-mako-2" && strings.Contains(nd.GetLabel(), "Claude Code") {
			sawClaudeTitle = true
		}
	}
	if !sawClaudeTitle {
		t.Fatalf("expected BareTitle fallback for unnamed session s-mako-2, nodes=%+v", g.GetGraphNodes())
	}

	// Select agent node s-mako-1 -> inspector renders Jump to Session button.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:agent-map/map", Node: g.GetId(), Kind: protocol.UiChange, Value: "sess:s-mako-1",
	}); err != nil {
		t.Fatalf("select graph node: %v", err)
	}
	inspTree := w.until("agent inspector", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "btn-jump-session") != nil && hasText(n, "Plugin Architect")
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

	// Switch to Projects cards view and Sessions table view in Korean.
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
