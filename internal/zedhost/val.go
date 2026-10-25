package zedhost

/*
#include <stdlib.h>
#include "shim.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Kind is a component value's kind, as wasmtime numbers them.
type Kind uint8

const (
	KBool     Kind = C.WASMTIME_COMPONENT_BOOL
	KS8       Kind = C.WASMTIME_COMPONENT_S8
	KU8       Kind = C.WASMTIME_COMPONENT_U8
	KS16      Kind = C.WASMTIME_COMPONENT_S16
	KU16      Kind = C.WASMTIME_COMPONENT_U16
	KS32      Kind = C.WASMTIME_COMPONENT_S32
	KU32      Kind = C.WASMTIME_COMPONENT_U32
	KS64      Kind = C.WASMTIME_COMPONENT_S64
	KU64      Kind = C.WASMTIME_COMPONENT_U64
	KF32      Kind = C.WASMTIME_COMPONENT_F32
	KF64      Kind = C.WASMTIME_COMPONENT_F64
	KChar     Kind = C.WASMTIME_COMPONENT_CHAR
	KString   Kind = C.WASMTIME_COMPONENT_STRING
	KList     Kind = C.WASMTIME_COMPONENT_LIST
	KRecord   Kind = C.WASMTIME_COMPONENT_RECORD
	KTuple    Kind = C.WASMTIME_COMPONENT_TUPLE
	KVariant  Kind = C.WASMTIME_COMPONENT_VARIANT
	KEnum     Kind = C.WASMTIME_COMPONENT_ENUM
	KOption   Kind = C.WASMTIME_COMPONENT_OPTION
	KResult   Kind = C.WASMTIME_COMPONENT_RESULT
	KFlags    Kind = C.WASMTIME_COMPONENT_FLAGS
	KResource Kind = C.WASMTIME_COMPONENT_RESOURCE
)

// Val is a component model value on the Go side. Which fields mean
// something follows Kind:
//
//   - integers, bools and chars: Int (Bool for bools)
//   - floats: Float
//   - string, enum: Str
//   - list, tuple: Elems
//   - record: Elems, named by Names
//   - flags: Names
//   - variant: Str is the case, Payload its value or nil
//   - option: Payload, nil for none
//   - result: Ok, and Payload its value or nil
//   - resource: Resource, a host resource's rep and type to pass in, or
//     what came from the guest
type Val struct {
	Kind     Kind
	Bool     bool
	Int      int64
	Float    float64
	Str      string
	Elems    []Val
	Names    []string
	Payload  *Val
	Ok       bool
	Resource *Resource
}

// Resource is a resource handle. A host resource, made with
// BorrowHost, has Rep and Type; one read from a call keeps the handle
// wasmtime gave, valid while that call lasts.
type Resource struct {
	Rep, Type uint32
	any       *C.wasmtime_component_resource_any_t
}

// Constructors, for building arguments and results.

func Bool(b bool) Val             { return Val{Kind: KBool, Bool: b} }
func U8(v uint8) Val              { return Val{Kind: KU8, Int: int64(v)} }
func S32(v int32) Val             { return Val{Kind: KS32, Int: int64(v)} }
func U32(v uint32) Val            { return Val{Kind: KU32, Int: int64(v)} }
func U64(v uint64) Val            { return Val{Kind: KU64, Int: int64(v)} }
func String(s string) Val         { return Val{Kind: KString, Str: s} }
func Enum(c string) Val           { return Val{Kind: KEnum, Str: c} }
func List(e ...Val) Val           { return Val{Kind: KList, Elems: e} }
func Tuple(e ...Val) Val          { return Val{Kind: KTuple, Elems: e} }
func Some(v Val) Val              { return Val{Kind: KOption, Payload: &v} }
func None() Val                   { return Val{Kind: KOption} }
func Ok(v Val) Val                { return Val{Kind: KResult, Ok: true, Payload: &v} }
func OkUnit() Val                 { return Val{Kind: KResult, Ok: true} }
func Err(msg string) Val          { s := String(msg); return Val{Kind: KResult, Payload: &s} }
func Errf(f string, a ...any) Val { return Err(fmt.Sprintf(f, a...)) }

// BorrowHost is a host resource to pass to the guest where it takes a
// borrow. It is handed over owned, which a borrow may be lent from, and
// dropped once the call returns: a borrow made on the host's side would
// outlive the call and fail it.
func BorrowHost(rep, ty uint32) Val {
	return Val{Kind: KResource, Resource: &Resource{Rep: rep, Type: ty}}
}

// Field is a record field, for Record.
type Field struct {
	Name string
	Val  Val
}

func Record(fields ...Field) Val {
	v := Val{Kind: KRecord}
	for _, f := range fields {
		v.Names = append(v.Names, f.Name)
		v.Elems = append(v.Elems, f.Val)
	}
	return v
}

// Bytes is a list<u8>.
func Bytes(b []byte) Val {
	e := make([]Val, len(b))
	for i, c := range b {
		e[i] = U8(c)
	}
	return List(e...)
}

// Field returns a record's field called name.
func (v Val) Field(name string) (Val, bool) {
	for i, n := range v.Names {
		if n == name && i < len(v.Elems) {
			return v.Elems[i], true
		}
	}
	return Val{}, false
}

// Strings reads a list<string>.
func (v Val) Strings() []string {
	out := make([]string, len(v.Elems))
	for i, e := range v.Elems {
		out[i] = e.Str
	}
	return out
}

// ByteSlice reads a list<u8>.
func (v Val) ByteSlice() []byte {
	out := make([]byte, len(v.Elems))
	for i, e := range v.Elems {
		out[i] = byte(e.Int)
	}
	return out
}

// Pairs reads a list<tuple<string, string>>.
func (v Val) Pairs() [][2]string {
	out := make([][2]string, 0, len(v.Elems))
	for _, e := range v.Elems {
		if len(e.Elems) == 2 {
			out = append(out, [2]string{e.Elems[0].Str, e.Elems[1].Str})
		}
	}
	return out
}

// Pairs is a list<tuple<string, string>> of kv.
func Pairs(kv [][2]string) Val {
	e := make([]Val, len(kv))
	for i, p := range kv {
		e[i] = Tuple(String(p[0]), String(p[1]))
	}
	return List(e...)
}

// cval is a pointer to a value in C memory.
type cval = *C.wasmtime_component_val_t

func of[T any](v cval) *T { return (*T)(unsafe.Pointer(&v.of)) }

// at is the i'th of n values from p.
func at(p cval, i int) cval {
	return cval(unsafe.Add(unsafe.Pointer(p), uintptr(i)*unsafe.Sizeof(*p)))
}

func goName(n *C.wasm_name_t) string {
	if n.size == 0 {
		return ""
	}
	return C.GoStringN((*C.char)(unsafe.Pointer(n.data)), C.int(n.size))
}

func setName(n *C.wasm_name_t, s string) {
	if s == "" {
		C.wasm_byte_vec_new_empty(n)
		return
	}
	cs := C.CBytes([]byte(s))
	C.wasm_byte_vec_new(n, C.size_t(len(s)), (*C.wasm_byte_t)(cs))
	C.free(cs)
}

// read copies a value out of C memory.
func read(p cval) Val {
	v := Val{Kind: Kind(p.kind)}
	switch v.Kind {
	case KBool:
		v.Bool = *of[C.bool](p) == true
	case KS8:
		v.Int = int64(*of[C.int8_t](p))
	case KU8:
		v.Int = int64(*of[C.uint8_t](p))
	case KS16:
		v.Int = int64(*of[C.int16_t](p))
	case KU16:
		v.Int = int64(*of[C.uint16_t](p))
	case KS32:
		v.Int = int64(*of[C.int32_t](p))
	case KU32, KChar:
		v.Int = int64(*of[C.uint32_t](p))
	case KS64:
		v.Int = int64(*of[C.int64_t](p))
	case KU64:
		v.Int = int64(*of[C.uint64_t](p))
	case KF32:
		v.Float = float64(*of[C.float](p))
	case KF64:
		v.Float = float64(*of[C.double](p))
	case KString, KEnum:
		v.Str = goName(of[C.wasm_name_t](p))
	case KList, KTuple:
		l := of[C.wasmtime_component_vallist_t](p)
		for i := range int(l.size) {
			v.Elems = append(v.Elems, read(at(l.data, i)))
		}
	case KRecord:
		r := of[C.wasmtime_component_valrecord_t](p)
		for i := range int(r.size) {
			e := (*C.wasmtime_component_valrecord_entry_t)(unsafe.Add(unsafe.Pointer(r.data), uintptr(i)*unsafe.Sizeof(*r.data)))
			v.Names = append(v.Names, goName(&e.name))
			v.Elems = append(v.Elems, read(&e.val))
		}
	case KFlags:
		f := of[C.wasmtime_component_valflags_t](p)
		for i := range int(f.size) {
			n := (*C.wasm_name_t)(unsafe.Add(unsafe.Pointer(f.data), uintptr(i)*unsafe.Sizeof(*f.data)))
			v.Names = append(v.Names, goName(n))
		}
	case KVariant:
		vr := of[C.wasmtime_component_valvariant_t](p)
		v.Str = goName(&vr.discriminant)
		if vr.val != nil {
			pl := read(vr.val)
			v.Payload = &pl
		}
	case KOption:
		if o := *of[cval](p); o != nil {
			pl := read(o)
			v.Payload = &pl
		}
	case KResult:
		r := of[C.wasmtime_component_valresult_t](p)
		v.Ok = r.is_ok == true
		if r.val != nil {
			pl := read(r.val)
			v.Payload = &pl
		}
	case KResource:
		v.Resource = &Resource{any: *of[*C.wasmtime_component_resource_any_t](p)}
	}
	return v
}

// write builds v in place at p, which holds nothing that needs freeing.
// Everything is allocated through wasmtime's own constructors, so that
// wasmtime can free it once the value is handed over.
func write(ctx *C.wasmtime_context_t, p cval, v Val) error {
	p.kind = C.wasmtime_component_valkind_t(v.Kind)
	switch v.Kind {
	case KBool:
		*of[C.bool](p) = C.bool(v.Bool)
	case KS8:
		*of[C.int8_t](p) = C.int8_t(v.Int)
	case KU8:
		*of[C.uint8_t](p) = C.uint8_t(v.Int)
	case KS16:
		*of[C.int16_t](p) = C.int16_t(v.Int)
	case KU16:
		*of[C.uint16_t](p) = C.uint16_t(v.Int)
	case KS32:
		*of[C.int32_t](p) = C.int32_t(v.Int)
	case KU32, KChar:
		*of[C.uint32_t](p) = C.uint32_t(v.Int)
	case KS64:
		*of[C.int64_t](p) = C.int64_t(v.Int)
	case KU64:
		*of[C.uint64_t](p) = C.uint64_t(v.Int)
	case KF32:
		*of[C.float](p) = C.float(v.Float)
	case KF64:
		*of[C.double](p) = C.double(v.Float)
	case KString, KEnum:
		setName(of[C.wasm_name_t](p), v.Str)
	case KList, KTuple:
		l := of[C.wasmtime_component_vallist_t](p)
		C.wasmtime_component_vallist_new_uninit(l, C.size_t(len(v.Elems)))
		for i, e := range v.Elems {
			if err := write(ctx, at(l.data, i), e); err != nil {
				return err
			}
		}
	case KRecord:
		r := of[C.wasmtime_component_valrecord_t](p)
		C.wasmtime_component_valrecord_new_uninit(r, C.size_t(len(v.Elems)))
		for i, e := range v.Elems {
			ent := (*C.wasmtime_component_valrecord_entry_t)(unsafe.Add(unsafe.Pointer(r.data), uintptr(i)*unsafe.Sizeof(*r.data)))
			setName(&ent.name, v.Names[i])
			if err := write(ctx, &ent.val, e); err != nil {
				return err
			}
		}
	case KFlags:
		f := of[C.wasmtime_component_valflags_t](p)
		C.wasmtime_component_valflags_new_uninit(f, C.size_t(len(v.Names)))
		for i, n := range v.Names {
			setName((*C.wasm_name_t)(unsafe.Add(unsafe.Pointer(f.data), uintptr(i)*unsafe.Sizeof(*f.data))), n)
		}
	case KVariant:
		vr := of[C.wasmtime_component_valvariant_t](p)
		setName(&vr.discriminant, v.Str)
		vr.val = nil
		if v.Payload != nil {
			vr.val = C.sb_val_box()
			return write(ctx, vr.val, *v.Payload)
		}
	case KOption:
		o := of[cval](p)
		*o = nil
		if v.Payload != nil {
			*o = C.sb_val_box()
			return write(ctx, *o, *v.Payload)
		}
	case KResult:
		r := of[C.wasmtime_component_valresult_t](p)
		r.is_ok = C.bool(v.Ok)
		r.val = nil
		if v.Payload != nil {
			r.val = C.sb_val_box()
			return write(ctx, r.val, *v.Payload)
		}
	case KResource:
		res := v.Resource
		if res == nil {
			return fmt.Errorf("zedhost: a resource value without a resource")
		}
		any := res.any
		if any == nil {
			host := C.wasmtime_component_resource_host_new(true, C.uint32_t(res.Rep), C.uint32_t(res.Type))
			err := C.wasmtime_component_resource_host_to_any(ctx, host, &any)
			C.wasmtime_component_resource_host_delete(host)
			if err != nil {
				p.kind = C.WASMTIME_COMPONENT_BOOL
				return takeError(err)
			}
		} else {
			any = C.wasmtime_component_resource_any_clone(any)
		}
		*of[*C.wasmtime_component_resource_any_t](p) = any
	default:
		p.kind = C.WASMTIME_COMPONENT_BOOL
		*of[C.bool](p) = false
		return fmt.Errorf("zedhost: cannot write a value of kind %d", v.Kind)
	}
	return nil
}

// hostRep is the rep of a host resource passed in by the guest.
func hostRep(ctx *C.wasmtime_context_t, r *Resource, ty uint32) (uint32, error) {
	if r == nil || r.any == nil {
		return 0, fmt.Errorf("zedhost: not a resource")
	}
	var host *C.wasmtime_component_resource_host_t
	if err := C.wasmtime_component_resource_any_to_host(ctx, r.any, &host); err != nil {
		return 0, takeError(err)
	}
	defer C.wasmtime_component_resource_host_delete(host)
	if uint32(C.wasmtime_component_resource_host_type(host)) != ty {
		return 0, fmt.Errorf("zedhost: a resource of another type")
	}
	return uint32(C.wasmtime_component_resource_host_rep(host)), nil
}

// takeError turns a wasmtime error into a Go one and frees it.
func takeError(err *C.wasmtime_error_t) error {
	if err == nil {
		return nil
	}
	var msg C.wasm_name_t
	C.wasmtime_error_message(err, &msg)
	s := goName(&msg)
	C.wasm_byte_vec_delete(&msg)
	C.wasmtime_error_delete(err)
	return fmt.Errorf("%s", s)
}

// newError is a wasmtime error, a trap, saying msg.
func newError(msg string) *C.wasmtime_error_t {
	cs := C.CString(msg)
	defer C.free(unsafe.Pointer(cs))
	return C.wasmtime_error_new(cs)
}
