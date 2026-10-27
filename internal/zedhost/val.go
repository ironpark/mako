package zedhost

import (
	"fmt"

	wasmtime "github.com/ironpark/wasmtime-pure-go"
)

// Val is a component model value on the Go side; wasmtime.ComponentVal
// says which fields mean something for which kind.
type Val = wasmtime.ComponentVal

// The kinds of values the host reads and writes.
const (
	KBool     = wasmtime.ComponentValKindBool
	KU8       = wasmtime.ComponentValKindU8
	KS32      = wasmtime.ComponentValKindS32
	KU32      = wasmtime.ComponentValKindU32
	KU64      = wasmtime.ComponentValKindU64
	KString   = wasmtime.ComponentValKindString
	KList     = wasmtime.ComponentValKindList
	KRecord   = wasmtime.ComponentValKindRecord
	KTuple    = wasmtime.ComponentValKindTuple
	KEnum     = wasmtime.ComponentValKindEnum
	KOption   = wasmtime.ComponentValKindOption
	KResult   = wasmtime.ComponentValKindResult
	KResource = wasmtime.ComponentValKindResource
)

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
	return Val{Kind: KResource, Resource: &wasmtime.ComponentResource{Rep: rep, Type: ty}}
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

// field returns a record's field called name.
func field(v Val, name string) (Val, bool) {
	for i, n := range v.Names {
		if n == name && i < len(v.Elems) {
			return v.Elems[i], true
		}
	}
	return Val{}, false
}

// strs reads a list<string>.
func strs(v Val) []string {
	out := make([]string, len(v.Elems))
	for i, e := range v.Elems {
		out[i] = e.Str
	}
	return out
}

// byteSlice reads a list<u8>.
func byteSlice(v Val) []byte {
	out := make([]byte, len(v.Elems))
	for i, e := range v.Elems {
		out[i] = byte(e.Int)
	}
	return out
}

// pairsOf reads a list<tuple<string, string>>.
func pairsOf(v Val) [][2]string {
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
