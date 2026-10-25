package syntax

import (
	"errors"
	"fmt"
)

// Point is a position as tree-sitter counts it: zero-based row, and
// column in bytes.
type Point struct {
	Row, Column int
}

// InputEdit describes one text change, as passed to ts_tree_edit.
type InputEdit struct {
	StartByte, OldEndByte, NewEndByte    int
	StartPoint, OldEndPoint, NewEndPoint Point
}

// Tree is a syntax tree. Close it when done; it lives in wasm memory.
type Tree struct {
	lang *Language
	ptr  uint32 // TSTree *
}

// Parse parses src (UTF-8). Pass the previous tree as old, after applying
// every edit to it with Tree.Edit, to reparse incrementally; old stays
// valid and must still be closed.
func (l *Language) Parse(src []byte, old *Tree) (*Tree, error) {
	r := l.rt
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usable(); err != nil {
		return nil, err
	}
	var oldPtr uint32
	if old != nil {
		if old.lang != l {
			return nil, errors.New("syntax: old tree is from another language")
		}
		if old.ptr == 0 {
			return nil, errors.New("syntax: old tree is closed")
		}
		oldPtr = old.ptr
	}
	p, err := r.writeBytes(src)
	if err != nil {
		return nil, err
	}
	defer r.free(p)
	t, err := r.call1("shim_parse", uint64(l.parser), uint64(oldPtr), uint64(p), uint64(len(src)))
	if err != nil {
		return nil, err
	}
	if t == 0 {
		return nil, fmt.Errorf("syntax: %s: parse failed", l.name)
	}
	return &Tree{lang: l, ptr: t}, nil
}

// Edit adjusts the tree for a change to its source, so the next Parse
// can reuse the unchanged parts.
func (t *Tree) Edit(e InputEdit) {
	r := t.lang.rt
	r.mu.Lock()
	defer r.mu.Unlock()
	if t.ptr == 0 || r.usable() != nil {
		return
	}
	u := func(v int) uint64 { return uint64(uint32(v)) }
	r.call("shim_tree_edit", uint64(t.ptr),
		u(e.StartByte), u(e.OldEndByte), u(e.NewEndByte),
		u(e.StartPoint.Row), u(e.StartPoint.Column),
		u(e.OldEndPoint.Row), u(e.OldEndPoint.Column),
		u(e.NewEndPoint.Row), u(e.NewEndPoint.Column))
}

// Close frees the tree. It is safe to call more than once.
func (t *Tree) Close() {
	r := t.lang.rt
	r.mu.Lock()
	defer r.mu.Unlock()
	if t.ptr == 0 {
		return
	}
	if r.usable() == nil {
		r.call("shim_tree_delete", uint64(t.ptr))
	}
	t.ptr = 0
}

// RootSExpr renders the tree as an s-expression, for tests and debugging.
func (t *Tree) RootSExpr() string {
	r := t.lang.rt
	r.mu.Lock()
	defer r.mu.Unlock()
	if t.ptr == 0 || r.usable() != nil {
		return ""
	}
	p, err := r.call1("shim_tree_sexpr", uint64(t.ptr))
	if err != nil || p == 0 {
		return ""
	}
	defer r.free(p)
	s, _ := r.readCString(p)
	return s
}
