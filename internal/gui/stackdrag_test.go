package gui

import (
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/layoutdoc"
)

func TestStackTabReorderAndVerticalSplitAndMerge(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	dispose := ggui.Root(func() { followWindowLayout(m) })
	t.Cleanup(dispose)

	// Start with default details stack: [mako.files, mako.git, mako.agents, mako.forge].
	doc := ggui.Peek(m.WinDoc)
	det := doc.Node(layoutdoc.DetailsNode)
	if det == nil || det.Kind != layoutdoc.KindStack || len(det.Children) != 4 {
		t.Fatalf("unexpected initial details node: %+v", det)
	}

	// 1. Reorder: drag mako.forge (index 3) before mako.files (index 0).
	m.moveStackBlock(layoutdoc.DetailsNode, forgeTab, layoutdoc.DetailsNode, 0)
	doc = ggui.Peek(m.WinDoc)
	det = doc.Node(layoutdoc.DetailsNode)
	if det.Kind != layoutdoc.KindStack || det.Children[0].Block != forgeTab || det.Children[1].Block != filesTab {
		t.Fatalf("reorder failed: got %+v", det.Children)
	}
	if ggui.Peek(m.DetailTab) != forgeTab {
		t.Fatalf("expected active tab %q after reorder, got %q", forgeTab, ggui.Peek(m.DetailTab))
	}

	// 2. Vertical split: drag mako.git to the bottom of details.
	m.splitStackBlock(layoutdoc.DetailsNode, gitTab, layoutdoc.DetailsNode, false)
	doc = ggui.Peek(m.WinDoc)
	det = doc.Node(layoutdoc.DetailsNode)
	if det == nil || det.Kind != layoutdoc.KindColumn || !det.Collapsible || det.Size != layoutdoc.DefaultDetailsSize {
		t.Fatalf("expected details to become collapsible column, got %+v", det)
	}
	if len(det.Children) != 2 {
		t.Fatalf("expected 2 split stacks in details column, got %d", len(det.Children))
	}
	top, bottom := det.Children[0], det.Children[1]
	if top.Kind != layoutdoc.KindStack || bottom.Kind != layoutdoc.KindStack {
		t.Fatalf("expected two stacks, got %s and %s", top.Kind, bottom.Kind)
	}
	if top.Grow <= 0 || bottom.Size <= 0 {
		t.Fatalf("expected top grow>0 (%v) and bottom size>0 (%v) for horizontal divider", top.Grow, bottom.Size)
	}
	if len(top.Children) != 3 || len(bottom.Children) != 1 || bottom.Children[0].Block != gitTab {
		t.Fatalf("unexpected split contents: top=%d bottom=%+v", len(top.Children), bottom.Children)
	}
	// Both the surviving active tab on top and git on bottom are shown at the same time.
	if !ggui.Untrack(func() bool { return m.blockShown(gitTab) }) {
		t.Fatal("git tab in bottom split stack should be shown")
	}
	if !ggui.Untrack(func() bool { return m.blockShown(forgeTab) }) {
		t.Fatal("forge tab in top split stack should remain shown")
	}

	// 3. Cross-stack move: move mako.agents into the bottom stack.
	m.moveStackBlock(top.ID, agentsTab, bottom.ID, 1)
	doc = ggui.Peek(m.WinDoc)
	det = doc.Node(layoutdoc.DetailsNode)
	top, bottom = det.Children[0], det.Children[1]
	if len(top.Children) != 2 || len(bottom.Children) != 2 || bottom.Children[1].Block != agentsTab {
		t.Fatalf("cross-stack move failed: top=%d bottom=%d", len(top.Children), len(bottom.Children))
	}

	// 4. Merging all tabs back collapses the split column back into a single details stack.
	m.moveStackBlock(bottom.ID, gitTab, top.ID, -1)
	doc = ggui.Peek(m.WinDoc)
	det = doc.Node(layoutdoc.DetailsNode)
	top, bottom = det.Children[0], det.Children[1]
	m.moveStackBlock(bottom.ID, agentsTab, top.ID, -1)

	doc = ggui.Peek(m.WinDoc)
	det = doc.Node(layoutdoc.DetailsNode)
	if det == nil || det.Kind != layoutdoc.KindStack || !det.Collapsible || len(det.Children) != 4 {
		t.Fatalf("expected split column to collapse back to single details stack, got %+v", det)
	}
}

func TestSplitTopAndPlacePluginBlockIntoSplitDetails(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	dispose := ggui.Root(func() { followWindowLayout(m) })
	t.Cleanup(dispose)

	// Split mako.agents to the top of details.
	m.splitStackBlock(layoutdoc.DetailsNode, agentsTab, layoutdoc.DetailsNode, true)
	doc := ggui.Peek(m.WinDoc)
	det := doc.Node(layoutdoc.DetailsNode)
	if det == nil || det.Kind != layoutdoc.KindColumn || len(det.Children) != 2 {
		t.Fatalf("expected split column, got %+v", det)
	}
	if det.Children[0].Children[0].Block != agentsTab {
		t.Fatalf("expected agents in top stack, got %+v", det.Children[0].Children)
	}

	// Splitting a single-tab stack onto itself must be a no-op.
	m.splitStackBlock(det.Children[0].ID, agentsTab, det.Children[0].ID, false)
	if len(ggui.Peek(m.WinDoc).Node(layoutdoc.DetailsNode).Children) != 2 {
		t.Fatal("splitting a 1-tab stack with itself should be a no-op")
	}

	// Placing a plugin block in "right-stack" still works when details is split,
	// and removing it cleanly preserves or collapses the layout.
	m.placePluginBlock("plugin:okf/vault", "right-stack")
	doc = ggui.Peek(m.WinDoc)
	if doc.BlockPath("plugin:okf/vault") == nil {
		t.Fatal("expected plugin:okf/vault placed inside split details")
	}
	m.removePluginBlock("plugin:okf/vault")
	if ggui.Peek(m.WinDoc).BlockPath("plugin:okf/vault") != nil {
		t.Fatal("expected plugin:okf/vault removed")
	}
}
