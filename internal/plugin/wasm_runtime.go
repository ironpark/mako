package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	wasmtime "github.com/ironpark/wasmtime-pure-go"
)

// WebAssembly plugin runtime (revision 49).
//
// A plugin whose manifest sets `entry = "plugin.wasm"` runs inside an isolated
// Wasmtime sandbox (wasmtime-pure-go, no cgo):
//   - No host filesystem preopens, no host environment, no network, discarded stdio.
//   - Linear memory and table growth capped by Limits.Memory via store.Limiter.
//   - CPU and wall time enforced via Wasmtime epoch interruption tied to *budget;
//     host calls pause the CPU clock identically to the QuickJS runtime.
//   - Compiled machine code cached as checksummed .cwasm outside the plugin dir.
//
// Both Core WebAssembly modules (magic \x00asm\x01\x00\x00\x00) and WebAssembly
// Component Model binaries (targeting mako:plugin@1.0.0 WIT) are supported.

const (
	wasmEpochTick   = 10 * time.Millisecond
	maxWasmRespSize = 4 << 20
	maxWasmReloads  = 64
)

// sharedWasmEngine is the process's shared Wasmtime engine with component
// model and epoch interruption enabled. A single background ticker advances
// the epoch periodically; each in-flight call also bumps the epoch immediately
// when its budget/context cancels.
var sharedWasmEngine = sync.OnceValues(func() (*wasmtime.Engine, error) {
	if err := wasmtime.LoadError(); err != nil {
		return nil, fmt.Errorf("wasmtime unavailable: %w", err)
	}
	cfg := wasmtime.NewConfig()
	cfg.SetWasmComponentModel(true)
	cfg.SetEpochInterruption(true)
	cfg.SetMaxWasmStack(DefaultLimits.Stack)
	eng := wasmtime.NewEngineWithConfig(cfg)
	go func() {
		t := time.NewTicker(wasmEpochTick)
		defer t.Stop()
		for range t.C {
			eng.IncrementEpoch()
		}
	}()
	return eng, nil
})

// isWasmComponent reports whether bin starts with a WebAssembly Component
// Model header (as opposed to a Core Wasm v1 module header).
func isWasmComponent(bin []byte) bool {
	return len(bin) >= 8 &&
		bin[0] == 0x00 && bin[1] == 'a' && bin[2] == 's' && bin[3] == 'm' &&
		!(bin[4] == 0x01 && bin[5] == 0x00 && bin[6] == 0x00 && bin[7] == 0x00)
}

type wasmReload struct {
	ID    string `json:"id"`
	Scope string `json:"scope"`
}

// startWasmRuntime compiles (or loads from cache) and instantiates a .wasm
// plugin version into an isolated Wasmtime store.
func startWasmRuntime(ctx context.Context, v *version, lim Limits, cacheDir string, bridge bridgeFunc) (_ pluginRuntime, err error) {
	defer trapped(&err)
	// Compiling/deserializing wasm is host work, not the plugin's CPU time.
	b, _ := ctx.(*budget)
	if b != nil {
		b.pause()
	}
	resumed := false
	resume := func() {
		if b != nil && !resumed {
			resumed = true
			b.resume()
		}
	}
	defer resume()

	engine, err := sharedWasmEngine()
	if err != nil {
		return nil, err
	}
	if isWasmComponent(v.wasm) {
		return startWasmComponentRuntime(ctx, engine, v, lim, cacheDir, bridge, resume)
	}
	return startWasmCoreRuntime(ctx, engine, v, lim, cacheDir, bridge, resume)
}

// wasmCoreRuntime runs a Core WebAssembly module speaking the Mako core ABI:
//
// Host imports (module "mako"):
//   - host_call(method_ptr, method_len, req_ptr, req_len) -> i64
//     Returns (is_err << 32) | byte_len of the response buffered on the host.
//   - host_read(dst_ptr, dst_len) -> i32
//     Copies buffered response bytes into guest linear memory.
//   - block_reload(id_ptr, id_len, scope_ptr, scope_len)
//     Queues a block reload for the host to run after the current call.
//
// Guest exports:
//   - memory: linear memory
//   - mako_alloc(size: i32) -> i32
//   - mako_free(ptr: i32, size: i32) (optional)
//   - mako_invoke(method_ptr, method_len, args_ptr, args_len) -> i64
//     Returns 0 for empty OK, or (ptr << 32) | len pointing to UTF-8 output.
//     If the first output byte is 0x01, the remainder is an error message.
type wasmCoreRuntime struct {
	engine  *wasmtime.Engine
	store   *wasmtime.Store
	linker  *wasmtime.Linker
	module  *wasmtime.Module
	inst    *wasmtime.Instance
	mem     *wasmtime.Memory
	allocFn *wasmtime.Func
	freeFn  *wasmtime.Func
	invFn   *wasmtime.Func

	bridge   bridgeFunc
	callCtx  context.Context
	lastResp []byte
	reloads  []wasmReload
	closed   bool
}

func startWasmCoreRuntime(ctx context.Context, engine *wasmtime.Engine, v *version, lim Limits, cacheDir string, bridge bridgeFunc, resume func()) (*wasmCoreRuntime, error) {
	mod, err := compileWasmModule(engine, cacheDir, v.wasm)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", v.man.Entry, err)
	}
	w := &wasmCoreRuntime{
		engine:  engine,
		module:  mod,
		bridge:  bridge,
		callCtx: ctx,
	}
	ok := false
	defer func() {
		if !ok {
			w.close()
		}
	}()

	w.store = newSandboxedWasmStore(engine, lim, func() context.Context { return w.callCtx })
	w.linker = wasmtime.NewLinker(engine)
	if err := w.linker.DefineWasi(); err != nil {
		return nil, fmt.Errorf("wasm wasi: %w", err)
	}
	if err := w.defineHostImports(); err != nil {
		return nil, fmt.Errorf("wasm imports: %w", err)
	}

	// Instantiation runs the module's start function if any: charge to plugin budget.
	resume()
	stopAfter := armWasmBudget(ctx, engine, w.store)
	inst, err := w.linker.Instantiate(w.store, mod)
	if err == nil {
		// WASI reactor modules (e.g. Go -buildmode=c-shared, Rust/Zig cdylibs)
		// export `_initialize` to set up guest runtime state before exports run.
		if initFn := inst.GetFunc(w.store, "_initialize"); initFn != nil {
			_, err = initFn.Call(w.store)
		}
	}
	stopAfter()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", v.man.Entry, classifyWasmErr(ctx, err))
	}
	w.inst = inst

	memExt := inst.GetExport(w.store, "memory")
	if memExt == nil || memExt.Memory() == nil {
		return nil, fmt.Errorf("%s: core wasm plugin must export linear memory named \"memory\"", v.man.Entry)
	}
	w.mem = memExt.Memory()
	w.allocFn = inst.GetFunc(w.store, "mako_alloc")
	w.freeFn = inst.GetFunc(w.store, "mako_free")
	w.invFn = inst.GetFunc(w.store, "mako_invoke")
	if w.invFn == nil {
		return nil, fmt.Errorf("%s: core wasm plugin must export \"mako_invoke\"", v.man.Entry)
	}
	if w.allocFn == nil {
		return nil, fmt.Errorf("%s: core wasm plugin must export \"mako_alloc\"", v.man.Entry)
	}
	ok = true
	return w, nil
}

func (w *wasmCoreRuntime) defineHostImports() error {
	if err := w.linker.FuncWrap("mako", "host_call", func(caller *wasmtime.Caller, mPtr, mLen, rPtr, rLen int32) int64 {
		w.lastResp = nil
		method, ok := readGuestBytes(caller, mPtr, mLen)
		if !ok {
			return w.setHostErr("host_call: invalid method memory range")
		}
		req, ok := readGuestBytes(caller, rPtr, rLen)
		if !ok {
			return w.setHostErr("host_call: invalid request memory range")
		}
		ctx := w.callCtx
		if ctx == nil {
			ctx = context.Background()
		}
		if b, ok := ctx.(*budget); ok {
			b.pause()
			defer b.resume()
		}
		resp, err := w.bridge(ctx, string(method), req)
		if err != nil {
			return w.setHostErr(err.Error())
		}
		if len(resp) > maxWasmRespSize {
			return w.setHostErr("host_call: response too large")
		}
		w.lastResp = append([]byte(nil), resp...)
		return int64(uint64(len(w.lastResp)))
	}); err != nil {
		return err
	}
	if err := w.linker.FuncWrap("mako", "host_read", func(caller *wasmtime.Caller, dstPtr, dstLen int32) int32 {
		if dstPtr < 0 || dstLen <= 0 || len(w.lastResp) == 0 {
			return 0
		}
		memExt := caller.GetExport("memory")
		if memExt == nil || memExt.Memory() == nil {
			return 0
		}
		data := memExt.Memory().UnsafeData(caller)
		start := int(dstPtr)
		n := min(int(dstLen), len(w.lastResp))
		if start < 0 || start > len(data) || n > len(data)-start {
			return -1
		}
		copy(data[start:start+n], w.lastResp[:n])
		return int32(n)
	}); err != nil {
		return err
	}
	if err := w.linker.FuncWrap("mako", "block_reload", func(caller *wasmtime.Caller, idPtr, idLen, scopePtr, scopeLen int32) {
		id, ok1 := readGuestBytes(caller, idPtr, idLen)
		scope, ok2 := readGuestBytes(caller, scopePtr, scopeLen)
		if !ok1 || !ok2 || len(id) == 0 || len(w.reloads) >= maxWasmReloads {
			return
		}
		w.reloads = append(w.reloads, wasmReload{ID: string(id), Scope: string(scope)})
	}); err != nil {
		return err
	}
	return nil
}

func (w *wasmCoreRuntime) setHostErr(msg string) int64 {
	if len(msg) > 4096 {
		msg = msg[:4096]
	}
	w.lastResp = []byte(msg)
	return int64((uint64(1) << 32) | uint64(len(w.lastResp)))
}

func readGuestBytes(caller *wasmtime.Caller, ptr, size int32) ([]byte, bool) {
	if ptr < 0 || size < 0 {
		return nil, false
	}
	if size == 0 {
		return nil, true
	}
	memExt := caller.GetExport("memory")
	if memExt == nil || memExt.Memory() == nil {
		return nil, false
	}
	data := memExt.Memory().UnsafeData(caller)
	start, length := int(ptr), int(size)
	if start < 0 || start > len(data) || length > len(data)-start {
		return nil, false
	}
	cp := make([]byte, length)
	copy(cp, data[start:start+length])
	return cp, true
}

func (w *wasmCoreRuntime) writeGuest(ctx context.Context, payload []byte) (int32, int32, error) {
	if len(payload) == 0 {
		return 0, 0, nil
	}
	res, err := w.allocFn.Call(w.store, int32(len(payload)))
	if err != nil {
		return 0, 0, classifyWasmErr(ctx, err)
	}
	ptr, ok := res.(int32)
	if !ok || ptr < 0 {
		return 0, 0, &limitError{msg: "the wasm plugin failed to allocate memory"}
	}
	data := w.mem.UnsafeData(w.store)
	start := int(ptr)
	if start > len(data) || len(payload) > len(data)-start {
		return 0, 0, &limitError{msg: "the wasm plugin returned an out-of-bounds allocation"}
	}
	copy(data[start:start+len(payload)], payload)
	return ptr, int32(len(payload)), nil
}

func (w *wasmCoreRuntime) freeGuest(ptr, size int32) {
	if w.freeFn != nil && ptr > 0 && size > 0 {
		_, _ = w.freeFn.Call(w.store, ptr, size)
	}
}

func (w *wasmCoreRuntime) invoke(ctx context.Context, method string, args ...any) (_ string, err error) {
	defer trapped(&err)
	if w.closed {
		return "", errors.New("the wasm plugin runtime is closed")
	}
	if method == "takeReloads" {
		hostReloads := w.reloads
		w.reloads = nil
		out, err := w.invokeCore(ctx, method, args)
		if err != nil {
			return "", err
		}
		return mergeWasmReloads(hostReloads, out), nil
	}
	out, err := w.invokeCore(ctx, method, args)
	if err != nil {
		return "", err
	}
	return defaultWasmReturn(method, out), nil
}

func (w *wasmCoreRuntime) invokeCore(ctx context.Context, method string, args []any) (string, error) {
	w.callCtx = ctx
	defer func() { w.callCtx = nil }()

	stopAfter := armWasmBudget(ctx, w.engine, w.store)
	defer stopAfter()

	if args == nil {
		args = []any{}
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	mPtr, mLen, err := w.writeGuest(ctx, []byte(method))
	if err != nil {
		return "", err
	}
	defer w.freeGuest(mPtr, mLen)

	aPtr, aLen, err := w.writeGuest(ctx, argsJSON)
	if err != nil {
		return "", err
	}
	defer w.freeGuest(aPtr, aLen)

	res, err := w.invFn.Call(w.store, mPtr, mLen, aPtr, aLen)
	if err != nil {
		return "", classifyWasmErr(ctx, err)
	}
	packed, ok := res.(int64)
	if !ok {
		return "", errors.New("mako_invoke did not return i64")
	}
	if packed == 0 {
		return "", nil
	}
	u := uint64(packed)
	outPtr := int32(u >> 32)
	outLen := int32(u & 0xffffffff)
	if outPtr < 0 || outLen < 0 || int(outLen) > maxWasmRespSize {
		return "", &limitError{msg: "the wasm plugin returned an invalid result pointer"}
	}
	data := w.mem.UnsafeData(w.store)
	start, length := int(outPtr), int(outLen)
	if start > len(data) || length > len(data)-start {
		return "", &limitError{msg: "the wasm plugin returned an out-of-bounds result"}
	}
	raw := make([]byte, length)
	copy(raw, data[start:start+length])
	w.freeGuest(outPtr, outLen)
	if len(raw) > 0 && raw[0] == 0x01 {
		return "", errors.New(string(raw[1:]))
	}
	return string(raw), nil
}

func (w *wasmCoreRuntime) close() {
	if w == nil || w.closed {
		return
	}
	w.closed = true
	if w.linker != nil {
		w.linker.Close()
		w.linker = nil
	}
	if w.store != nil {
		w.store.Close()
		w.store = nil
	}
	if w.module != nil {
		w.module.Close()
		w.module = nil
	}
}

// Shared helpers for Core and Component Wasm runtimes.

func newSandboxedWasmStore(engine *wasmtime.Engine, lim Limits, getCtx func() context.Context) *wasmtime.Store {
	store := wasmtime.NewStore(engine)
	// Memory cap + conservative table/instance bounds.
	store.Limiter(int64(lim.Memory), 10000, 16, 16, 4)
	// Empty WASI config: no preopened dirs, no inherited env/argv/stdio.
	wasi := wasmtime.NewWasiConfig()
	_ = wasi.SetStdoutWriter(io.Discard)
	_ = wasi.SetStderrWriter(io.Discard)
	store.SetWasi(wasi)
	store.SetEpochDeadline(1)
	store.SetEpochDeadlineCallback(func() (uint64, error) {
		if ctx := getCtx(); ctx != nil {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		return 1, nil
	})
	return store
}

func armWasmBudget(ctx context.Context, engine *wasmtime.Engine, store *wasmtime.Store) func() {
	store.SetEpochDeadline(1)
	if ctx == nil {
		return func() {}
	}
	stop := context.AfterFunc(ctx, func() {
		engine.IncrementEpoch()
	})
	return func() { stop() }
}

func classifyWasmErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if b, ok := ctx.(*budget); ok {
		if over := b.exceeded(); over != nil {
			return &limitError{msg: over.Error()}
		}
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "epoch") || strings.Contains(low, "interrupt"):
		return &limitError{msg: errCPU.Error()}
	case strings.Contains(low, "call stack exhausted") || strings.Contains(low, "stack overflow"):
		return &limitError{msg: "the plugin's calls went deeper than they may"}
	case strings.Contains(low, "out of memory") || strings.Contains(low, "memory minimum") || strings.Contains(low, "exceeds memory limit") || strings.Contains(low, "failed to grow memory"):
		return &limitError{msg: "the plugin used more memory than it may"}
	}
	return err
}

func defaultWasmReturn(method, out string) string {
	if out != "" {
		return out
	}
	switch method {
	case "refresh":
		return "0"
	case "save":
		return "null"
	case "takeReloads", "blocks":
		return "[]"
	}
	return out
}

func mergeWasmReloads(hostReloads []wasmReload, guestJSON string) string {
	var all []wasmReload
	all = append(all, hostReloads...)
	if strings.TrimSpace(guestJSON) != "" {
		var guest []wasmReload
		if json.Unmarshal([]byte(guestJSON), &guest) == nil {
			all = append(all, guest...)
		}
	}
	if len(all) == 0 {
		return "[]"
	}
	// Deduplicate preserving order.
	seen := make(map[string]bool, len(all))
	dedup := make([]wasmReload, 0, len(all))
	for _, r := range all {
		k := r.ID + "\x00" + r.Scope
		if r.ID == "" || seen[k] {
			continue
		}
		seen[k] = true
		dedup = append(dedup, r)
	}
	b, _ := json.Marshal(dedup)
	return string(b)
}

func compileWasmModule(engine *wasmtime.Engine, cacheDir string, bin []byte) (*wasmtime.Module, error) {
	if cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0o755); err == nil {
			sum := sha256.Sum256(bin)
			cache := filepath.Join(cacheDir, ".mako-core-"+hex.EncodeToString(sum[:8])+".cwasm")
			if code, ok := readWasmCache(cache); ok {
				if mod, err := wasmtime.NewModuleDeserialize(engine, code); err == nil {
					return mod, nil
				}
			}
			mod, err := wasmtime.NewModule(engine, bin)
			if err != nil {
				return nil, err
			}
			if code, err := mod.Serialize(); err == nil {
				cleanOldWasmCache(cacheDir, ".mako-core-*.cwasm", cache)
				_ = writeWasmCache(cache, code)
			}
			return mod, nil
		}
	}
	return wasmtime.NewModule(engine, bin)
}

func compileWasmComponent(engine *wasmtime.Engine, cacheDir string, bin []byte) (*wasmtime.Component, error) {
	if cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0o755); err == nil {
			sum := sha256.Sum256(bin)
			cache := filepath.Join(cacheDir, ".mako-comp-"+hex.EncodeToString(sum[:8])+".cwasm")
			if code, ok := readWasmCache(cache); ok {
				if comp, err := wasmtime.NewComponentDeserialize(engine, code); err == nil {
					return comp, nil
				}
			}
			comp, err := wasmtime.NewComponent(engine, bin)
			if err != nil {
				return nil, err
			}
			if code, err := comp.Serialize(); err == nil {
				cleanOldWasmCache(cacheDir, ".mako-comp-*.cwasm", cache)
				_ = writeWasmCache(cache, code)
			}
			return comp, nil
		}
	}
	return wasmtime.NewComponent(engine, bin)
}

func readWasmCache(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) <= sha256.Size {
		return nil, false
	}
	code, sum := data[:len(data)-sha256.Size], data[len(data)-sha256.Size:]
	if got := sha256.Sum256(code); !bytes.Equal(got[:], sum) {
		return nil, false
	}
	return code, true
}

func writeWasmCache(path string, code []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".compiling-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	sum := sha256.Sum256(code)
	if _, err := tmp.Write(append(slices.Clip(code), sum[:]...)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func cleanOldWasmCache(dir, glob, keep string) {
	old, _ := filepath.Glob(filepath.Join(dir, glob))
	for _, o := range old {
		if o != keep {
			_ = os.Remove(o)
		}
	}
}
