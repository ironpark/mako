package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/uiblock"
)

func TestLayoutDocTier1(t *testing.T) {
	cat := layoutdoc.BuiltinCatalog()
	def := layoutdoc.Default()
	rawDef, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal Default: %v", err)
	}

	t.Run("parse_canonical_pages", func(t *testing.T) {
		parsed, err := layoutdoc.Parse(rawDef, cat)
		if err != nil {
			t.Fatalf("Parse(Default): %v", err)
		}
		canon, canonDoc, err := layoutdoc.Canonical(rawDef, cat)
		if err != nil {
			t.Fatalf("Canonical: %v", err)
		}
		if len(canon) == 0 || canonDoc.Node(layoutdoc.DetailsNode) == nil {
			t.Fatalf("unexpected canonical result: len=%d doc=%+v", len(canon), canonDoc)
		}
		pages := layoutdoc.Pages(parsed)
		if len(pages) == 0 {
			t.Fatal("expected Pages(Default) to return at least 1 page")
		}
		if len(layoutdoc.Builtin()) == 0 {
			t.Fatal("expected non-empty layoutdoc.Builtin()")
		}
	})

	t.Run("valid_apply_ops_and_input_immutability", func(t *testing.T) {
		orig := layoutdoc.Default()
		origBytes, err := json.Marshal(orig)
		if err != nil {
			t.Fatal(err)
		}

		idx0 := 0
		newSize := 360.0
		ops := []layoutdoc.Op{
			{Op: "insert", Parent: layoutdoc.DetailsNode, Index: &idx0, Node: layoutdoc.BlockAvatar},
			{Op: "select", ID: layoutdoc.DetailsNode, Block: layoutdoc.BlockAvatar},
			{Op: "resize", ID: layoutdoc.SidebarNode, Size: &newSize},
			{Op: "move", ID: layoutdoc.BlockForge, Parent: layoutdoc.DetailsNode, Index: &idx0},
			{Op: "remove", ID: layoutdoc.BlockForge},
		}
		next, err := layoutdoc.Apply(orig, ops, cat)
		if err != nil {
			t.Fatalf("Apply valid ops: %v", err)
		}
		if got := next.Node(layoutdoc.DetailsNode).ActiveBlock(); got != layoutdoc.BlockAvatar {
			t.Fatalf("active block = %q, want %q", got, layoutdoc.BlockAvatar)
		}
		if next.Node(layoutdoc.SidebarNode).Size != newSize {
			t.Fatalf("sidebar size = %v, want %v", next.Node(layoutdoc.SidebarNode).Size, newSize)
		}
		if next.Node(layoutdoc.BlockForge) != nil {
			t.Fatal("expected BlockForge to be removed")
		}

		// Verify input Doc was not mutated by Apply.
		afterBytes, err := json.Marshal(orig)
		if err != nil {
			t.Fatal(err)
		}
		if string(origBytes) != string(afterBytes) {
			t.Fatalf("Apply mutated input Doc:\nbefore=%s\nafter=%s", origBytes, afterBytes)
		}
	})

	t.Run("invalid_ops_rejected", func(t *testing.T) {
		orig := layoutdoc.Default()
		badCases := []struct {
			name string
			ops  []layoutdoc.Op
		}{
			{"empty_ops", nil},
			{"remove_root", []layoutdoc.Op{{Op: "remove", ID: "window"}}},
			{"remove_main_panes", []layoutdoc.Op{{Op: "remove", ID: layoutdoc.MainPanes}}},
			{"duplicate_id", []layoutdoc.Op{{Op: "insert", Parent: layoutdoc.DetailsNode, Node: layoutdoc.BlockFiles}}},
			{"unknown_block_props", []layoutdoc.Op{{Op: "insert", Parent: layoutdoc.DetailsNode, Node: map[string]any{"id": "x", "block": layoutdoc.BlockAvatar, "props": map[string]any{"bogus": 1}}}}},
			{"unknown_op", []layoutdoc.Op{{Op: "teleport", ID: layoutdoc.DetailsNode}}},
		}
		for _, tc := range badCases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := layoutdoc.Apply(orig, tc.ops, cat); err == nil {
					t.Fatalf("expected Apply(%s) to return error", tc.name)
				}
			})
		}
	})
}

func TestLayoutDocTier2SelectPanicsB8(t *testing.T) {
	cat := layoutdoc.BuiltinCatalog()

	safeApply := func(t *testing.T, d *layoutdoc.Doc, ops []layoutdoc.Op) (*layoutdoc.Doc, error) {
		t.Helper()
		var out *layoutdoc.Doc
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("unexpected panic (B8): %v", r)
				}
			}()
			out, err = layoutdoc.Apply(d, ops, cat)
		}()
		return out, err
	}

	t.Run("B8_remove_all_children_then_select_by_block", func(t *testing.T) {
		ops := []layoutdoc.Op{
			{Op: "remove", ID: layoutdoc.BlockFiles},
			{Op: "remove", ID: layoutdoc.BlockGit},
			{Op: "remove", ID: layoutdoc.BlockAgents},
			{Op: "remove", ID: layoutdoc.BlockForge},
			{Op: "select", ID: layoutdoc.DetailsNode, Block: layoutdoc.BlockFiles},
		}
		_, err := safeApply(t, layoutdoc.Default(), ops)
		if !t.Failed() && err == nil {
			t.Fatal("expected error selecting from emptied stack, got nil")
		}
	})

	t.Run("B8_select_oob_active_then_select_by_block", func(t *testing.T) {
		oob := 99
		ops := []layoutdoc.Op{
			{Op: "select", ID: layoutdoc.DetailsNode, Active: &oob},
			{Op: "select", ID: layoutdoc.DetailsNode, Block: layoutdoc.BlockAvatar},
		}
		_, err := safeApply(t, layoutdoc.Default(), ops)
		if !t.Failed() && err == nil {
			t.Fatal("expected error with OOB Active before select by Block, got nil")
		}
	})

	t.Run("B8_empty_stack_in_doc_then_select_by_block", func(t *testing.T) {
		doc := &layoutdoc.Doc{
			Version: 1,
			Root: &layoutdoc.Node{
				Kind: layoutdoc.KindRow,
				ID:   "root",
				Children: []*layoutdoc.Node{
					{Kind: layoutdoc.KindPanes, ID: layoutdoc.MainPanes, Panes: layoutdoc.MainPanes},
					{Kind: layoutdoc.KindStack, ID: "empty_stack"},
				},
			},
		}
		_, err := safeApply(t, doc, []layoutdoc.Op{
			{Op: "select", ID: "empty_stack", Block: layoutdoc.BlockGit},
		})
		if !t.Failed() && err == nil {
			t.Fatal("expected error selecting from empty stack in Doc, got nil")
		}
	})
}

func TestUIBlockTier1(t *testing.T) {
	richJSON := `{
		"kind": "column",
		"gap": 8,
		"children": [
			{
				"kind": "row",
				"children": [
					{"kind": "text", "key": "hdr", "text": "Header"},
					{"kind": "toggle", "key": "flag", "text": "Enabled", "checked": true, "onChange": ["toggled"]}
				]
			},
			{
				"kind": "split",
				"ratio": 0.4,
				"children": [
					{
						"kind": "tree",
						"key": "nav",
						"children": [
							{
								"kind": "tree_item",
								"key": "root",
								"text": "src",
								"value": "src",
								"onTap": ["open", "src"],
								"children": [
									{"kind": "tree_item", "key": "main", "text": "main.go", "value": "src/main.go", "onTap": ["open", "src/main.go"]}
								]
							}
						]
					},
					{
						"kind": "grid",
						"columns": 2,
						"children": [
							{"kind": "input", "key": "search", "placeholder": "Find...", "value": "ok", "onChange": ["search"], "onSubmit": ["submitSearch"]},
							{"kind": "textarea", "key": "notes", "value": "line1\n\tindented", "onChange": ["notes"]},
							{"kind": "select", "key": "env", "options": ["dev", "prod"], "value": "dev", "onChange": ["pickEnv"]},
							{"kind": "tabs", "key": "view", "options": ["Board", "List"], "value": "Board", "onChange": ["pickView"]}
						]
					}
				]
			},
			{
				"kind": "chart",
				"key": "metrics",
				"chart": "bar",
				"labels": ["Mon", "Tue"],
				"series": [{"name": "Runs", "tone": "accent", "values": [3, 7]}]
			},
			{
				"kind": "graph",
				"key": "deps",
				"graphNodes": [{"id": "a", "label": "A"}, {"id": "b", "label": "B"}],
				"graphEdges": [{"from": "a", "to": "b", "label": "uses"}]
			},
			{
				"kind": "markdown",
				"key": "doc",
				"text": "# Title\n\n[Open](mako:open)"
			}
		]
	}`

	var tr *uiblock.Tree
	t.Run("read_rich_tree", func(t *testing.T) {
		var err error
		tr, err = uiblock.Read([]byte(richJSON))
		if err != nil {
			t.Fatalf("uiblock.Read(richJSON): %v", err)
		}
		if tr.Root == nil || tr.Root.GetKind() != uiblock.KindColumn {
			t.Fatalf("unexpected root: %+v", tr.Root)
		}
	})
	if tr == nil {
		return
	}

	t.Run("message_tap_change_submit", func(t *testing.T) {
		tapMsg, err := tr.Message(&protocol.BlockEvent{Block: "plugin:test/ui", Node: "/#1/nav/root/main", Kind: protocol.UiTap})
		if err != nil || tapMsg.Name != "open" || tapMsg.Kind != uiblock.KindTreeItem {
			t.Fatalf("tap Message = %+v, err=%v", tapMsg, err)
		}

		toggleMsg, err := tr.Message(&protocol.BlockEvent{Block: "plugin:test/ui", Node: "/#0/flag", Kind: protocol.UiChange, Value: "false"})
		if err != nil || toggleMsg.Name != "toggled" || len(toggleMsg.Args) != 1 || string(toggleMsg.Args[0]) != "false" {
			t.Fatalf("toggle Message = %+v, err=%v", toggleMsg, err)
		}

		submitMsg, err := tr.Message(&protocol.BlockEvent{Block: "plugin:test/ui", Node: "/#1/#1/search", Kind: protocol.UiSubmit, Value: "query"})
		if err != nil || submitMsg.Name != "submitSearch" || len(submitMsg.Args) != 1 || string(submitMsg.Args[0]) != `"query"` {
			t.Fatalf("submit Message = %+v, err=%v", submitMsg, err)
		}
	})

	t.Run("values_filtering_and_non_input_rejection", func(t *testing.T) {
		vals, err := tr.Values(map[string]string{
			"/#1/#1/search": "abc",
			"/#1/#1/notes":  "row1\n\trow2",
			"/#1/#1/env":    "prod",
			"/#0/flag":      "true",
			"/gone/node":    "ignored",
		})
		if err != nil {
			t.Fatalf("Values: %v", err)
		}
		if vals["/#1/#1/notes"] != "row1\n\trow2" || vals["/#1/#1/env"] != "prod" {
			t.Fatalf("unexpected Values map: %+v", vals)
		}
		if _, exists := vals["/gone/node"]; exists {
			t.Fatal("expected gone node to be omitted from Values")
		}
		// Values for a non-input node (e.g. text) must be rejected.
		if _, err := tr.Values(map[string]string{"/#0/hdr": "spoof"}); err == nil {
			t.Fatal("expected Values on text node to fail")
		}
	})
}

func TestUIBlockTier2SpoofingB20(t *testing.T) {
	spoofs := []struct {
		name  string
		token string
	}{
		{"bidi_rlo", "safe\u202eevil"},
		{"ansi_esc", "safe\x1b[31mred"},
		{"nul_byte", "safe\x00nul"},
		{"zero_width", "safe\u200bhidden"},
	}

	makeDoc := func(nodeJSON string) []byte {
		return []byte(`{"kind":"column","children":[` + nodeJSON + `]}`)
	}

	for _, sp := range spoofs {
		q, _ := json.Marshal(sp.token)
		tok := string(q)

		cases := []struct {
			field string
			json  []byte
		}{
			{"input.placeholder", makeDoc(`{"kind":"input","key":"k","placeholder":` + tok + `}`)},
			{"textarea.placeholder", makeDoc(`{"kind":"textarea","key":"k","placeholder":` + tok + `}`)},
			{"input.value", makeDoc(`{"kind":"input","key":"k","value":` + tok + `}`)},
			{"textarea.value", makeDoc(`{"kind":"textarea","key":"k","value":` + tok + `}`)},
			{"select.value", makeDoc(`{"kind":"select","key":"k","options":["a","b"],"value":` + tok + `}`)},
			{"tabs.value", makeDoc(`{"kind":"tabs","key":"k","options":["a",` + tok + `],"value":` + tok + `}`)},
			{"select.options", makeDoc(`{"kind":"select","key":"k","options":["a",` + tok + `],"value":"a"}`)},
			{"tabs.options", makeDoc(`{"kind":"tabs","key":"k","options":["a",` + tok + `],"value":"a"}`)},
			{"text.text", makeDoc(`{"kind":"text","text":` + tok + `}`)},
			{"tree_item.value", makeDoc(`{"kind":"tree","children":[{"kind":"tree_item","text":"item","value":` + tok + `}]}`)},
			{"breadcrumb.options", makeDoc(`{"kind":"breadcrumb","options":["root",` + tok + `]}`)},
		}

		for _, tc := range cases {
			t.Run(sp.name+"/"+tc.field, func(t *testing.T) {
				if _, err := uiblock.Read(tc.json); err == nil {
					t.Fatalf("uiblock.Read unexpectedly accepted %s in %s (B20)", sp.name, tc.field)
				}
			})
		}
	}

	t.Run("textarea_allows_newline_and_tab", func(t *testing.T) {
		doc := makeDoc(`{"kind":"textarea","key":"notes","placeholder":"line1\n\tline2","value":"hello\n\tworld"}`)
		tr, err := uiblock.Read(doc)
		if err != nil {
			t.Fatalf("expected valid newline/tab in textarea to be allowed, got: %v", err)
		}
		if tr == nil || !strings.Contains(tr.Root.GetChildren()[0].GetValue(), "\n\t") {
			t.Fatalf("unexpected textarea value: %+v", tr)
		}
	})
}
