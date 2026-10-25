package syntax

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Just enough of the wasm binary format to link grammar dynamic libraries:
// read sections, the dylink.0 custom section, types and imports, and
// rewrite imports and the table section.

const (
	secCustom = 0
	secType   = 1
	secImport = 2
	secTable  = 4
	secExport = 7

	kindFunc   = 0
	kindTable  = 1
	kindMemory = 2
	kindGlobal = 3

	valI32 = 0x7f
	valI64 = 0x7e
	valF32 = 0x7d
	valF64 = 0x7c
)

var wasmHeader = []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}

var errWasm = errors.New("malformed wasm module")

type section struct {
	id   byte
	data []byte
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) byte() byte {
	if r.err != nil || len(r.b) == 0 {
		r.err = errWasm
		return 0
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *reader) u32() uint32 {
	var v uint32
	for shift := 0; shift < 35; shift += 7 {
		c := r.byte()
		v |= uint32(c&0x7f) << shift
		if c < 0x80 {
			return v
		}
	}
	r.err = errWasm
	return 0
}

func (r *reader) bytes(n uint32) []byte {
	if r.err != nil || uint32(len(r.b)) < n {
		r.err = errWasm
		return nil
	}
	s := r.b[:n]
	r.b = r.b[n:]
	return s
}

func (r *reader) name() string { return string(r.bytes(r.u32())) }

// limits skips a limits encoding and returns its flags, min and max.
func (r *reader) limits() (flags byte, min, max uint32) {
	flags = r.byte()
	min = r.u32()
	if flags&1 != 0 {
		max = r.u32()
	}
	return
}

func readSections(b []byte) ([]section, error) {
	if !bytes.HasPrefix(b, wasmHeader) {
		return nil, errors.New("not a wasm module")
	}
	r := &reader{b: b[len(wasmHeader):]}
	var out []section
	for len(r.b) > 0 && r.err == nil {
		id := r.byte()
		data := r.bytes(r.u32())
		out = append(out, section{id, data})
	}
	return out, r.err
}

func writeSections(secs []section) []byte {
	var w bytes.Buffer
	w.Write(wasmHeader)
	for _, s := range secs {
		w.WriteByte(s.id)
		w.Write(leb(uint32(len(s.data))))
		w.Write(s.data)
	}
	return w.Bytes()
}

// leb is v in unsigned LEB128, which a uvarint is.
func leb(v uint32) []byte { return binary.AppendUvarint(nil, uint64(v)) }

func encName(s string) []byte { return append(leb(uint32(len(s))), s...) }

// dylinkInfo is the mem_info subsection of dylink.0.
type dylinkInfo struct {
	memSize, memAlign     uint32 // alignments are log2
	tableSize, tableAlign uint32
}

func readDylink(secs []section) (dylinkInfo, error) {
	for _, s := range secs {
		if s.id != secCustom {
			continue
		}
		r := &reader{b: s.data}
		if r.name() != "dylink.0" {
			continue
		}
		for len(r.b) > 0 && r.err == nil {
			kind := r.byte()
			sub := &reader{b: r.bytes(r.u32())}
			if kind == 1 {
				d := dylinkInfo{sub.u32(), sub.u32(), sub.u32(), sub.u32()}
				if sub.err != nil {
					return d, sub.err
				}
				return d, nil
			}
		}
		if r.err != nil {
			return dylinkInfo{}, r.err
		}
		return dylinkInfo{}, nil
	}
	return dylinkInfo{}, errors.New("no dylink.0 section: not a wasm dynamic library")
}

type funcType struct{ params, results []byte }

func readTypes(secs []section) ([]funcType, error) {
	for _, s := range secs {
		if s.id != secType {
			continue
		}
		r := &reader{b: s.data}
		n := r.u32()
		out := make([]funcType, 0, n)
		for i := uint32(0); i < n && r.err == nil; i++ {
			if r.byte() != 0x60 {
				return nil, errWasm
			}
			p := r.bytes(r.u32())
			res := r.bytes(r.u32())
			out = append(out, funcType{p, res})
		}
		return out, r.err
	}
	return nil, nil
}

// wasmImport is one import entry; desc is its raw type descriptor.
type wasmImport struct {
	module, name string
	kind         byte
	desc         []byte
	typeIdx      uint32 // functions only
	mutable      bool   // globals only
}

func readImports(secs []section) ([]wasmImport, error) {
	for _, s := range secs {
		if s.id != secImport {
			continue
		}
		r := &reader{b: s.data}
		n := r.u32()
		out := make([]wasmImport, 0, n)
		for i := uint32(0); i < n && r.err == nil; i++ {
			im := wasmImport{module: r.name(), name: r.name(), kind: r.byte()}
			start := r.b
			switch im.kind {
			case kindFunc:
				im.typeIdx = r.u32()
			case kindTable:
				r.byte()
				r.limits()
			case kindMemory:
				r.limits()
			case kindGlobal:
				r.byte()
				im.mutable = r.byte() == 1
			default:
				return nil, fmt.Errorf("unknown import kind %d", im.kind)
			}
			im.desc = start[:len(start)-len(r.b)]
			out = append(out, im)
		}
		return out, r.err
	}
	return nil, nil
}

func encodeImports(ims []wasmImport) []byte {
	out := leb(uint32(len(ims)))
	for _, im := range ims {
		out = append(out, encName(im.module)...)
		out = append(out, encName(im.name)...)
		out = append(out, im.kind)
		out = append(out, im.desc...)
	}
	return out
}

// replaceSection swaps the first section with the given id.
func replaceSection(secs []section, id byte, data []byte) {
	for i := range secs {
		if secs[i].id == id {
			secs[i].data = data
			return
		}
	}
}

// growTable raises the minimum of the module's single defined table by
// extra slots and drops its maximum. It returns the old minimum, which is
// the first slot the module itself does not use.
func growTable(secs []section, extra uint32) (uint32, error) {
	for i, s := range secs {
		if s.id != secTable {
			continue
		}
		r := &reader{b: s.data}
		if r.u32() != 1 {
			return 0, errors.New("runtime module must define exactly one table")
		}
		ref := r.byte()
		_, min, _ := r.limits()
		if r.err != nil {
			return 0, r.err
		}
		data := []byte{1, ref, 0}
		data = append(data, leb(min+extra)...)
		secs[i].data = data
		return min, nil
	}
	return 0, errors.New("runtime module defines no table")
}

// globalDef is an i32 global the linker provides to a grammar.
type globalDef struct {
	name    string
	value   uint32
	mutable bool
}
