// Package syntax parses source text with tree-sitter grammars and runs
// highlight queries over the result, in pure Go.
//
// The tree-sitter C runtime is compiled to WebAssembly (runtime/build.sh)
// and run by wazero. Grammars are the precompiled wasm dynamic libraries
// that Zed extensions ship (grammars/<name>.wasm); LoadLanguage links one
// into the runtime's memory and function table, the way a dynamic loader
// would, so the C parser calls the grammar's lexer and external scanner
// directly.
package syntax

//go:generate sh runtime/build.sh

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed runtime/tree-sitter.wasm
var runtimeWasm []byte

//go:embed runtime/libc-symbols.txt
var libcSymbolsTxt string

//go:embed runtime/VERSION
var tsVersion string

// TreeSitterVersion is the tree-sitter release the runtime is built from.
var TreeSitterVersion = strings.TrimSpace(tsVersion)

// ABI versions of grammars the runtime accepts.
const (
	MinABIVersion = 13
	MaxABIVersion = 15
)

const (
	runtimeModule = "sanbo_tree_sitter"
	// tableReserve is how many function table slots are set aside for
	// grammars. A grammar uses a handful (its lexers and scanner hooks).
	tableReserve = 4096
	// grammarStack is the shadow stack each grammar's own code runs on.
	grammarStack = 256 << 10
	// matchLimit caps in-progress matches per query cursor, as editors do,
	// so pathological queries stay bounded on large files.
	matchLimit = 1 << 16
)

// compileCache shares compiled machine code between Runtimes in the
// process, so only the first NewRuntime pays for compiling the runtime.
var compileCache = wazero.NewCompilationCache()

// ErrClosed is returned after the Runtime is closed.
var ErrClosed = errors.New("syntax: runtime closed")

// Runtime is one wazero runtime holding the tree-sitter instance and every
// language loaded into it. It is safe for concurrent use; calls into wasm
// are serialized by a mutex.
type Runtime struct {
	mu     sync.Mutex
	ctx    context.Context // for calls; wasm is never interrupted midway
	wr     wazero.Runtime
	mod    api.Module
	mem    api.Memory
	fns    map[string]api.Function
	libc   map[string]uint32 // libc export -> function table index
	table  uint32            // next free reserved table slot
	tblEnd uint32
	nlang  int
	err    error // sticky: set once a trap leaves wasm state unknown
	closed bool
}

// NewRuntime compiles and starts the tree-sitter runtime. ctx only
// bounds the setup.
func NewRuntime(ctx context.Context) (*Runtime, error) {
	secs, err := readSections(runtimeWasm)
	if err != nil {
		return nil, err
	}
	base, err := growTable(secs, tableReserve)
	if err != nil {
		return nil, err
	}
	bin := writeSections(secs)

	wr := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCompilationCache(compileCache))
	r := &Runtime{ctx: context.Background(), wr: wr, table: base, tblEnd: base + tableReserve}
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, wr); err != nil {
		wr.Close(ctx)
		return nil, err
	}
	mod, err := wr.InstantiateWithConfig(ctx, bin, wazero.NewModuleConfig().
		WithName(runtimeModule).WithStartFunctions("_initialize"))
	if err != nil {
		wr.Close(ctx)
		return nil, fmt.Errorf("syntax: start runtime: %w", err)
	}
	r.mod = mod
	r.mem = mod.Memory()
	r.fns = map[string]api.Function{}
	if err := r.readLibc(); err != nil {
		wr.Close(ctx)
		return nil, err
	}
	return r, nil
}

// readLibc records the table index of each exported libc function, for
// grammars that take their address (GOT.func imports).
func (r *Runtime) readLibc() error {
	var names []string
	for _, l := range strings.Split(libcSymbolsTxt, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			names = append(names, l)
		}
	}
	ptr, err := r.call1("shim_libc_fns")
	if err != nil {
		return err
	}
	n, err := r.call1("shim_libc_count")
	if err != nil {
		return err
	}
	if int(n) != len(names) {
		return fmt.Errorf("syntax: runtime has %d libc symbols, want %d", n, len(names))
	}
	r.libc = make(map[string]uint32, n)
	for i, name := range names {
		idx, ok := r.mem.ReadUint32Le(ptr + uint32(i)*4)
		if !ok {
			return errWasm
		}
		r.libc[name] = idx
	}
	return nil
}

// Close releases the runtime and everything loaded into it.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.wr.Close(r.ctx)
}

// usable reports why wasm can't be called right now. Callers hold r.mu.
func (r *Runtime) usable() error {
	if r.closed {
		return ErrClosed
	}
	return r.err
}

// call runs an exported runtime function. A trap poisons the runtime:
// the C heap and stack may be mid-update, so nothing is trusted after.
func (r *Runtime) call(name string, args ...uint64) ([]uint64, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}
	f := r.fns[name]
	if f == nil {
		if f = r.mod.ExportedFunction(name); f == nil {
			return nil, fmt.Errorf("syntax: runtime lacks %s", name)
		}
		r.fns[name] = f
	}
	res, err := f.Call(r.ctx, args...)
	if err != nil {
		r.err = fmt.Errorf("syntax: runtime trapped in %s: %w", name, err)
		return nil, r.err
	}
	return res, nil
}

// call1 is call for functions returning one i32.
func (r *Runtime) call1(name string, args ...uint64) (uint32, error) {
	res, err := r.call(name, args...)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	return uint32(res[0]), nil
}

// malloc allocates n bytes in wasm memory.
func (r *Runtime) malloc(n int) (uint32, error) {
	p, err := r.call1("shim_malloc", uint64(n))
	if err == nil && p == 0 && n > 0 {
		err = errors.New("syntax: out of wasm memory")
	}
	return p, err
}

func (r *Runtime) free(p uint32) {
	if p != 0 {
		r.call("shim_free", uint64(p))
	}
}

// writeBytes copies b into a fresh allocation.
func (r *Runtime) writeBytes(b []byte) (uint32, error) {
	n := len(b)
	if n == 0 {
		n = 1
	}
	p, err := r.malloc(n)
	if err != nil {
		return 0, err
	}
	if !r.mem.Write(p, b) {
		r.free(p)
		return 0, errWasm
	}
	return p, nil
}

// readString copies n bytes at p out of wasm memory.
func (r *Runtime) readString(p, n uint32) string {
	b, ok := r.mem.Read(p, n)
	if !ok {
		return ""
	}
	return string(b)
}

// readCString copies a NUL-terminated string out of wasm memory.
func (r *Runtime) readCString(p uint32) (string, error) {
	n, err := r.call1("strlen", uint64(p))
	if err != nil {
		return "", err
	}
	return r.readString(p, n), nil
}
