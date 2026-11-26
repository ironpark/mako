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

func TestRichUIv2KindsEventsAndDiffPatch(t *testing.T) {
	beforeJSON := `{
		"kind": "split",
		"fill": true,
		"direction": "horizontal",
		"ratio": 0.3,
		"children": [
			{
				"kind": "column",
				"key": "left",
				"gap": 8,
				"children": [
					{"kind": "breadcrumb", "key": "crumb", "options": ["Vault", "Notes", "Design"], "onChange": ["nav"]},
					{"kind": "tree", "key": "files", "onDrop": ["moveRoot"], "dropAccept": ["note"], "children": [
						{"kind": "tree_item", "key": "f1", "text": "Projects", "value": "2", "icon": "folder", "tone": "accent", "checked": true, "onTap": ["open", "f1"], "onChange": ["toggle", "f1"], "onDrop": ["moveInto", "f1"], "dropAccept": ["note"], "children": [
							{"kind": "tree_item", "key": "n1", "text": "Spec.md", "value": "4 KB", "icon": "file-text", "drag": "n1", "dragGroup": "note", "onTap": ["open", "n1"]}
						]}
					]},
					{"kind": "collapsible", "key": "meta", "text": "Metadata", "icon": "tag", "tone": "muted", "checked": false, "onChange": ["toggleMeta"], "children": [
						{"kind": "wrap", "key": "tags", "gap": 4, "children": [
							{"kind": "chip", "text": "arch"},
							{"kind": "chip", "text": "v2"}
						]}
					]}
				]
			},
			{
				"kind": "column",
				"key": "right",
				"gap": 8,
				"children": [
					{"kind": "grid", "key": "cards", "columns": 3, "gap": 8, "children": [
						{"kind": "card", "key": "k1", "variant": "outline", "children": [
							{"kind": "button", "key": "iconOnly", "icon": "plus", "size": "small", "variant": "ghost", "onTap": ["add"]}
						]},
						{"kind": "card", "key": "k2", "variant": "primary", "children": [
							{"kind": "button", "key": "save", "text": "Save", "size": "large", "variant": "primary", "onTap": ["save"]}
						]},
						{"kind": "textarea", "key": "ed", "value": "hello", "size": "small", "onChange": ["edit"]}
					]},
					{"kind": "markdown", "key": "md", "text": "See [[Spec]]", "onLink": ["follow"]},
					{"kind": "graph", "key": "g", "text": "Links", "value": "a", "maxHeight": 320, "onChange": ["pickNode"],
						"graphNodes": [
							{"id": "a", "label": "Alpha", "group": "core", "tone": "accent", "subtitle": "root"},
							{"id": "b", "label": "Beta", "group": "leaf", "tone": "success", "weight": 2.5}
						],
						"graphEdges": [
							{"from": "a", "to": "b", "label": "links", "tone": "muted", "dashed": true}
						]
					}
				]
			}
		]
	}`
	tr, err := Read([]byte(beforeJSON))
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !tr.Root.GetFill() || tr.Root.GetDirection() != "horizontal" || tr.Root.GetRatio() != 0.3 {
		t.Fatalf("root split = %+v", tr.Root)
	}
	grid := nodeOf(tr.Root, "/right/cards")
	if grid == nil || grid.GetColumns() != 3 {
		t.Fatalf("grid = %+v", grid)
	}
	iconBtn := nodeOf(tr.Root, "/right/cards/k1/iconOnly")
	if iconBtn == nil || iconBtn.GetText() != "" || iconBtn.GetIcon() != "plus" || iconBtn.GetSize() != "small" || iconBtn.GetVariant() != "ghost" {
		t.Fatalf("iconBtn = %+v", iconBtn)
	}
	md := nodeOf(tr.Root, "/right/md")
	if md == nil || !md.GetChange() {
		t.Fatalf("markdown = %+v", md)
	}
	g := nodeOf(tr.Root, "/right/g")
	if g == nil || len(g.GetGraphNodes()) != 2 || g.GetGraphNodes()[0].GetWeight() != 1 || g.GetGraphNodes()[1].GetWeight() != 2.5 ||
		len(g.GetGraphEdges()) != 1 || !g.GetGraphEdges()[0].GetDashed() || g.GetValue() != "a" || g.GetMaxHeight() != 320 || !g.GetChange() {
		t.Fatalf("graph = %+v", g)
	}

	// Verify event handling for breadcrumb, collapsible, tree_item, markdown onLink, and graph.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/left/crumb", Kind: protocol.UiChange, Value: "1"}); err != nil || m.Name != "nav" || string(m.Args[0]) != `"1"` {
		t.Fatalf("breadcrumb event = %+v, %v", m, err)
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/left/meta", Kind: protocol.UiChange, Value: "true"}); err != nil || m.Name != "toggleMeta" || string(m.Args[0]) != "true" {
		t.Fatalf("collapsible event = %+v, %v", m, err)
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/left/files/f1", Kind: protocol.UiTap}); err != nil || m.Name != "open" || string(m.Args[0]) != `"f1"` {
		t.Fatalf("tree_item tap = %+v, %v", m, err)
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/left/files/f1", Kind: protocol.UiChange, Value: "false"}); err != nil || m.Name != "toggle" || string(m.Args[1]) != "false" {
		t.Fatalf("tree_item change = %+v, %v", m, err)
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/left/files/f1", Kind: protocol.UiDrop, Value: "n1"}); err != nil || m.Name != "moveInto" || string(m.Args[1]) != `"n1"` {
		t.Fatalf("tree_item drop = %+v, %v", m, err)
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/right/md", Kind: protocol.UiChange, Value: "Spec"}); err != nil || m.Name != "follow" || string(m.Args[0]) != `"Spec"` {
		t.Fatalf("markdown onLink = %+v, %v", m, err)
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/right/g", Kind: protocol.UiChange, Value: "b"}); err != nil || m.Name != "pickNode" || string(m.Args[0]) != `"b"` {
		t.Fatalf("graph event = %+v, %v", m, err)
	}

	// Default split ratio when omitted is 0.5.
	defSplit, err := Read([]byte(`{"kind":"split","children":[{"kind":"text","text":"L"},{"kind":"text","text":"R"}]}`))
	if err != nil || defSplit.Root.GetRatio() != 0.5 {
		t.Fatalf("default split ratio = %v, err = %v", defSplit, err)
	}

	// Diff & patch round-trip modifying every new field: fill, ratio, direction, columns, variant, graph_nodes, graph_edges.
	next := proto.CloneOf(tr.Root)
	next.Fill = false
	next.Ratio = 0.65
	next.Direction = "vertical"
	nodeOf(next, "/right/cards").Columns = 2
	nodeOf(next, "/right/cards/k1").Variant = "secondary"
	nodeOf(next, "/right/cards/k2/save").Variant = "danger"
	ng := nodeOf(next, "/right/g")
	ng.Value = "b"
	ng.MaxHeight = 480
	ng.GraphNodes = append(ng.GraphNodes, &protocol.UiGraphNode{Id: "c", Label: "Gamma", Tone: "warning", Weight: 1.5})
	ng.GraphEdges = []*protocol.UiGraphEdge{
		{From: "a", To: "b", Label: "refs", Tone: "accent", Dashed: false},
		{From: "b", To: "c", Label: "next"},
	}
	ops := protocol.DiffUi(tr.Root, next)
	if len(ops) == 0 {
		t.Fatal("expected non-empty UiOp diff for updated rich UI v2 fields")
	}
	got, err := protocol.ApplyUi(tr.Root, ops)
	if err != nil {
		t.Fatalf("ApplyUi failed: %v", err)
	}
	if !proto.Equal(got, next) {
		t.Fatalf("ApplyUi mismatch:\ngot:  %v\nwant: %v", got, next)
	}
}

func TestRichUIv2Rejections(t *testing.T) {
	// Build oversized graph fixtures.
	var manyNodes []string
	for i := range maxGraphNodes + 1 {
		manyNodes = append(manyNodes, fmt.Sprintf(`{"id":"n%d","label":"N%d"}`, i, i))
	}
	var manyEdges []string
	for range maxGraphEdges + 1 {
		manyEdges = append(manyEdges, `{"from":"a","to":"b"}`)
	}
	cases := map[string]struct{ tree, want string }{
		"variant-bad":       {`{"kind":"button","text":"x","variant":"neon"}`, "takes no variant"},
		"variant-text":      {`{"kind":"text","text":"x","variant":"primary"}`, "takes no variant"},
		"button-blank":      {`{"kind":"button"}`, "text or icon"},
		"grid-zero":         {`{"kind":"grid","columns":0,"children":[]}`, "1 to 6 columns"},
		"grid-seven":        {`{"kind":"grid","columns":7,"children":[]}`, "1 to 6 columns"},
		"columns-on-col":    {`{"kind":"column","columns":2,"children":[]}`, "only a grid"},
		"split-one":         {`{"kind":"split","children":[{"kind":"text","text":"a"}]}`, "exactly 2 children"},
		"split-three":       {`{"kind":"split","children":[{"kind":"text","text":"a"},{"kind":"text","text":"b"},{"kind":"text","text":"c"}]}`, "exactly 2 children"},
		"split-ratio-zero":  {`{"kind":"split","ratio":0,"children":[{"kind":"text","text":"a"},{"kind":"text","text":"b"}]}`, "ratio must be"},
		"split-ratio-low":   {`{"kind":"split","ratio":0.05,"children":[{"kind":"text","text":"a"},{"kind":"text","text":"b"}]}`, "ratio must be"},
		"split-ratio-high":  {`{"kind":"split","ratio":0.95,"children":[{"kind":"text","text":"a"},{"kind":"text","text":"b"}]}`, "ratio must be"},
		"ratio-on-row":      {`{"kind":"row","ratio":0.5,"children":[]}`, "only a split"},
		"fill-non-root":     {`{"kind":"column","children":[{"kind":"column","fill":true,"children":[]}]}`, "fill is only allowed"},
		"fill-on-card":      {`{"kind":"card","fill":true,"children":[]}`, "fill is only allowed"},
		"crumb-empty":       {`{"kind":"breadcrumb","options":[]}`, "at least one option"},
		"crumb-spoof":       {`{"kind":"breadcrumb","options":["a\u202eb"]}`, "reordering"},
		"crumb-tap":         {`{"kind":"breadcrumb","options":["a"],"onTap":["x"]}`, "onChange, not onTap"},
		"crumb-drag":        {`{"kind":"breadcrumb","options":["a"],"drag":"x"}`, "does not take drag"},
		"crumb-children":    {`{"kind":"breadcrumb","options":["a"],"children":[{"kind":"text","text":"x"}]}`, "holds no children"},
		"collapsible-tap":   {`{"kind":"collapsible","text":"S","onTap":["x"],"children":[]}`, "onChange, not onTap"},
		"tree-bad-child":    {`{"kind":"tree","children":[{"kind":"text","text":"x"}]}`, "must be a tree_item"},
		"tree-item-root":    {`{"kind":"tree_item","text":"x"}`, "must be inside a tree"},
		"tree-item-in-col":  {`{"kind":"column","children":[{"kind":"tree_item","text":"x"}]}`, "must be inside a tree"},
		"tree-item-child":   {`{"kind":"tree","children":[{"kind":"tree_item","text":"a","children":[{"kind":"text","text":"b"}]}]}`, "must be a tree_item"},
		"tree-item-spoof":   {`{"kind":"tree","children":[{"kind":"tree_item","text":"a","value":"bad\u202eval"}]}`, "reordering"},
		"md-onChange":       {`{"kind":"markdown","text":"hi","onChange":["x"]}`, "takes onLink"},
		"md-onTap":          {`{"kind":"markdown","text":"hi","onTap":["x"]}`, "takes onLink"},
		"md-drag":           {`{"kind":"markdown","text":"hi","drag":"x"}`, "does not take drag"},
		"onLink-on-text":    {`{"kind":"text","text":"hi","onLink":["x"]}`, "only a markdown"},
		"graph-too-many-n":  {`{"kind":"graph","graphNodes":[` + strings.Join(manyNodes, ",") + `]}`, "at most 120 nodes"},
		"graph-too-many-e":  {`{"kind":"graph","graphNodes":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"graphEdges":[` + strings.Join(manyEdges, ",") + `]}`, "at most 400 edges"},
		"graph-dup-id":      {`{"kind":"graph","graphNodes":[{"id":"a","label":"A"},{"id":"a","label":"A2"}]}`, "duplicated"},
		"graph-empty-id":    {`{"kind":"graph","graphNodes":[{"id":"","label":"A"}]}`, "invalid id"},
		"graph-empty-label": {`{"kind":"graph","graphNodes":[{"id":"a","label":""}]}`, "invalid label"},
		"graph-spoof-label": {`{"kind":"graph","graphNodes":[{"id":"a","label":"A\u202eB"}]}`, "invalid label"},
		"graph-bad-tone":    {`{"kind":"graph","graphNodes":[{"id":"a","label":"A","tone":"#fff"}]}`, "colour token"},
		"graph-weight-zero": {`{"kind":"graph","graphNodes":[{"id":"a","label":"A","weight":0}]}`, "finite positive"},
		"graph-weight-neg":  {`{"kind":"graph","graphNodes":[{"id":"a","label":"A","weight":-1}]}`, "finite positive"},
		"graph-bad-from":    {`{"kind":"graph","graphNodes":[{"id":"a","label":"A"}],"graphEdges":[{"from":"x","to":"a"}]}`, "unknown source"},
		"graph-bad-to":      {`{"kind":"graph","graphNodes":[{"id":"a","label":"A"}],"graphEdges":[{"from":"a","to":"y"}]}`, "unknown target"},
		"graph-edge-tone":   {`{"kind":"graph","graphNodes":[{"id":"a","label":"A"}],"graphEdges":[{"from":"a","to":"a","tone":"pink"}]}`, "colour token"},
		"graph-bad-value":   {`{"kind":"graph","value":"missing","graphNodes":[{"id":"a","label":"A"}]}`, "does not match any node"},
		"graph-height-low":  {`{"kind":"graph","maxHeight":80,"graphNodes":[]}`, "120 to 1200"},
		"graph-height-high": {`{"kind":"graph","maxHeight":1500,"graphNodes":[]}`, "120 to 1200"},
		"graph-elsewhere":   {`{"kind":"text","text":"x","graphNodes":[{"id":"a","label":"A"}]}`, "only a graph"},
		"graph-tap":         {`{"kind":"graph","onTap":["x"],"graphNodes":[]}`, "onChange, not onTap"},
		"graph-children":    {`{"kind":"graph","graphNodes":[],"children":[{"kind":"text","text":"x"}]}`, "holds no children"},
	}
	for name, c := range cases {
		if _, err := Read([]byte(c.tree)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want substring %q", name, err, c.want)
		}
	}
}

func TestVariantByKind(t *testing.T) {
	valid := []string{
		`{"kind":"card","variant":"soft","tone":"accent","children":[]}`,
		`{"kind":"pill","text":"on","variant":"soft","icon":"sparkles"}`,
		`{"kind":"pill","text":"on","variant":"solid","tone":"success"}`,
		`{"kind":"pill","text":"on","variant":"outline","tone":"warning"}`,
		`{"kind":"chip","text":"tag","variant":"soft","icon":"tag"}`,
		`{"kind":"chip","text":"tag","variant":"solid"}`,
		`{"kind":"chip","text":"tag","variant":"outline"}`,
		`{"kind":"progress","progress":0,"variant":"spinner","text":"Loading..."}`,
		`{"kind":"scroll","variant":"follow","children":[]}`,
		`{"kind":"tree","children":[{"kind":"tree_item","text":"docs","value":"3","variant":"badge","icon":"folder"}]}`,
	}
	for _, raw := range valid {
		if _, err := Read([]byte(raw)); err != nil {
			t.Errorf("valid %s failed: %v", raw, err)
		}
	}
	invalid := map[string]string{
		"button-soft":     `{"kind":"button","text":"x","variant":"soft"}`,
		"pill-primary":    `{"kind":"pill","text":"x","variant":"primary"}`,
		"chip-spinner":    `{"kind":"chip","text":"x","variant":"spinner"}`,
		"progress-follow": `{"kind":"progress","progress":0,"variant":"follow"}`,
		"scroll-badge":    `{"kind":"scroll","variant":"badge","children":[]}`,
		"tree-item-soft":  `{"kind":"tree","children":[{"kind":"tree_item","text":"a","variant":"soft"}]}`,
		"row-soft":        `{"kind":"row","variant":"soft","children":[]}`,
	}
	for name, raw := range invalid {
		if _, err := Read([]byte(raw)); err == nil || !strings.Contains(err.Error(), "takes no variant") {
			t.Errorf("%s: got %v, want takes no variant error", name, err)
		}
	}
}

func TestSpoofingChecksAllFieldsAndAllowsMultilineTextarea(t *testing.T) {
	rejected := map[string]string{
		"input-placeholder-bidi":      `{"kind":"input","placeholder":"Name\u202e"}`,
		"input-placeholder-with-text": `{"kind":"input","text":"Name","placeholder":"bad\u001b"}`,
		"input-placeholder-nul":       `{"kind":"input","placeholder":"bad\u0000"}`,
		"textarea-placeholder-bidi":   `{"kind":"textarea","placeholder":"Notes\u202e"}`,
		"input-value-bidi":            `{"kind":"input","value":"v\u202e"}`,
		"input-value-ctrl":            `{"kind":"input","value":"v\u0007"}`,
		"textarea-value-bidi":         `{"kind":"textarea","value":"line1\n\u202eline2"}`,
		"select-value-bidi":           `{"kind":"select","value":"a\u202eb","options":["a","b"]}`,
		"select-option-bidi":          `{"kind":"select","options":["ok","bad\u202eopt"]}`,
		"select-option-newline":       `{"kind":"select","options":["ok","bad\nopt"]}`,
		"tabs-option-bidi":            `{"kind":"tabs","options":["one","two\u202e"]}`,
	}
	for name, raw := range rejected {
		if _, err := Read([]byte(raw)); err == nil || !strings.Contains(err.Error(), "reordering") {
			t.Errorf("%s: got %v, want reordering error", name, err)
		}
	}

	valid := []string{
		`{"kind":"textarea","value":"line 1\n\tindented line 2","placeholder":"first line\n\tsecond line"}`,
		`{"kind":"input","placeholder":"Your name","value":"Ada"}`,
		`{"kind":"select","value":"M","options":["S","M","L"]}`,
		`{"kind":"tabs","value":"Overview","options":["Overview","Logs"]}`,
	}
	for _, raw := range valid {
		if _, err := Read([]byte(raw)); err != nil {
			t.Errorf("valid %s failed: %v", raw, err)
		}
	}
}

func TestRev46KindsEventsAndValidation(t *testing.T) {
	treeJSON := `{
		"kind": "column",
		"fill": true,
		"children": [
			{
				"kind": "row",
				"key": "bar",
				"variant": "toolbar",
				"children": [
					{
						"kind": "menu",
						"key": "branch",
						"text": "main",
						"icon": "git-branch",
						"variant": "ghost",
						"children": [
							{"kind": "menu_item", "key": "m1", "text": "main", "checked": true, "onTap": ["checkout", "main"]},
							{"kind": "divider"},
							{
								"kind": "menu",
								"key": "sub",
								"text": "More",
								"children": [
									{"kind": "menu_item", "key": "new", "text": "New branch", "icon": "plus", "onTap": ["askBranch"]}
								]
							}
						]
					},
					{"kind": "button", "key": "pull", "text": "Pull", "badges": [{"text": "2", "tone": "accent", "variant": "soft"}], "onTap": ["pull"]}
				]
			},
			{
				"kind": "split",
				"key": "sp",
				"direction": "auto",
				"ratio": 0.45,
				"children": [
					{
						"kind": "column",
						"key": "left",
						"children": [
							{
								"kind": "tree",
								"key": "changes",
								"children": [
									{
										"kind": "tree_item",
										"key": "f1",
										"text": "main.go",
										"icon": "file-diff",
										"check": "mixed",
										"onCheck": ["pick", "src"],
										"badges": [
											{"text": "+12", "tone": "success", "variant": "mono"},
											{"text": "-3", "tone": "danger", "variant": "mono"}
										],
										"contextMenu": "file-ctx",
										"contextArg": "src/main.go",
										"onTap": ["open", "src/main.go"]
									}
								]
							},
							{"kind": "checkbox", "key": "amend", "text": "Amend", "check": "off", "onChange": ["toggleAmend"]},
							{"kind": "checkbox", "key": "defcheck", "text": "Default off", "onChange": ["toggleDef"]}
						]
					},
					{
						"kind": "dag",
						"key": "history",
						"value": "c1",
						"maxHeight": 500,
						"contextMenu": "commit-ctx",
						"onChange": ["selectCommit"],
						"rows": [
							{
								"id": "c1",
								"parents": ["c0"],
								"text": "Initial commit",
								"detail": "Ada",
								"meta": "2m",
								"strong": true,
								"tone": "accent",
								"icon": "git-commit-horizontal",
								"badges": [{"text": "HEAD -> main", "tone": "accent", "variant": "soft", "icon": "git-branch"}]
							},
							{"id": "c0", "text": "Root"}
						]
					}
				]
			},
			{
				"kind": "dialog",
				"key": "dlg",
				"text": "Discard changes?",
				"variant": "danger",
				"onChange": ["closeDialog"],
				"children": [
					{"kind": "text", "text": "Are you sure?"},
					{"kind": "button", "key": "ok", "text": "Discard", "variant": "danger", "onTap": ["confirmDiscard"]}
				]
			},
			{
				"kind": "menu",
				"key": "file-ctx",
				"variant": "context",
				"children": [
					{"kind": "menu_item", "key": "discard", "text": "Discard", "tone": "danger", "onTap": ["discardFile", "worktree"]},
					{
						"kind": "menu",
						"key": "more",
						"text": "More",
						"children": [
							{"kind": "menu_item", "key": "ignore", "text": "Ignore", "onTap": ["ignoreFile"]}
						]
					}
				]
			},
			{
				"kind": "menu",
				"key": "commit-ctx",
				"variant": "context",
				"children": [
					{"kind": "menu_item", "key": "copy", "text": "Copy hash", "onTap": ["copyHash"]}
				]
			},
			{
				"kind": "menu",
				"key": "unused-ctx",
				"variant": "context",
				"children": [
					{"kind": "menu_item", "key": "never", "text": "Never", "onTap": ["never"]}
				]
			}
		]
	}`

	tr, err := Read([]byte(treeJSON))
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	// Verify parsed fields.
	bar := nodeOf(tr.Root, "/bar")
	if bar == nil || bar.GetVariant() != "toolbar" {
		t.Fatalf("toolbar = %+v", bar)
	}
	sp := nodeOf(tr.Root, "/sp")
	if sp == nil || sp.GetDirection() != "auto" || sp.GetRatio() != 0.45 {
		t.Fatalf("split = %+v", sp)
	}
	f1 := nodeOf(tr.Root, "/sp/left/changes/f1")
	if f1 == nil || f1.GetCheck() != "mixed" || !f1.GetChecks() || len(f1.GetBadges()) != 2 || f1.GetContextMenu() != "file-ctx" || f1.GetContextArg() != "src/main.go" {
		t.Fatalf("tree_item = %+v", f1)
	}
	defCheck := nodeOf(tr.Root, "/sp/left/defcheck")
	if defCheck == nil || defCheck.GetCheck() != "off" {
		t.Fatalf("default checkbox check = %+v", defCheck)
	}
	hist := nodeOf(tr.Root, "/sp/history")
	if hist == nil || len(hist.GetRows()) != 2 || !hist.GetRows()[0].GetStrong() || hist.GetRows()[0].GetBadges()[0].GetText() != "HEAD -> main" {
		t.Fatalf("dag = %+v", hist)
	}

	// Dropdown menu_item tap does NOT append value.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/bar/branch/m1", Kind: protocol.UiTap, Value: "ignored"}); err != nil || m.Name != "checkout" || len(m.Args) != 1 || string(m.Args[0]) != `"main"` {
		t.Fatalf("dropdown menu_item tap = %+v, %v", m, err)
	}

	// Context menu_item tap validates opener arg and appends it last.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/file-ctx/discard", Kind: protocol.UiTap, Value: "src/main.go"}); err != nil || m.Name != "discardFile" || len(m.Args) != 2 || string(m.Args[0]) != `"worktree"` || string(m.Args[1]) != `"src/main.go"` {
		t.Fatalf("context menu_item tap = %+v, %v", m, err)
	}
	// Context submenu item tap also validates opener arg and appends it last.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/file-ctx/more/ignore", Kind: protocol.UiTap, Value: "src/main.go"}); err != nil || m.Name != "ignoreFile" || len(m.Args) != 1 || string(m.Args[0]) != `"src/main.go"` {
		t.Fatalf("context submenu_item tap = %+v, %v", m, err)
	}
	// Context menu_item tap with forged arg is refused.
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/file-ctx/discard", Kind: protocol.UiTap, Value: "../../etc/passwd"}); err == nil {
		t.Fatal("forged context_arg was accepted")
	}
	// Context menu_item tap on unreferenced context menu is refused.
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/unused-ctx/never", Kind: protocol.UiTap, Value: ""}); err == nil {
		t.Fatal("unreferenced context menu tap was accepted")
	}

	// Dag context menu_item tap accepts any row id in the dag and rejects unknown ids.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/commit-ctx/copy", Kind: protocol.UiTap, Value: "c1"}); err != nil || m.Name != "copyHash" || len(m.Args) != 1 || string(m.Args[0]) != `"c1"` {
		t.Fatalf("dag context menu tap = %+v, %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/commit-ctx/copy", Kind: protocol.UiTap, Value: "deadbeef"}); err == nil {
		t.Fatal("forged dag row context_arg was accepted")
	}

	// tree_item check event (UiCheck).
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/sp/left/changes/f1", Kind: protocol.UiCheck, Value: "on"}); err != nil || m.Name != "pick" || len(m.Args) != 2 || string(m.Args[0]) != `"src"` || string(m.Args[1]) != `"on"` {
		t.Fatalf("tree_item check = %+v, %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/sp/left/changes/f1", Kind: protocol.UiCheck, Value: "mixed"}); err == nil {
		t.Fatal("invalid check event value 'mixed' was accepted")
	}

	// checkbox change event & Values().
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/sp/left/amend", Kind: protocol.UiChange, Value: "on"}); err != nil || m.Name != "toggleAmend" || len(m.Args) != 1 || string(m.Args[0]) != `"on"` {
		t.Fatalf("checkbox change = %+v, %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/sp/left/amend", Kind: protocol.UiChange, Value: "true"}); err == nil {
		t.Fatal("invalid checkbox change value 'true' was accepted")
	}
	if vals, err := tr.Values(map[string]string{"/sp/left/amend": "on"}); err != nil || vals["/sp/left/amend"] != "on" {
		t.Fatalf("checkbox form values = %v, %v", vals, err)
	}

	// dialog dismiss event.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/dlg", Kind: protocol.UiChange, Value: "dismiss"}); err != nil || m.Name != "closeDialog" || len(m.Args) != 1 || string(m.Args[0]) != `"dismiss"` {
		t.Fatalf("dialog dismiss = %+v, %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/dlg", Kind: protocol.UiChange, Value: "other"}); err == nil {
		t.Fatal("invalid dialog change value was accepted")
	}

	// dag row selection event.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/sp/history", Kind: protocol.UiChange, Value: "c0"}); err != nil || m.Name != "selectCommit" || len(m.Args) != 1 || string(m.Args[0]) != `"c0"` {
		t.Fatalf("dag select = %+v, %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/sp/history", Kind: protocol.UiChange, Value: "missing"}); err == nil {
		t.Fatal("dag unknown row selection was accepted")
	}
}

func TestRev46Rejections(t *testing.T) {
	var manyBadges []string
	for i := range maxBadges + 1 {
		manyBadges = append(manyBadges, fmt.Sprintf(`{"text":"b%d"}`, i))
	}
	var manyRowBadges []string
	for i := range maxDagRowBadges + 1 {
		manyRowBadges = append(manyRowBadges, fmt.Sprintf(`{"text":"b%d"}`, i))
	}
	var manyRows []string
	for i := range maxDagRows + 1 {
		manyRows = append(manyRows, fmt.Sprintf(`{"id":"r%d","text":"t"}`, i))
	}
	var manyParents []string
	for i := range maxDagParents + 1 {
		manyParents = append(manyParents, fmt.Sprintf(`"p%d"`, i))
	}

	cases := map[string]struct{ tree, want string }{
		"scroll-auto":          {`{"kind":"scroll","direction":"auto","children":[]}`, "not vertical or horizontal"},
		"split-bad-dir":        {`{"kind":"split","direction":"diagonal","children":[{"kind":"text","text":"a"},{"kind":"text","text":"b"}]}`, "vertical, horizontal or auto"},
		"menu-blank":           {`{"kind":"menu","children":[]}`, "menu requires text or icon"},
		"menu-ctx-no-key":      {`{"kind":"column","children":[{"kind":"menu","variant":"context","children":[]}]}`, "context menu requires a valid key"},
		"menu-ctx-dup-key":     {`{"kind":"column","children":[{"kind":"menu","key":"m","variant":"context","children":[]},{"kind":"column","children":[{"kind":"menu","key":"m","variant":"context","children":[]}]}]}`, "duplicate context menu key"},
		"menu-ctx-nested":      {`{"kind":"menu","text":"M","children":[{"kind":"menu","key":"c","variant":"context","children":[]}]}`, "context menu cannot be nested"},
		"menu-depth-3":         {`{"kind":"menu","text":"L1","children":[{"kind":"menu","text":"L2","children":[{"kind":"menu","text":"L3","children":[]}]}]}`, "menu nesting exceeds depth 2"},
		"menu-bad-child":       {`{"kind":"menu","text":"M","children":[{"kind":"text","text":"nope"}]}`, "must be a menu_item, divider or menu"},
		"menu-onTap":           {`{"kind":"menu","text":"M","onTap":["x"],"children":[]}`, "sends no messages"},
		"menu-item-root":       {`{"kind":"menu_item","text":"x"}`, "must be inside a menu"},
		"menu-item-in-col":     {`{"kind":"column","children":[{"kind":"menu_item","text":"x"}]}`, "must be inside a menu"},
		"menu-item-blank":      {`{"kind":"menu","text":"M","children":[{"kind":"menu_item","text":""}]}`, "menu_item requires text"},
		"menu-item-onChange":   {`{"kind":"menu","text":"M","children":[{"kind":"menu_item","text":"a","onChange":["x"]}]}`, "takes onTap, not onChange"},
		"menu-item-children":   {`{"kind":"menu","text":"M","children":[{"kind":"menu_item","text":"a","children":[]}]}`, ""},
		"menu-item-has-child":  {`{"kind":"menu","text":"M","children":[{"kind":"menu_item","text":"a","children":[{"kind":"text","text":"b"}]}]}`, "holds no children"},
		"checkbox-bad-check":   {`{"kind":"checkbox","text":"A","check":"yes"}`, "not on, off or mixed"},
		"checkbox-onTap":       {`{"kind":"checkbox","text":"A","onTap":["x"]}`, "onChange, not onTap"},
		"checkbox-onCheck":     {`{"kind":"checkbox","text":"A","check":"on","onCheck":["x"]}`, "only a tree_item takes onCheck"},
		"check-on-text":        {`{"kind":"text","text":"A","check":"on"}`, "only a checkbox or tree_item"},
		"onCheck-no-check":     {`{"kind":"tree","children":[{"kind":"tree_item","text":"a","onCheck":["x"]}]}`, "onCheck requires check"},
		"dialog-two":           {`{"kind":"column","children":[{"kind":"dialog","text":"1","children":[]},{"kind":"dialog","text":"2","children":[]}]}`, "at most one dialog"},
		"dialog-nested":        {`{"kind":"dialog","text":"1","children":[{"kind":"dialog","text":"2","children":[]}]}`, "at most one dialog"},
		"dialog-bad-variant":   {`{"kind":"dialog","text":"1","variant":"primary","children":[]}`, "takes no variant"},
		"dialog-onTap":         {`{"kind":"dialog","text":"1","onTap":["x"],"children":[]}`, "onChange, not onTap"},
		"badge-too-many":       {`{"kind":"text","text":"x","badges":[` + strings.Join(manyBadges, ",") + `]}`, "at most 8 badges"},
		"badge-empty":          {`{"kind":"text","text":"x","badges":[{}]}`, "requires text or icon"},
		"badge-too-long":       {`{"kind":"text","text":"x","badges":[{"text":"` + strings.Repeat("a", maxBadgeText+1) + `"}]}`, "over 32 runes"},
		"badge-bad-variant":    {`{"kind":"text","text":"x","badges":[{"text":"a","variant":"neon"}]}`, "not soft, solid, outline or mono"},
		"badge-bad-tone":       {`{"kind":"text","text":"x","badges":[{"text":"a","tone":"#f00"}]}`, "colour token"},
		"badge-spoof":          {`{"kind":"text","text":"x","badges":[{"text":"a\u202eb"}]}`, "reordering"},
		"ctx-unknown":          {`{"kind":"text","text":"x","contextMenu":"nope"}`, "unknown context menu"},
		"ctx-arg-no-menu":      {`{"kind":"text","text":"x","contextArg":"abc"}`, "contextArg requires contextMenu"},
		"ctx-arg-long":         {`{"kind":"column","children":[{"kind":"text","text":"x","contextMenu":"m","contextArg":"` + strings.Repeat("a", maxContextArg+1) + `"},{"kind":"menu","key":"m","variant":"context","children":[]}]}`, "over 256 characters"},
		"dag-too-many-rows":    {`{"kind":"dag","rows":[` + strings.Join(manyRows, ",") + `]}`, "at most 1000 rows"},
		"dag-dup-id":           {`{"kind":"dag","rows":[{"id":"a","text":"1"},{"id":"a","text":"2"}]}`, "duplicated"},
		"dag-empty-id":         {`{"kind":"dag","rows":[{"id":"","text":"1"}]}`, "invalid id"},
		"dag-long-id":          {`{"kind":"dag","rows":[{"id":"` + strings.Repeat("a", maxDagID+1) + `","text":"1"}]}`, "invalid id"},
		"dag-too-many-p":       {`{"kind":"dag","rows":[{"id":"a","parents":[` + strings.Join(manyParents, ",") + `],"text":"1"}]}`, "more than 8 parents"},
		"dag-long-text":        {`{"kind":"dag","rows":[{"id":"a","text":"` + strings.Repeat("x", maxDagText+1) + `"}]}`, "over 320 characters"},
		"dag-too-many-b":       {`{"kind":"dag","rows":[{"id":"a","text":"1","badges":[` + strings.Join(manyRowBadges, ",") + `]}]}`, "at most 6 badges"},
		"dag-onTap":            {`{"kind":"dag","onTap":["x"],"rows":[]}`, "onChange, not onTap"},
		"dag-children":         {`{"kind":"dag","rows":[],"children":[{"kind":"text","text":"x"}]}`, "holds no children"},
		"rows-on-text":         {`{"kind":"text","text":"x","rows":[{"id":"a","text":"1"}]}`, "only a dag has rows"},
		"dag-bad-maxHeight":    {`{"kind":"dag","maxHeight":-5,"rows":[]}`, "maxHeight is from 0 to 4000"},
		"placeholder-too-long": {`{"kind":"button","icon":"plus","placeholder":"` + strings.Repeat("a", maxPlaceholder+1) + `"}`, "placeholder is over 256 characters"},
	}
	for name, c := range cases {
		_, err := Read([]byte(c.tree))
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want substring %q", name, err, c.want)
		}
	}
}

func TestPlaceholderPreservedAcrossKinds(t *testing.T) {
	tr, err := Read([]byte(`{"kind":"column","children":[
		{"kind":"button","key":"b","icon":"refresh-cw","placeholder":"Refresh","onTap":["r"]},
		{"kind":"menu","key":"m","icon":"ellipsis","placeholder":"Git actions","children":[{"kind":"menu_item","text":"Fetch","onTap":["f"]}]},
		{"kind":"tree","key":"t","children":[{"kind":"tree_item","key":"ti","text":"a.go","placeholder":"Commit a.go","check":"on","onCheck":["p"]}]},
		{"kind":"checkbox","key":"cb","text":"Amend","placeholder":"Amend last commit","check":"off","onChange":["a"]},
		{"kind":"icon","key":"ic","icon":"info","placeholder":"Details"},
		{"kind":"pill","key":"pl","text":"ok","placeholder":"All checks passed"},
		{"kind":"chip","key":"ch","text":"main","placeholder":"Current branch"},
		{"kind":"input","key":"in","placeholder":"Type here","onChange":["c"]},
		{"kind":"textarea","key":"ta","placeholder":"Commit message","onChange":["c"]}
	]}`))
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	want := map[string]string{
		"/b":    "Refresh",
		"/m":    "Git actions",
		"/t/ti": "Commit a.go",
		"/cb":   "Amend last commit",
		"/ic":   "Details",
		"/pl":   "All checks passed",
		"/ch":   "Current branch",
		"/in":   "Type here",
		"/ta":   "Commit message",
	}
	got := map[string]string{}
	var walk func(n *protocol.UiNode)
	walk = func(n *protocol.UiNode) {
		if n.GetPlaceholder() != "" {
			got[n.GetId()] = n.GetPlaceholder()
		}
		for _, c := range n.GetChildren() {
			walk(c)
		}
	}
	walk(tr.Root)
	for id, wantPH := range want {
		if got[id] != wantPH {
			t.Errorf("node %s placeholder = %q, want %q", id, got[id], wantPH)
		}
	}
}

func TestRev49ComponentsAndValidation(t *testing.T) {
	tr, err := Read([]byte(`{"kind":"column","children":[
		{"kind":"text","key":"h","text":"Heading","variant":"heading"},
		{"kind":"text","key":"m","text":"0xdeadbeef","variant":"mono"},
		{"kind":"text","key":"c","text":"Updated 1m ago","variant":"caption"},
		{"kind":"card","key":"crd","text":"Summary","icon":"activity","badges":[{"text":"live","tone":"success"}],"children":[
			{"kind":"stat","key":"st","text":"Pass rate","value":"99.4%","placeholder":"+0.3% vs yesterday","icon":"check","tone":"success","badges":[{"text":"24h"}],"onTap":["openStat"]}
		]},
		{"kind":"alert","key":"al","text":"Rate limit low","placeholder":"12 requests left","tone":"warning","variant":"outline","icon":"alert-triangle","onChange":["dismissAlert"],"children":[
			{"kind":"button","key":"retry","text":"Upgrade","onTap":["upgrade"]}
		]},
		{"kind":"empty","key":"em","text":"No deployments","placeholder":"Trigger a build to see results","icon":"rocket","children":[
			{"kind":"button","key":"go","text":"Deploy","onTap":["deploy"]}
		]},
		{"kind":"diff","key":"df","diffFrom":"a/main.go","diffTo":"b/main.go","language":"go","variant":"unified","maxHeight":320,"onChange":["pickLine"],"diffLines":[
			{"op":"@","text":"@@ -1,2 +1,2 @@"},
			{"op":" ","text":"package main","oldLine":1,"newLine":1},
			{"op":"-","text":"var x = 1","oldLine":2},
			{"op":"+","text":"var x = 2","newLine":2}
		]},
		{"kind":"radio","key":"rd","value":"fast","options":["fast","balanced","thorough"],"direction":"horizontal","onChange":["setMode"]},
		{"kind":"slider","key":"sl","text":"Concurrency","value":"4","min":1,"max":16,"step":1,"onChange":["setConc"]},
		{"kind":"kbd","key":"kb","options":["Cmd","K"]},
		{"kind":"skeleton","key":"sk","variant":"rect","size":"large","maxHeight":48},
		{"kind":"row","children":[{"kind":"text","text":"L"},{"kind":"spacer","key":"sp"},{"kind":"text","text":"R"}]},
		{"kind":"input","key":"srch","value":"foo","variant":"search","icon":"search","placeholder":"Search…","onChange":["q"]},
		{"kind":"input","key":"pw","value":"secret","variant":"password","placeholder":"Token","onChange":["pw"]},
		{"kind":"textarea","key":"ta","value":"SELECT 1","variant":"mono","onChange":["sql"]},
		{"kind":"tabs","key":"tb","value":"Diff","options":["Diff","Logs"],"variant":"underline","onChange":["tab"]},
		{"kind":"progress","key":"pr","progress":0.75,"variant":"ring","text":"75%"},
		{"kind":"code","key":"cd","text":"func main() {}","language":"go","maxHeight":200},
		{"kind":"table","key":"tbl","labels":["Name","Duration"],"value":"Duration:desc","variant":"striped","onChange":["sort"],"children":[
			{"kind":"row","children":[{"kind":"text","text":"build"},{"kind":"text","text":"12s"}]}
		]}
	]}`))
	if err != nil {
		t.Fatalf("Read rev 49 tree failed: %v", err)
	}

	// Verify event validation for new/updated kinds.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/al", Kind: protocol.UiChange, Value: "dismiss"}); err != nil || m.Name != "dismissAlert" {
		t.Fatalf("alert dismiss: %+v %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/al", Kind: protocol.UiChange, Value: "other"}); err == nil {
		t.Error("alert accepted non-dismiss value")
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/df", Kind: protocol.UiChange, Value: "2"}); err != nil || m.Name != "pickLine" || string(m.Args[0]) != "2" {
		t.Fatalf("diff change: %+v %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/df", Kind: protocol.UiChange, Value: "99"}); err == nil {
		t.Error("diff accepted out-of-range line index")
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/rd", Kind: protocol.UiChange, Value: "thorough"}); err != nil || string(m.Args[0]) != `"thorough"` {
		t.Fatalf("radio change: %+v %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/rd", Kind: protocol.UiChange, Value: "nope"}); err == nil {
		t.Error("radio accepted unknown option")
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/sl", Kind: protocol.UiChange, Value: "8"}); err != nil || string(m.Args[0]) != "8" {
		t.Fatalf("slider change: %+v %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/sl", Kind: protocol.UiChange, Value: "999"}); err == nil {
		t.Error("slider accepted out-of-range value")
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/tbl", Kind: protocol.UiChange, Value: "Name:asc"}); err != nil || string(m.Args[0]) != `"Name:asc"` {
		t.Fatalf("table sort: %+v %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/tbl", Kind: protocol.UiChange, Value: "Missing:asc"}); err == nil {
		t.Error("table accepted unknown sort column")
	}

	// Verify radio and slider participate in form Values().
	vals, err := tr.Values(map[string]string{"/rd": "balanced", "/sl": "12", "/srch": "bar"})
	if err != nil || vals["/rd"] != "balanced" || vals["/sl"] != "12" || vals["/srch"] != "bar" {
		t.Fatalf("Values = %v, err = %v", vals, err)
	}

	// Verify invalid inputs are rejected.
	bad := map[string]string{
		"slider-min-ge-max": `{"kind":"slider","min":10,"max":10,"value":"10"}`,
		"slider-bad-step":   `{"kind":"slider","min":0,"max":10,"step":-1,"value":"5"}`,
		"slider-onTap":      `{"kind":"slider","min":0,"max":10,"onTap":["x"]}`,
		"min-on-text":       `{"kind":"text","text":"hi","min":1}`,
		"radio-empty-opts":  `{"kind":"radio","options":[]}`,
		"diff-bad-op":       `{"kind":"diff","diffLines":[{"op":"bogus","text":"x"}]}`,
		"diff-onTap":        `{"kind":"diff","onTap":["x"]}`,
		"kbd-onTap":         `{"kind":"kbd","text":"K","onTap":["x"]}`,
		"spacer-children":   `{"kind":"spacer","children":[{"kind":"text","text":"x"}]}`,
		"skeleton-children": `{"kind":"skeleton","children":[{"kind":"text","text":"x"}]}`,
		"alert-bad-var":     `{"kind":"alert","text":"hi","variant":"neon"}`,
		"skeleton-bad-var":  `{"kind":"skeleton","variant":"blob"}`,
	}
	for name, raw := range bad {
		if _, err := Read([]byte(raw)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestGraphM1ValidationAndEvents(t *testing.T) {
	raw := `{
		"kind": "column",
		"children": [
			{
				"kind": "graph",
				"key": "map",
				"variant": "cards",
				"layout": "hierarchy",
				"minimap": true,
				"fill": true,
				"value": "a",
				"onChange": ["selectNode"],
				"onDoubleTap": ["jumpNode"],
				"onDrop": ["savePos"],
				"contextMenu": "graph-ctx",
				"graphNodes": [
					{
						"id": "a",
						"label": "Coordinator",
						"group": "core",
						"tone": "accent",
						"subtitle": "claude · main",
						"weight": 2.5,
						"shape": "card",
						"icon": "bot",
						"status": "working",
						"badges": [{"text": "main", "tone": "accent", "variant": "soft"}],
						"x": 12.5,
						"y": -8.25,
						"hasPos": true,
						"pinned": true,
						"contextMenu": "node-ctx",
						"contextArg": "sess:a"
					},
					{
						"id": "b",
						"label": "Worker",
						"group": "core",
						"shape": "circle",
						"status": "waiting"
					}
				],
				"graphEdges": [
					{"from": "a", "to": "b", "label": "msg", "tone": "accent", "dashed": true, "active": true, "kind": "agentMessage"}
				]
			},
			{
				"kind": "menu",
				"key": "graph-ctx",
				"variant": "context",
				"children": [
					{"kind": "menu_item", "key": "inspect", "text": "Inspect", "onTap": ["inspectNode"]}
				]
			},
			{
				"kind": "menu",
				"key": "node-ctx",
				"variant": "context",
				"children": [
					{"kind": "menu_item", "key": "kill", "text": "Kill", "tone": "danger", "onTap": ["killNode"]}
				]
			}
		]
	}`
	tr, err := Read([]byte(raw))
	if err != nil {
		t.Fatalf("Read M1 graph tree failed: %v", err)
	}
	g := nodeOf(tr.Root, "/map")
	if g == nil || g.GetVariant() != "cards" || g.GetGraphLayout() != "hierarchy" || !g.GetMinimap() || !g.GetFill() || !g.GetDoubleTap() || !g.GetDrop() {
		t.Fatalf("parsed graph node = %+v", g)
	}
	if len(g.GetGraphNodes()) != 2 || g.GetGraphNodes()[0].GetShape() != "card" || g.GetGraphNodes()[0].GetStatus() != "working" || !g.GetGraphNodes()[0].GetHasPos() || !g.GetGraphNodes()[0].GetPinned() || g.GetGraphNodes()[0].GetContextMenu() != "node-ctx" {
		t.Fatalf("parsed graphNodes = %+v", g.GetGraphNodes())
	}
	if len(g.GetGraphEdges()) != 1 || !g.GetGraphEdges()[0].GetActive() || g.GetGraphEdges()[0].GetKind() != "agentMessage" {
		t.Fatalf("parsed graphEdges = %+v", g.GetGraphEdges())
	}

	// Verify UiOp diff/apply preserves new fields.
	next := proto.CloneOf(tr.Root)
	ng := nodeOf(next, "/map")
	ng.GraphLayout = "radial"
	ng.Minimap = false
	ng.GraphNodes[1].Status = "done"
	ng.GraphEdges[0].Active = false
	ops := protocol.DiffUi(tr.Root, next)
	applied, err := protocol.ApplyUi(tr.Root, ops)
	if err != nil || !proto.Equal(applied, next) {
		t.Fatalf("ApplyUi mismatch: err=%v equal=%v", err, proto.Equal(applied, next))
	}

	// UiChange validation.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/map", Kind: protocol.UiChange, Value: "b"}); err != nil || m.Name != "selectNode" || string(m.Args[0]) != `"b"` {
		t.Fatalf("graph change = %+v, %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/map", Kind: protocol.UiChange, Value: "unknown"}); err == nil {
		t.Fatal("accepted UiChange for unknown node id")
	}

	// UiDoubleTap validation.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/map", Kind: protocol.UiDoubleTap, Value: "a"}); err != nil || m.Name != "jumpNode" || string(m.Args[0]) != `"a"` {
		t.Fatalf("graph doubleTap = %+v, %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/map", Kind: protocol.UiDoubleTap, Value: "missing"}); err == nil {
		t.Fatal("accepted UiDoubleTap for unknown node id")
	}

	// UiDrop JSON object payload validation.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/map", Kind: protocol.UiDrop, Value: `{"id":"a","x":42.5,"y":-10}`}); err != nil || m.Name != "savePos" || len(m.Args) != 1 || string(m.Args[0]) != `{"id":"a","x":42.5,"y":-10}` {
		t.Fatalf("graph drop = %+v, %v", m, err)
	}
	for _, badDrop := range []string{
		`not-json`,
		`{"id":"missing","x":1,"y":2}`,
		`{"id":"a","x":1e9,"y":2}`,
		`{"id":"a","x":1,"y":2,"extra":true}`,
	} {
		if _, err := tr.Message(&protocol.BlockEvent{Node: "/map", Kind: protocol.UiDrop, Value: badDrop}); err == nil {
			t.Errorf("accepted invalid graph drop payload %q", badDrop)
		}
	}

	// Node-level & graph-level contextMenu args validation.
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/node-ctx/kill", Kind: protocol.UiTap, Value: "sess:a"}); err != nil || m.Name != "killNode" || string(m.Args[0]) != `"sess:a"` {
		t.Fatalf("node contextMenu tap = %+v, %v", m, err)
	}
	if _, err := tr.Message(&protocol.BlockEvent{Node: "/node-ctx/kill", Kind: protocol.UiTap, Value: "forged"}); err == nil {
		t.Fatal("accepted forged contextArg on node contextMenu")
	}
	if m, err := tr.Message(&protocol.BlockEvent{Node: "/graph-ctx/inspect", Kind: protocol.UiTap, Value: "b"}); err != nil || m.Name != "inspectNode" || string(m.Args[0]) != `"b"` {
		t.Fatalf("graph contextMenu tap = %+v, %v", m, err)
	}

	// Rejection cases.
	bad := map[string]string{
		"bad-layout":       `{"kind":"graph","layout":"grid","graphNodes":[]}`,
		"layout-non-graph": `{"kind":"text","text":"x","layout":"force"}`,
		"minimap-non-gr":   `{"kind":"text","text":"x","minimap":true}`,
		"dbltap-non-graph": `{"kind":"button","text":"x","onDoubleTap":["x"]}`,
		"bad-variant":      `{"kind":"graph","variant":"neon","graphNodes":[]}`,
		"bad-shape":        `{"kind":"graph","graphNodes":[{"id":"a","label":"A","shape":"hex"}]}`,
		"bad-status":       `{"kind":"graph","graphNodes":[{"id":"a","label":"A","status":"flying"}]}`,
		"bad-icon":         `{"kind":"graph","graphNodes":[{"id":"a","label":"A","icon":"Bad!"}]}`,
		"haspos-no-xy":     `{"kind":"graph","graphNodes":[{"id":"a","label":"A","hasPos":true}]}`,
		"bad-coord":        `{"kind":"graph","graphNodes":[{"id":"a","label":"A","x":1e9,"y":0}]}`,
		"ctxarg-no-menu":   `{"kind":"graph","graphNodes":[{"id":"a","label":"A","contextArg":"x"}]}`,
		"unknown-ctxmenu":  `{"kind":"graph","graphNodes":[{"id":"a","label":"A","contextMenu":"nope"}]}`,
		"bad-edge-kind":    `{"kind":"graph","graphNodes":[{"id":"a","label":"A"}],"graphEdges":[{"from":"a","to":"a","kind":"bad kind!"}]}`,
	}
	for name, tc := range bad {
		if _, err := Read([]byte(tc)); err == nil {
			t.Errorf("%s: expected rejection error, got nil", name)
		}
	}
}

func TestGraphM1DropQuotedMaxID(t *testing.T) {
	id := strings.Repeat("\"", maxGraphID)
	treeObj := map[string]any{
		"kind": "column",
		"children": []map[string]any{
			{
				"kind":   "graph",
				"key":    "map",
				"onDrop": []string{"savePos"},
				"graphNodes": []map[string]any{
					{"id": id, "label": "Quoted Max ID"},
				},
			},
		},
	}
	raw, err := json.Marshal(treeObj)
	if err != nil {
		t.Fatalf("marshal tree: %v", err)
	}
	tr, err := Read(raw)
	if err != nil {
		t.Fatalf("Read failed for 128-byte quoted node id: %v", err)
	}
	payload, err := json.Marshal(struct {
		ID string  `json:"id"`
		X  float64 `json:"x"`
		Y  float64 `json:"y"`
	}{ID: id, X: 12.34, Y: 56.78})
	if err != nil {
		t.Fatalf("marshal drop payload: %v", err)
	}
	m, err := tr.Message(&protocol.BlockEvent{
		Node:  "/map",
		Kind:  protocol.UiDrop,
		Value: string(payload),
	})
	if err != nil {
		t.Fatalf("UiDrop failed for valid 128-byte quoted node id (%d bytes): %v", len(payload), err)
	}
	if m.Name != "savePos" || len(m.Args) != 1 {
		t.Fatalf("unexpected drop message: %+v", m)
	}
	var got struct {
		ID string  `json:"id"`
		X  float64 `json:"x"`
		Y  float64 `json:"y"`
	}
	if err := json.Unmarshal(m.Args[0], &got); err != nil {
		t.Fatalf("unmarshal drop arg: %v", err)
	}
	if got.ID != id || got.X != 12.34 || got.Y != 56.78 {
		t.Fatalf("drop arg = %+v, want id=%d-quotes x=12.34 y=56.78", got, len(id))
	}
}
