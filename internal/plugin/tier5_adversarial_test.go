package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

func TestTier5WatchScopedViewParamsLoadedNoRace(t *testing.T) {
	r := newRig(t, Limits{}, nil)
	r.write("probe-race", map[string]string{
		ManifestName: manifest("probe-race", nil, ""),
		"main.js": `export default {
  activate(m) {
    m.blocks.register("ui", {
      state: { tab: "" },
      load(s, ctx) { s.tab = (ctx.tab && ctx.tab.params && ctx.tab.params.k) || ""; },
      render: (s, ui) => ui.text("tab=" + s.tab),
    });
  },
};`,
	})
	r.state("probe-race", protocol.PluginReady)

	var wg sync.WaitGroup
	for g := range 2 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 30 {
				ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
				p := `{"k":"` + string(rune('a'+(id*7+i)%26)) + `"}`
				_ = r.m.WatchScopedView(ctx, "plugin:probe-race/ui", "", "", View{Instance: "inst1", Params: p}, func(*protocol.BlockUpdate) error {
					return nil
				})
				cancel()
			}
		}(g)
	}
	wg.Wait()
}

func TestTier5StartAgentUnknownParentSessionRejected(t *testing.T) {
	agents := &fakeAgents{}
	sessions := []*protocol.SessionInfo{
		{Id: "s-real", Name: "Real", Agent: "claude", Project: "/proj", State: protocol.StateRunning},
	}
	r := startRig(t, Config{
		Limits:   testLimits,
		Agents:   agents,
		Sessions: func() []*protocol.SessionInfo { return sessions },
		Projects: func() []ProjectRoots { return []ProjectRoots{{ID: "p1", Name: "Proj", Roots: []string{"/proj"}}} },
	})
	r.write("bad-parent-exist", map[string]string{
		ManifestName: manifest("bad-parent-exist", []string{"sessions:write"}, ""),
		"main.js": `export default {
  activate(m) {
    m.blocks.register("ui", {
      state: { err: "" },
      async load(s) {
        try {
          await m.messages.startAgent({
            agent: "claude",
            project: "p1",
            root: "/proj",
            prompt: "hi",
            parentSessionId: "does-not-exist-999",
          });
          s.err = "unexpected-ok";
        } catch (e) {
          s.err = "rejected:" + e.message;
        }
      },
      render: (s, ui) => ui.text(s.err),
    });
  },
};`,
	})
	r.state("bad-parent-exist", protocol.PluginWaiting)
	r.grant("bad-parent-exist")

	w := r.watch("plugin:bad-parent-exist/ui")
	w.until("unknown parent rejected", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		for _, line := range texts(n) {
			if strings.Contains(line, "rejected:") && strings.Contains(line, "parentSessionId") {
				return true
			}
		}
		return false
	})
	if agents.starts() != 0 {
		t.Fatalf("expected 0 agent starts, got %d", agents.starts())
	}
}

func TestTier5UIWorkbenchStorageQuotaErrorResilient(t *testing.T) {
	lim := testLimits
	lim.Storage = 256
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

	tabW := watchViewForTest(t, t.Context(), r.m, "plugin:ui-workbench/viewer", "", "en", View{
		Instance: "diff-auth",
		Params:   `{"preset":"diff-auth","section":"diff"}`,
	})
	tree := tabW.until("viewer ready", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "comment-draft") != nil && findBySuffix(n, "btn-save-comment") != nil
	})

	// Exceed quota (256 bytes) via saveComment -> must not return handler error.
	draft := findBySuffix(tree, "comment-draft")
	saveBtn := findBySuffix(tree, "btn-save-comment")
	longText := strings.Repeat("quota-exceeded-", 25)
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block:    "plugin:ui-workbench/viewer",
		Instance: "diff-auth",
		Node:     saveBtn.GetId(),
		Kind:     protocol.UiTap,
		Values:   map[string]string{draft.GetId(): longText},
	}); err != nil {
		t.Fatalf("saveComment failed on storage quota error: %v", err)
	}
	diffWithComment := tabW.until("comment saved in state despite storage error", func(n *protocol.UiNode) bool {
		return hasText(n, "quota-exceeded-") && findBySuffix(n, "btn-clear-comments") != nil
	})
	clearBtn := findBySuffix(diffWithComment, "btn-clear-comments")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block:    "plugin:ui-workbench/viewer",
		Instance: "diff-auth",
		Node:     clearBtn.GetId(),
		Kind:     protocol.UiTap,
	}); err != nil {
		t.Fatalf("clearComments failed on storage error: %v", err)
	}

	// Switch to Tab Instance -> saveScratchpad over quota must not error.
	mainTabs := findBySuffix(tree, "main-tabs")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block:    "plugin:ui-workbench/viewer",
		Instance: "diff-auth",
		Node:     mainTabs.GetId(),
		Kind:     protocol.UiChange,
		Value:    "Tab Instance",
	}); err != nil {
		t.Fatalf("switch to Tab Instance: %v", err)
	}
	compTree := tabW.until("scratchpad ready", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "scratch-input") != nil && findBySuffix(n, "inst-save") != nil
	})
	scratchInput := findBySuffix(compTree, "scratch-input")
	saveScratch := findBySuffix(compTree, "inst-save")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block:    "plugin:ui-workbench/viewer",
		Instance: "diff-auth",
		Node:     saveScratch.GetId(),
		Kind:     protocol.UiTap,
		Values:   map[string]string{scratchInput.GetId(): longText},
	}); err != nil {
		t.Fatalf("saveScratchpad failed on storage quota error: %v", err)
	}

	// Switch to Graph -> dropGraphNode & resetGraphPositions over quota must not error.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block:    "plugin:ui-workbench/viewer",
		Instance: "diff-auth",
		Node:     mainTabs.GetId(),
		Kind:     protocol.UiChange,
		Value:    "Graph",
	}); err != nil {
		t.Fatalf("switch to Graph: %v", err)
	}
	graphTree := tabW.until("graph ready", func(n *protocol.UiNode) bool {
		return findByKind(n, "graph") != nil && findBySuffix(n, "graph-reset-pos") != nil
	})
	gNode := findByKind(graphTree, "graph")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block:    "plugin:ui-workbench/viewer",
		Instance: "diff-auth",
		Node:     gNode.GetId(),
		Kind:     protocol.UiDrop,
		Value:    `{"id":"wb-lead","x":180,"y":90}`,
	}); err != nil {
		t.Fatalf("dropGraphNode failed on storage quota error: %v", err)
	}
	resetBtn := findBySuffix(graphTree, "graph-reset-pos")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block:    "plugin:ui-workbench/viewer",
		Instance: "diff-auth",
		Node:     resetBtn.GetId(),
		Kind:     protocol.UiTap,
	}); err != nil {
		t.Fatalf("resetGraphPositions failed on storage quota error: %v", err)
	}
}

func TestTier5UIWorkbenchCorruptStorageResilient(t *testing.T) {
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

	p, ok := r.m.plugin("ui-workbench")
	if !ok {
		t.Fatal("missing ui-workbench plugin")
	}
	if err := os.MkdirAll(filepath.Dir(p.store.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.store.path, []byte("{corrupt json!"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.store.reset()

	tabW := watchViewForTest(t, t.Context(), r.m, "plugin:ui-workbench/viewer", "", "en", View{
		Instance: "diff-auth",
		Params:   `{"preset":"diff-auth","section":"diff"}`,
	})
	tabW.until("viewer renders cleanly despite corrupt storage.json", func(n *protocol.UiNode) bool {
		return findBySuffix(n, "main-tabs") != nil
	})

	expW := r.watch("plugin:ui-workbench/explorer")
	expTree := expW.until("explorer ready", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return findBySuffix(n, "tb-reset") != nil
	})
	resetBtn := findBySuffix(expTree, "tb-reset")
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:ui-workbench/explorer",
		Node:  resetBtn.GetId(),
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatalf("explorer resetAll failed under corrupt storage.json: %v", err)
	}
}
