package syntax

import (
	"crypto/sha256"
	"fmt"
	"sync"

	wasmtime "github.com/ironpark/wasmtime-pure-go"
)

// Grammars import their glue globals and stub functions from these
// linker modules. Each LoadLanguage redefines them before instantiating.
const (
	glueModule = "mako_glue"
	stubModule = "mako_stub"
)

// grammarCache holds compiled grammar modules by the SHA-256 of the
// grammar wasm. The import rewrite only depends on the grammar and the
// (embedded) runtime, so a compiled module is reusable by every Runtime.
var grammarCache sync.Map // [sha256.Size]byte -> *wasmtime.Module

// Language is a grammar linked into a Runtime.
type Language struct {
	rt     *Runtime
	name   string
	ptr    uint32 // TSLanguage *
	parser uint32 // TSParser *, reused for every parse
	abi    uint32
}

// Name is the grammar name, as in tree_sitter_<name>.
func (l *Language) Name() string { return l.name }

// ABIVersion is the tree-sitter language ABI the grammar was generated for.
func (l *Language) ABIVersion() int { return int(l.abi) }

// LoadLanguage links a grammar wasm dynamic library (as shipped in Zed
// extensions) into the runtime. name is the grammar name, as in the
// exported tree_sitter_<name> function.
//
// Linking follows the wasm dynamic-linking convention: the grammar gets a
// zeroed block of the runtime's heap for its data (__memory_base), a range
// of reserved slots in the shared function table (__table_base) and a
// stack of its own. Its imports of memory, the table and libc resolve to
// the runtime's exports. Loaded languages live until the Runtime closes.
func (r *Runtime) LoadLanguage(name string, grammarWasm []byte) (*Language, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usable(); err != nil {
		return nil, err
	}
	l, err := r.link(name, grammarWasm)
	if err != nil {
		return nil, fmt.Errorf("syntax: load %s: %w", name, err)
	}
	return l, nil
}

func (r *Runtime) link(name string, bin []byte) (*Language, error) {
	secs, err := readSections(bin)
	if err != nil {
		return nil, err
	}
	dl, err := readDylink(secs)
	if err != nil {
		return nil, err
	}
	types, err := readTypes(secs)
	if err != nil {
		return nil, err
	}
	ims, err := readImports(secs)
	if err != nil {
		return nil, err
	}
	if dl.memAlign > 16 || dl.tableAlign > 16 {
		return nil, fmt.Errorf("unsupported alignment in dylink.0")
	}

	// Static data block.
	memBase, err := r.call1("shim_alloc_zeroed", uint64(1)<<dl.memAlign, uint64(dl.memSize))
	if err != nil {
		return nil, err
	}
	if memBase == 0 {
		return nil, fmt.Errorf("out of wasm memory")
	}
	// Function table slots.
	talign := uint32(1) << dl.tableAlign
	tableBase := (r.table + talign - 1) / talign * talign
	if tableBase+dl.tableSize > r.tblEnd {
		return nil, fmt.Errorf("function table full (%d slots reserved)", tableReserve)
	}
	r.table = tableBase + dl.tableSize
	// Stack, growing down from the top.
	stack, err := r.malloc(grammarStack)
	if err != nil {
		return nil, err
	}
	sp := (stack + grammarStack) &^ 15

	glueName, stubName := glueModule, stubModule

	globals := []globalDef{
		{name: "__stack_pointer", value: sp, mutable: true},
		{name: "__memory_base", value: memBase},
		{name: "__table_base", value: tableBase},
	}
	have := map[string]bool{}
	for _, g := range globals {
		have[g.name] = true
	}
	var stubs []wasmImport
	for i := range ims {
		im := &ims[i]
		switch {
		case im.module == "env" && im.kind == kindMemory:
			im.module = runtimeModule
			im.desc = []byte{0, 0} // no limits: the runtime's memory decides
		case im.module == "env" && im.kind == kindTable:
			im.module = runtimeModule
			im.desc = []byte{0x70, 0, 0}
		case im.module == "env" && im.kind == kindGlobal && have[im.name]:
			im.module = glueName
		case im.module == "env" && im.kind == kindFunc:
			if r.exports[im.name] {
				im.module = runtimeModule
			} else {
				im.module = stubName
				stubs = append(stubs, *im)
			}
		case im.module == "GOT.func" && im.kind == kindGlobal:
			idx, ok := r.libc[im.name]
			if !ok {
				return nil, fmt.Errorf("unsupported import GOT.func.%s", im.name)
			}
			gn := "GOT.func." + im.name
			if !have[gn] {
				globals = append(globals, globalDef{name: gn, value: idx, mutable: im.mutable})
				have[gn] = true
			}
			im.module, im.name = glueName, gn
		default:
			return nil, fmt.Errorf("unsupported import %s.%s", im.module, im.name)
		}
	}
	replaceSection(secs, secImport, encodeImports(ims))

	for _, g := range globals {
		v := wasmtime.ValI32(int32(g.value))
		ty := wasmtime.NewGlobalType(wasmtime.NewValType(wasmtime.KindI32), g.mutable)
		glob, err := wasmtime.NewGlobal(r.store, ty, v)
		if err != nil {
			return nil, fmt.Errorf("glue global %s: %w", g.name, err)
		}
		if err := r.linker.Define(r.store, glueName, g.name, glob); err != nil {
			return nil, fmt.Errorf("glue global %s: %w", g.name, err)
		}
	}
	if err := r.defineStubs(stubName, stubs, types); err != nil {
		return nil, err
	}
	mod, err := grammarModule(bin, secs)
	if err != nil {
		return nil, err
	}
	inst, err := r.linker.Instantiate(r.store, mod)
	if err != nil {
		return nil, err
	}

	for _, init := range []string{"__wasm_apply_data_relocs", "_initialize", "__wasm_call_ctors"} {
		f := inst.GetFunc(r.store, init)
		if f == nil {
			continue
		}
		if _, err := callFunc(r.store, f, nil); err != nil {
			r.err = fmt.Errorf("syntax: %s trapped in %s: %w", name, init, err)
			return nil, r.err
		}
		if init == "_initialize" {
			break // it runs the constructors itself
		}
	}
	langFn := inst.GetFunc(r.store, "tree_sitter_"+name)
	if langFn == nil {
		return nil, fmt.Errorf("grammar does not export tree_sitter_%s", name)
	}
	ptr, err := callFunc(r.store, langFn, nil)
	if err != nil {
		r.err = fmt.Errorf("syntax: tree_sitter_%s trapped: %w", name, err)
		return nil, r.err
	}

	abi, err := r.call1("shim_language_abi", uint64(ptr))
	if err != nil {
		return nil, err
	}
	if abi < MinABIVersion || abi > MaxABIVersion {
		return nil, fmt.Errorf("grammar ABI %d outside supported %d..%d", abi, MinABIVersion, MaxABIVersion)
	}
	parser, err := r.call1("shim_parser_new", uint64(ptr))
	if err != nil {
		return nil, err
	}
	if parser == 0 {
		return nil, fmt.Errorf("tree-sitter rejected the language")
	}
	return &Language{rt: r, name: name, ptr: ptr, parser: parser, abi: abi}, nil
}

// grammarModule compiles the rewritten grammar, or returns the copy
// compiled earlier from the same grammar wasm.
func grammarModule(orig []byte, secs []section) (*wasmtime.Module, error) {
	key := sha256.Sum256(orig)
	if m, ok := grammarCache.Load(key); ok {
		return m.(*wasmtime.Module), nil
	}
	m, err := wasmtime.NewModule(engine, writeSections(secs))
	if err != nil {
		return nil, err
	}
	actual, _ := grammarCache.LoadOrStore(key, m)
	return actual.(*wasmtime.Module), nil
}

// defineStubs satisfies function imports the runtime does not provide
// with host functions that trap when called, so grammars whose scanners
// reference rare libc functions still load.
func (r *Runtime) defineStubs(module string, stubs []wasmImport, types []funcType) error {
	for _, s := range stubs {
		if int(s.typeIdx) >= len(types) {
			return errWasm
		}
		t := types[s.typeIdx]
		params, err := valueTypes(t.params)
		if err != nil {
			return err
		}
		results, err := valueTypes(t.results)
		if err != nil {
			return err
		}
		msg := fmt.Sprintf("grammar called unsupported function %s", s.name)
		fn := wasmtime.NewFunc(r.store, wasmtime.NewFuncType(params, results),
			func(*wasmtime.Caller, []wasmtime.Val) ([]wasmtime.Val, *wasmtime.Trap) {
				return nil, wasmtime.NewTrap(msg)
			})
		if err := r.linker.Define(r.store, module, s.name, fn); err != nil {
			return err
		}
	}
	return nil
}

func valueTypes(b []byte) ([]*wasmtime.ValType, error) {
	out := make([]*wasmtime.ValType, len(b))
	for i, c := range b {
		var k wasmtime.ValKind
		switch c {
		case valI32:
			k = wasmtime.KindI32
		case valI64:
			k = wasmtime.KindI64
		case valF32:
			k = wasmtime.KindF32
		case valF64:
			k = wasmtime.KindF64
		default:
			return nil, fmt.Errorf("unsupported value type 0x%x in stub signature", c)
		}
		out[i] = wasmtime.NewValType(k)
	}
	return out, nil
}

// MissingImports lists the functions a grammar imports that the runtime
// does not provide. LoadLanguage still loads such a grammar, but a parse
// that reaches one of them fails.
func (r *Runtime) MissingImports(grammarWasm []byte) ([]string, error) {
	secs, err := readSections(grammarWasm)
	if err != nil {
		return nil, err
	}
	ims, err := readImports(secs)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, im := range ims {
		if im.module == "env" && im.kind == kindFunc && !r.exports[im.name] {
			out = append(out, im.name)
		}
	}
	return out, nil
}
