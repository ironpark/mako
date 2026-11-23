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
