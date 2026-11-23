package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/keychain"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

func (r *rig) watchScoped(block, scope string) *watcher {
	r.t.Helper()
	id, _, _ := splitBlock(block)
	r.info(id, func(*protocol.PluginInfo) bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	w := &watcher{t: r.t, cancel: cancel, changed: make(chan struct{}, 1)}
	go func() {
		err := r.m.WatchScoped(ctx, block, scope, func(u *protocol.BlockUpdate) error {
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

func TestParseScopeKeyValidation(t *testing.T) {
	good := []struct {
		raw, kind, id string
	}{
		{"", protocol.ScopeGlobal, ""},
		{"space:sp-1", protocol.ScopeSpace, "sp-1"},
		{"project:proj_123.abc", protocol.ScopeProject, "proj_123.abc"},
		{"project:repo:github.com/ironpark/mako", protocol.ScopeProject, "repo:github.com/ironpark/mako"},
		{"project:free:my-workspace", protocol.ScopeProject, "free:my-workspace"},
		{"session:s1", protocol.ScopeSession, "s1"},
		{"project:" + strings.Repeat("a", 256), protocol.ScopeProject, strings.Repeat("a", 256)},
	}
	for _, tc := range good {
		kind, id, err := parseScopeKey(tc.raw)
		if err != nil || kind != tc.kind || id != tc.id {
			t.Errorf("parseScopeKey(%q) = (%q, %q, %v); want (%q, %q, nil)", tc.raw, kind, id, err, tc.kind, tc.id)
		}
	}

	bad := []string{
		"global",
		"global:x",
		"project:",
		"space:",
		"session:",
		"org:123",
		"project:../secret",
		"project:repo:github.com/../secret",
		"project:a@b",
		"project:a b",
		"project:a\nb",
		"project:\u202ebad",
		"project:-leading-dash",
		"project:/leading-slash",
		"project:" + strings.Repeat("a", 257),
	}
	for _, raw := range bad {
		if _, _, err := parseScopeKey(raw); err == nil {
			t.Errorf("parseScopeKey(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestScopedStorageIsolationAndQuota(t *testing.T) {
	store := newKVStore(filepath.Join(t.TempDir(), "storage.json"), 1024)

	if _, err := store.setScoped("", "k", "global-val"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.setScoped("project:alpha", "k", "alpha-val"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.setScoped("project:alpha", "only-alpha", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.setScoped("project:beta", "k", "beta-val"); err != nil {
		t.Fatal(err)
	}
	// Scope IDs containing slashes/colons (e.g. repo:github.com/owner/repo) must
	// never collide across key boundaries.
	if _, err := store.setScoped("project:repo:github.com/a", "b/c", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.setScoped("project:repo:github.com/a/b", "c", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := store.getScoped("project:repo:github.com/a", "b/c"); err != nil || !ok || v != "v1" {
		t.Fatalf("slash scope 1 = (%q, %v, %v)", v, ok, err)
	}
	if v, ok, err := store.getScoped("project:repo:github.com/a/b", "c"); err != nil || !ok || v != "v2" {
		t.Fatalf("slash scope 2 = (%q, %v, %v)", v, ok, err)
	}

	// Global read/list stays isolated from scoped entries.
	if v, ok, err := store.getScoped("", "k"); err != nil || !ok || v != "global-val" {
		t.Fatalf("global get = (%q, %v, %v)", v, ok, err)
	}
	if keys, err := store.keysScoped("", ""); err != nil || strings.Join(keys, ",") != "k" {
		t.Fatalf("global keys = %v, %v; want [k]", keys, err)
	}

	// Scoped read/list strips internal prefix and isolates scopes.
	if v, ok, err := store.getScoped("project:alpha", "k"); err != nil || !ok || v != "alpha-val" {
		t.Fatalf("alpha get = (%q, %v, %v)", v, ok, err)
	}
	if keys, err := store.keysScoped("project:alpha", ""); err != nil || strings.Join(keys, ",") != "k,only-alpha" {
		t.Fatalf("alpha keys = %v, %v; want [k only-alpha]", keys, err)
	}
	if keys, err := store.keysScoped("project:beta", ""); err != nil || strings.Join(keys, ",") != "k" {
		t.Fatalf("beta keys = %v, %v; want [k]", keys, err)
	}

	// Direct @scope/ key access is refused.
	if _, err := store.setScoped("", "@scope/project:alpha/k", "hack"); err == nil {
		t.Fatal("allowed raw @scope/ key write")
	}
	if _, _, err := store.getScoped("", "@scope/project:alpha/k"); err == nil {
		t.Fatal("allowed raw @scope/ key read")
	}
	// Invalid scope rejected.
	if _, err := store.setScoped("project:../bad", "k", "v"); err == nil {
		t.Fatal("allowed invalid scope key")
	}

	// Delete in one scope leaves others untouched.
	if found, err := store.delScoped("project:alpha", "k"); err != nil || !found {
		t.Fatalf("delScoped alpha = (%v, %v)", found, err)
	}
	if _, ok, err := store.getScoped("project:alpha", "k"); err != nil || ok {
		t.Fatalf("alpha k still present after delete")
	}
	if v, ok, err := store.getScoped("project:beta", "k"); err != nil || !ok || v != "beta-val" {
		t.Fatalf("beta k disturbed: (%q, %v, %v)", v, ok, err)
	}

	// Shared quota applies across global + scoped keys.
	if _, err := store.setScoped("project:beta", "big", strings.Repeat("x", 1024)); err == nil {
		t.Fatal("exceeded shared quota")
	}
}

func TestScopedSettingsAndSecrets(t *testing.T) {
	kc := &keychain.Fake{}
	r := keychainRig(t, kc)
	r.write("scoped-set", map[string]string{
		ManifestName: `id = "scoped-set"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = "ui"
title = "Scoped Settings"
scope = "project"

[contributes.settings]
repo = { type = "string", label = "Repo", default = "default/repo", scope = "project" }
retries = { type = "number", label = "Retries", default = 2, scope = "project" }
enabled = { type = "boolean", label = "Enabled", default = true, scope = "project" }
token = { type = "secret", label = "Token", scope = "project" }
`,
		"main.js": `export default {
  activate(m) {
    m.blocks.register("ui", {
      state: { repo: "", retries: 0, enabled: false, tok: "" },
      async load(s, ctx) {
        s.repo = (await ctx.settings.getSetting("repo")).setting.text;
        s.retries = (await ctx.settings.getSetting("retries")).setting.number;
        s.enabled = (await ctx.settings.getSetting("enabled")).setting.flag;
        s.tok = (await ctx.settings.getSetting("token")).setting.text;
      },
      render(s, ui, ctx) {
        return ui.text(ctx.scopeKey + "|" + s.repo + "|" + s.retries + "|" + s.enabled + "|" + s.tok);
      },
    });
  },
};`,
	})
	r.state("scoped-set", protocol.PluginReady)

	// Set global values first.
	for k, v := range map[string]string{"repo": "global/repo", "retries": "5", "token": "global-tok"} {
		if _, err := r.act(&protocol.PluginAction{PluginId: "scoped-set", Op: protocol.PluginSet, Key: k, Value: v}); err != nil {
			t.Fatal(err)
		}
	}
	// Set scoped overrides for project:alpha only.
	for k, v := range map[string]string{"repo": "alpha/repo", "retries": "9", "enabled": "false", "token": "alpha-tok"} {
		if _, err := r.act(&protocol.PluginAction{PluginId: "scoped-set", Op: protocol.PluginSet, Key: k, Value: v, Scope: "project:alpha"}); err != nil {
			t.Fatal(err)
		}
	}

	// Verify keychain holds scoped secret under setting:token@project:alpha and settings.json never leaks it.
	service := keychainService(r.data, "scoped-set")
	if v, err := kc.Get(service, "setting:token@project:alpha"); err != nil || v != "alpha-tok" {
		t.Fatalf("scoped secret in keychain = %q, %v", v, err)
	}
	if raw := readFile(t, filepath.Join(r.data, "scoped-set", "settings.json")); strings.Contains(raw, "alpha-tok") || strings.Contains(raw, "global-tok") {
		t.Fatalf("settings.json leaked a secret: %s", raw)
	}

	// Watch project:alpha (sees overrides) and project:beta (inherits globals + default).
	wAlpha := r.watchScoped("plugin:scoped-set/ui", "project:alpha")
	wBeta := r.watchScoped("plugin:scoped-set/ui", "project:beta")

	wAlpha.until("alpha overrides", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetScope() == "project:alpha" && hasText(n, "project:alpha|alpha/repo|9|false|alpha-tok")
	})
	wBeta.until("beta inherits global", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetScope() == "project:beta" && hasText(n, "project:beta|global/repo|5|true|global-tok")
	})

	// Inspect PluginInfo: ScopedValues populated for non-secrets, ScopedSet for secrets (never values!).
	info := r.state("scoped-set", protocol.PluginRunning)
	if len(info.GetBlocks()) != 1 || info.GetBlocks()[0].GetScope() != protocol.ScopeProject {
		t.Fatalf("block scope = %+v", info.GetBlocks())
	}
	byKey := map[string]*protocol.PluginSetting{}
	for _, s := range info.GetSettings() {
		byKey[s.GetKey()] = s
	}
	if byKey["repo"].GetScope() != protocol.ScopeProject || byKey["repo"].GetScopedValues()["project:alpha"] != "alpha/repo" {
		t.Fatalf("repo setting = %+v", byKey["repo"])
	}
	if byKey["token"].GetValue() != "" || len(byKey["token"].GetScopedValues()) != 0 || !slices.Equal(byKey["token"].GetScopedSet(), []string{"project:alpha"}) {
		t.Fatalf("token setting = %+v", byKey["token"])
	}
	if strings.Contains(r.logs.String(), "alpha-tok") {
		t.Fatal("scoped secret was logged")
	}

	// Clearing a scoped override (Value == "") reverts project:alpha to global value.
	for _, k := range []string{"repo", "enabled", "token"} {
		if _, err := r.act(&protocol.PluginAction{PluginId: "scoped-set", Op: protocol.PluginSet, Key: k, Value: "", Scope: "project:alpha"}); err != nil {
			t.Fatal(err)
		}
	}
	wAlpha.until("alpha reverted to global", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "project:alpha|global/repo|9|true|global-tok")
	})
	if _, err := kc.Get(service, "setting:token@project:alpha"); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("cleared scoped secret still in keychain: %v", err)
	}
}

func TestScopedBlocksRuntimeIsolationToolsAndSleepRestore(t *testing.T) {
	lim := testLimits
	lim.Idle = 250 * time.Millisecond
	r := startRig(t, Config{
		Limits: lim,
		ResolveScope: func(scopeKey string) ScopeContext {
			switch scopeKey {
			case "project:alpha":
				return ScopeContext{
					SpaceID:     "sp-1",
					SpaceName:   "Work",
					ProjectID:   "alpha",
					ProjectName: "Alpha App",
					Roots:       []string{"/tmp/alpha"},
				}
			case "project:beta":
				return ScopeContext{
					SpaceID:     "sp-1",
					SpaceName:   "Work",
					ProjectID:   "beta",
					ProjectName: "Beta API",
					Roots:       []string{"/tmp/beta"},
				}
			}
			return ScopeContext{}
		},
	})

	r.write("board", map[string]string{
		ManifestName: `id = "board"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["storage"]

[[contributes.block]]
id = "view"
title = "Project Board"
scope = "project"

[[contributes.block]]
id = "summary"
title = "Global Summary"
scope = "global"

[[contributes.tool]]
id = "add_item"
description = "Add an item in the active project"
scope = "project"

[contributes.tool.params.title]
type = "string"
required = true

[[contributes.tool]]
id = "global_ping"
description = "Global tool ignores caller scope"
scope = "global"

[[contributes.tool]]
id = "space_info"
description = "Space-scoped tool"
scope = "space"

[[contributes.tool]]
id = "session_info"
description = "Session-scoped tool"
scope = "session"
`,
		"main.js": `let mako;
export default {
  activate(m) {
    mako = m;
    mako.blocks.register("view", {
      state: { items: [], clicks: 0 },
      async load(s, ctx) {
        const res = await ctx.storage.getItem("items");
        s.items = res.found ? JSON.parse(res.value) : [];
      },
      render(s, ui, ctx) {
        return ui.column([
          ui.text("proj=" + ctx.projectName + " root=" + (ctx.roots[0] || "") + " clicks=" + s.clicks + " items=" + s.items.join(","), { key: "info" }),
          ui.button("Click", { key: "click", onTap: ["tap"] }),
        ]);
      },
      on: {
        async tap(s) {
          const ctx = mako.blocks.context("view");
          s.clicks += 1;
          s.items.push("tap-" + ctx.projectId + "-" + s.clicks);
          await ctx.storage.setItem("items", JSON.stringify(s.items));
        },
      },
    });
    mako.blocks.register("summary", {
      state: { lastAdded: "" },
      render(s, ui, ctx) {
        return ui.text("summary:" + ctx.scope + ":" + s.lastAdded);
      },
    });
    mako.tools.register("add_item", async (args, ctx) => {
      const s = mako.blocks.state("view", ctx.scopeKey);
      s.items.push(args.title);
      await ctx.storage.setItem("items", JSON.stringify(s.items));
      mako.blocks.state("summary", "").lastAdded = ctx.projectId + ":" + args.title;
      return { scope: ctx.scopeKey, projectName: ctx.projectName, count: s.items.length };
    });
    mako.tools.register("global_ping", async (args, ctx) => {
      return { scope: ctx.scope, scopeKey: ctx.scopeKey };
    });
    mako.tools.register("space_info", async (args, ctx) => ({ scopeKey: ctx.scopeKey, spaceId: ctx.spaceId }));
    mako.tools.register("session_info", async (args, ctx) => ({ scopeKey: ctx.scopeKey, sessionId: ctx.sessionId }));
  },
};`,
	})

	r.state("board", protocol.PluginWaiting)
	r.grant("board")

	wAlpha := r.watchScoped("plugin:board/view", "project:alpha")
	wBeta := r.watchScoped("plugin:board/view", "project:beta")
	wSummary := r.watch("plugin:board/summary")

	wAlpha.until("alpha initial", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetScope() == "project:alpha" && hasText(n, "proj=Alpha App root=/tmp/alpha clicks=0 items=")
	})
	wBeta.until("beta initial", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetScope() == "project:beta" && hasText(n, "proj=Beta API root=/tmp/beta clicks=0 items=")
	})

	// Tap inside project:alpha only affects alpha.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:board/view",
		Node:  "/click",
		Kind:  protocol.UiTap,
		Scope: "project:alpha",
	}); err != nil {
		t.Fatal(err)
	}
	wAlpha.until("alpha clicked", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "clicks=1 items=tap-alpha-1")
	})

	// Tool scope validation: project tool without ProjectID is refused.
	if _, err := r.m.CallToolScoped(t.Context(), "board", "add_item", []byte(`{"title":"oops"}`), ScopeContext{}); err == nil {
		t.Fatal("project tool succeeded without ProjectID")
	}
	// Space/session tool validation.
	if _, err := r.m.CallToolScoped(t.Context(), "board", "space_info", nil, ScopeContext{ProjectID: "alpha"}); err == nil {
		t.Fatal("space tool succeeded without SpaceID")
	}
	if _, err := r.m.CallToolScoped(t.Context(), "board", "session_info", nil, ScopeContext{SpaceID: "sp-1"}); err == nil {
		t.Fatal("session tool succeeded without SessionID")
	}
	if out, err := r.m.CallToolScoped(t.Context(), "board", "space_info", nil, ScopeContext{SpaceID: "sp-1"}); err != nil || !strings.Contains(out, `"space:sp-1"`) {
		t.Fatalf("space_info = %q, %v", out, err)
	}
	if out, err := r.m.CallToolScoped(t.Context(), "board", "session_info", nil, ScopeContext{SessionID: "sess-9"}); err != nil || !strings.Contains(out, `"session:sess-9"`) {
		t.Fatalf("session_info = %q, %v", out, err)
	}
	// Global tool clears scopeKey even if caller passes a ProjectID.
	if out, err := r.m.CallToolScoped(t.Context(), "board", "global_ping", nil, ScopeContext{ProjectID: "alpha"}); err != nil || !strings.Contains(out, `"scopeKey":""`) {
		t.Fatalf("global_ping = %q, %v", out, err)
	}

	// Project tool in project:beta updates beta AND re-renders both beta and global summary!
	out, err := r.m.CallToolScoped(t.Context(), "board", "add_item", []byte(`{"title":"from-agent"}`), ScopeContext{ProjectID: "beta"})
	if err != nil || !strings.Contains(out, `"Beta API"`) {
		t.Fatalf("add_item beta = %q, %v", out, err)
	}
	wBeta.until("beta updated by tool", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "clicks=0 items=from-agent")
	})
	wSummary.until("global summary re-rendered", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "summary:global:beta:from-agent")
	})

	// Sleep and wake: per-scope in-memory state (clicks=1 on alpha, clicks=0 on beta) is preserved!
	wAlpha.cancel()
	wBeta.cancel()
	wSummary.cancel()
	r.state("board", protocol.PluginReady)

	wAlpha2 := r.watchScoped("plugin:board/view", "project:alpha")
	wAlpha2.until("alpha restored after sleep", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "clicks=1 items=tap-alpha-1")
	})
}

func TestScopedBlockLRUEvictionCap(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("lru", map[string]string{
		ManifestName: `id = "lru"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = "ui"
title = "LRU"
scope = "project"
`,
		"main.js": `export default {
  activate(m) {
    m.blocks.register("ui", {
      state: { count: 0 },
      load(s, ctx) { s.count += 1; },
      render(s, ui, ctx) { return ui.text(ctx.scopeKey + ":" + s.count); },
    });
  },
};`,
	})
	r.state("lru", protocol.PluginReady)

	// Open and close 20 distinct scopes sequentially: cache should stay capped at 16.
	for i := 1; i <= 20; i++ {
		sk := fmt.Sprintf("project:p%02d", i)
		w := r.watchScoped("plugin:lru/ui", sk)
		w.until(sk, func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
			return hasText(n, sk+":1")
		})
		w.cancel()
		time.Sleep(5 * time.Millisecond)
	}

	p, ok := r.m.plugin("lru")
	if !ok {
		t.Fatal("missing lru plugin")
	}
	p.mu.Lock()
	count := len(p.inst)
	_, hasOldest := p.inst[instanceKey("ui", "project:p01")]
	_, hasNewest := p.inst[instanceKey("ui", "project:p20")]
	p.mu.Unlock()
	if count != maxScopeInstancesPerBlock {
		t.Fatalf("cached instances = %d; want %d", count, maxScopeInstancesPerBlock)
	}
	if hasOldest {
		t.Fatal("oldest unwatched instance project:p01 was not evicted")
	}
	if !hasNewest {
		t.Fatal("newest instance project:p20 was unexpectedly evicted")
	}

	// Never evict actively watched instances even when >16 are watched simultaneously.
	var watchers []*watcher
	var wg sync.WaitGroup
	for i := 1; i <= 18; i++ {
		sk := fmt.Sprintf("project:active%02d", i)
		w := r.watchScoped("plugin:lru/ui", sk)
		watchers = append(watchers, w)
		wg.Add(1)
		go func(w *watcher, sk string) {
			defer wg.Done()
			w.until(sk, func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
				return hasText(n, sk+":1")
			})
		}(w, sk)
	}
	wg.Wait()

	p.mu.Lock()
	activeCount := len(p.inst)
	p.mu.Unlock()
	if activeCount < 18 {
		t.Fatalf("actively watched instances were evicted: count=%d", activeCount)
	}

	// Once all active watchers unsubscribe, trim brings cached instances back to 16.
	for _, w := range watchers {
		w.cancel()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		n := len(p.inst)
		p.mu.Unlock()
		if n <= maxScopeInstancesPerBlock {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.mu.Lock()
	finalCount := len(p.inst)
	p.mu.Unlock()
	t.Fatalf("instances after unsubscribing = %d; want <= %d", finalCount, maxScopeInstancesPerBlock)
}

func TestScopedBlockPropsSchema(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("props", map[string]string{
		ManifestName: `id = "props"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = "global-ui"
title = "Global UI"

[[contributes.block]]
id = "project-ui"
title = "Project UI"
scope = "project"
`,
		"main.js": `export default {
  activate(m) {
    m.blocks.register("global-ui", { state: {}, render: (s, ui) => ui.text("g") });
    m.blocks.register("project-ui", { state: {}, render: (s, ui) => ui.text("p") });
  },
};`,
	})
	r.state("props", protocol.PluginReady)

	globalProps, ok := r.m.BlockProps("plugin:props/global-ui")
	if !ok {
		t.Fatal("missing global-ui in catalog")
	}
	if gp, _ := globalProps["properties"].(map[string]any); len(gp) != 0 {
		t.Fatalf("global block should have empty properties, got %#v", globalProps)
	}

	projProps, ok := r.m.BlockProps("plugin:props/project-ui")
	if !ok {
		t.Fatal("missing project-ui in catalog")
	}
	propsMap, _ := projProps["properties"].(map[string]any)
	scopeProp, _ := propsMap["scope"].(map[string]any)
	if scopeProp["type"] != "string" || projProps["additionalProperties"] != false {
		t.Fatalf("unexpected scoped block props schema: %#v", projProps)
	}

	// Verify Manager.Blocks() carries the same props schema.
	var foundProj bool
	for _, b := range r.m.Blocks() {
		if b.Name == "plugin:props/project-ui" {
			foundProj = true
			if p, _ := b.Props["properties"].(map[string]any); p["scope"] == nil {
				t.Fatalf("Blocks() project-ui missing scope prop: %#v", b.Props)
			}
		}
	}
	if !foundProj {
		t.Fatal("Blocks() missing plugin:props/project-ui")
	}

	// Verify layoutdoc.CheckProps accepts pinned props.scope (and empty props)
	// on scoped blocks, rejects non-string/unknown props, and rejects
	// props.scope on global blocks.
	if err := layoutdoc.CheckProps(projProps, map[string]any{"scope": "project:alpha"}); err != nil {
		t.Fatalf("CheckProps rejected pinned scoped block: %v", err)
	}
	if err := layoutdoc.CheckProps(projProps, nil); err != nil {
		t.Fatalf("CheckProps rejected unpinned scoped block: %v", err)
	}
	if err := layoutdoc.CheckProps(projProps, map[string]any{"scope": 123}); err == nil {
		t.Fatal("CheckProps accepted non-string scope prop")
	}
	if err := layoutdoc.CheckProps(projProps, map[string]any{"scope": "project:alpha", "extra": "nope"}); err == nil {
		t.Fatal("CheckProps accepted unknown prop on scoped block")
	}
	if err := layoutdoc.CheckProps(globalProps, map[string]any{"scope": "project:alpha"}); err == nil {
		t.Fatal("CheckProps accepted props.scope on global block")
	}
}

func TestKVStoreDelRollbackAndCorruptJSON(t *testing.T) {
	t.Run("delScoped rolls back on save failure", func(t *testing.T) {
		dir := t.TempDir()
		storePath := filepath.Join(dir, "storage.json")
		kv := newKVStore(storePath, 1<<20)
		if _, err := kv.setScoped("", "k1", "v1"); err != nil {
			t.Fatal(err)
		}
		// Replace storage.json with a directory so fsutil.Write/Rename fails.
		if err := os.Remove(storePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(storePath, 0o700); err != nil {
			t.Fatal(err)
		}
		deleted, err := kv.delScoped("", "k1")
		if err == nil || deleted {
			t.Fatalf("expected delScoped error with deleted=false, got deleted=%v err=%v", deleted, err)
		}
		v, ok, err := kv.getScoped("", "k1")
		if err != nil || !ok || v != "v1" {
			t.Fatalf("expected rolled back value v1, got ok=%v v=%q err=%v", ok, v, err)
		}
	})

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "corrupt json", raw: "{bad-json"},
		{name: "null json", raw: "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "storage.json")
			if err := os.WriteFile(storePath, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			kv := newKVStore(storePath, 1<<20)
			if _, _, err := kv.getScoped("", "k1"); err == nil {
				t.Fatal("expected getScoped error on corrupt storage.json")
			}
			if _, err := kv.setScoped("", "k1", "v1"); err == nil {
				t.Fatal("expected setScoped error on corrupt storage.json")
			}
			if _, err := kv.delScoped("", "k1"); err == nil {
				t.Fatal("expected delScoped error on corrupt storage.json")
			}
			if _, err := kv.keysScoped("", ""); err == nil {
				t.Fatal("expected keysScoped error on corrupt storage.json")
			}
			if got := kv.used(); got != 0 {
				t.Fatalf("used() = %d, want 0", got)
			}
			after, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != tc.raw {
				t.Fatalf("corrupt storage.json was overwritten: got %q, want %q", string(after), tc.raw)
			}
		})
	}
}
