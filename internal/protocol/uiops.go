package protocol

import (
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"
)

// A plugin block's tree goes to a front-end whole once, then as the
// changes between one render and the next (BlockUpdate.ops). Nodes are
// matched by id, which is a node's path by its own and its ancestors'
// keys, so a keyed child that moves among its siblings is moved, not
// drawn again.

// The ops of a UiOp.
const (
	UiReplace = "replace"
	UiUpdate  = "update"
	UiInsert  = "insert"
	UiRemove  = "remove"
	UiMove    = "move"
)

// UiCheck is the BlockEvent kind sent when a node's tri-state check is toggled
// (revision 46).
const UiCheck = "check"

// DiffUi is the ops that make old into next, both trees of the same
// root id; none when they are the same.
func DiffUi(old, next *UiNode) []*UiOp {
	var ops []*UiOp
	diffUi(old, next, &ops)
	return ops
}

func diffUi(old, next *UiNode, ops *[]*UiOp) {
	if old.GetKind() != next.GetKind() {
		*ops = append(*ops, &UiOp{Op: UiReplace, Id: next.GetId(), Node: next})
		return
	}
	if !proto.Equal(own(old), own(next)) {
		*ops = append(*ops, &UiOp{Op: UiUpdate, Id: next.GetId(), Node: own(next)})
	}
	byID := map[string]*UiNode{}
	for _, c := range old.GetChildren() {
		byID[c.GetId()] = c
	}
	wanted := map[string]bool{}
	for _, c := range next.GetChildren() {
		wanted[c.GetId()] = true
	}
	// The children as the front-end will have them, op by op.
	var cur []string
	for _, c := range old.GetChildren() {
		if wanted[c.GetId()] {
			cur = append(cur, c.GetId())
		} else {
			*ops = append(*ops, &UiOp{Op: UiRemove, Id: c.GetId()})
		}
	}
	for i, c := range next.GetChildren() {
		prev, had := byID[c.GetId()]
		switch {
		case !had:
			*ops = append(*ops, &UiOp{Op: UiInsert, Parent: next.GetId(), Index: int32(i), Node: c})
			cur = slices.Insert(cur, i, c.GetId())
		case cur[i] != c.GetId():
			*ops = append(*ops, &UiOp{Op: UiMove, Id: c.GetId(), Parent: next.GetId(), Index: int32(i)})
			cur = slices.Delete(cur, slices.Index(cur, c.GetId()), slices.Index(cur, c.GetId())+1)
			cur = slices.Insert(cur, i, c.GetId())
			diffUi(prev, c, ops)
		default:
			diffUi(prev, c, ops)
		}
	}
}

// own is a node without its children.
func own(n *UiNode) *UiNode {
	c := proto.CloneOf(n)
	c.Children = nil
	return c
}

// ApplyUi applies ops to a copy of root and returns it; root is left as it
// was. An op that names a node the tree does not hold fails, and the
// front-end starts again from a whole tree.
func ApplyUi(root *UiNode, ops []*UiOp) (*UiNode, error) {
	root = proto.CloneOf(root)
	for _, op := range ops {
		var err error
		if root, err = applyUi(root, op); err != nil {
			return nil, err
		}
	}
	return root, nil
}

func applyUi(root *UiNode, op *UiOp) (*UiNode, error) {
	switch op.GetOp() {
	case UiReplace:
		if op.GetId() == root.GetId() {
			return proto.CloneOf(op.GetNode()), nil
		}
		parent, i := findUi(root, op.GetId())
		if parent == nil {
			return nil, fmt.Errorf("replace: no node %q", op.GetId())
		}
		parent.Children[i] = proto.CloneOf(op.GetNode())
	case UiUpdate:
		n := root
		if op.GetId() != root.GetId() {
			parent, i := findUi(root, op.GetId())
			if parent == nil {
				return nil, fmt.Errorf("update: no node %q", op.GetId())
			}
			n = parent.Children[i]
		}
		kids := n.Children
		proto.Reset(n)
		proto.Merge(n, op.GetNode())
		n.Children = kids
	case UiInsert:
		parent := nodeUi(root, op.GetParent())
		if parent == nil || op.GetIndex() < 0 || int(op.GetIndex()) > len(parent.GetChildren()) {
			return nil, fmt.Errorf("insert: no place %d in %q", op.GetIndex(), op.GetParent())
		}
		parent.Children = slices.Insert(parent.Children, int(op.GetIndex()), proto.CloneOf(op.GetNode()))
	case UiRemove:
		parent, i := findUi(root, op.GetId())
		if parent == nil {
			return nil, fmt.Errorf("remove: no node %q", op.GetId())
		}
		parent.Children = slices.Delete(parent.Children, i, i+1)
	case UiMove:
		parent, i := findUi(root, op.GetId())
		if parent == nil || parent.GetId() != op.GetParent() || op.GetIndex() < 0 || int(op.GetIndex()) >= len(parent.GetChildren()) {
			return nil, fmt.Errorf("move: no node %q in %q", op.GetId(), op.GetParent())
		}
		n := parent.Children[i]
		parent.Children = slices.Delete(parent.Children, i, i+1)
		parent.Children = slices.Insert(parent.Children, int(op.GetIndex()), n)
	default:
		return nil, fmt.Errorf("unknown op %q", op.GetOp())
	}
	return root, nil
}

// findUi is the parent of the node of id, and its index there; nil when
// no node but the root has it.
func findUi(n *UiNode, id string) (*UiNode, int) {
	for i, c := range n.GetChildren() {
		if c.GetId() == id {
			return n, i
		}
		if p, j := findUi(c, id); p != nil {
			return p, j
		}
	}
	return nil, 0
}

// nodeUi is the node of id, nil for none.
func nodeUi(n *UiNode, id string) *UiNode {
	if n.GetId() == id {
		return n
	}
	for _, c := range n.GetChildren() {
		if m := nodeUi(c, id); m != nil {
			return m
		}
	}
	return nil
}
