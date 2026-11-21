package uiblock

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
)

func TestTreeIDsAndActions(t *testing.T) {
	tr, err := Read([]byte(`{"kind":"column","children":[
		{"kind":"text","text":"a"},
		{"kind":"list","key":"runs","children":[{"kind":"row","key":"7","onTap":["open",7],"children":[{"kind":"pill","text":"ok","tone":"success"}]}]},
		{"kind":"input","key":"q","value":"x","placeholder":"Find","onSubmit":["find"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{tr.Root.GetId(), tr.Root.Children[0].GetId(), tr.Root.Children[1].Children[0].GetId(), tr.Root.Children[2].GetId()}
	if fmt.Sprint(ids) != "[ /#0 /runs/7 /q]" {
		t.Fatalf("ids %v", ids)
	}
	if string(tr.actions["/runs/7"].tap) != `["open",7]` || !tr.Root.Children[1].Children[0].GetTap() {
		t.Fatalf("the row's tap: %v", tr.actions)
	}
	if !tr.Root.Children[2].GetChange() || tr.Root.Children[2].GetText() != "Find" {
		t.Fatalf("the input: %v", tr.Root.Children[2])
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
		tr, err := Read(b)
		if err != nil {
			t.Fatal(err)
		}
		return tr.Root
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
		tr, err := Read([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return tr.Root
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

// A form of many fields sends them all: values are bounded by their
// bytes, and by the nodes a tree holds, not by a count of their own.
func TestALargeFormsValuesAreTaken(t *testing.T) {
	var fields []string
	in := map[string]string{}
	for i := range 200 {
		key := fmt.Sprintf("f%d", i)
		fields = append(fields, `{"kind":"input","key":"`+key+`","value":""}`)
		in["/"+key] = "some text"
	}
	tr, err := Read([]byte(`{"kind":"column","children":[` + strings.Join(fields, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	vals, err := tr.Values(in)
	if err != nil || len(vals) != 200 {
		t.Fatalf("200 fields: %d taken, %v", len(vals), err)
	}
	for id := range in {
		in[id] = strings.Repeat("x", 200)
	}
	if _, err := tr.Values(in); err == nil || !strings.Contains(err.Error(), "32 KiB") {
		t.Fatalf("40 KiB of values: %v", err)
	}
	many := map[string]string{}
	for i := range maxNodes + 1 {
		many[fmt.Sprintf("/x%d", i)] = ""
	}
	if _, err := tr.Values(many); err == nil {
		t.Fatal("more values than a tree holds nodes were taken")
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

// A form's submit carries its fields' values, kept to its inputs,
// toggles and selects; a change's value comes last, as a toggle's boolean.
func TestEventsCarryTheirMessageAndValues(t *testing.T) {
	tr, err := Read([]byte(`{"kind":"column","children":[
		{"kind":"input","key":"name","value":""},
		{"kind":"toggle","key":"gift","checked":false,"onChange":["gift"]},
		{"kind":"text","key":"note","text":"x"},
		{"kind":"button","key":"send","text":"Send","onTap":["submit","form-1"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := tr.Message(&protocol.BlockEvent{Node: "/send", Kind: protocol.UiTap})
	if err != nil || m.Name != "submit" || string(m.Given) != `["submit","form-1"]` || len(m.Args) != 1 {
		t.Fatalf("%+v %v", m, err)
	}
	m, err = tr.Message(&protocol.BlockEvent{Node: "/gift", Kind: protocol.UiChange, Value: "true"})
	if err != nil || string(m.Args[len(m.Args)-1]) != "true" {
		t.Fatalf("%+v %v", m, err)
	}
	vals, err := tr.Values(map[string]string{"/name": "Ada", "/gift": "true"})
	if err != nil || vals["/name"] != "Ada" || len(vals) != 2 {
		t.Fatalf("%v %v", vals, err)
	}
	for _, bad := range []map[string]string{{"/note": "x"}, {"/name": strings.Repeat("x", MaxEventValue+1)}} {
		if _, err := tr.Values(bad); err == nil {
			t.Errorf("%v was taken", bad)
		}
	}
	// A field the daemon's tree no longer has, the front-end's a render
	// behind, is left out; the rest of the form stands.
	if vals, err := tr.Values(map[string]string{"/name": "Ada", "/gone": "x"}); err != nil || len(vals) != 1 || vals["/name"] != "Ada" {
		t.Fatalf("with a field gone: %v %v", vals, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/note", Kind: protocol.UiTap}); err == nil {
		t.Error("a tap on text that sends nothing was taken")
	}
	var nilTree *Tree
	if _, err := nilTree.Message(&protocol.BlockEvent{Node: "/send", Kind: protocol.UiTap}); err == nil {
		t.Error("an event of a block with no tree was taken")
	}
}

func TestDragAndDropValidationAndMessages(t *testing.T) {
	tr, err := Read([]byte(`{"kind":"row","children":[
		{"kind":"column","key":"todo","onDrop":["move","todo"],"children":[
			{"kind":"column","key":"c1","drag":"card-1","onTap":["select","c1"],"onDrop":["before","todo","c1"],"children":[
				{"kind":"text","text":"First card"}]}]},
		{"kind":"column","key":"done","onDrop":["move","done"],"children":[
			{"kind":"text","text":"Empty"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	card := nodeOf(tr.Root, "/todo/c1")
	if card == nil || card.GetDrag() != "card-1" || !card.GetDrop() || !card.GetTap() {
		t.Fatalf("card = %+v", card)
	}
	doneCol := nodeOf(tr.Root, "/done")
	if doneCol == nil || doneCol.GetDrag() != "" || !doneCol.GetDrop() {
		t.Fatalf("done column = %+v", doneCol)
	}
	m, err := tr.Message(&protocol.BlockEvent{Node: "/done", Kind: protocol.UiDrop, Value: "card-1"})
	if err != nil || m.Name != "move" || len(m.Args) != 2 || string(m.Args[0]) != `"done"` || string(m.Args[1]) != `"card-1"` {
		t.Fatalf("drop message = %+v, err = %v", m, err)
	}
	// Dropping with an empty or control-character payload, or onto a node without onDrop, is refused.
	for _, ev := range []*protocol.BlockEvent{
		{Node: "/done", Kind: protocol.UiDrop, Value: ""},
		{Node: "/done", Kind: protocol.UiDrop, Value: "bad\x00id"},
		{Node: "/done/#0", Kind: protocol.UiDrop, Value: "card-1"},
	} {
		if _, err := tr.Message(ev); err == nil {
			t.Errorf("event %+v should have been refused", ev)
		}
	}
	// Inputs and dividers cannot take drag or onDrop; oversized/spoofing drag payloads are refused.
	for _, bad := range []string{
		`{"kind":"input","drag":"x"}`,
		`{"kind":"input","onDrop":["move"]}`,
		`{"kind":"divider","onDrop":["move"]}`,
		`{"kind":"text","text":"x","drag":"bad\u202eid"}`,
		`{"kind":"text","text":"x","drag":"` + strings.Repeat("a", maxDrag+1) + `"}`,
		`{"kind":"text","text":"x","dragGroup":"card"}`,
		`{"kind":"column","drag":"c1","dragGroup":"Bad Group"}`,
		`{"kind":"column","dropAccept":["card"]}`,
		`{"kind":"column","onDrop":["move"],"dropAccept":["Bad Group"]}`,
	} {
		if _, err := Read([]byte(bad)); err == nil {
			t.Errorf("bad tree %s was accepted", bad)
		}
	}
}

func TestRichUIKindsAndDragGroups(t *testing.T) {
	tr, err := Read([]byte(`{"kind":"column","children":[
		{"kind":"tabs","key":"mode","value":"Board","options":["Board","Table"],"onChange":["mode"]},
		{"kind":"scroll","key":"sc","direction":"vertical","maxHeight":360,"onDrop":["move","todo"],"dropAccept":["card"],"children":[
			{"kind":"card","key":"c1","drag":"c1","dragGroup":"card","onTap":["pick","c1"],"children":[
				{"kind":"text","text":"Card 1"},
				{"kind":"textarea","key":"note","value":"hello","placeholder":"Notes","onChange":["note"]}
			]}
		]},
		{"kind":"table","key":"tbl","labels":["Title","Status"],"children":[
			{"kind":"row","key":"r1","children":[{"kind":"text","text":"Card 1"},{"kind":"pill","text":"Todo"}]}
		]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	sc := nodeOf(tr.Root, "/sc")
	if sc == nil || sc.GetDirection() != "vertical" || sc.GetMaxHeight() != 360 || len(sc.GetDropAccept()) != 1 || sc.GetDropAccept()[0] != "card" {
		t.Fatalf("scroll = %+v", sc)
	}
	c1 := nodeOf(tr.Root, "/sc/c1")
	if c1 == nil || c1.GetKind() != KindCard || c1.GetDragGroup() != "card" {
		t.Fatalf("card = %+v", c1)
	}
	vals, err := tr.Values(map[string]string{"/mode": "Table", "/sc/c1/note": "updated\nline 2"})
	if err != nil || vals["/mode"] != "Table" || vals["/sc/c1/note"] != "updated\nline 2" {
		t.Fatalf("values = %v, err = %v", vals, err)
	}
	// Invalid rich UI attributes are rejected.
	for _, bad := range []string{
		`{"kind":"scroll","direction":"diagonal","children":[]}`,
		`{"kind":"scroll","maxHeight":-10,"children":[]}`,
		`{"kind":"column","direction":"horizontal","children":[]}`,
		`{"kind":"column","maxHeight":200,"children":[]}`,
		`{"kind":"tabs","options":[]}`,
		`{"kind":"tabs","options":["a","b"],"value":"c"}`,
		`{"kind":"table","labels":["a"],"children":[{"kind":"text","text":"not a row"}]}`,
		`{"kind":"textarea","children":[{"kind":"text","text":"bad"}]}`,
	} {
		if _, err := Read([]byte(bad)); err == nil {
			t.Errorf("bad rich UI tree %s was accepted", bad)
		}
	}
}
