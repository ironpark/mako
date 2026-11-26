package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wasmtime "github.com/ironpark/wasmtime-pure-go"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

// makeTestWasm compiles WAT to a Core WebAssembly binary for tests.
func makeTestWasm(t *testing.T, wat string) []byte {
	t.Helper()
	if err := wasmtime.LoadError(); err != nil {
		t.Skipf("wasmtime unavailable: %v", err)
	}
	bin, err := wasmtime.Wat2Wasm(wat)
	if err != nil {
		t.Fatalf("Wat2Wasm: %v", err)
	}
	return bin
}

// simpleWasmPluginWAT builds a Core Wasm plugin implementing the Mako Core ABI:
// - mako_alloc bumps a pointer in linear memory starting at offset 4096
// - mako_invoke checks the first byte of method:
//   - 'r' (render) -> returns JSON tree {"kind":"text","text":"hello wasm","tone":"accent"}
//   - 'e' (event)  -> calls mako.host_call("storage.setItem", {"key":"k","value":"v"}) and checks result
//   - everything else -> returns 0 (empty OK)
const simpleWasmPluginWAT = `
(module
  (import "mako" "host_call" (func $host_call (param i32 i32 i32 i32) (result i64)))
  (import "mako" "host_read" (func $host_read (param i32 i32) (result i32)))
  (import "mako" "block_reload" (func $block_reload (param i32 i32 i32 i32)))

  (memory (export "memory") 2)
  (global $bump (mut i32) (i32.const 4096))

  ;; Static data:
  ;; offset 0..64:   render tree JSON (79 bytes)
  (data (i32.const 0) "{\"kind\":\"column\",\"children\":[{\"kind\":\"button\",\"key\":\"go\",\"text\":\"Go\",\"onTap\":[\"go\"]}]}")
  ;; offset 256: method "storage.setItem" (15 bytes)
  (data (i32.const 256) "storage.setItem")
  ;; offset 288: req JSON {"key":"k","value":"from-wasm"} (31 bytes)
  (data (i32.const 288) "{\"key\":\"k\",\"value\":\"from-wasm\"}")
  ;; offset 384: error response prefix \01 + "denied"
  (data (i32.const 384) "\01host call failed")

  (func (export "mako_alloc") (param $n i32) (result i32)
    (local $p i32)
    (local.set $p (global.get $bump))
    (global.set $bump (i32.add (local.get $p) (local.get $n)))
    (local.get $p)
  )

  (func (export "mako_invoke") (param $mptr i32) (param $mlen i32) (param $aptr i32) (param $alen i32) (result i64)
    (local $first i32)
    (local $res i64)
    (if (i32.eq (local.get $mlen) (i32.const 0))
      (then (return (i64.const 0)))
    )
    (local.set $first (i32.load8_u (local.get $mptr)))

    ;; "render" starts with 'r' (114) and has len 6
    (if (i32.and (i32.eq (local.get $first) (i32.const 114)) (i32.eq (local.get $mlen) (i32.const 6)))
      (then
        ;; ptr = 0, len = 86 -> (0 << 32) | 86
        (return (i64.const 86))
      )
    )

    ;; "event" starts with 'e' (101) and has len 5
    (if (i32.and (i32.eq (local.get $first) (i32.const 101)) (i32.eq (local.get $mlen) (i32.const 5)))
      (then
        (local.set $res (call $host_call (i32.const 256) (i32.const 15) (i32.const 288) (i32.const 31)))
        ;; high 32 bits != 0 means host error
        (if (i64.ne (i64.shr_u (local.get $res) (i64.const 32)) (i64.const 0))
          (then
            ;; return ptr=384, len=17 ("\01host call failed")
            (return (i64.or (i64.shl (i64.const 384) (i64.const 32)) (i64.const 17)))
          )
        )
        (return (i64.const 0))
      )
    )

    (i64.const 0)
  )
)
`

func TestWasmPluginRenderAndHostCall(t *testing.T) {
	wasmBin := makeTestWasm(t, simpleWasmPluginWAT)

	dir := t.TempDir()
	pdir := filepath.Join(dir, "plugins", "hello-wasm")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `
id = "hello-wasm"
version = "0.1.0"
publisher = "tester"
api = 1
entry = "plugin.wasm"
permissions = ["storage"]

[[contributes.block]]
id = "main"
title = "Wasm Panel"
icon = "cpu"
slots = ["right", "sidebar", "tab"]
`
	if err := os.WriteFile(filepath.Join(pdir, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "plugin.wasm"), wasmBin, 0o644); err != nil {
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

	// Wait for plugin to be discovered.
	var info *protocol.PluginInfo
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range m.List() {
			if p.GetId() == "hello-wasm" {
				info = p
				break
			}
		}
		if info != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if info == nil {
		t.Fatal("wasm plugin was not listed")
	}
	if len(info.GetBlocks()) != 1 || info.GetBlocks()[0].GetIcon() != "cpu" {
		t.Fatalf("unexpected blocks: %+v", info.GetBlocks())
	}

	// Grant storage permission.
	if _, err := m.Act(ctx, &protocol.PluginAction{PluginId: "hello-wasm", Op: protocol.PluginGrant}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Watch the block and receive the rendered UI tree.
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	updates := make(chan *protocol.BlockUpdate, 8)
	go func() {
		_ = m.WatchScopedView(watchCtx, "plugin:hello-wasm/main", "", "en", View{Instance: "tab-1", Params: `{"item":42}`}, func(u *protocol.BlockUpdate) error {
			updates <- u
			return nil
		})
	}()

	var tree *protocol.UiNode
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case u := <-updates:
			if u.GetTree() != nil {
				if u.GetInstance() != "tab-1" {
					t.Fatalf("expected instance tab-1, got %q", u.GetInstance())
				}
				tree = u.GetTree()
			}
		case <-time.After(50 * time.Millisecond):
		}
		if tree != nil {
			break
		}
	}
	if tree == nil || len(tree.GetChildren()) != 1 || tree.GetChildren()[0].GetText() != "Go" {
		t.Fatalf("unexpected rendered tree: %+v", tree)
	}

	// Trigger the button tap -> calls mako.host_call("storage.setItem", ...).
	if _, err := m.Event(ctx, &protocol.BlockEvent{
		Block:    "plugin:hello-wasm/main",
		Instance: "tab-1",
		Node:     "/go",
		Kind:     protocol.UiTap,
	}); err != nil {
		t.Fatalf("event failed: %v", err)
	}

	// Verify storage persisted the value written by the wasm plugin.
	p, ok := m.plugin("hello-wasm")
	if !ok {
		t.Fatal("plugin missing")
	}
	val, found, err := p.store.getScoped("", "k")
	if err != nil || !found || val != "from-wasm" {
		t.Fatalf("expected storage k=from-wasm, got found=%v val=%q err=%v", found, val, err)
	}

	// Verify compiled .cwasm cache file was written in DataDir/.compiled/hello-wasm.
	matches, _ := filepath.Glob(filepath.Join(dir, "data", ".compiled", "hello-wasm", ".mako-core-*.cwasm"))
	if len(matches) == 0 {
		t.Fatalf("expected compiled .cwasm cache file")
	}
}

func TestWasmPluginPermissionDeniedAndCPUBudget(t *testing.T) {
	// Infinite loop WAT for CPU epoch interruption check.
	spinWAT := `
(module
  (memory (export "memory") 1)
  (func (export "mako_alloc") (param i32) (result i32) (i32.const 1024))
  (func (export "mako_invoke") (param i32 i32 i32 i32) (result i64)
    (loop $inf (br $inf))
    (i64.const 0)
  )
)
`
	spinBin := makeTestWasm(t, spinWAT)
	v := &version{
		man:  &Manifest{ID: "spin", Version: "0.1.0", Publisher: "tester", API: 1, Entry: "spin.wasm"},
		wasm: spinBin,
	}
	lim := DefaultLimits.withDefaults()
	lim.CPU = 80 * time.Millisecond
	lim.Wall = 500 * time.Millisecond

	b := newBudget(context.Background(), lim.CPU, lim.Wall)
	defer b.release()

	rt, err := startWasmRuntime(b, v, lim, t.TempDir(), func(context.Context, string, []byte) ([]byte, error) {
		return []byte("{}"), nil
	})
	if err != nil {
		t.Fatalf("startWasmRuntime: %v", err)
	}
	defer rt.close()

	start := time.Now()
	_, err = rt.invoke(b, "render", "main", "", "{}")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected CPU budget interruption error")
	}
	err = classify(b, err)
	var limErr *limitError
	if !strings.Contains(err.Error(), "longer than a call may") && !isLimitErr(err, &limErr) {
		t.Fatalf("expected limitError for infinite loop, got: %T %v", err, err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("epoch interruption took too long: %v", elapsed)
	}
}

func isLimitErr(err error, target **limitError) bool {
	if le, ok := err.(*limitError); ok {
		*target = le
		return true
	}
	return false
}

func TestWasmPluginMemoryLimit(t *testing.T) {
	// Attempts to grow memory by 500 pages (~32 MiB) when limit is 2 MiB.
	oomWAT := `
(module
  (memory (export "memory") 1)
  (func (export "mako_alloc") (param i32) (result i32) (i32.const 1024))
  (func (export "mako_invoke") (param i32 i32 i32 i32) (result i64)
    ;; Try growing by 500 pages; memory.grow returns -1 on limiter denial
    (if (i32.eq (memory.grow (i32.const 500)) (i32.const -1))
      (then
        ;; Write "\01oom" at offset 64 and return ptr=64, len=4
        (i32.store8 (i32.const 64) (i32.const 1))
        (i32.store8 (i32.const 65) (i32.const 111))
        (i32.store8 (i32.const 66) (i32.const 111))
        (i32.store8 (i32.const 67) (i32.const 109))
        (return (i64.or (i64.shl (i64.const 64) (i64.const 32)) (i64.const 4)))
      )
    )
    (i64.const 0)
  )
)
`
	bin := makeTestWasm(t, oomWAT)
	v := &version{
		man:  &Manifest{ID: "oom", Version: "0.1.0", Publisher: "tester", API: 1, Entry: "oom.wasm"},
		wasm: bin,
	}
	lim := DefaultLimits.withDefaults()
	lim.Memory = 2 << 20
	b := newBudget(context.Background(), lim.CPU, lim.Wall)
	defer b.release()
	rt, err := startWasmRuntime(b, v, lim, "", func(context.Context, string, []byte) ([]byte, error) {
		return []byte("{}"), nil
	})
	if err != nil {
		t.Fatalf("startWasmRuntime: %v", err)
	}
	defer rt.close()

	_, err = rt.invoke(b, "render", "main", "", "{}")
	if err == nil || !strings.Contains(err.Error(), "oom") {
		t.Fatalf("expected memory.grow to be denied by store.Limiter, got err=%v", err)
	}
}

func TestManifestSidebarTabSlotsAndWasmEntry(t *testing.T) {
	raw := `
id = "tab-demo"
version = "1.0.0"
publisher = "acme"
api = 1
entry = "dist/plugin.wasm"
permissions = []

[[contributes.block]]
id = "nav"
title = "Explorer"
icon = "compass"
slots = ["sidebar"]

[[contributes.block]]
id = "viewer"
title = "Viewer"
icon = "file-code"
slots = ["tab"]
`
	man, err := ParseManifest([]byte(raw))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(man.Blocks) != 2 || man.Blocks[0].Icon != "compass" || man.Blocks[1].Icon != "file-code" {
		t.Fatalf("unexpected blocks: %+v", man.Blocks)
	}

	// OpenTab & CloseTab command validation via host.
	p := &plugin{
		id: "tab-demo",
		v:  &version{man: man},
	}
	h := &host{p: p}
	arg, _ := json.Marshal(map[string]any{
		"block":   "viewer",
		"key":     "doc-1",
		"title":   "My Doc",
		"params":  map[string]any{"id": 123},
		"preview": true,
	})
	// Without gesture: must fail.
	if _, err := h.CommandsRunCommand(context.Background(), &hostapi.RunCommandRequest{Id: cmdOpenTab, Arg: string(arg)}); err == nil {
		t.Fatal("expected openTab without gesture to fail")
	}
	// With gesture: succeeds and queues tab intent.
	p.gesture = true
	if _, err := h.CommandsRunCommand(context.Background(), &hostapi.RunCommandRequest{Id: cmdOpenTab, Arg: string(arg)}); err != nil {
		t.Fatalf("openTab failed: %v", err)
	}
	// Non-tab block ("nav" only has "sidebar") must be rejected.
	badArg, _ := json.Marshal(map[string]any{"block": "nav", "key": "x"})
	if _, err := h.CommandsRunCommand(context.Background(), &hostapi.RunCommandRequest{Id: cmdOpenTab, Arg: string(badArg)}); err == nil {
		t.Fatal("expected openTab on sidebar-only block to fail")
	}
	// CloseTab succeeds and queues close_tab intent.
	closeArg, _ := json.Marshal(map[string]any{"block": "viewer", "key": "doc-1"})
	if _, err := h.CommandsRunCommand(context.Background(), &hostapi.RunCommandRequest{Id: cmdCloseTab, Arg: string(closeArg)}); err != nil {
		t.Fatalf("closeTab failed: %v", err)
	}
	done := p.drainEffects()
	if len(done.GetOpen()) != 2 {
		t.Fatalf("expected 2 intents, got %d", len(done.GetOpen()))
	}
	openIntent := done.GetOpen()[0]
	if openIntent.GetKind() != "tab" || openIntent.GetBlock() != "plugin:tab-demo/viewer" || openIntent.GetKey() != "doc-1" || openIntent.GetTitle() != "My Doc" || openIntent.GetIcon() != "file-code" || !openIntent.GetPreview() {
		t.Fatalf("unexpected open intent: %+v", openIntent)
	}
	closeIntent := done.GetOpen()[1]
	if closeIntent.GetKind() != "close_tab" || closeIntent.GetBlock() != "plugin:tab-demo/viewer" || closeIntent.GetKey() != "doc-1" {
		t.Fatalf("unexpected close intent: %+v", closeIntent)
	}
}
