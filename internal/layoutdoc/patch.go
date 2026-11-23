package layoutdoc

import (
	"encoding/json"
	"errors"
	"fmt"
)

// A patch is a list of small changes to a document by node ID, applied in
// order to a copy, which is then checked whole: a patch that leaves a
// document a window could not draw changes nothing. No change renames a
// node, so the IDs that devices keep their sizes by stay as they were.

// Op is one change of a patch.
type Op struct {
	// Op is insert, remove, move, resize or select.
	Op string `json:"op" jsonschema:"insert, remove, move, resize or select"`
	// ID is the node removed, moved, resized or selected.
	ID string `json:"id,omitempty" jsonschema:"The node to remove, move, resize, or select in (a stack)."`
	// Parent and Index are where insert and move put a node: a row's,
	// column's or stack's children, at Index, or at the end without one.
	Parent string `json:"parent,omitempty" jsonschema:"The row, column or stack that insert and move put the node in."`
	Index  *int   `json:"index,omitempty" jsonschema:"Where among the parent's children; the end when left out."`
	// Node is the node insert puts in, in the document's form.
	Node any `json:"node,omitempty" jsonschema:"The node to insert, as the document writes one: an object, or a block's name."`
	// Size or Grow is what resize gives the node, the other then none.
	Size *float64 `json:"size,omitempty" jsonschema:"resize: the node's default size in px."`
	Grow *float64 `json:"grow,omitempty" jsonschema:"resize: the node's share of the room left, in place of a size."`
	// Active or Block is the child select has a stack show by default.
	Active *int   `json:"active,omitempty" jsonschema:"select: the index of the child the stack shows."`
	Block  string `json:"block,omitempty" jsonschema:"select: the block the stack shows, in place of active."`
}

// Apply is d with ops applied, checked against cat; d is not changed.
func Apply(d *Doc, ops []Op, cat Catalog) (*Doc, error) {
	if d == nil {
		return nil, errors.New("no document")
	}
	if d.Version > Version {
		return nil, fmt.Errorf("the document is of version %d, which this node does not write", d.Version)
	}
	if len(ops) == 0 {
		return nil, errors.New("no changes")
	}
	out := d.Clone()
	for i, op := range ops {
		if err := out.apply(op); err != nil {
			return nil, fmt.Errorf("change %d (%s): %w", i+1, op.Op, err)
		}
	}
	if err := out.Check(cat); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *Doc) apply(op Op) error {
	switch op.Op {
	case "insert":
		raw, err := json.Marshal(op.Node)
		if err != nil || op.Node == nil {
			return errors.New("no node to insert")
		}
		var p parser
		n, err := p.node(raw, 1)
		if err != nil {
			return err
		}
		return d.insert(n, op.Parent, op.Index)
	case "remove":
		_, err := d.remove(op.ID)
		return err
	case "move":
		if path := d.PathTo(func(n *Node) bool { return n.ID == op.Parent }); path != nil {
			for _, n := range path {
				if n.ID == op.ID {
					return errors.New("a node cannot go inside itself")
				}
			}
		}
		n, err := d.remove(op.ID)
		if err != nil {
			return err
		}
		n.short = false
		return d.insert(n, op.Parent, op.Index)
	case "resize":
		n := d.Node(op.ID)
		switch {
		case n == nil:
			return fmt.Errorf("no node %q", op.ID)
		case (op.Size == nil) == (op.Grow == nil):
			return errors.New("give size or grow, one of them")
		case op.Size != nil:
			if *op.Size <= 0 {
				return errors.New("size is not above 0")
			}
			n.Size, n.Grow = *op.Size, 0
		default:
			if *op.Grow <= 0 {
				return errors.New("grow is not above 0")
			}
			n.Size, n.Grow, n.Collapsible = 0, *op.Grow, false
		}
		n.short = false
		return nil
	case "select":
		n := d.Node(op.ID)
		if n == nil || n.Kind != KindStack {
			return fmt.Errorf("no stack %q", op.ID)
		}
		switch {
		case op.Block != "":
			i := StackIndex(n, op.Block)
			if i < 0 || i >= len(n.Children) || n.Children[i].Block != op.Block {
				return fmt.Errorf("stack %q holds no %s", op.ID, op.Block)
			}
			n.Active = i
		case op.Active != nil:
			n.Active = *op.Active
		default:
			return errors.New("give active or block")
		}
		return nil
	}
	return fmt.Errorf("no change %q: insert, remove, move, resize or select", op.Op)
}

// insert puts n among parent's children at index, at the end for none.
func (d *Doc) insert(n *Node, parent string, index *int) error {
	p := d.Node(parent)
	if p == nil || (p.Kind != KindRow && p.Kind != KindColumn && p.Kind != KindStack) {
		return fmt.Errorf("no row, column or stack %q", parent)
	}
	at := len(p.Children)
	if index != nil {
		at = *index
	}
	if at < 0 || at > len(p.Children) {
		return fmt.Errorf("index %d of %d children", at, len(p.Children))
	}
	if p.Kind == KindStack && at <= p.Active && len(p.Children) > 0 {
		p.Active++ // the stack goes on showing the child it did
	}
	p.Children = append(p.Children[:at], append([]*Node{n}, p.Children[at:]...)...)
	return nil
}

// remove takes the node of the ID out of its parent, and returns it.
func (d *Doc) remove(id string) (*Node, error) {
	path := d.PathTo(func(n *Node) bool { return n.ID == id })
	switch {
	case path == nil:
		return nil, fmt.Errorf("no node %q", id)
	case len(path) == 1:
		return nil, errors.New("the root cannot be taken out")
	}
	p, n := path[len(path)-2], path[len(path)-1]
	for i, c := range p.Children {
		if c == n {
			p.Children = append(p.Children[:i:i], p.Children[i+1:]...)
			if p.Kind == KindStack && p.Active >= i && p.Active > 0 {
				p.Active--
			}
			break
		}
	}
	return n, nil
}
