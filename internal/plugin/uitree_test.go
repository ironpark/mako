package plugin

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
)

func TestTreeIDsAndActions(t *testing.T) {
	tr, err := readTree([]byte(`{"kind":"column","children":[
		{"kind":"text","text":"a"},
		{"kind":"list","key":"runs","children":[{"kind":"row","key":"7","onTap":["open",7],"children":[{"kind":"pill","text":"ok","tone":"success"}]}]},
		{"kind":"input","key":"q","value":"x","placeholder":"Find","onSubmit":["find"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{tr.root.GetId(), tr.root.Children[0].GetId(), tr.root.Children[1].Children[0].GetId(), tr.root.Children[2].GetId()}
	if fmt.Sprint(ids) != "[ /#0 /runs/7 /q]" {
		t.Fatalf("ids %v", ids)
	}
	if string(tr.actions["/runs/7"].tap) != `["open",7]` || !tr.root.Children[1].Children[0].GetTap() {
		t.Fatalf("the row's tap: %v", tr.actions)
	}
	if !tr.root.Children[2].GetChange() || tr.root.Children[2].GetText() != "Find" {
		t.Fatalf("the input: %v", tr.root.Children[2])
	}
}

// randomTree is a tree of keyed and unkeyed children, for diffing.
func randomTree(r *rand.Rand, depth int) map[string]any {
	kinds := []string{"text", "pill", "chip", "button"}
	n := map[string]any{"kind": kinds[r.IntN(len(kinds))], "text": fmt.Sprint(r.IntN(3))}
	if depth > 0 && r.IntN(3) > 0 {
		n = map[string]any{"kind": []string{"column", "row", "list"}[r.IntN(3)], "gap": float64(r.IntN(2))}
		var kids []any
		keys := r.Perm(8)[:r.IntN(6)]
		for _, k := range keys {
			c := randomTree(r, depth-1)
			if r.IntN(4) > 0 {
				c["key"] = fmt.Sprint(k)
			}
			kids = append(kids, c)
		}
		n["children"] = kids
	}
	return n
}

// TestDiffAppliesToTheNext diffs random trees, keyed and moved about, and
// applies the ops as a front-end would: it must come to the next tree.
func TestDiffAppliesToTheNext(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	read := func(v map[string]any) *protocol.UiNode {
		b, _ := json.Marshal(v)
		tr, err := readTree(b)
		if err != nil {
			t.Fatal(err)
		}
		return tr.root
	}
	for i := range 500 {
		a, b := read(randomTree(r, 3)), read(randomTree(r, 3))
		ops := protocol.DiffUi(a, b)
		got, err := protocol.ApplyUi(a, ops)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if !proto.Equal(got, b) {
			t.Fatalf("%d: applying %d ops did not come to the next tree", i, len(ops))
		}
		if len(protocol.DiffUi(b, b)) != 0 {
			t.Fatalf("%d: a tree differs from itself", i)
		}
	}
}

func TestMovedKeyedChildIsMoved(t *testing.T) {
	read := func(s string) *protocol.UiNode {
		tr, err := readTree([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return tr.root
	}
	a := read(`{"kind":"list","children":[{"kind":"text","key":"a","text":"A"},{"kind":"text","key":"b","text":"B"},{"kind":"text","key":"c","text":"C"}]}`)
	b := read(`{"kind":"list","children":[{"kind":"text","key":"c","text":"C"},{"kind":"text","key":"a","text":"A"},{"kind":"text","key":"b","text":"B2"}]}`)
	ops := protocol.DiffUi(a, b)
	var kinds []string
	for _, op := range ops {
		kinds = append(kinds, op.GetOp())
	}
	if fmt.Sprint(kinds) != "[move update]" {
		t.Fatalf("ops %v", kinds)
	}
}

func TestApplyRefusesOpsOfAnotherTree(t *testing.T) {
	root := &protocol.UiNode{Kind: "column", Children: []*protocol.UiNode{{Id: "/a", Kind: "text"}}}
	for _, op := range []*protocol.UiOp{
		{Op: protocol.UiRemove, Id: "/missing"},
		{Op: protocol.UiInsert, Parent: "/missing", Node: &protocol.UiNode{}},
		{Op: protocol.UiInsert, Parent: "", Index: 5, Node: &protocol.UiNode{}},
		{Op: protocol.UiMove, Id: "/a", Parent: "/other", Index: 0},
		{Op: "explode", Id: "/a"},
	} {
		if _, err := protocol.ApplyUi(root, []*protocol.UiOp{op}); err == nil {
			t.Errorf("%v was applied", op)
		}
	}
}
