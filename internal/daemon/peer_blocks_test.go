package daemon

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// A phone's node, let in by a computer, watches and acts on the
// computer's blocks through its own daemon, which routes the requests by
// node_id (revision 30): a node let in starts sessions there already.

// blockPair is a computer that lets a phone in, and the phone's front-end.
type blockPair struct {
	phone, server *Daemon
	c             *client.Client
}

func newBlockPair(t *testing.T) blockPair {
	t.Helper()
	phone := meshDaemon(t, "phone")
	server := meshDaemon(t, "server", p2pAddr(phone))
	letIn(t, server, phone)
	waitFor(t, "the server among the phone's peers", func() bool { _, ok := phone.peers.get(server.config().NodeID); return ok })
	return blockPair{phone: phone, server: server, c: dialDaemon(t, phone)}
}

// followBlock watches a block of node through c, applying the updates to
// the tree, which each update it hands on holds whole.
func followBlock(ctx context.Context, c *client.Client, node, block string) (<-chan *protocol.BlockUpdate, <-chan error) {
	return followBlockScoped(ctx, c, node, block, "")
}

func followBlockScoped(ctx context.Context, c *client.Client, node, block, scope string) (<-chan *protocol.BlockUpdate, <-chan error) {
	got := make(chan *protocol.BlockUpdate, 16)
	ended := make(chan error, 2)
	go func() {
		var tree *protocol.UiNode
		ended <- c.WatchBlockOnScoped(ctx, node, block, scope, func(u *protocol.BlockUpdate) {
			switch {
			case u.GetTree() != nil:
				tree = u.GetTree()
			case len(u.GetOps()) > 0:
				next, err := protocol.ApplyUi(tree, u.GetOps())
				if err != nil {
					ended <- err
					return
				}
				tree = next
			}
			got <- &protocol.BlockUpdate{Block: u.GetBlock(), Scope: u.GetScope(), Tree: tree, Plugin: u.GetPlugin(), SessionId: u.GetSessionId(),
				Agent: u.GetAgent(), Title: u.GetTitle(), State: u.GetState(), Gone: u.GetGone()}
		})
	}()
	return got, ended
}

// untilUpdate reads updates until one is as wanted.
func untilUpdate(ctx context.Context, t *testing.T, got <-chan *protocol.BlockUpdate, ended <-chan error, what string, wanted func(*protocol.BlockUpdate) bool) *protocol.BlockUpdate {
	t.Helper()
	for {
		select {
		case u := <-got:
			if wanted(u) {
				return u
			}
		case err := <-ended:
			t.Fatalf("the watch ended before %s: %v", what, err)
		case <-ctx.Done():
			t.Fatalf("never %s", what)
		}
	}
}

// until reads updates until one's tree holds text, or, for "", until the
// block is gone.
func until(ctx context.Context, t *testing.T, got <-chan *protocol.BlockUpdate, ended <-chan error, text string) *protocol.BlockUpdate {
	t.Helper()
	return untilUpdate(ctx, t, got, ended, "\""+text+"\"", func(u *protocol.BlockUpdate) bool {
		return text == "" && u.GetGone() || text != "" && strings.Contains(treeText(u.GetTree()), text)
	})
}

// treeText is the text of a tree's nodes, joined.
func treeText(n *protocol.UiNode) string {
	var b strings.Builder
	var walk func(*protocol.UiNode)
	walk = func(n *protocol.UiNode) {
		b.WriteString(n.GetText())
		b.WriteByte('\n')
		for _, c := range n.GetChildren() {
			walk(c)
		}
	}
	if n != nil {
		walk(n)
	}
	return b.String()
}

// The phone lists the computer's agents' blocks, watches one, taps it,
// and sees it change and close, all through its own daemon; its own node
// has none.
func TestANodeLetInFollowsAnAgentsBlock(t *testing.T) {
	p := newBlockPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addSessionOf(t, p.server, "s1", "claude")
	if _, err := p.server.agentUI.Show("s1", "claude", "board", "Board", []byte(`{"kind":"column","children":[
		{"kind":"input","key":"name","value":""},
		{"kind":"button","key":"go","text":"Go 0","onTap":["go"]}]}`)); err != nil {
		t.Fatal(err)
	}
	server := p.server.config().NodeID
	list, err := p.c.AgentBlocksOn(ctx, server)
	if err != nil || len(list.GetBlocks()) != 1 || list.GetBlocks()[0].GetName() != "agent:s1/board" {
		t.Fatalf("the server's agents' blocks: %v %v", list, err)
	}
	if own, err := p.c.AgentBlocks(ctx); err != nil || len(own.GetBlocks()) != 0 {
		t.Fatalf("the phone's own: %v %v", own, err)
	}
	got, ended := followBlock(ctx, p.c, server, "agent:s1/board")
	u := until(ctx, t, got, ended, "Go 0")
	if u.GetSessionId() != "s1" || u.GetAgent() != "claude" || u.GetTitle() != "Board" {
		t.Fatalf("the frame's origin: %v", u)
	}
	if _, err := p.c.BlockEvent(ctx, &protocol.BlockEvent{NodeId: server, Block: "agent:s1/board", Node: "/go", Kind: protocol.UiTap,
		Values: map[string]string{"/name": "Ada"}}); err != nil {
		t.Fatal(err)
	}
	pending, _ := p.server.projectAgents.messages.pending("s1")
	if len(pending) != 1 || !strings.Contains(pending[0].Text, `["go"]`) || !strings.Contains(pending[0].Text, "Ada") || pending[0].From != windowSender {
		t.Fatalf("the agent's inbox: %+v", pending)
	}
	if err := p.server.agentUI.Update("s1", "board", nil, []byte(`{"kind":"column","children":[
		{"kind":"input","key":"name","value":""},
		{"kind":"button","key":"go","text":"Go 1","onTap":["go"]}]}`)); err != nil {
		t.Fatal(err)
	}
	until(ctx, t, got, ended, "Go 1")
	if err := p.server.agentUI.Close("s1", "board"); err != nil {
		t.Fatal(err)
	}
	until(ctx, t, got, ended, "")
}

// The phone watches a plugin's block on the computer and taps it; the
// plugin, allowed on the computer, draws the tap.
func TestANodeLetInTapsAPluginsBlock(t *testing.T) {
	p := newBlockPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := filepath.Join(p.server.dir, "plugins", "hello")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"plugin.toml": "id = \"hello\"\nversion = \"1.0.0\"\npublisher = \"test\"\napi = 1\nentry = \"main.js\"\npermissions = [\"storage\"]\n\n[[contributes.block]]\nid = \"hi\"\ntitle = \"Hello\"\n",
		"main.js": `export default { activate(m) { m.blocks.register("hi", { state: { n: 0 },
			render: (s, ui) => ui.column([ui.button("Taps " + s.n, { key: "b", onTap: ["tap"] })]),
			on: { tap(s) { s.n++ } } }) } }`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	waitForWithin(t, 20*time.Second, "the plugin listed, waiting", func() bool {
		list := p.server.plugins.List()
		return len(list) == 1 && list[0].GetState() == protocol.PluginWaiting
	})
	server := p.server.config().NodeID
	got, ended := followBlock(ctx, p.c, server, "plugin:hello/hi")
	// Waiting, the phone is told so, and cannot allow it: that is the
	// computer's.
	untilUpdate(ctx, t, got, ended, "the plugin waiting", func(u *protocol.BlockUpdate) bool {
		return u.GetState() == protocol.PluginWaiting && u.GetPlugin() == "hello"
	})
	if _, err := dialDaemon(t, p.server).PluginAction(ctx, &protocol.PluginAction{PluginId: "hello", Op: protocol.PluginGrant}); err != nil {
		t.Fatal(err)
	}
	until(ctx, t, got, ended, "Taps 0")
	if _, err := p.c.BlockEvent(ctx, &protocol.BlockEvent{NodeId: server, Block: "plugin:hello/hi", Node: "/b", Kind: protocol.UiTap}); err != nil {
		t.Fatal(err)
	}
	until(ctx, t, got, ended, "Taps 1")
}

// A node that does not let the phone in shows it none of its blocks, and
// takes no event from it.
func TestANodeNotLetInReachesNoBlocks(t *testing.T) {
	phone := meshDaemon(t, "phone")
	stranger := meshDaemon(t, "stranger", p2pAddr(phone))
	waitFor(t, "the stranger among the phone's peers", func() bool { _, ok := phone.peers.get(stranger.config().NodeID); return ok })
	addSessionOf(t, stranger, "s1", "claude")
	if _, err := stranger.agentUI.Show("s1", "claude", "board", "Board", []byte(`{"kind":"button","text":"Go","onTap":["go"]}`)); err != nil {
		t.Fatal(err)
	}
	c := dialDaemon(t, phone)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	node := stranger.config().NodeID
	if list, err := c.AgentBlocksOn(ctx, node); err == nil {
		t.Errorf("listed a stranger's agents' blocks: %v", list)
	}
	if err := c.WatchBlockOn(ctx, node, "agent:s1/board", func(u *protocol.BlockUpdate) { t.Errorf("an update: %v", u) }); err == nil {
		t.Error("watched a stranger's block")
	}
	if _, err := c.BlockEvent(ctx, &protocol.BlockEvent{NodeId: node, Block: "agent:s1/board", Node: "", Kind: protocol.UiTap}); err == nil {
		t.Error("tapped a stranger's block")
	}
	if pending, _ := stranger.projectAgents.messages.pending("s1"); len(pending) != 0 {
		t.Errorf("the stranger's agent heard %+v", pending)
	}
}

// A block's updates relayed from the computer reach the phone's front-end
// in plain frames, as its socket carries them, though the computer sends
// the larger ones compressed: the app reads no compressed frame.
func TestARelayedBlockComesPlain(t *testing.T) {
	p := newBlockPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addSessionOf(t, p.server, "s1", "claude")
	long := strings.Repeat("the same line again and again\n", 900)
	tree, _ := json.Marshal(map[string]any{"kind": "markdown", "text": long})
	if _, err := p.server.agentUI.Show("s1", "claude", "notes", "Notes", tree); err != nil {
		t.Fatal(err)
	}
	stream, _, err := transport.Dial(ctx, p.phone.Socket(), &protocol.Hello{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := stream.Send(protocol.TypeWatchBlock, "", &protocol.WatchBlock{Block: "agent:s1/notes", NodeId: p.server.config().NodeID}); err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(20 * time.Second))
	var hdr [4]byte
	if _, err := io.ReadFull(stream, hdr[:]); err != nil {
		t.Fatal(err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n&(3<<30) != 0 {
		t.Fatal("a compressed frame reached the socket")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(stream, body); err != nil {
		t.Fatal(err)
	}
	env := &protocol.Envelope{}
	if err := proto.Unmarshal(body, env); err != nil {
		t.Fatal(err)
	}
	if env.GetBlockUpdate().GetTree().GetText() != long {
		t.Fatalf("the update: %v", env)
	}
}

// Remote peer forwarding preserves Scope on both WatchBlock and BlockEvent.
func TestANodeLetInWatchesScopedPluginBlock(t *testing.T) {
	p := newBlockPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dirA := t.TempDir()
	dirB := t.TempDir()
	setTestWorkspaceSpaceAndProjects(t, p.server, dirA, dirB)

	writePlugin(t, p.server, "rscope", map[string]string{
		"plugin.toml": `id = "rscope"
version = "1.0.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = "counter"
title = "Counter"
scope = "project"
`,
		"main.js": `export default {
  activate(m) {
    m.blocks.register("counter", {
      state: { n: 0 },
      render: (s, ui, ctx) => ui.column([ui.button(ctx.projectName + ":" + s.n, { key: "b", onTap: ["tap"] })]),
      on: { tap(s) { s.n++ } },
    });
  },
};`,
	})
	waitForWithin(t, 20*time.Second, "the scoped plugin ready", func() bool {
		list := p.server.plugins.List()
		return len(list) == 1 && list[0].GetState() == protocol.PluginReady
	})

	server := p.server.config().NodeID
	gotA, endedA := followBlockScoped(ctx, p.c, server, "plugin:rscope/counter", "project:p1")
	gotB, endedB := followBlockScoped(ctx, p.c, server, "plugin:rscope/counter", "project:p2")

	uA := until(ctx, t, gotA, endedA, "Alpha:0")
	if uA.GetScope() != "project:p1" {
		t.Fatalf("remote update scope = %q, want project:p1", uA.GetScope())
	}
	uB := until(ctx, t, gotB, endedB, "Beta:0")
	if uB.GetScope() != "project:p2" {
		t.Fatalf("remote update scope = %q, want project:p2", uB.GetScope())
	}

	if _, err := p.c.BlockEvent(ctx, &protocol.BlockEvent{
		NodeId: server,
		Block:  "plugin:rscope/counter",
		Scope:  "project:p1",
		Node:   "/b",
		Kind:   protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	until(ctx, t, gotA, endedA, "Alpha:1")
	if _, err := p.c.BlockEvent(ctx, &protocol.BlockEvent{
		NodeId: server,
		Block:  "plugin:rscope/counter",
		Scope:  "project:p2",
		Node:   "/b",
		Kind:   protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	until(ctx, t, gotB, endedB, "Beta:1")
}
