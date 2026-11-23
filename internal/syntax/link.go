package syntax

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"sync"

	wasmtime "github.com/ironpark/wasmtime-pure-go"
)

// Grammars import their glue globals and stub functions from these
// linker modules. Each LoadLanguage redefines them before instantiating.
const (
	glueModule      = "mako_glue"
	stubModule      = "mako_stub"
	maxGrammarCache = 64
)

type cachedGrammar struct {
	key     [sha256.Size]byte
	mod     *wasmtime.Module
	refs    int // 1 held by cache while present + 1 per active caller
	evicted bool
	elem    *list.Element
}

// grammarModuleCache holds compiled grammar modules by the SHA-256 of the
// grammar wasm with bounded LRU eviction and reference-counted module closing.
// The import rewrite only depends on the grammar and the (embedded) runtime, so
// a compiled module is reusable by every Runtime.
type grammarModuleCache struct {
	mu      sync.Mutex
	cap     int
	lru     *list.List
	entries map[[sha256.Size]byte]*cachedGrammar
}

func newGrammarModuleCache(cap int) *grammarModuleCache {
	return &grammarModuleCache{
		cap:     cap,
		lru:     list.New(),
		entries: map[[sha256.Size]byte]*cachedGrammar{},
	}
}

var grammarCache = newGrammarModuleCache(maxGrammarCache)

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

	// Validate function table capacity and imports before allocating Wasm resources.
	talign := uint64(1) << dl.tableAlign
	tableBase64 := (uint64(r.table) + talign - 1) / talign * talign
	if tableBase64 > uint64(r.tblEnd) || uint64(dl.tableSize) > uint64(r.tblEnd)-tableBase64 {
		return nil, fmt.Errorf("function table full (%d slots reserved)", tableReserve)
	}
	tableBase := uint32(tableBase64)

	have := map[string]bool{
		"__stack_pointer": true,
		"__memory_base":   true,
		"__table_base":    true,
	}
	var gotImports []struct {
		name    string
		val     uint32
		mutable bool
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
			im.module = glueModule
		case im.module == "env" && im.kind == kindFunc:
			if r.exports[im.name] {
				im.module = runtimeModule
			} else {
				im.module = stubModule
				stubs = append(stubs, *im)
			}
		case im.module == "GOT.func" && im.kind == kindGlobal:
			idx, ok := r.libc[im.name]
			if !ok {
				return nil, fmt.Errorf("unsupported import GOT.func.%s", im.name)
			}
			gn := "GOT.func." + im.name
			if !have[gn] {
				gotImports = append(gotImports, struct {
					name    string
					val     uint32
					mutable bool
				}{name: gn, val: idx, mutable: im.mutable})
				have[gn] = true
			}
			im.module, im.name = glueModule, gn
		default:
			return nil, fmt.Errorf("unsupported import %s.%s", im.module, im.name)
		}
	}
	replaceSection(secs, secImport, encodeImports(ims))

	prevTable := r.table
	var memBase, stack uint32
	ok := false
	defer func() {
		if ok {
			return
		}
		r.table = prevTable
		if r.usable() == nil {
			if stack != 0 {
				r.free(stack)
			}
			if memBase != 0 {
				r.free(memBase)
			}
		}
	}()

	// Static data block.
	memBase, err = r.call1("shim_alloc_zeroed", uint64(1)<<dl.memAlign, uint64(dl.memSize))
	if err != nil {
		return nil, err
	}
	if memBase == 0 {
		return nil, fmt.Errorf("out of wasm memory")
	}
	// Reserve function table slots.
	r.table = tableBase + dl.tableSize
	// Stack, growing down from the top.
	stack, err = r.malloc(grammarStack)
	if err != nil {
		return nil, err
	}
	sp := (stack + grammarStack) &^ 15

	glueName, stubName := glueModule, stubModule

	globals := make([]globalDef, 0, 3+len(gotImports))
	globals = append(globals,
		globalDef{name: "__stack_pointer", value: sp, mutable: true},
		globalDef{name: "__memory_base", value: memBase},
		globalDef{name: "__table_base", value: tableBase},
	)
	for _, gi := range gotImports {
		globals = append(globals, globalDef{name: gi.name, value: gi.val, mutable: gi.mutable})
	}

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
	modEntry, err := grammarModule(bin, secs)
	if err != nil {
		return nil, err
	}
	defer grammarCache.release(modEntry)
	inst, err := r.linker.Instantiate(r.store, modEntry.mod)
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
	ok = true
	return &Language{rt: r, name: name, ptr: ptr, parser: parser, abi: abi}, nil
}

// grammarModule compiles the rewritten grammar, or returns the copy
// compiled earlier from the same grammar wasm.
func grammarModule(orig []byte, secs []section) (*cachedGrammar, error) {
	return grammarCache.acquire(orig, func() (*wasmtime.Module, error) {
		return wasmtime.NewModule(engine, writeSections(secs))
	})
}

func (c *grammarModuleCache) acquire(orig []byte, compile func() (*wasmtime.Module, error)) (*cachedGrammar, error) {
	key := sha256.Sum256(orig)
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.lru.MoveToFront(e.elem)
		e.refs++
		c.mu.Unlock()
		return e, nil
	}
	c.mu.Unlock()

	m, err := compile()
	if err != nil {
		return nil, err
	}

	var toClose []*wasmtime.Module
	c.mu.Lock()
	if existing, ok := c.entries[key]; ok {
		c.lru.MoveToFront(existing.elem)
		existing.refs++
		c.mu.Unlock()
		m.Close()
		return existing, nil
	}
	e := &cachedGrammar{key: key, mod: m, refs: 2}
	e.elem = c.lru.PushFront(e)
	c.entries[key] = e
	for c.lru.Len() > c.cap {
		back := c.lru.Back()
		if back == nil {
			break
		}
		victim := c.lru.Remove(back).(*cachedGrammar)
		delete(c.entries, victim.key)
		victim.evicted = true
		victim.refs--
		if victim.refs == 0 && victim.mod != nil {
			toClose = append(toClose, victim.mod)
		}
	}
	c.mu.Unlock()

	for _, cm := range toClose {
		cm.Close()
	}
	return e, nil
}

func (c *grammarModuleCache) release(e *cachedGrammar) {
	if e == nil {
		return
	}
	c.mu.Lock()
	e.refs--
	closeMod := e.refs == 0 && e.evicted && e.mod != nil
	c.mu.Unlock()
	if closeMod {
		e.mod.Close()
	}
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
