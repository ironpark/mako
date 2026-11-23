package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

func (r *rig) watchScopedView(block, scope, locale string, view View) *watcher {
	r.t.Helper()
	id, _, _ := splitBlock(block)
	r.info(id, func(*protocol.PluginInfo) bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	w := &watcher{t: r.t, cancel: cancel, changed: make(chan struct{}, 1)}
	go func() {
		err := r.m.WatchScopedView(ctx, block, scope, locale, view, func(u *protocol.BlockUpdate) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.updates++
			w.last = u
			switch {
			case u.GetTree() != nil:
				w.tree = u.GetTree()
			case len(u.GetOps()) > 0:
				next, err := protocol.ApplyUi(w.tree, u.GetOps())
				if err != nil {
					w.err = err
					return err
				}
				w.tree = next
			}
			select {
			case w.changed <- struct{}{}:
			default:
			}
			return nil
		})
		w.mu.Lock()
		if w.err == nil && err != nil && ctx.Err() == nil {
			w.err = err
		}
		w.mu.Unlock()
	}()
	r.t.Cleanup(cancel)
	return w
}

func TestBundledPluginLifecycleTrustAndHiddenBlocks(t *testing.T) {
	bundled := fstest.MapFS{
		"mako-demo/plugin.toml": &fstest.MapFile{Data: []byte(`
id = "mako-demo"
version = "1.0.0"
publisher = "mako"
api = 1
entry = "main.js"
permissions = ["storage", "git:read"]

[[contributes.block]]
id = "panel"
title = "Demo Panel"
scope = "folder"
chrome = "toolbar"
hidden = true

[[contributes.block]]
id = "visible"
title = "Visible Panel"
scope = "global"
`)},
		"mako-demo/main.js": &fstest.MapFile{Data: []byte(`
export default {
  activate(mako) {
    mako.blocks.register("panel", {
      render(state, ui, ctx) {
        return ui.text("folder=" + ctx.folder + " node=" + ctx.node + " solo=" + ctx.view.standalone);
      },
    });
    mako.blocks.register("visible", {
      render(state, ui, ctx) {
        return ui.text("visible solo=" + ctx.view.standalone);
      },
    });
  },
};
`)},
	}

	r := startRig(t, Config{Limits: testLimits, Bundled: bundled})

	// Bundled plugin loads ready immediately with all manifest permissions auto-granted.
	info := r.state("mako-demo", protocol.PluginReady)
	if !info.GetBundled() {
		t.Fatalf("expected Bundled=true, got %+v", info)
	}
	if len(info.GetAsked()) != 0 {
		t.Fatalf("expected no asked permissions for bundled plugin, got %v", info.GetAsked())
	}
	if strings.Join(info.GetGranted(), ",") != "storage,git:read" {
		t.Fatalf("granted = %v; want [storage git:read]", info.GetGranted())
	}

	// Hidden block is omitted from PluginInfo.Blocks, Manager.Blocks, and Manager.BlockProps.
	if len(info.GetBlocks()) != 1 || info.GetBlocks()[0].GetName() != "plugin:mako-demo/visible" {
		t.Fatalf("PluginInfo.Blocks = %+v; want only visible block", info.GetBlocks())
	}
	blocks := r.m.Blocks()
	if len(blocks) != 1 || blocks[0].Name != "plugin:mako-demo/visible" {
		t.Fatalf("Manager.Blocks = %+v; want only visible block", blocks)
	}
	if _, ok := r.m.BlockProps("plugin:mako-demo/panel"); ok {
		t.Fatalf("BlockProps unexpectedly exposed hidden block")
	}
	if _, ok := r.m.BlockProps("plugin:mako-demo/visible"); !ok {
		t.Fatalf("BlockProps missing visible block")
	}

	// Hidden block is still watchable directly with folder scope and reports chrome="toolbar".
	scopeKey := protocol.FolderScope("", "/tmp/demo-repo")
	w := r.watchScopedView("plugin:mako-demo/panel", scopeKey, "en", View{Standalone: true})
	tree := w.until("folder render", func(tr *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return hasText(tr, "folder=/tmp/demo-repo node=local solo=true") && u.GetChrome() == BlockChromeToolbar
	})
	if !hasText(tree, "folder=/tmp/demo-repo node=local solo=true") {
		t.Fatalf("unexpected tree: %v", tree)
	}

	// Deny is rejected for bundled plugins; Disable / Enable are honoured.
	if _, err := r.act(&protocol.PluginAction{PluginId: "mako-demo", Op: protocol.PluginDeny}); err == nil {
		t.Fatalf("expected PluginDeny to fail on bundled plugin")
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "mako-demo", Op: protocol.PluginDisable}); err != nil {
		t.Fatalf("PluginDisable failed: %v", err)
	}
	r.state("mako-demo", protocol.PluginDisabled)
	if _, err := r.act(&protocol.PluginAction{PluginId: "mako-demo", Op: protocol.PluginEnable}); err != nil {
		t.Fatalf("PluginEnable failed: %v", err)
	}
	r.state("mako-demo", protocol.PluginRunning)

	// Store mutation operations refuse bundled IDs.
	if err := r.m.Remove("mako-demo"); err == nil {
		t.Fatalf("Remove unexpectedly allowed bundled plugin")
	}
	if err := r.m.Replace("mako-demo", t.TempDir()); err == nil {
		t.Fatalf("Replace unexpectedly allowed bundled plugin")
	}
	if err := r.m.Forget("mako-demo"); err == nil {
		t.Fatalf("Forget unexpectedly allowed bundled plugin")
	}
	if err := r.m.Disown("mako-demo"); err == nil {
		t.Fatalf("Disown unexpectedly allowed bundled plugin")
	}
}

func TestBundledDevOverrideHotReloads(t *testing.T) {
	bundled := fstest.MapFS{
		"mako-hot/plugin.toml": &fstest.MapFile{Data: []byte(`
id = "mako-hot"
version = "1.0.0"
publisher = "mako"
api = 1
entry = "main.js"

[[contributes.block]]
id = "ui"
title = "Hot"
`)},
		"mako-hot/main.js": &fstest.MapFile{Data: []byte(`
export default {
  activate(mako) {
    mako.blocks.register("ui", { render: (s, ui) => ui.text("embedded") });
  },
};
`)},
	}

	devRoot := t.TempDir()
	writeFiles(t, filepath.Join(devRoot, "mako-hot"), map[string]string{
		"plugin.toml": `id = "mako-hot"
version = "1.0.0"
publisher = "mako"
api = 1
entry = "main.js"

[[contributes.block]]
id = "ui"
title = "Hot"
`,
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("ui", { render: (s, ui) => ui.text("dev-v1") });
  },
};`,
	})

	r := startRig(t, Config{Limits: testLimits, Bundled: bundled, BundledDev: devRoot})
	w := r.watchScopedView("plugin:mako-hot/ui", "", "en", View{})
	w.until("dev-v1", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "dev-v1")
	})

	// Edit JS on disk and verify hot-reload picks up dev-v2.
	time.Sleep(15 * time.Millisecond)
	writeFiles(t, filepath.Join(devRoot, "mako-hot"), map[string]string{
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("ui", { render: (s, ui) => ui.text("dev-v2") });
  },
};`,
	})
	w.until("dev-v2", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "dev-v2")
	})
}

func TestWatchScopedViewStandaloneAggregation(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("solo", map[string]string{
		"plugin.toml": manifest("solo", nil, `
[[contributes.block]]
id = "panel"
title = "Solo Test"
chrome = "toolbar"
`),
		"main.js": `export default {
  activate(mako) {
    mako.blocks.register("panel", {
      render(state, ui, ctx) {
        return ui.text("standalone=" + ctx.view.standalone);
      },
    });
  },
};`,
	})
	r.state("solo", protocol.PluginReady)

	// Viewer 1 watches standalone -> standalone=true, chrome=toolbar.
	w1 := r.watchScopedView("plugin:solo/panel", "", "en", View{Standalone: true})
	w1.until("standalone=true", func(tr *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return hasText(tr, "standalone=true") && u.GetChrome() == BlockChromeToolbar
	})

	// Viewer 2 watches tabbed -> AND aggregation flips both to standalone=false.
	w2 := r.watchScopedView("plugin:solo/panel", "", "en", View{Standalone: false})
	w1.until("standalone=false on w1", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "standalone=false")
	})
	w2.until("standalone=false on w2", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "standalone=false")
	})

	// Viewer 2 unsubscribes -> w1 flips back to standalone=true automatically.
	w2.cancel()
	w1.until("standalone=true again on w1", func(tr *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(tr, "standalone=true")
	})
}

func TestBundledWorksWithMinimalConfig(t *testing.T) {
	bundled := fstest.MapFS{
		"mako-min/plugin.toml": &fstest.MapFile{Data: []byte(`
id = "mako-min"
version = "1.0.0"
publisher = "mako"
api = 1
entry = "main.js"

[[contributes.block]]
id = "ui"
title = "Minimal"
`)},
		"mako-min/main.js": &fstest.MapFile{Data: []byte(`
export default {
  activate(mako) {
    mako.blocks.register("ui", { render: (s, ui) => ui.text("minimal-ok") });
  },
};
`)},
	}
	// Mirrors Track G test construction: Config{Bundled: fs, Git: LocalGit{}} with empty Dir/DataDir.
	m := New(Config{Bundled: bundled, Git: LocalGit{}, Limits: testLimits, Poll: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.Now().Add(10 * time.Second)
	for len(m.List()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("bundled plugin never listed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var mu sync.Mutex
	var tree *protocol.UiNode
	got := make(chan struct{}, 1)
	wctx, wcancel := context.WithCancel(context.Background())
	defer wcancel()
	go func() {
		_ = m.WatchScopedView(wctx, "plugin:mako-min/ui", "", "en", View{}, func(u *protocol.BlockUpdate) error {
			mu.Lock()
			if u.GetTree() != nil {
				tree = u.GetTree()
			}
			ok := hasText(tree, "minimal-ok")
			mu.Unlock()
			if ok {
				select {
				case got <- struct{}{}:
				default:
				}
			}
			return nil
		})
	}()
	select {
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("never rendered minimal-ok")
	}
	// Ensure no stray grants.json was written to cwd.
	if _, err := os.Stat("grants.json"); err == nil {
		t.Fatal("minimal config unexpectedly wrote grants.json in cwd")
	}
}
