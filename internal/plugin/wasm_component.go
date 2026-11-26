package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	wasmtime "github.com/ironpark/wasmtime-pure-go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/ironpark/mako/internal/plugin/apigen"
	// Ensure mako.plugin.v1 descriptors are registered in protoregistry.GlobalFiles.
	_ "github.com/ironpark/mako/internal/plugin/hostapi"
)

// wasmComponentRuntime runs a WebAssembly Component Model plugin targeting the
// mako:plugin@1.0.0 WIT world (internal/plugin/hostapi/mako.wit).
//
// Host imports linked automatically:
//   - WASI Preview 2 (sandboxed empty WasiConfig: no preopens, no env, discard stdio).
//   - Every Host API WIT interface generated in mako:plugin@1.0.0 (log, settings,
//     storage, sessions, http, commands, db, datafs, messages, files, secrets,
//     layout, git, forge, decision, agents), bridged via protoreflect to the
//     exact permission-checked Host API dispatcher.
//   - Convenience interface mako:plugin/host@1.0.0 with:
//     call(method: string, req-json: string) -> result<string, string>
//     block-reload(id: string, scope: string)
//
// Guest exports supported (top-level or under interface "mako:plugin/guest@1.0.0"):
//   - invoke(method: string, args-json: string) -> result<string, string>
//   - OR individual lifecycle exports: activate, load, render, event, command,
//     call-tool, session-event, db-sync-event, git-done, save, restore, evict,
//     refresh, take-reloads.
type wasmComponentRuntime struct {
	engine  *wasmtime.Engine
	store   *wasmtime.Store
	linker  *wasmtime.ComponentLinker
	comp    *wasmtime.Component
	inst    *wasmtime.ComponentInstance
	bridge  bridgeFunc
	callCtx context.Context
	reloads []wasmReload
	closed  bool
}

func startWasmComponentRuntime(ctx context.Context, engine *wasmtime.Engine, v *version, lim Limits, cacheDir string, bridge bridgeFunc, resume func()) (*wasmComponentRuntime, error) {
	comp, err := compileWasmComponent(engine, cacheDir, v.wasm)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", v.man.Entry, err)
	}
	w := &wasmComponentRuntime{
		engine:  engine,
		comp:    comp,
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
	w.linker = wasmtime.NewComponentLinker(engine)
	if err := w.linker.AddWasip2(); err != nil {
		return nil, fmt.Errorf("wasm wasip2: %w", err)
	}
	if err := w.defineHostInterfaces(); err != nil {
		return nil, fmt.Errorf("wasm component imports: %w", err)
	}
	// Unknown optional imports trap safely if called rather than blocking load.
	_ = w.linker.DefineUnknownImportsAsTraps(comp)

	resume()
	stopAfter := armWasmBudget(ctx, engine, w.store)
	inst, err := w.linker.Instantiate(w.store, comp)
	stopAfter()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", v.man.Entry, classifyWasmErr(ctx, err))
	}
	w.inst = inst

	if w.findExport("invoke") == nil && w.findExport("render") == nil && w.findExport("activate") == nil && w.findExport("command") == nil {
		return nil, fmt.Errorf("%s: wasm component exports none of invoke, render, activate, or command", v.man.Entry)
	}
	ok = true
	return w, nil
}

func (w *wasmComponentRuntime) defineHostInterfaces() error {
	root := w.linker.Root()
	// 1. Convenience generic host bridge: mako:plugin/host@1.0.0.
	hostInst, err := root.AddInstance("mako:plugin/host@1.0.0")
	if err != nil {
		return err
	}
	if err := hostInst.AddFunc("call", func(_ *wasmtime.ComponentCaller, args []wasmtime.ComponentVal) ([]wasmtime.ComponentVal, error) {
		if len(args) < 2 {
			return []wasmtime.ComponentVal{compErr("call requires method and req-json")}, nil
		}
		resp, err := w.callBridge(args[0].Str, []byte(args[1].Str))
		if err != nil {
			return []wasmtime.ComponentVal{compErr(err.Error())}, nil
		}
		return []wasmtime.ComponentVal{compOk(compStr(string(resp)))}, nil
	}); err != nil {
		hostInst.Close()
		return err
	}
	if err := hostInst.AddFunc("block-reload", func(_ *wasmtime.ComponentCaller, args []wasmtime.ComponentVal) ([]wasmtime.ComponentVal, error) {
		if len(args) >= 2 && args[0].Str != "" && len(w.reloads) < maxWasmReloads {
			w.reloads = append(w.reloads, wasmReload{ID: args[0].Str, Scope: args[1].Str})
		}
		return nil, nil
	}); err != nil {
		hostInst.Close()
		return err
	}
	hostInst.Close()

	// 2. Typed WIT interfaces from mako.plugin.v1 protobuf descriptors (mako.wit).
	var linkErr error
	protoregistry.GlobalFiles.RangeFilesByPackage(protoreflect.FullName(apigen.Package), func(fd protoreflect.FileDescriptor) bool {
		svcs := fd.Services()
		for i := range svcs.Len() {
			sd := svcs.Get(i)
			svcName := string(sd.Name())
			short := strings.TrimSuffix(svcName, "Service")
			if short == svcName || short == "" {
				continue
			}
			witIface := "mako:plugin/" + camelToKebab(short) + "@1.0.0"
			jsGroup := lowerFirst(short)
			inst, err := root.AddInstance(witIface)
			if err != nil {
				linkErr = err
				return false
			}
			methods := sd.Methods()
			for j := range methods.Len() {
				md := methods.Get(j)
				methodName := string(md.Name())
				witFn := camelToKebab(methodName)
				hostMethod := jsGroup + "." + lowerFirst(methodName)
				inDesc := md.Input()
				outDesc := md.Output()
				if err := inst.AddFunc(witFn, w.makeTypedHostFunc(hostMethod, inDesc, outDesc)); err != nil {
					inst.Close()
					linkErr = err
					return false
				}
			}
			inst.Close()
		}
		return true
	})
	return linkErr
}

func (w *wasmComponentRuntime) callBridge(method string, req []byte) ([]byte, error) {
	ctx := w.callCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if b, ok := ctx.(*budget); ok {
		b.pause()
		defer b.resume()
	}
	return w.bridge(ctx, method, req)
}

func (w *wasmComponentRuntime) makeTypedHostFunc(hostMethod string, inDesc, outDesc protoreflect.MessageDescriptor) wasmtime.ComponentHostFunc {
	return func(_ *wasmtime.ComponentCaller, args []wasmtime.ComponentVal) ([]wasmtime.ComponentVal, error) {
		reqJSON := []byte("{}")
		if inDesc.Fields().Len() > 0 && len(args) > 0 {
			msg := dynamicpb.NewMessage(inDesc)
			if err := compValToProto(args[0], msg); err != nil {
				return []wasmtime.ComponentVal{compErr(fmt.Sprintf("%s: %v", hostMethod, err))}, nil
			}
			b, err := protojson.Marshal(msg)
			if err != nil {
				return []wasmtime.ComponentVal{compErr(err.Error())}, nil
			}
			reqJSON = b
		}
		respJSON, err := w.callBridge(hostMethod, reqJSON)
		if err != nil {
			return []wasmtime.ComponentVal{compErr(err.Error())}, nil
		}
		if outDesc.Fields().Len() == 0 {
			return []wasmtime.ComponentVal{compOkUnit()}, nil
		}
		outMsg := dynamicpb.NewMessage(outDesc)
		if len(respJSON) > 0 && string(respJSON) != "null" {
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(respJSON, outMsg); err != nil {
				return []wasmtime.ComponentVal{compErr(err.Error())}, nil
			}
		}
		return []wasmtime.ComponentVal{compOk(protoToCompVal(outMsg))}, nil
	}
}

func (w *wasmComponentRuntime) findExport(name string) *wasmtime.ComponentFunc {
	// Prefer interface export mako:plugin/guest@1.0.0/<name>, fallback to top-level <name>.
	if ifaceIdx := w.inst.GetExportIndex(w.store, nil, "mako:plugin/guest@1.0.0"); ifaceIdx != nil {
		defer ifaceIdx.Close()
		if fnIdx := w.inst.GetExportIndex(w.store, ifaceIdx, name); fnIdx != nil {
			defer fnIdx.Close()
			if fn := w.inst.GetFunc(w.store, fnIdx); fn != nil {
				return fn
			}
		}
	}
	idx := w.inst.GetExportIndex(w.store, nil, name)
	if idx == nil {
		return nil
	}
	defer idx.Close()
	return w.inst.GetFunc(w.store, idx)
}

func (w *wasmComponentRuntime) invoke(ctx context.Context, method string, args ...any) (_ string, err error) {
	defer trapped(&err)
	if w.closed {
		return "", errors.New("the wasm component runtime is closed")
	}
	w.callCtx = ctx
	defer func() { w.callCtx = nil }()

	stopAfter := armWasmBudget(ctx, w.engine, w.store)
	defer stopAfter()

	if method == "takeReloads" {
		hostReloads := w.reloads
		w.reloads = nil
		var guestOut string
		if inv := w.findExport("invoke"); inv != nil {
			guestOut, err = w.callInvokeExport(ctx, inv, method, args)
			if err != nil {
				return "", err
			}
		} else if fn := w.findExport("take-reloads"); fn != nil {
			res, callErr := fn.Call(w.store, nil, 1)
			if callErr != nil {
				return "", classifyWasmErr(ctx, callErr)
			}
			if len(res) == 1 && res[0].Kind == wasmtime.ComponentValKindString {
				guestOut = res[0].Str
			}
		}
		return mergeWasmReloads(hostReloads, guestOut), nil
	}

	// Single dispatcher export: invoke(method: string, args-json: string) -> result<string, string>.
	if inv := w.findExport("invoke"); inv != nil {
		out, err := w.callInvokeExport(ctx, inv, method, args)
		if err != nil {
			return "", err
		}
		return defaultWasmReturn(method, out), nil
	}

	// Individual named component exports.
	witName := camelToKebab(method)
	fn := w.findExport(witName)
	if fn == nil {
		switch method {
		case "activate", "load", "restore", "evict", "sessionEvent", "dbSyncEvent", "gitDone":
			return "", nil
		case "refresh", "save", "blocks":
			return defaultWasmReturn(method, ""), nil
		default:
			return "", fmt.Errorf("wasm component does not export %q", witName)
		}
	}
	cArgs := make([]wasmtime.ComponentVal, len(args))
	for i, a := range args {
		cArgs[i] = compStr(fmt.Sprint(a))
	}
	nres := 1
	if method == "restore" || method == "evict" {
		nres = 0
	}
	res, callErr := fn.Call(w.store, cArgs, nres)
	if callErr != nil {
		return "", classifyWasmErr(ctx, callErr)
	}
	if len(res) == 0 {
		return defaultWasmReturn(method, ""), nil
	}
	return unwrapComponentReturn(method, res[0])
}

func (w *wasmComponentRuntime) callInvokeExport(ctx context.Context, fn *wasmtime.ComponentFunc, method string, args []any) (string, error) {
	if args == nil {
		args = []any{}
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	res, callErr := fn.Call(w.store, []wasmtime.ComponentVal{compStr(method), compStr(string(argsJSON))}, 1)
	if callErr != nil {
		return "", classifyWasmErr(ctx, callErr)
	}
	if len(res) == 0 {
		return "", nil
	}
	return unwrapComponentReturn(method, res[0])
}

func unwrapComponentReturn(method string, v wasmtime.ComponentVal) (string, error) {
	switch v.Kind {
	case wasmtime.ComponentValKindResult:
		if !v.Ok {
			msg := "plugin error"
			if v.Payload != nil && v.Payload.Str != "" {
				msg = v.Payload.Str
			}
			return "", errors.New(msg)
		}
		if v.Payload == nil {
			return defaultWasmReturn(method, ""), nil
		}
		return unwrapComponentReturn(method, *v.Payload)
	case wasmtime.ComponentValKindOption:
		if v.Payload == nil {
			return defaultWasmReturn(method, ""), nil
		}
		return unwrapComponentReturn(method, *v.Payload)
	case wasmtime.ComponentValKindString:
		return defaultWasmReturn(method, v.Str), nil
	case wasmtime.ComponentValKindU32, wasmtime.ComponentValKindS32, wasmtime.ComponentValKindU64, wasmtime.ComponentValKindS64:
		return fmt.Sprint(v.Int), nil
	case wasmtime.ComponentValKindF32, wasmtime.ComponentValKindF64:
		return fmt.Sprint(v.Float), nil
	case wasmtime.ComponentValKindBool:
		return fmt.Sprint(v.Bool), nil
	default:
		return defaultWasmReturn(method, ""), nil
	}
}

func (w *wasmComponentRuntime) close() {
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
	if w.comp != nil {
		w.comp.Close()
		w.comp = nil
	}
}

// ComponentVal <-> protobuf reflection conversion (for mako.wit Host API).

func compStr(s string) wasmtime.ComponentVal {
	return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindString, Str: s}
}

func compOk(v wasmtime.ComponentVal) wasmtime.ComponentVal {
	return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindResult, Ok: true, Payload: &v}
}

func compOkUnit() wasmtime.ComponentVal {
	return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindResult, Ok: true}
}

func compErr(msg string) wasmtime.ComponentVal {
	s := compStr(msg)
	return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindResult, Ok: false, Payload: &s}
}

func compValToProto(v wasmtime.ComponentVal, msg protoreflect.Message) error {
	if v.Kind != wasmtime.ComponentValKindRecord {
		return fmt.Errorf("expected WIT record, got kind %d", v.Kind)
	}
	fieldsByKebab := make(map[string]protoreflect.FieldDescriptor, msg.Descriptor().Fields().Len())
	fds := msg.Descriptor().Fields()
	for i := range fds.Len() {
		fd := fds.Get(i)
		fieldsByKebab[strings.ReplaceAll(string(fd.Name()), "_", "-")] = fd
	}
	for i, name := range v.Names {
		if i >= len(v.Elems) {
			break
		}
		fd, ok := fieldsByKebab[name]
		if !ok {
			continue
		}
		if err := setProtoFieldFromCompVal(msg, fd, v.Elems[i]); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func setProtoFieldFromCompVal(msg protoreflect.Message, fd protoreflect.FieldDescriptor, val wasmtime.ComponentVal) error {
	if val.Kind == wasmtime.ComponentValKindOption {
		if val.Payload == nil {
			return nil
		}
		val = *val.Payload
	}
	if fd.IsMap() {
		mp := msg.Mutable(fd).Map()
		valDesc := fd.MapValue()
		for _, elem := range val.Elems {
			if len(elem.Elems) != 2 {
				continue
			}
			k := protoreflect.ValueOfString(elem.Elems[0].Str).MapKey()
			mv, err := compValToScalarOrMsg(valDesc, elem.Elems[1])
			if err != nil {
				return err
			}
			mp.Set(k, mv)
		}
		return nil
	}
	if fd.IsList() {
		list := msg.Mutable(fd).List()
		for _, elem := range val.Elems {
			lv, err := compValToScalarOrMsg(fd, elem)
			if err != nil {
				return err
			}
			list.Append(lv)
		}
		return nil
	}
	pv, err := compValToScalarOrMsg(fd, val)
	if err != nil {
		return err
	}
	msg.Set(fd, pv)
	return nil
}

func compValToScalarOrMsg(fd protoreflect.FieldDescriptor, v wasmtime.ComponentVal) (protoreflect.Value, error) {
	if v.Kind == wasmtime.ComponentValKindOption {
		if v.Payload == nil {
			return protoreflect.Value{}, nil
		}
		v = *v.Payload
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(v.Str), nil
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(v.Bool), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(int32(v.Int)), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(uint32(v.Int)), nil
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(float32(v.Float)), nil
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(v.Float), nil
	case protoreflect.BytesKind:
		b := make([]byte, len(v.Elems))
		for i, e := range v.Elems {
			b[i] = byte(e.Int)
		}
		return protoreflect.ValueOfBytes(b), nil
	case protoreflect.EnumKind:
		ed := fd.Enum()
		want := strings.ToUpper(strings.ReplaceAll(v.Str, "-", "_"))
		prefix := strings.ToUpper(strings.ReplaceAll(camelToKebab(string(ed.Name())), "-", "_")) + "_"
		for i := range ed.Values().Len() {
			ev := ed.Values().Get(i)
			name := string(ev.Name())
			if name == want || strings.TrimPrefix(name, prefix) == want {
				return protoreflect.ValueOfEnum(ev.Number()), nil
			}
		}
		return protoreflect.ValueOfEnum(0), nil
	case protoreflect.MessageKind:
		sub := dynamicpb.NewMessage(fd.Message())
		if err := compValToProto(v, sub); err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfMessage(sub), nil
	default:
		return protoreflect.Value{}, fmt.Errorf("unsupported proto field kind %v", fd.Kind())
	}
}

func protoToCompVal(msg protoreflect.Message) wasmtime.ComponentVal {
	fds := msg.Descriptor().Fields()
	rec := wasmtime.ComponentVal{
		Kind:  wasmtime.ComponentValKindRecord,
		Names: make([]string, 0, fds.Len()),
		Elems: make([]wasmtime.ComponentVal, 0, fds.Len()),
	}
	for i := range fds.Len() {
		fd := fds.Get(i)
		kebabName := strings.ReplaceAll(string(fd.Name()), "_", "-")
		rec.Names = append(rec.Names, kebabName)
		rec.Elems = append(rec.Elems, protoFieldToCompVal(msg, fd))
	}
	return rec
}

func protoFieldToCompVal(msg protoreflect.Message, fd protoreflect.FieldDescriptor) wasmtime.ComponentVal {
	if fd.IsMap() {
		mp := msg.Get(fd).Map()
		var elems []wasmtime.ComponentVal
		mp.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			elems = append(elems, wasmtime.ComponentVal{
				Kind:  wasmtime.ComponentValKindTuple,
				Elems: []wasmtime.ComponentVal{compStr(k.String()), protoValueToCompVal(fd.MapValue(), v)},
			})
			return true
		})
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindList, Elems: elems}
	}
	if fd.IsList() {
		list := msg.Get(fd).List()
		elems := make([]wasmtime.ComponentVal, list.Len())
		for i := range list.Len() {
			elems[i] = protoValueToCompVal(fd, list.Get(i))
		}
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindList, Elems: elems}
	}
	// Proto3 optional scalars and message fields are option<T> in mako.wit.
	isOpt := fd.HasOptionalKeyword() || fd.Kind() == protoreflect.MessageKind
	if isOpt && !msg.Has(fd) {
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindOption}
	}
	val := protoValueToCompVal(fd, msg.Get(fd))
	if isOpt {
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindOption, Payload: &val}
	}
	return val
}

func protoValueToCompVal(fd protoreflect.FieldDescriptor, v protoreflect.Value) wasmtime.ComponentVal {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return compStr(v.String())
	case protoreflect.BoolKind:
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindBool, Bool: v.Bool()}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindS32, Int: v.Int()}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindU32, Int: int64(v.Uint())}
	case protoreflect.FloatKind:
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindF32, Float: v.Float()}
	case protoreflect.DoubleKind:
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindF64, Float: v.Float()}
	case protoreflect.BytesKind:
		bs := v.Bytes()
		elems := make([]wasmtime.ComponentVal, len(bs))
		for i, b := range bs {
			elems[i] = wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindU8, Int: int64(b)}
		}
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindList, Elems: elems}
	case protoreflect.EnumKind:
		ed := fd.Enum()
		ev := ed.Values().ByNumber(v.Enum())
		if ev == nil {
			ev = ed.Values().Get(0)
		}
		prefix := strings.ToUpper(strings.ReplaceAll(camelToKebab(string(ed.Name())), "-", "_")) + "_"
		caseName := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(string(ev.Name()), prefix), "_", "-"))
		return wasmtime.ComponentVal{Kind: wasmtime.ComponentValKindEnum, Str: caseName}
	case protoreflect.MessageKind:
		return protoToCompVal(v.Message())
	default:
		return compStr("")
	}
}

// camelToKebab converts UpperCamelCase / lowerCamelCase to WIT kebab-case,
// matching internal/plugin/apigen's naming.
func camelToKebab(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if unicode.IsUpper(r) {
			if i > 0 && (unicode.IsLower(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
		} else if r == '_' {
			b.WriteByte('-')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func lowerFirst(s string) string {
	if s == "" {
		return ""
	}
	rs := []rune(s)
	rs[0] = unicode.ToLower(rs[0])
	return string(rs)
}
