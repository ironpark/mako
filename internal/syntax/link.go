package syntax

import (
	"context"
	"fmt"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

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
	ctx := r.ctx
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

	r.nlang++
	id := fmt.Sprintf("%d_%s", r.nlang, name)
	glueName := "sanbo_glue_" + id
	stubName := "sanbo_stub_" + id

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
			if r.mod.ExportedFunction(im.name) != nil {
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

	if _, err := r.wr.InstantiateWithConfig(ctx, globalModule(globals),
		moduleConfig(glueName)); err != nil {
		return nil, fmt.Errorf("glue module: %w", err)
	}
	if len(stubs) > 0 {
		if err := r.stubModule(ctx, stubName, stubs, types); err != nil {
			return nil, err
		}
	}
	mod, err := r.wr.InstantiateWithConfig(ctx, writeSections(secs),
		moduleConfig("sanbo_lang_"+id))
	if err != nil {
		return nil, err
	}

	for _, init := range []string{"__wasm_apply_data_relocs", "_initialize", "__wasm_call_ctors"} {
		f := mod.ExportedFunction(init)
		if f == nil {
			continue
		}
		if _, err := f.Call(ctx); err != nil {
			r.err = fmt.Errorf("syntax: %s trapped in %s: %w", name, init, err)
			return nil, r.err
		}
		if init == "_initialize" {
			break // it runs the constructors itself
		}
	}
	langFn := mod.ExportedFunction("tree_sitter_" + name)
	if langFn == nil {
		return nil, fmt.Errorf("grammar does not export tree_sitter_%s", name)
	}
	res, err := langFn.Call(ctx)
	if err != nil {
		r.err = fmt.Errorf("syntax: tree_sitter_%s trapped: %w", name, err)
		return nil, r.err
	}
	ptr := uint32(res[0])

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

// stubModule satisfies function imports the runtime does not provide with
// host functions that fail when called, so grammars whose scanners
// reference rare libc functions still load.
func (r *Runtime) stubModule(ctx context.Context, name string, stubs []wasmImport, types []funcType) error {
	b := r.wr.NewHostModuleBuilder(name)
	for _, s := range stubs {
		if int(s.typeIdx) >= len(types) {
			return errWasm
		}
		t := types[s.typeIdx]
		fn := s.name
		b.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(func(context.Context, api.Module, []uint64) {
				panic(fmt.Sprintf("grammar called unsupported function %s", fn))
			}), valueTypes(t.params), valueTypes(t.results)).
			Export(fn)
	}
	_, err := b.Instantiate(ctx)
	return err
}

func valueTypes(b []byte) []api.ValueType {
	out := make([]api.ValueType, len(b))
	copy(out, b)
	return out
}

// moduleConfig names a module and stops wazero from running _start.
func moduleConfig(name string) wazero.ModuleConfig {
	return wazero.NewModuleConfig().WithName(name).WithStartFunctions()
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
		if im.module == "env" && im.kind == kindFunc && r.mod.ExportedFunction(im.name) == nil {
			out = append(out, im.name)
		}
	}
	return out, nil
}
