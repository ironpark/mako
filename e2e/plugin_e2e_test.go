package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/plugin"
	pluginregistry "github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

func TestPluginOfflineCLI(t *testing.T) {
	env := newTestEnv(t)
	workDir := t.TempDir()
	keyPath := filepath.Join(workDir, "publisher.key")

	t.Run("keygen_and_existing_key_error", func(t *testing.T) {
		out := env.mustCLI("plugins", "keygen", keyPath)
		if !strings.Contains(out, "ed25519:") || !strings.Contains(out, "fingerprint") {
			t.Fatalf("unexpected keygen output:\n%s", out)
		}
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("stat generated key: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("key permissions = %#o, want 0600", mode)
		}
		_, errOut, code := env.runCLI("plugins", "keygen", keyPath)
		if code != 1 {
			t.Fatalf("re-running keygen on existing file exited %d, want 1 (stderr=%q)", code, errOut)
		}
	})

	// Generate a second key for negative signature verification.
	wrongKeyPath := filepath.Join(workDir, "other.key")
	wrongOut := env.mustCLI("plugins", "keygen", wrongKeyPath)
	wrongPub := extractPubKey(t, wrongOut)

	// Extract primary public key from keygen output (or from verify output).
	regDir := filepath.Join(workDir, "registry")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var packedFiles []string

	for _, name := range []string{"ci-status", "kanban", "okf"} {
		t.Run("pack_verify_index_"+name, func(t *testing.T) {
			subEnv := &testEnv{t: t, makoHome: env.makoHome, home: env.home, tmpDir: env.tmpDir, sockPath: env.sockPath}
			srcDir := filepath.Join(repoRootDir, "examples", "plugins", name)
			pkgPath := filepath.Join(workDir, name+".makoplugin")
			packOut := subEnv.mustCLI("plugins", "pack", srcDir, "-key", keyPath, "-o", pkgPath)
			if !strings.Contains(packOut, name) {
				t.Fatalf("pack output missing %q:\n%s", name, packOut)
			}
			packedFiles = append(packedFiles, pkgPath)

			// Verify without explicit -key passes.
			verOut := subEnv.mustCLI("plugins", "verify", pkgPath)
			pubKey := extractPubKey(t, verOut)
			if pubKey == "" {
				t.Fatalf("verify output missing public key:\n%s", verOut)
			}

			// Verify with matching -key passes.
			subEnv.mustCLI("plugins", "verify", pkgPath, "-key", pubKey)

			// Verify with mismatched -key fails.
			_, errOut, code := subEnv.runCLI("plugins", "verify", pkgPath, "-key", wrongPub)
			if code != 1 {
				t.Fatalf("verify with mismatched key exited %d, want 1 (stderr=%q)", code, errOut)
			}

			// Index into local registry folder using loopback URL.
			dlURL := fmt.Sprintf("http://127.0.0.1:18080/packages/%s.makoplugin", name)
			subEnv.mustCLI("plugins", "index", regDir, pkgPath, "-url", dlURL, "-registry-name", "E2E Local")
		})
	}

	t.Run("registry_index_valid", func(t *testing.T) {
		if len(packedFiles) != 3 {
			t.Fatalf("expected 3 packed plugins, got %d", len(packedFiles))
		}
		indexBytes, err := os.ReadFile(filepath.Join(regDir, pluginregistry.IndexName))
		if err != nil {
			t.Fatalf("read mako-registry.json: %v", err)
		}
		idx, err := pluginregistry.Parse(indexBytes, true)
		if err != nil {
			t.Fatalf("registry.Parse: %v", err)
		}
		if idx.Name != "E2E Local" {
			t.Fatalf("registry name = %q, want %q", idx.Name, "E2E Local")
		}
		for _, id := range []string{"ci-status", "kanban", "okf"} {
			if idx.Find(id) == nil {
				t.Fatalf("registry index missing plugin %q", id)
			}
		}
	})
}

func extractPubKey(t *testing.T, out string) string {
	t.Helper()
	for _, field := range strings.Fields(out) {
		if strings.HasPrefix(field, "ed25519:") {
			return field
		}
	}
	t.Fatalf("no ed25519 public key found in:\n%s", out)
	return ""
}

type pluginHarness struct {
	t       *testing.T
	dir     string
	dataDir string
	m       *plugin.Manager
}

func newPluginHarness(t *testing.T, lim plugin.Limits) *pluginHarness {
	t.Helper()
	dir := t.TempDir()
	dataDir := t.TempDir()
	m := plugin.New(plugin.Config{
		Dir:     dir,
		DataDir: dataDir,
		Poll:    25 * time.Millisecond,
		Limits:  lim,
		ResolveScope: func(scopeKey string) plugin.ScopeContext {
			if scopeKey == "project:p1" {
				return plugin.ScopeContext{
					Scope:       protocol.ScopeProject,
					ScopeKey:    scopeKey,
					ProjectID:   "p1",
					ProjectName: "Alpha",
					Roots:       []string{"/tmp/alpha"},
				}
			}
			return plugin.ScopeContext{ScopeKey: scopeKey}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return &pluginHarness{t: t, dir: dir, dataDir: dataDir, m: m}
}

func (h *pluginHarness) writeFiles(id string, files map[string]string) {
	h.t.Helper()
	pdir := filepath.Join(h.dir, id)
	for rel, body := range files {
		dst := filepath.Join(pdir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			h.t.Fatalf("mkdir %s: %v", dst, err)
		}
		if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
			h.t.Fatalf("write %s: %v", dst, err)
		}
	}
}

func (h *pluginHarness) waitState(id, want string) *protocol.PluginInfo {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range h.m.List() {
			if p.GetId() == id && p.GetState() == want {
				return p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for plugin %q state %q (got %+v)", id, want, h.m.List())
	return nil
}

func (h *pluginHarness) grant(id string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.m.Act(ctx, &protocol.PluginAction{PluginId: id, Op: protocol.PluginGrant}); err != nil {
		h.t.Fatalf("PluginGrant(%s): %v", id, err)
	}
	h.waitState(id, protocol.PluginReady)
}

type blockWatcher struct {
	t      *testing.T
	cancel context.CancelFunc
	ch     chan *protocol.UiNode
	errs   chan error
	tree   *protocol.UiNode
}

func (h *pluginHarness) watchScoped(block, scope string) *blockWatcher {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := &blockWatcher{
		t:      h.t,
		cancel: cancel,
		ch:     make(chan *protocol.UiNode, 32),
		errs:   make(chan error, 1),
	}
	h.t.Cleanup(cancel)
	go func() {
		err := h.m.WatchScoped(ctx, block, scope, func(u *protocol.BlockUpdate) error {
			if u.GetTree() != nil {
				w.tree = u.GetTree()
			} else if len(u.GetOps()) > 0 && w.tree != nil {
				next, applyErr := protocol.ApplyUi(w.tree, u.GetOps())
				if applyErr != nil {
					return applyErr
				}
				w.tree = next
			}
			if w.tree != nil {
				select {
				case w.ch <- w.tree:
				default:
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case w.errs <- err:
			default:
			}
		}
	}()
	return w
}

func (w *blockWatcher) waitText(want string) *protocol.UiNode {
	w.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case tr := <-w.ch:
			if uiTreeContainsText(tr, want) {
				return tr
			}
		case err := <-w.errs:
			w.t.Fatalf("watch stream error while waiting for %q: %v", want, err)
		case <-deadline:
			w.t.Fatalf("timed out waiting for %q in UI tree", want)
		}
	}
}

func uiTreeContainsText(n *protocol.UiNode, sub string) bool {
	if n == nil {
		return false
	}
	if strings.Contains(n.GetText(), sub) || strings.Contains(n.GetValue(), sub) {
		return true
	}
	for _, c := range n.GetChildren() {
		if uiTreeContainsText(c, sub) {
			return true
		}
	}
	return false
}

func TestPluginRuntimeExamplesOffline(t *testing.T) {
	t.Run("kanban_offline_kv_sqlite_watch_event_tool", func(t *testing.T) {
		h := newPluginHarness(t, plugin.Limits{})
		copyDir(t, filepath.Join(repoRootDir, "examples", "plugins", "kanban"), filepath.Join(h.dir, "kanban"))
		h.waitState("kanban", protocol.PluginWaiting)
		h.grant("kanban")

		w := h.watchScoped("plugin:kanban/board", "project:p1")
		w.waitText("Kanban · Alpha · 1/5 done")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		res, err := h.m.CallToolScoped(ctx, "kanban", "add_card",
			[]byte(`{"title":"E2E Kanban Task","column":"review","priority":"high"}`),
			plugin.ScopeContext{ProjectID: "p1"})
		if err != nil || !strings.Contains(res, `"E2E Kanban Task"`) {
			t.Fatalf("add_card failed: res=%s err=%v", res, err)
		}
		w.waitText("E2E Kanban Task")

		if _, err := h.m.Event(ctx, &protocol.BlockEvent{
			Block: "plugin:kanban/board",
			Scope: "project:p1",
			Node:  "/columns/col-done",
			Kind:  protocol.UiDrop,
			Value: "c1",
		}); err != nil {
			t.Fatalf("BlockEvent UiDrop: %v", err)
		}
		w.waitText("Kanban · Alpha · 2/6 done")
	})

	t.Run("okf_multifile_lib_and_datafs", func(t *testing.T) {
		h := newPluginHarness(t, plugin.Limits{})
		copyDir(t, filepath.Join(repoRootDir, "examples", "plugins", "okf"), filepath.Join(h.dir, "okf"))
		h.waitState("okf", protocol.PluginWaiting)
		h.grant("okf")

		w := h.watchScoped("plugin:okf/vault", "project:p1")
		w.waitText("Alpha Overview")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		out, err := h.m.CallToolScoped(ctx, "okf", "upsert_doc", []byte(`{
			"path":"concepts/e2e.md",
			"title":"E2E Knowledge Note",
			"type":"Concept",
			"body":"# E2E\n\nLinks to [[concepts/overview]]."
		}`), plugin.ScopeContext{ProjectID: "p1", ProjectName: "Alpha"})
		if err != nil || !strings.Contains(out, `"saved":true`) {
			t.Fatalf("okf upsert_doc failed: out=%s err=%v", out, err)
		}
		w.waitText("E2E Knowledge Note")

		readOut, err := h.m.CallToolScoped(ctx, "okf", "read_doc", []byte(`{"path":"concepts/e2e.md"}`), plugin.ScopeContext{ProjectID: "p1"})
		if err != nil || !strings.Contains(readOut, "E2E Knowledge Note") {
			t.Fatalf("okf read_doc failed: out=%s err=%v", readOut, err)
		}
	})
}

func TestPluginTier2Boundaries(t *testing.T) {
	// B10: PluginDeny stops runtime and rejects subsequent calls, even when p.jobs is saturated.
	t.Run("B10_plugin_deny_normal_and_saturated_jobs", func(t *testing.T) {
		for _, saturate := range []bool{false, true} {
			name := "normal"
			if saturate {
				name = "saturated_jobs"
			}
			t.Run(name, func(t *testing.T) {
				h := newPluginHarness(t, plugin.Limits{Events: 1000})
				// In-memory counter resets to 1 only when a fresh QuickJS runtime activates.
				h.writeFiles("denyprobe", map[string]string{
					plugin.ManifestName: `id = "denyprobe"
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = ["storage", "fs:data"]

[[contributes.block]]
id = "ui"
title = "Deny Probe"

[[contributes.command]]
id = "ping"
title = "Ping"

[[contributes.tool]]
id = "bump"
description = "Increment in-memory counter"

[[contributes.tool]]
id = "hold"
description = "Signal start via datafs and hold the plugin actor busy"
`,
					"main.js": `let memCount = 0;
export default {
  activate(mako) {
    memCount = 1;
    mako.blocks.register("ui", {
      state: { ticks: 0 },
      on: {
        tap(s) {
          s.ticks += 1;
        },
      },
      render: (s, ui) => ui.button("ticks:" + s.ticks, { onTap: ["tap"] }),
    });
    mako.commands.register("ping", () => ({ toast: "pong:" + memCount }));
    mako.tools.register("bump", () => {
      memCount += 1;
      return { memCount };
    });
    mako.tools.register("hold", async (_args, ctx) => {
      await ctx.datafs.write("started", { text: "1" });
      const until = Date.now() + 250;
      while (Date.now() < until) {}
      return { held: true };
    });
  },
};`,
				})

				h.waitState("denyprobe", protocol.PluginWaiting)
				h.grant("denyprobe")

				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()

				// Watch the UI block and exercise a real tap event (using on: { tap(s) }).
				w := h.watchScoped("plugin:denyprobe/ui", "")
				w.waitText("ticks:0")
				if _, err := h.m.Event(ctx, &protocol.BlockEvent{
					Block: "plugin:denyprobe/ui",
					Node:  "",
					Kind:  protocol.UiTap,
				}); err != nil {
					t.Fatalf("BlockEvent UiTap: %v", err)
				}
				w.waitText("ticks:1")

				// Bump in-memory counter from 1 -> 2 so we can detect if runtime ever stays alive across Deny->Grant.
				res, err := h.m.CallTool(ctx, "denyprobe", "bump", nil)
				if err != nil || !strings.Contains(res, `"memCount":2`) {
					t.Fatalf("initial CallTool(bump): res=%s err=%v", res, err)
				}

				if saturate {
					// Hold the plugin actor inside JS via CallTool("hold"), wait only for its
					// datafs "started" marker (never sleep after queueing), then fill all 32 slots
					// of p.jobs with non-blocking PluginReload calls (whose unchanged-fingerprint
					// fast path returns without calling ensure/closeRuntime). Act(PluginDeny)
					// executes while hold is still running and p.jobs is saturated 32/32.
					var holdWG sync.WaitGroup
					var holdDone atomic.Bool
					holdWG.Add(1)
					go func() {
						defer holdWG.Done()
						defer holdDone.Store(true)
						_, _ = h.m.CallTool(ctx, "denyprobe", "hold", nil)
					}()

					startedMarker := filepath.Join(h.dataDir, "denyprobe", "files", "global", "started")
					markerDeadline := time.Now().Add(5 * time.Second)
					for {
						if _, statErr := os.Stat(startedMarker); statErr == nil {
							break
						}
						if holdDone.Load() {
							t.Fatal("hold tool finished before writing started marker")
						}
						if time.Now().After(markerDeadline) {
							t.Fatalf("timed out waiting for hold started marker at %s", startedMarker)
						}
						time.Sleep(2 * time.Millisecond)
					}

					for range 48 {
						_, _ = h.m.Act(ctx, &protocol.PluginAction{PluginId: "denyprobe", Op: protocol.PluginReload})
					}
					if holdDone.Load() {
						t.Fatal("hold tool exited before Act(PluginDeny); p.jobs was not guaranteed saturated")
					}
					if _, err := h.m.Act(ctx, &protocol.PluginAction{PluginId: "denyprobe", Op: protocol.PluginDeny}); err != nil {
						t.Fatalf("Act(PluginDeny): %v", err)
					}
					if holdDone.Load() {
						t.Fatal("hold tool exited during Act(PluginDeny); p.jobs may not have been saturated")
					}
					holdWG.Wait()
					// Give the actor loop one scheduling turn after hold finishes to process p.recheck
					// (without calling CallTool/PluginRun first, which would trigger ensure() teardown).
					time.Sleep(20 * time.Millisecond)

					info := h.waitState("denyprobe", protocol.PluginDenied)
					if info.GetState() != protocol.PluginDenied {
						t.Fatalf("state after deny = %q, want %q", info.GetState(), protocol.PluginDenied)
					}

					// Re-grant directly before any CallTool/PluginRun so we prove PluginDeny itself
					// (via recheck/restand) tore down the runtime even when p.jobs was full 32/32.
					if _, err := h.m.Act(ctx, &protocol.PluginAction{PluginId: "denyprobe", Op: protocol.PluginGrant}); err != nil {
						t.Fatalf("PluginGrant after saturated deny: %v", err)
					}
					deadline := time.Now().Add(5 * time.Second)
					for time.Now().Before(deadline) {
						var st string
						for _, p := range h.m.List() {
							if p.GetId() == "denyprobe" {
								st = p.GetState()
							}
						}
						if st == protocol.PluginReady || st == protocol.PluginRunning {
							break
						}
						time.Sleep(10 * time.Millisecond)
					}
					resAfterRegrant, err := h.m.CallTool(ctx, "denyprobe", "bump", nil)
					if err != nil {
						t.Fatalf("CallTool after re-grant: %v", err)
					}
					if !strings.Contains(resAfterRegrant, `"memCount":2`) {
						t.Fatalf("runtime survived saturated-jobs PluginDeny without teardown (expected fresh memCount:2, got %s)", resAfterRegrant)
					}
				}

				if _, err := h.m.Act(ctx, &protocol.PluginAction{PluginId: "denyprobe", Op: protocol.PluginDeny}); err != nil {
					t.Fatalf("Act(PluginDeny): %v", err)
				}

				info := h.waitState("denyprobe", protocol.PluginDenied)
				if info.GetState() != protocol.PluginDenied {
					t.Fatalf("state after deny = %q, want %q", info.GetState(), protocol.PluginDenied)
				}

				if _, err := h.m.CallTool(ctx, "denyprobe", "bump", nil); err == nil {
					t.Fatal("expected CallTool after PluginDeny to fail")
				}
				if _, err := h.m.Act(ctx, &protocol.PluginAction{PluginId: "denyprobe", Op: protocol.PluginRun, Command: "ping"}); err == nil {
					t.Fatal("expected PluginRun after PluginDeny to fail")
				}

				// Re-granting must spin up a fresh runtime (memCount resets to 1 -> 2),
				// proving PluginDeny actually closed the previous runtime even under job load.
				h.grant("denyprobe")
				resAfter, err := h.m.CallTool(ctx, "denyprobe", "bump", nil)
				if err != nil {
					t.Fatalf("CallTool after re-grant: %v", err)
				}
				if !strings.Contains(resAfter, `"memCount":2`) {
					t.Fatalf("runtime survived PluginDeny without teardown (expected fresh memCount:2, got %s)", resAfter)
				}
			})
		}
	})

	// B13: datafs binary Uint8Array write/read exact byte-for-byte roundtrip.
	t.Run("B13_datafs_binary_uint8array_roundtrip", func(t *testing.T) {
		h := newPluginHarness(t, plugin.Limits{})
		h.writeFiles("binfs", map[string]string{
			plugin.ManifestName: `id = "binfs"
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = ["fs:data"]

[[contributes.tool]]
id = "roundtrip"
description = "Write and read binary Uint8Array via mako.datafs"
`,
			"main.js": `const B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
function decodeBase64Bytes(str) {
  const clean = String(str || "").replace(/=+$/, "");
  const out = [];
  for (let i = 0; i < clean.length; i += 4) {
    const a = B64.indexOf(clean[i]);
    const b = B64.indexOf(clean[i + 1]);
    const c = i + 2 < clean.length ? B64.indexOf(clean[i + 2]) : -1;
    const d = i + 3 < clean.length ? B64.indexOf(clean[i + 3]) : -1;
    if (a < 0 || b < 0) throw new Error("invalid base64");
    out.push((a << 2) | (b >> 4));
    if (c >= 0) out.push(((b & 15) << 4) | (c >> 2));
    if (d >= 0) out.push(((c & 3) << 6) | d);
  }
  return out;
}

export default {
  activate(mako) {
    mako.tools.register("roundtrip", async (_args, ctx) => {
      const want = [0x00, 0xff, 0x41, 0x80, 0x01];
      await ctx.datafs.write("blob.bin", { data: new Uint8Array(want) });
      const res = await ctx.datafs.read("blob.bin");
      const got = res.binary ? decodeBase64Bytes(res.data) : [];
      return { found: res.found, binary: res.binary, size: res.size, rawData: res.data, got };
    });
  },
};`,
		})

		h.waitState("binfs", protocol.PluginWaiting)
		h.grant("binfs")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		raw, err := h.m.CallTool(ctx, "binfs", "roundtrip", nil)
		if err != nil {
			t.Fatalf("CallTool(roundtrip) failed (B13): %v", err)
		}
		var out struct {
			Found  bool   `json:"found"`
			Binary bool   `json:"binary"`
			Size   int    `json:"size"`
			Got    []int  `json:"got"`
			Raw    string `json:"rawData"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("unmarshal tool result %q: %v", raw, err)
		}
		want := []int{0x00, 0xff, 0x41, 0x80, 0x01}
		if !out.Found || !out.Binary || out.Size != len(want) || fmt.Sprint(out.Got) != fmt.Sprint(want) {
			t.Fatalf("binary Uint8Array roundtrip mismatch (B13): got %+v, want bytes %v", out, want)
		}
	})

	// B9: Limits.Idle sleep & wake state restoration via Watch/Event.
	// Note: In an opaque-box test without unexported p.saved injection, R.save produces valid JSON
	// via JSON.stringify and R.restore runs JSON.parse; this verifies normal Idle sleep/wake preservation.
	t.Run("B9_idle_sleep_and_wake_restores_state", func(t *testing.T) {
		h := newPluginHarness(t, plugin.Limits{Idle: 1 * time.Second})
		h.writeFiles("sleeper", map[string]string{
			plugin.ManifestName: `id = "sleeper"
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"

[[contributes.block]]
id = "counter"
title = "Counter"
`,
			"main.js": `export default {
  activate(mako) {
    mako.blocks.register("counter", {
      state: { count: 0 },
      on: {
        inc(s) {
          s.count += 1;
        },
      },
      render: (s, ui) => ui.button("Count:" + s.count, { onTap: ["inc"] }),
    });
  },
};`,
		})

		h.waitState("sleeper", protocol.PluginReady)
		w1 := h.watchScoped("plugin:sleeper/counter", "")
		w1.waitText("Count:0")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := h.m.Event(ctx, &protocol.BlockEvent{
			Block: "plugin:sleeper/counter",
			Node:  "",
			Kind:  protocol.UiTap,
		}); err != nil {
			t.Fatalf("Event tap: %v", err)
		}
		w1.waitText("Count:1")
		h.waitState("sleeper", protocol.PluginRunning)

		// Stop watching so tick() sees !p.watched() and puts runtime to sleep after Idle=1s.
		w1.cancel()
		h.waitState("sleeper", protocol.PluginReady)

		// Wake via new Watch stream and verify Count:1 was saved and restored.
		w2 := h.watchScoped("plugin:sleeper/counter", "")
		w2.waitText("Count:1")
	})
}
