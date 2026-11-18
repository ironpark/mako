package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

// An agent's block goes as the right panel's last tab, shown, over the
// document, never into it; where the user allowed it elsewhere, there;
// and with no agents' blocks the document drawn is the document itself.
func TestAgentBlocksAreOverlaid(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	base := ggui.Peek(m.WinDoc)
	if m.overlaid(base) != base {
		t.Fatal("no blocks changed the document")
	}
	m.setAgentBlocks([]*protocol.AgentBlock{{Name: "agent:s1/form", SessionId: "s1", Agent: "claude", Title: "Order"}})
	d := ggui.Peek(m.WinDoc)
	path := d.BlockPath("agent:s1/form")
	if len(path) < 2 || path[len(path)-2].ID != layoutdoc.DetailsNode {
		t.Fatalf("placed at %v", path)
	}
	if ggui.Peek(m.DetailTab) != "agent:s1/form" {
		t.Fatalf("the right panel shows %q", ggui.Peek(m.DetailTab))
	}
	if m.winBase.BlockPath("agent:s1/form") != nil || base.BlockPath("agent:s1/form") != nil {
		t.Fatal("the document itself holds the block")
	}
	if _, ok := m.blocks.lookup("agent:s1/form"); !ok {
		t.Fatal("the block is not registered")
	}
	m.setAgentBlocks([]*protocol.AgentBlock{
		{Name: "agent:s1/form", SessionId: "s1"},
		{Name: "agent:s1/board", SessionId: "s1", Document: "default", Parent: "workspace", Index: -1, Size: 180},
	})
	d = ggui.Peek(m.WinDoc)
	if path := d.BlockPath("agent:s1/board"); len(path) < 2 || path[len(path)-2].ID != "workspace" || path[len(path)-1].Size != 180 {
		t.Fatalf("placed at %v", path)
	}
	// Placed in another document than the one drawn, it goes to the right
	// panel.
	if op := placeOp(d, &protocol.AgentBlock{Name: "agent:s1/x", Document: "other", Parent: "workspace"}, "default"); op.Parent != layoutdoc.DetailsNode {
		t.Fatalf("%+v", op)
	}
	m.setAgentBlocks(nil)
	d = ggui.Peek(m.WinDoc)
	if d.BlockPath("agent:s1/form") != nil || d.BlockPath("agent:s1/board") != nil {
		t.Fatal("closed blocks stayed")
	}
	if _, ok := m.blocks.lookup("agent:s1/form"); ok {
		t.Fatal("a closed block stayed registered")
	}
}

// An agent's block is framed with its agent and session, not a plugin.
func TestAgentBlockFrame(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	tree := &protocol.UiNode{Kind: "column", Children: []*protocol.UiNode{
		{Id: "/name", Kind: "input", Text: "Your name"},
		{Id: "/send", Kind: "button", Text: "Send", Tap: true},
	}}
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return blockFrame(m, "agent:1a2b3c4d5e6f/order", blockOrigin{session: "1a2b3c4d5e6f", agent: "claude"}, "Order",
			&protocol.BlockUpdate{SessionId: "1a2b3c4d5e6f", Agent: "claude", State: protocol.PluginRunning, Tree: tree}, newBlockForm())
	}, ggui.Sz(400, 300))
	defer p.Close()
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	for _, want := range []string{"Order", "Agent", "claude · 1a2b3c4d", "Send"} {
		if !strings.Contains(sem, want) {
			t.Errorf("no %q in:\n%s", want, sem)
		}
	}
	if strings.Contains(sem, "Plugin") {
		t.Error("an agent's block is framed as a plugin's")
	}
}

// A block's fields keep what the user set across updates, give way to a
// value the tree gives anew, and are what an event carries.
func TestBlockFormKeepsFields(t *testing.T) {
	f := newBlockForm()
	f.redraw()
	name := f.field("/name", "")
	name.Set("Ada")
	f.field("/gift", "false").Set("true")
	f.redraw()
	if f.field("/name", "").Get() != "Ada" {
		t.Fatal("an update lost what was typed")
	}
	f.field("/gift", "false")
	if v := f.values(); v["/name"] != "Ada" || v["/gift"] != "true" || len(v) != 2 {
		t.Fatalf("%v", v)
	}
	f.redraw()
	if f.field("/name", "Grace").Get() != "Grace" {
		t.Fatal("a value the tree gave anew did not replace the field")
	}
	if v := f.values(); len(v) != 1 {
		t.Fatalf("a field no longer drawn is sent: %v", v)
	}
}
