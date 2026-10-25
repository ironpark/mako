// Package zedhost runs the wasm of Zed extensions, the components their
// Rust is compiled to against Zed's extension API, so sanbo can ask an
// extension what Zed would: chiefly the command that starts each of its
// context servers (MCP servers).
//
// Components run under wasmtime, through its C API: the libwasmtime that
// wasmtime-go bundles, whose own Go bindings don't reach far enough into
// the component model to call an export or define a host function. The
// headers under include/ are that library's (include/sync.sh copies them).
//
// The host imports are Zed's (crates/extension_api/wit): settings,
// downloads, Node.js and npm, GitHub releases, the platform, processes
// and HTTP, implemented as an extension expects them, in its own work
// directory. Imports a context server has no use for, such as the
// language server and debugger ones, trap if called.
package zedhost

/*
#cgo CFLAGS: -I${SRCDIR}/include
#include <stdlib.h>
#include "shim.h"
*/
import "C"

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/cgo"
	"strings"
	"sync"
	"unsafe"

	// It links libwasmtime.a, whose C API this package calls.
	_ "github.com/bytecodealliance/wasmtime-go/v50"

	"github.com/ironpark/sanbo/internal/zed"
)

// Resource types the host defines; any values do, being the host's own.
const (
	resWorktree uint32 = iota + 1
	resProject
	resKeyValueStore
)

var (
	engineOnce sync.Once
	engine     *C.wasm_engine_t
)

// sharedEngine is the process's one engine, for compiling and running.
func sharedEngine() *C.wasm_engine_t {
	engineOnce.Do(func() { engine = C.sb_engine_new() })
	return engine
}

// Host runs extensions. Its fields are read when an extension loads.
type Host struct {
	// WorkDir holds a directory for each extension to download and
	// install into, as Zed's extensions/work does.
	WorkDir string
	// Settings answers an extension's get-settings: the JSON of the
	// settings in category ("context_servers", "lsp", "language") for
	// key, such as a context server's ID. Nil, or "" with no error, is
	// the defaults.
	Settings func(category, key string) (string, error)
	Log      *slog.Logger
}

// ContextServerConfiguration is what an extension says about setting up
// one of its context servers.
type ContextServerConfiguration struct {
	InstallationInstructions string // Markdown
	SettingsSchema           string // JSON Schema of its settings
	DefaultSettings          string // JSON
}

// Command is a program to start, with arguments and environment.
type Command struct {
	Command string
	Args    []string
	Env     [][2]string
}

// Extension is an extension's component, instantiated. Calls into it are
// serialized; Close frees it.
type Extension struct {
	Manifest zed.Manifest
	api      apiVersion
	dir      string
	work     string
	host     *Host
	log      *slog.Logger

	mu      sync.Mutex
	ctx     context.Context // the call in progress, for host imports
	store   *C.wasmtime_store_t
	cctx    *C.wasmtime_context_t
	linker  *C.wasmtime_component_linker_t
	comp    *C.wasmtime_component_t
	inst    C.wasmtime_component_instance_t
	handles []cgo.Handle
	err     error // sticky: a trap leaves the guest's state unknown
	closed  bool
}

// Load compiles and instantiates an installed extension's wasm and runs
// its init-extension.
func (h *Host) Load(ctx context.Context, ext zed.Extension) (*Extension, error) {
	bin, err := os.ReadFile(filepath.Join(ext.Dir, "extension.wasm"))
	if err != nil {
		return nil, fmt.Errorf("zedhost: %s: %w", ext.ID, err)
	}
	api, err := parseAPIVersion(ext.Lib.Version, bin)
	if err != nil {
		return nil, fmt.Errorf("zedhost: %s: %w", ext.ID, err)
	}
	work := filepath.Join(h.WorkDir, ext.ID)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	work, err = filepath.Abs(work)
	if err != nil {
		return nil, err
	}
	log := h.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	e := &Extension{
		Manifest: ext.Manifest, api: api, dir: ext.Dir, work: work, host: h,
		log: log.With("extension", ext.ID),
	}
	e.comp, err = compile(work, bin)
	if err != nil {
		return nil, fmt.Errorf("zedhost: %s: %w", ext.ID, err)
	}
	if err := e.instantiate(); err != nil {
		e.Close()
		return nil, fmt.Errorf("zedhost: %s: %w", ext.ID, err)
	}
	if _, err := e.call(ctx, "init-extension", 0); err != nil {
		e.Close()
		return nil, fmt.Errorf("zedhost: %s: init-extension: %w", ext.ID, err)
	}
	return e, nil
}

// compile compiles a component, or reads the machine code a compile
// before left in the work directory; wasmtime refuses code from another
// version of itself, which then compiles again.
func compile(work string, bin []byte) (*C.wasmtime_component_t, error) {
	sum := sha256.Sum256(bin)
	cache := filepath.Join(work, ".sanbo-"+hex.EncodeToString(sum[:8])+".cwasm")
	var comp *C.wasmtime_component_t
	if code, err := os.ReadFile(cache); err == nil && len(code) > 0 {
		if C.wasmtime_component_deserialize(sharedEngine(), (*C.uint8_t)(unsafe.Pointer(&code[0])), C.size_t(len(code)), &comp) == nil {
			return comp, nil
		}
	}
	if err := takeError(C.wasmtime_component_new(sharedEngine(), (*C.uint8_t)(unsafe.Pointer(&bin[0])), C.size_t(len(bin)), &comp)); err != nil {
		return nil, err
	}
	var out C.wasm_byte_vec_t
	if C.wasmtime_component_serialize(comp, &out) == nil {
		code := C.GoBytes(unsafe.Pointer(out.data), C.int(out.size))
		C.wasm_byte_vec_delete(&out)
		old, _ := filepath.Glob(filepath.Join(work, ".sanbo-*.cwasm"))
		for _, o := range old {
			os.Remove(o)
		}
		_ = os.WriteFile(cache, code, 0o644)
	}
	return comp, nil
}

func (e *Extension) instantiate() error {
	e.store = C.wasmtime_store_new(sharedEngine(), nil, nil)
	e.cctx = C.wasmtime_store_context(e.store)
	if err := e.setWASI(); err != nil {
		return err
	}
	e.linker = C.wasmtime_component_linker_new(sharedEngine())
	if err := takeError(C.wasmtime_component_linker_add_wasip2(e.linker)); err != nil {
		return err
	}
	if err := e.defineImports(); err != nil {
		return err
	}
	return takeError(C.wasmtime_component_linker_instantiate(e.linker, e.cctx, e.comp, &e.inst))
}

// setWASI gives the guest its work directory, as "." and as PWD, which
// std::env::current_dir reads, as Zed does.
func (e *Extension) setWASI() error {
	env := [][2]string{{"PWD", e.work}, {"RUST_BACKTRACE", "full"}}
	names := make([]*C.char, len(env))
	values := make([]*C.char, len(env))
	for i, kv := range env {
		names[i], values[i] = C.CString(kv[0]), C.CString(kv[1])
	}
	defer func() {
		for i := range env {
			C.free(unsafe.Pointer(names[i]))
			C.free(unsafe.Pointer(values[i]))
		}
	}()
	// The arrays hold C pointers only, so C may keep reading them.
	cn := (**C.char)(C.malloc(C.size_t(len(env)) * C.size_t(unsafe.Sizeof(names[0]))))
	cv := (**C.char)(C.malloc(C.size_t(len(env)) * C.size_t(unsafe.Sizeof(values[0]))))
	defer C.free(unsafe.Pointer(cn))
	defer C.free(unsafe.Pointer(cv))
	copy(unsafe.Slice(cn, len(env)), names)
	copy(unsafe.Slice(cv, len(env)), values)
	dir := C.CString(e.work)
	defer C.free(unsafe.Pointer(dir))
	return takeError(C.sb_set_wasi(e.cctx, dir, C.size_t(len(env)), cn, cv))
}

// Close frees the instance. It is safe to call more than once.
func (e *Extension) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	if e.linker != nil {
		C.wasmtime_component_linker_delete(e.linker)
	}
	if e.store != nil {
		C.wasmtime_store_delete(e.store)
	}
	if e.comp != nil {
		C.wasmtime_component_delete(e.comp)
	}
}

// HasExport reports whether the extension exports a function by name.
func (e *Extension) HasExport(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	_, ok := e.export(name)
	return ok
}

func (e *Extension) export(name string) (C.wasmtime_component_func_t, bool) {
	var f C.wasmtime_component_func_t
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	idx := C.wasmtime_component_instance_get_export_index(&e.inst, e.cctx, nil, cs, C.size_t(len(name)))
	if idx == nil {
		return f, false
	}
	defer C.wasmtime_component_export_index_delete(idx)
	return f, bool(C.wasmtime_component_instance_get_func(&e.inst, e.cctx, idx, &f))
}

// errNoExport is a call of a function the extension does not export, as
// an extension built against an older API lacks the newer ones.
var errNoExport = errors.New("zedhost: the extension does not export it")

// call runs an export with args and nresults results. ctx bounds the host
// imports it calls (downloads, installs); the guest's own code runs on.
func (e *Extension) call(ctx context.Context, name string, nresults int, args ...Val) ([]Val, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("zedhost: extension closed")
	}
	if e.err != nil {
		return nil, e.err
	}
	f, ok := e.export(name)
	if !ok {
		return nil, errNoExport
	}
	e.ctx = ctx
	defer func() { e.ctx = nil }()

	n := max(len(args), 1)
	cargs := cval(C.calloc(C.size_t(n), C.size_t(unsafe.Sizeof(C.wasmtime_component_val_t{}))))
	defer C.free(unsafe.Pointer(cargs))
	written := 0
	defer func() {
		for i := range written {
			// A host resource lent for the call is dropped after it.
			if a := at(cargs, i); a.kind == C.WASMTIME_COMPONENT_RESOURCE {
				if err := C.wasmtime_component_resource_any_drop(e.cctx, *of[*C.wasmtime_component_resource_any_t](a)); err != nil {
					C.wasmtime_error_delete(err)
				}
			}
			C.wasmtime_component_val_delete(at(cargs, i))
		}
	}()
	for i, a := range args {
		err := write(e.cctx, at(cargs, i), a)
		written = i + 1
		if err != nil {
			return nil, err
		}
	}
	m := max(nresults, 1)
	cres := cval(C.calloc(C.size_t(m), C.size_t(unsafe.Sizeof(C.wasmtime_component_val_t{}))))
	defer C.free(unsafe.Pointer(cres))
	// calloc leaves each a false bool, as wasmtime asks results to be.
	if err := takeError(C.wasmtime_component_func_call(&f, e.cctx, cargs, C.size_t(len(args)), cres, C.size_t(nresults))); err != nil {
		e.err = fmt.Errorf("zedhost: %s trapped in %s: %w", e.Manifest.ID, name, err)
		return nil, e.err
	}
	out := make([]Val, nresults)
	for i := range out {
		out[i] = read(at(cres, i))
		C.wasmtime_component_val_delete(at(cres, i))
	}
	return out, nil
}

// result unwraps a result<T, string>.
func result(v []Val, err error) (*Val, error) {
	if err != nil {
		return nil, err
	}
	if len(v) != 1 || v[0].Kind != KResult {
		return nil, errors.New("zedhost: the extension answered with something other than a result")
	}
	if !v[0].Ok {
		msg := "failed"
		if v[0].Payload != nil {
			msg = v[0].Payload.Str
		}
		return nil, errors.New(msg)
	}
	return v[0].Payload, nil
}

// project is the one project the host hands extensions: sanbo resolves
// a context server for the node, not for one of its folders.
const projectRep = 1

// ContextServerCommand asks the extension for the command that starts
// the context server id. A relative program is taken in the extension's
// work directory, where it would have installed it.
func (e *Extension) ContextServerCommand(ctx context.Context, id string) (Command, error) {
	pl, err := result(e.call(ctx, "context-server-command", 1, String(id), BorrowHost(projectRep, resProject)))
	if err != nil {
		return Command{}, err
	}
	if pl == nil {
		return Command{}, errors.New("zedhost: no command")
	}
	cmd, _ := pl.Field("command")
	args, _ := pl.Field("args")
	env, _ := pl.Field("env")
	c := Command{Command: cmd.Str, Args: args.Strings(), Env: env.Pairs()}
	if c.Command == "" {
		return Command{}, errors.New("zedhost: an empty command")
	}
	if !filepath.IsAbs(c.Command) && strings.ContainsRune(c.Command, '/') {
		c.Command = filepath.Join(e.work, c.Command)
	}
	return c, nil
}

// ContextServerConfiguration asks the extension how to set up the
// context server id. It is nil when the extension has nothing to say,
// as one built against an API older than 0.5.0 never does.
func (e *Extension) ContextServerConfiguration(ctx context.Context, id string) (*ContextServerConfiguration, error) {
	pl, err := result(e.call(ctx, "context-server-configuration", 1, String(id), BorrowHost(projectRep, resProject)))
	if errors.Is(err, errNoExport) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pl == nil || pl.Payload == nil {
		return nil, nil
	}
	c := *pl.Payload
	get := func(name string) string { v, _ := c.Field(name); return v.Str }
	return &ContextServerConfiguration{
		InstallationInstructions: get("installation-instructions"),
		SettingsSchema:           get("settings-schema"),
		DefaultSettings:          get("default-settings"),
	}, nil
}

// WorkDir is the extension's work directory.
func (e *Extension) WorkDir() string { return e.work }
