package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

// panelsText is panels as name=title:kind, joined by spaces.
func panelsText(p *protocol.Panels) string {
	var out []string
	for _, x := range p.GetPanels() {
		out = append(out, fmt.Sprintf("%s=%s:%s", x.GetBlock(), x.GetTitle(), x.GetKind()))
	}
	return strings.Join(out, " ")
}

// A phone lists a computer's panels by name and title, through its own
// daemon: the blocks the computer's documents place, a plugin's by its
// manifest's title and one it lacks as missing, then its agents'; and
// nothing of a plugin no document places. A node not let in lists none.
func TestANodeLetInListsTheComputersPanels(t *testing.T) {
	p := newBlockPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server := p.server
	plugin := func(id, title string) map[string]string {
		return map[string]string{
			"plugin.toml": "id = \"" + id + "\"\nversion = \"1.0.0\"\npublisher = \"test\"\napi = 1\nentry = \"main.js\"\npermissions = [\"storage\"]\n\n[[contributes.block]]\nid = \"ui\"\ntitle = \"" + title + "\"\n",
			"main.js":     `export default { activate(m) { m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("x") }) } }`,
		}
	}
	writePlugin(t, server, "placed", plugin("placed", "Placed block"))
	writePlugin(t, server, "hidden", plugin("hidden", "Secret project tracker"))
	waitForWithin(t, 20*time.Second, "the plugins listed", func() bool { return len(server.plugins.List()) == 2 })
	doc := layoutdoc.Default()
	details := doc.Root.Children[len(doc.Root.Children)-1]
	details.Children = append(details.Children,
		&layoutdoc.Node{Kind: layoutdoc.KindBlock, ID: "p1", Block: "plugin:placed/ui"},
		&layoutdoc.Node{Kind: layoutdoc.KindBlock, ID: "p2", Block: "plugin:gone/ui"})
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.editWorkspace(&protocol.WorkspaceState{WindowLayouts: map[string]*protocol.LayoutEntry{"default": {Doc: body}}}, nil, true); err != nil {
		t.Fatal(err)
	}
	addSessionOf(t, server, "s1", "claude")
	if _, err := server.agentUI.Show("s1", "claude", "board", "Board", []byte(`{"kind":"text","text":"x"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := p.c.Panels(ctx, server.cfg.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	text := panelsText(got)
	for _, want := range []string{"mako.files=Files:builtin", "plugin:placed/ui=Placed block:plugin", "plugin:gone/ui=:missing", "agent:s1/board=Board:agent"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %s in %s", want, text)
		}
	}
	if strings.Contains(text, "hidden") || strings.Contains(text, "Secret") {
		t.Fatalf("a plugin no document places was named: %s", text)
	}
	if strings.Index(text, "agent:s1/board") < strings.Index(text, "plugin:gone/ui") {
		t.Fatalf("the agents' blocks come before the documents': %s", text)
	}
	// Only names, titles and kinds cross: nothing more of a plugin.
	for _, x := range got.GetPanels() {
		if len(x.ProtoReflect().GetUnknown()) > 0 {
			t.Fatalf("a panel carries more: %v", x)
		}
	}
	// The phone's own daemon answers for itself, with no node named.
	own, err := p.c.Panels(ctx, "")
	if err != nil || strings.Contains(panelsText(own), "plugin:") {
		t.Fatalf("the phone's own panels: %v %v", own, err)
	}
}

func TestANodeNotLetInListsNoPanels(t *testing.T) {
	phone := meshDaemon(t, "phone")
	stranger := meshDaemon(t, "stranger", p2pAddr(phone))
	waitFor(t, "the stranger among the phone's peers", func() bool { _, ok := phone.peers.get(stranger.cfg.NodeID); return ok })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if list, err := dialDaemon(t, phone).Panels(ctx, stranger.cfg.NodeID); err == nil {
		t.Fatalf("listed a stranger's panels: %v", list)
	}
}
