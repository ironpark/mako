package protocol

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestDiffApplyUiRev46Fields(t *testing.T) {
	before := &UiNode{
		Id:        "",
		Kind:      "split",
		Direction: "auto",
		Ratio:     0.45,
		Children: []*UiNode{
			{
				Id:          "/changes",
				Kind:        "tree_item",
				Text:        "src/main.go",
				Check:       "on",
				Checks:      true,
				Placeholder: "Commit src/main.go",
				ContextMenu: "change-file",
				ContextArg:  "src/main.go",
				Badges: []*UiBadge{
					{Text: "+12", Tone: "success", Variant: "mono"},
					{Text: "-3", Tone: "danger", Variant: "mono"},
				},
				Children: []*UiNode{
					{Id: "/changes/sub", Kind: "tree_item", Text: "kept"},
				},
			},
			{
				Id:          "/history",
				Kind:        "dag",
				Value:       "aaa111",
				ContextMenu: "commit",
				MaxHeight:   400,
				Change:      true,
				Rows: []*UiRow{
					{
						Id:      "aaa111",
						Parents: []string{"bbb222"},
						Text:    "Initial commit",
						Detail:  "Ada",
						Meta:    "2m",
						Tone:    "accent",
						Strong:  true,
						Icon:    "git-commit-horizontal",
						Badges: []*UiBadge{
							{Text: "HEAD -> main", Tone: "accent", Variant: "soft", Icon: "git-branch"},
						},
					},
					{
						Id:   "bbb222",
						Text: "Root",
					},
				},
			},
		},
	}

	// Equal trees produce no ops.
	if ops := DiffUi(before, proto.CloneOf(before)); len(ops) != 0 {
		t.Fatalf("equal trees produced %d ops", len(ops))
	}

	// Mutate every new rev-46 field (badges, check, checks, context_menu, context_arg, rows).
	next := proto.CloneOf(before)
	ti := next.Children[0]
	ti.Check = "mixed"
	ti.Checks = false
	ti.Placeholder = "Commit src"
	ti.ContextMenu = "change-dir"
	ti.ContextArg = "src"
	ti.Badges = []*UiBadge{
		{Text: "+20", Tone: "success", Variant: "mono"},
	}

	dag := next.Children[1]
	dag.Value = "ccc333"
	dag.Rows = []*UiRow{
		{
			Id:      "ccc333",
			Parents: []string{"aaa111", "ddd444"},
			Text:    "Merge feature",
			Detail:  "Grace",
			Meta:    "now",
			Strong:  true,
			Badges: []*UiBadge{
				{Text: "main", Tone: "success", Variant: "solid"},
				{Text: "v1.0", Tone: "warning", Variant: "outline"},
			},
		},
		{
			Id:      "aaa111",
			Parents: []string{"bbb222"},
			Text:    "Initial commit",
			Detail:  "Ada",
			Meta:    "3m",
		},
	}

	ops := DiffUi(before, next)
	if len(ops) != 2 {
		t.Fatalf("expected 2 update ops, got %d: %+v", len(ops), ops)
	}
	for _, op := range ops {
		if op.GetOp() != UiUpdate {
			t.Fatalf("expected update op, got %q", op.GetOp())
		}
		if len(op.GetNode().GetChildren()) != 0 {
			t.Fatalf("update op for %q should not carry children", op.GetId())
		}
	}

	got, err := ApplyUi(before, ops)
	if err != nil {
		t.Fatalf("ApplyUi failed: %v", err)
	}
	if !proto.Equal(got, next) {
		t.Fatalf("ApplyUi mismatch:\ngot:  %v\nwant: %v", got, next)
	}
	// Verify child of updated tree_item was preserved.
	if len(got.Children[0].Children) != 1 || got.Children[0].Children[0].GetText() != "kept" {
		t.Fatalf("children not preserved on update: %+v", got.Children[0].Children)
	}

	// Clearing repeated Badges and Rows back to empty must also diff and apply cleanly.
	cleared := proto.CloneOf(next)
	cleared.Children[0].Badges = nil
	cleared.Children[0].Check = ""
	cleared.Children[0].Placeholder = ""
	cleared.Children[0].ContextMenu = ""
	cleared.Children[0].ContextArg = ""
	cleared.Children[1].Rows = nil

	ops2 := DiffUi(next, cleared)
	if len(ops2) != 2 {
		t.Fatalf("expected 2 update ops when clearing fields, got %d", len(ops2))
	}
	got2, err := ApplyUi(next, ops2)
	if err != nil {
		t.Fatalf("ApplyUi clear failed: %v", err)
	}
	if !proto.Equal(got2, cleared) {
		t.Fatalf("ApplyUi clear mismatch:\ngot:  %v\nwant: %v", got2, cleared)
	}
}

func TestDiffUiDetectsSingleFieldChange(t *testing.T) {
	base := &UiNode{
		Id:          "",
		Kind:        "tree_item",
		Text:        "file.go",
		Check:       "off",
		Checks:      true,
		ContextMenu: "file",
		ContextArg:  "file.go",
		Badges:      []*UiBadge{{Text: "+1", Tone: "success", Variant: "mono"}},
		Rows:        []*UiRow{{Id: "r1", Parents: []string{"p1"}, Text: "row 1", Badges: []*UiBadge{{Text: "tag"}}}},
	}

	mutations := map[string]func(*UiNode){
		"badge-text":    func(n *UiNode) { n.Badges[0].Text = "+2" },
		"badge-tone":    func(n *UiNode) { n.Badges[0].Tone = "danger" },
		"badge-variant": func(n *UiNode) { n.Badges[0].Variant = "solid" },
		"badge-icon":    func(n *UiNode) { n.Badges[0].Icon = "tag" },
		"check":         func(n *UiNode) { n.Check = "on" },
		"checks":        func(n *UiNode) { n.Checks = false },
		"placeholder":   func(n *UiNode) { n.Placeholder = "Commit file.go" },
		"context-menu":  func(n *UiNode) { n.ContextMenu = "dir" },
		"context-arg":   func(n *UiNode) { n.ContextArg = "other.go" },
		"row-parent":    func(n *UiNode) { n.Rows[0].Parents = append(n.Rows[0].Parents, "p2") },
		"row-strong":    func(n *UiNode) { n.Rows[0].Strong = true },
		"row-badge":     func(n *UiNode) { n.Rows[0].Badges[0].Text = "v2" },
		"row-icon":      func(n *UiNode) { n.Rows[0].Icon = "star" },
		"row-meta":      func(n *UiNode) { n.Rows[0].Meta = "5m" },
		"row-detail":    func(n *UiNode) { n.Rows[0].Detail = "author" },
		"row-tone":      func(n *UiNode) { n.Rows[0].Tone = "muted" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			next := proto.CloneOf(base)
			mutate(next)
			ops := DiffUi(base, next)
			if len(ops) != 1 || ops[0].GetOp() != UiUpdate {
				t.Fatalf("%s: got ops %+v, want 1 update op", name, ops)
			}
			got, err := ApplyUi(base, ops)
			if err != nil {
				t.Fatalf("%s: ApplyUi error: %v", name, err)
			}
			if !proto.Equal(got, next) {
				t.Fatalf("%s: ApplyUi mismatch", name)
			}
		})
	}
}
