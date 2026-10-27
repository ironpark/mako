// Package syntax parses source text with tree-sitter grammars and runs
// highlight queries over the result.
//
// The tree-sitter C runtime is compiled to WebAssembly (runtime/build.sh)
// and run by wasmtime, through wasmtime-go (cgo). Grammars are the precompiled wasm dynamic libraries
// that Zed extensions ship (grammars/<name>.wasm); LoadLanguage links one
// into the runtime's memory and function table, the way a dynamic loader
// would, so the C parser calls the grammar's lexer and external scanner
// directly.
package syntax

//go:generate sh runtime/build.sh

import (
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/bytecodealliance/wasmtime-go/v50"
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
	runtimeModule = "mako_tree_sitter"
	// tableReserve is how many function table slots are set aside for
	// grammars. A grammar uses a handful (its lexers and scanner hooks).
	tableReserve = 4096
	// grammarStack is the shadow stack each grammar's own code runs on.
	grammarStack = 256 << 10
	// matchLimit caps in-progress matches per query cursor, as editors do,
	// so pathological queries stay bounded on large files.
	matchLimit = 1 << 16
)

// engine is shared by every Runtime in the process; compiled modules
// belong to an engine, so sharing it lets them share compiled code.
var engine = wasmtime.NewEngine()

// runtimeMod is the compiled tree-sitter runtime, with its table grown by
// tableReserve. It is compiled once, by the first NewRuntime.
var runtimeMod = sync.OnceValues(func() (*runtimeModuleInfo, error) {
	secs, err := readSections(runtimeWasm)
	if err != nil {
		return nil, err
	}
	base, err := growTable(secs, tableReserve)
	if err != nil {
		return nil, err
	}
	mod, err := wasmtime.NewModule(engine, writeSections(secs))
	if err != nil {
		return nil, fmt.Errorf("syntax: compile runtime: %w", err)
	}
	info := &runtimeModuleInfo{mod: mod, tableBase: base, funcs: map[string]bool{}}
	for _, e := range mod.Exports() {
		if e.Type().FuncType() != nil {
			info.funcs[e.Name()] = true
		}
	}
	return info, nil
})

type runtimeModuleInfo struct {
	mod       *wasmtime.Module
	tableBase uint32          // first table slot reserved for grammars
	funcs     map[string]bool // exported function names
}

// ErrClosed is returned after the Runtime is closed.
var ErrClosed = errors.New("syntax: runtime closed")

// Runtime is one wasmtime store holding the tree-sitter instance and every
// language loaded into it. It is safe for concurrent use; calls into wasm
// are serialized by a mutex.
type Runtime struct {
	mu      sync.Mutex
	store   *wasmtime.Store
	linker  *wasmtime.Linker
	inst    *wasmtime.Instance
	mem     *wasmtime.Memory
	exports map[string]bool // functions the runtime exports
	fns     map[string]*wasmtime.Func
	libc    map[string]uint32 // libc export -> function table index
	table   uint32            // next free reserved table slot
	tblEnd  uint32
	err     error // sticky: set once a trap leaves wasm state unknown
	closed  bool
}

// NewRuntime compiles and starts the tree-sitter runtime. ctx only
// bounds the setup.
func NewRuntime(ctx context.Context) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := runtimeMod()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store := wasmtime.NewStore(engine)
	store.SetWasi(wasmtime.NewWasiConfig())
	linker := wasmtime.NewLinker(engine)
	linker.AllowShadowing(true) // each grammar redefines the glue and stubs
	r := &Runtime{
		store:   store,
		linker:  linker,
		exports: info.funcs,
		fns:     map[string]*wasmtime.Func{},
		table:   info.tableBase,
		tblEnd:  info.tableBase + tableReserve,
	}
	fail := func(err error) (*Runtime, error) {
		linker.Close()
		store.Close()
		return nil, err
	}
	if err := linker.DefineWasi(); err != nil {
		return fail(err)
	}
	inst, err := linker.Instantiate(store, info.mod)
	if err != nil {
		return fail(fmt.Errorf("syntax: start runtime: %w", err))
	}
	if err := linker.DefineInstance(store, runtimeModule, inst); err != nil {
		return fail(err)
	}
	r.inst = inst
	ext := inst.GetExport(store, "memory")
	if ext == nil || ext.Memory() == nil {
		return fail(errors.New("syntax: runtime exports no memory"))
	}
	r.mem = ext.Memory()
	if r.exports["_initialize"] {
		if _, err := r.call("_initialize"); err != nil {
			return fail(err)
		}
	}
	if err := r.readLibc(); err != nil {
		return fail(err)
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
		idx, ok := r.readUint32(ptr + uint32(i)*4)
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
	r.fns = nil
	r.linker.Close()
	r.store.Close()
	return nil
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
func (r *Runtime) call(name string, args ...uint64) (uint32, error) {
	if err := r.usable(); err != nil {
		return 0, err
	}
	f := r.fns[name]
	if f == nil {
		if f = r.inst.GetFunc(r.store, name); f == nil {
			return 0, fmt.Errorf("syntax: runtime lacks %s", name)
		}
		r.fns[name] = f
	}
	res, err := callFunc(r.store, f, args)
	if err != nil {
		r.err = fmt.Errorf("syntax: runtime trapped in %s: %w", name, err)
		return 0, r.err
	}
	return res, nil
}

// call1 is call for functions returning one i32.
func (r *Runtime) call1(name string, args ...uint64) (uint32, error) {
	return r.call(name, args...)
}

// callFunc calls f with i32 arguments and returns its i32 result, if any.
func callFunc(store *wasmtime.Store, f *wasmtime.Func, args []uint64) (uint32, error) {
	var in [10]any
	vals := in[:0]
	for _, a := range args {
		vals = append(vals, int32(uint32(a)))
	}
	res, err := f.Call(store, vals...)
	if err != nil {
		return 0, err
	}
	switch v := res.(type) {
	case int32:
		return uint32(v), nil
	case int64:
		return uint32(v), nil
	}
	return 0, nil
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
	if !r.write(p, b) {
		r.free(p)
		return 0, errWasm
	}
	return p, nil
}

// readString copies n bytes at p out of wasm memory.
func (r *Runtime) readString(p, n uint32) string {
	b, ok := r.read(p, n)
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

// read returns the n bytes at p, or false if they are out of bounds. The
// slice aliases wasm memory: it is valid only until the next call into
// wasm, which may grow (and move) the memory.
func (r *Runtime) read(p, n uint32) ([]byte, bool) {
	data := r.mem.UnsafeData(r.store)
	if uint64(p)+uint64(n) > uint64(len(data)) {
		return nil, false
	}
	return data[p : p+n : p+n], true
}

// readUint32 reads a little-endian uint32 at p.
func (r *Runtime) readUint32(p uint32) (uint32, bool) {
	b, ok := r.read(p, 4)
	if !ok {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b), true
}

// write copies b to p, or reports false if it would be out of bounds.
func (r *Runtime) write(p uint32, b []byte) bool {
	dst, ok := r.read(p, uint32(len(b)))
	if !ok || uint64(len(b)) > 1<<32-1 {
		return false
	}
	copy(dst, b)
	return true
}
