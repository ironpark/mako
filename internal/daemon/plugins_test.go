package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/internal/workspace"
)

// TestPluginsOverTheSocket puts a plugin in a daemon's plugins folder and
// drives it as a front-end does: listed, told of, allowed, watched, acted
// on; and its block in the catalog agents read.
func TestPluginsOverTheSocket(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	changed := make(chan *protocol.Changed, 16)
	go func() { _ = c.Watch(ctx, func(ch *protocol.Changed) { changed <- ch }) }()
	dir := filepath.Join(d.dir, "plugins", "hello")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plugin.toml": `id = "hello"
version = "1.0.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["storage"]

[[contributes.block]]
id = "hi"
title = "Hello"
`,
		"main.js": `export default { activate(m) { m.blocks.register("hi", { state: { n: 0 },
			render: (s, ui) => ui.column([ui.button("Taps " + s.n, { key: "b", onTap: ["tap"] })]),
			on: { tap(s) { s.n++ } } }) } }`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The front-end is told, and lists it, waiting for the user.
	for told := false; !told; {
		select {
		case ch := <-changed:
			told = ch.GetPlugins()
		case <-ctx.Done():
			t.Fatal("no Changed.plugins came")
		}
	}
	for {
		list, err := c.Plugins(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if p := findPlugin(list.GetPlugins(), "hello"); p != nil && p.GetState() == protocol.PluginWaiting {
			// A test reaches no keychain: the listing says secrets are
			// kept in files, and why.
			if list.GetSecrets() != "file" || list.GetSecretsNote() == "" {
				t.Fatalf("secrets %q, %q", list.GetSecrets(), list.GetSecretsNote())
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := d.blockCatalog().BlockProps("plugin:hello/hi"); !ok {
		t.Fatal("the block is not in the daemon's catalog")
	}
	if !strings.Contains(blockNames(d.blocks()), "plugin:hello/hi") {
		t.Fatalf("list_blocks: %s", blockNames(d.blocks()))
	}
	if _, err := c.PluginAction(ctx, &protocol.PluginAction{PluginId: "hello", Op: protocol.PluginGrant}); err != nil {
		t.Fatal(err)
	}
	updates := make(chan *protocol.BlockUpdate, 16)
	go func() { _ = c.WatchBlock(ctx, "plugin:hello/hi", func(u *protocol.BlockUpdate) { updates <- u }) }()
	var tree *protocol.UiNode
	wait := func(text string) {
		t.Helper()
		for {
			select {
			case u := <-updates:
				if u.GetTree() != nil {
					tree = u.GetTree()
				} else if len(u.GetOps()) > 0 {
					next, err := protocol.ApplyUi(tree, u.GetOps())
					if err != nil {
						t.Fatal(err)
					}
					tree = next
				}
				if tree != nil && tree.GetChildren()[0].GetText() == text {
					return
				}
			case <-ctx.Done():
				t.Fatalf("never %q; tree %v", text, tree)
			}
		}
	}
	wait("Taps 0")
	if _, err := c.BlockEvent(ctx, &protocol.BlockEvent{Block: "plugin:hello/hi", Node: "/b", Kind: protocol.UiTap}); err != nil {
		t.Fatal(err)
	}
	wait("Taps 1")
}

func findPlugin(list []*protocol.PluginInfo, id string) *protocol.PluginInfo {
	for _, p := range list {
		if p.GetId() == id {
			return p
		}
	}
	return nil
}

func blockNames(bs []layoutdoc.Block) string {
	var names []string
	for _, b := range bs {
		names = append(names, b.Name)
	}
	return strings.Join(names, " ")
}

// TestPeersReachNoPlugins: a node's plugins are its own front-ends' to
// list and act on; their blocks a node let in reaches too
// (peer_blocks_test.go).
func TestPeersReachNoPlugins(t *testing.T) {
	d := meshDaemon(t, "a")
	env, err := protocol.Encode(protocol.TypeListPlugins, "1", &protocol.ListPlugins{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.dispatch(context.Background(), nil, env, true); !errors.Is(err, errPluginPeer) {
		t.Fatalf("a peer's listing: %v", err)
	}
	for _, msg := range []struct {
		typ  protocol.Type
		body proto.Message
	}{
		{protocol.TypePluginAction, &protocol.PluginAction{PluginId: "x", Op: protocol.PluginGrant}},
		{protocol.TypePluginAction, &protocol.PluginAction{PluginId: "x", Op: protocol.PluginEnable}},
		{protocol.TypePluginAction, &protocol.PluginAction{PluginId: "x", Op: protocol.PluginSet, Key: "token", Value: "t"}},
	} {
		env, err := protocol.Encode(msg.typ, "1", msg.body)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.dispatch(context.Background(), nil, env, true); !errors.Is(err, errPluginPeer) {
			t.Fatalf("a peer's %s: %v", msg.typ, err)
		}
	}
}

// One watch_block serves both kinds of block: an agent's, here, as a
// plugin's above, and its event goes to the agent, not to a plugin.
func TestWatchBlockServesAgentsBlocks(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := d.agentUI.Show("s1", "claude", "b", "Board", []byte(`{"kind":"column","children":[{"kind":"button","key":"go","text":"Go","onTap":["go"]}]}`)); err != nil {
		t.Fatal(err)
	}
	list, err := c.AgentBlocks(ctx)
	if err != nil || len(list.GetBlocks()) != 1 || list.GetBlocks()[0].GetName() != "agent:s1/b" {
		t.Fatalf("%v %v", list, err)
	}
	got := make(chan *protocol.BlockUpdate, 4)
	go func() { _ = c.WatchBlock(ctx, "agent:s1/b", func(u *protocol.BlockUpdate) { got <- u }) }()
	select {
	case u := <-got:
		if u.GetTree() == nil || u.GetSessionId() != "s1" || u.GetAgent() != "claude" || u.GetTitle() != "Board" || u.GetPlugin() != "" {
			t.Fatalf("%v", u)
		}
	case <-ctx.Done():
		t.Fatal("no update")
	}
	_, err = c.BlockEvent(ctx, &protocol.BlockEvent{Block: "agent:s1/b", Node: "/go", Kind: protocol.UiTap})
	if err == nil || !strings.Contains(err.Error(), "session is gone") {
		t.Fatalf("an event of a session not running: %v", err)
	}
	if _, err := c.BlockEvent(ctx, &protocol.BlockEvent{Block: "mako.sidebar", Node: "/", Kind: protocol.UiTap}); err == nil {
		t.Fatal("an event of the window's own block went through")
	}
}

// writePlugin puts a plugin's folder in d's plugins folder whole, made
// beside it and moved in, so that the manager never reads it half
// written.
func writePlugin(t *testing.T, d *Daemon, id string, files map[string]string) {
	t.Helper()
	stage := filepath.Join(d.dir, "stage-"+id)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(stage, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(d.dir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, filepath.Join(d.dir, "plugins", id)); err != nil {
		t.Fatal(err)
	}
}

// A plugin subscribed to sessions' events hears of this node's sessions
// starting and of messages for its agents, through the notifier and the
// mailbox; of a peer's sessions, which this node lists, nothing.
func TestPluginEventsAreOfThisNodesSessions(t *testing.T) {
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, a, b)
	letIn(t, b, a)
	ca := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	writePlugin(t, a, "ears", map[string]string{
		"plugin.toml": `id = "ears"
version = "1.0.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["sessions:read", "storage"]

[[contributes.command]]
id = "go"
title = "Go"
`,
		"main.js": `let mako;
async function note(line) {
  const kept = await mako.storage.getItem({ key: "seen" });
  await mako.storage.setItem({ key: "seen", value: (kept.found ? kept.value + ";" : "") + line });
}
export default {
  activate(m) { mako = m; m.commands.register("go", () => mako.sessions.subscribe({ events: ["sessionStarted", "agentMessage"] })); },
  on: {
    sessionStarted: (e) => note("started " + e.session.id + "@" + e.session.nodeId),
    agentMessage: (e) => note("message " + e.message.toSessionId + " " + e.message.text),
  },
};`,
	})
	waitFor(t, "the plugin to be listed", func() bool {
		list, err := ca.Plugins(ctx)
		return err == nil && findPlugin(list.GetPlugins(), "ears") != nil
	})
	if _, err := ca.PluginAction(ctx, &protocol.PluginAction{PluginId: "ears", Op: protocol.PluginGrant}); err != nil {
		t.Fatal(err)
	}
	if _, err := ca.PluginAction(ctx, &protocol.PluginAction{PluginId: "ears", Op: protocol.PluginRun, Command: "go"}); err != nil {
		t.Fatal(err)
	}
	seen := func() string {
		data, _ := os.ReadFile(filepath.Join(a.dir, "plugin-data", "ears", "storage.json"))
		var kv map[string]string
		_ = json.Unmarshal(data, &kv)
		return kv["seen"]
	}
	// A peer's session, which a lists, is not a's to tell of.
	addSession(t, b, "peer-session")
	waitForWithin(t, 20*time.Second, "a to list b's session", func() bool { return lists(ctx, ca, "peer-session") })
	addSession(t, a, "own-session")
	waitForWithin(t, 10*time.Second, "the plugin to hear of a's session", func() bool { return strings.Contains(seen(), "started own-session@") })
	if _, err := a.projectAgents.messages.send("someone", "own-session", "hello there", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the plugin to hear of the message", func() bool { return strings.Contains(seen(), "message own-session hello there") })
	if strings.Contains(seen(), "peer-session") {
		t.Fatalf("the plugin heard of a peer's session: %s", seen())
	}
}

// The Host API's groups that reach the daemon are wired to it: the
// projects it knows, its sessions' transcripts, and its agents.
func TestPluginHostReachesTheDaemon(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	d.projects.set([]*protocol.ProjectScope{{Id: "p1", Name: "One", Paths: []string{root}}})
	addSession(t, d, "s1")
	writePlugin(t, d, "wired", map[string]string{
		"plugin.toml": `id = "wired"
version = "1.0.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["sessions:read", "sessions:write", "fs:project", "storage"]

[[contributes.command]]
id = "go"
title = "Go"
`,
		"main.js": `let mako;
export default {
  activate(m) {
    mako = m;
    m.commands.register("go", async () => {
      const { projects } = await mako.files.listProjects({});
      const page = await mako.sessions.readTranscript({ sessionId: "s1" });
      let sent = "";
      try { await mako.messages.sendMessage({ sessionId: "s1", text: "hi" }); } catch (e) { sent = e.message; }
      await mako.storage.setItem({ key: "seen", value: JSON.stringify([projects, page.messages.length, sent]) });
    });
  },
};`,
	})
	waitFor(t, "the plugin to be listed", func() bool {
		list, err := c.Plugins(ctx)
		return err == nil && findPlugin(list.GetPlugins(), "wired") != nil
	})
	if _, err := c.PluginAction(ctx, &protocol.PluginAction{PluginId: "wired", Op: protocol.PluginGrant}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PluginAction(ctx, &protocol.PluginAction{PluginId: "wired", Op: protocol.PluginRun, Command: "go"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(d.dir, "plugin-data", "wired", "storage.json"))
	var kv map[string]string
	_ = json.Unmarshal(data, &kv)
	want := `[[{"id":"p1","name":"One","roots":["` + root + `"]}],0,"the agent of session s1 takes no messages"]`
	if kv["seen"] != want {
		t.Fatalf("the plugin saw %s\nwant %s", kv["seen"], want)
	}
}

// A plugin declaring [[contributes.tool]] exposes it to the daemon's agent MCP
// snapshot as plugin_<id>_<tool>, and calling it executes the registered JS handler.
func TestPluginContributedMCPTools(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	writePlugin(t, d, "helper", map[string]string{
		"plugin.toml": `id = "helper"
version = "1.0.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = []

[[contributes.tool]]
id = "echo_sum"
description = "Add two numbers and echo a label"
[contributes.tool.params]
label = { type = "string", required = true }
a = { type = "number", required = true }
b = { type = "number", required = true }
`,
		"main.js": `export default {
  activate(m) {
    m.tools.register("echo_sum", (args) => ({ label: args.label, sum: Number(args.a) + Number(args.b) }));
  },
};`,
	})
	waitFor(t, "helper plugin ready", func() bool {
		list, err := c.Plugins(ctx)
		p := findPlugin(list.GetPlugins(), "helper")
		return err == nil && p != nil && p.GetState() == protocol.PluginReady
	})
	tools := d.plugins.Tools()
	if len(tools) != 1 || tools[0].PluginID != "helper" || tools[0].ID != "echo_sum" {
		t.Fatalf("plugin tools = %+v", tools)
	}
	out, err := d.plugins.CallTool(ctx, "helper", "echo_sum", []byte(`{"label":"total","a":19,"b":23}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Label string  `json:"label"`
		Sum   float64 `json:"sum"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Label != "total" || got.Sum != 42 {
		t.Fatalf("tool result = %s, err = %v", out, err)
	}
}

// setTestWorkspaceSpaceAndProjects populates the daemon's workspace with a space
// ("sp1" -> "Engineering") holding project "p1" ("Alpha", dirA) and a second
// project "p2" ("Beta", dirB) outside any space, syncing d.projects.
func setTestWorkspaceSpaceAndProjects(t *testing.T, d *Daemon, dirA, dirB string) {
	t.Helper()
	delta := &protocol.WorkspaceState{
		Spaces: map[string]*protocol.SpaceEntry{
			"sp1": {
				CreatedAt: mark(),
				Name:      &protocol.StringReg{Value: "Engineering"},
				Projects: map[string]*protocol.ElementMeta{
					"p1": {AddedAt: mark()},
				},
			},
		},
		Projects: map[string]*protocol.ProjectEntry{
			"p1": {
				CreatedAt: mark(),
				Name:      &protocol.StringReg{Value: "Alpha"},
				Roots: map[string]*protocol.ProjectRootEntry{
					"r1": {
						CreatedAt: mark(),
						Locations: map[string]*protocol.ElementMeta{
							workspace.LocationKey(d.config().NodeID, dirA): {AddedAt: mark()},
						},
					},
				},
			},
			"p2": {
				CreatedAt: mark(),
				Name:      &protocol.StringReg{Value: "Beta"},
				Roots: map[string]*protocol.ProjectRootEntry{
					"r2": {
						CreatedAt: mark(),
						Locations: map[string]*protocol.ElementMeta{
							workspace.LocationKey(d.config().NodeID, dirB): {AddedAt: mark()},
						},
					},
				},
			},
		},
	}
	if !d.workspace.edit(delta) {
		t.Fatal("workspace edit had no effect")
	}
	d.syncWorkspaceProjects()
}

// TestScopedWatchBlockAndEventIsolation verifies that WatchBlockScoped and
// BlockEvent isolate state, storage, and updates across two project scopes,
// while enriching ScopeContext via the daemon's ResolveScope callback.
func TestScopedWatchBlockAndEventIsolation(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dirA := t.TempDir()
	dirB := t.TempDir()
	setTestWorkspaceSpaceAndProjects(t, d, dirA, dirB)

	writePlugin(t, d, "scoped", map[string]string{
		"plugin.toml": `id = "scoped"
version = "1.0.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["storage"]

[[contributes.block]]
id = "board"
title = "Board"
scope = "project"
`,
		"main.js": `export default {
  activate(m) {
    m.blocks.register("board", {
      state: { count: 0 },
      async load(s, ctx) {
        const res = await ctx.storage.getItem("count");
        s.count = res.found ? Number(res.value) : 0;
      },
      render: (s, ui, ctx) => ui.column([
        ui.button(ctx.projectName + ":" + s.count + ":" + ctx.spaceName, { key: "inc", onTap: ["inc"] }),
      ]),
      on: {
        async inc(s) {
          const ctx = m.blocks.context("board");
          s.count += 1;
          await ctx.storage.setItem("count", String(s.count));
        },
      },
    });
  },
};`,
	})

	waitFor(t, "scoped plugin listed", func() bool {
		list, err := c.Plugins(ctx)
		p := findPlugin(list.GetPlugins(), "scoped")
		return err == nil && p != nil && p.GetState() == protocol.PluginWaiting
	})
	if _, err := c.PluginAction(ctx, &protocol.PluginAction{PluginId: "scoped", Op: protocol.PluginGrant}); err != nil {
		t.Fatal(err)
	}

	watchScope := func(scope string) (<-chan *protocol.BlockUpdate, <-chan error) {
		got := make(chan *protocol.BlockUpdate, 16)
		ended := make(chan error, 2)
		go func() {
			var tree *protocol.UiNode
			ended <- c.WatchBlockScoped(ctx, "plugin:scoped/board", scope, func(u *protocol.BlockUpdate) {
				if u.GetTree() != nil {
					tree = u.GetTree()
				} else if len(u.GetOps()) > 0 {
					next, err := protocol.ApplyUi(tree, u.GetOps())
					if err != nil {
						ended <- err
						return
					}
					tree = next
				}
				got <- &protocol.BlockUpdate{
					Block: u.GetBlock(),
					Scope: u.GetScope(),
					Tree:  tree,
					State: u.GetState(),
				}
			})
		}()
		return got, ended
	}

	gotA, endedA := watchScope("project:p1")
	gotB, endedB := watchScope("project:p2")

	uA := until(ctx, t, gotA, endedA, "Alpha:0:Engineering")
	if uA.GetScope() != "project:p1" {
		t.Fatalf("scope A = %q, want project:p1", uA.GetScope())
	}
	uB := until(ctx, t, gotB, endedB, "Beta:0:")
	if uB.GetScope() != "project:p2" {
		t.Fatalf("scope B = %q, want project:p2", uB.GetScope())
	}

	// Tapping p1 increments only p1.
	if _, err := c.BlockEvent(ctx, &protocol.BlockEvent{
		Block: "plugin:scoped/board",
		Scope: "project:p1",
		Node:  "/inc",
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	until(ctx, t, gotA, endedA, "Alpha:1:Engineering")

	// Ensure no stray update leaked to p2 before tapping p2.
	select {
	case leak := <-gotB:
		if strings.Contains(treeText(leak.GetTree()), "Beta:1:") || strings.Contains(treeText(leak.GetTree()), "Alpha") {
			t.Fatalf("update leaked into p2: %s", treeText(leak.GetTree()))
		}
	case <-time.After(150 * time.Millisecond):
	}

	// Tapping p2 increments only p2.
	if _, err := c.BlockEvent(ctx, &protocol.BlockEvent{
		Block: "plugin:scoped/board",
		Scope: "project:p2",
		Node:  "/inc",
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatal(err)
	}
	until(ctx, t, gotB, endedB, "Beta:1:")
}

// TestResolvePluginScopeEnrichment checks daemon-side ScopeContext resolution
// for space, project, and session scopes against workspace state, projects, and
// live sessions.
func TestResolvePluginScopeEnrichment(t *testing.T) {
	d := meshDaemon(t, "a")
	dirA := t.TempDir()
	dirB := t.TempDir()
	dirOther := t.TempDir()
	setTestWorkspaceSpaceAndProjects(t, d, dirA, dirB)

	startIn := func(id, dir string) *session.Session {
		t.Helper()
		s, err := session.Start(session.Spec{
			ID: id, NodeID: d.config().NodeID, Agent: "claude", Project: dir,
			Command: []string{"sh", "-c", "sleep 30"}, Cols: 40, Rows: 6,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		d.sessions.add(s)
		return s
	}
	startIn("sess-a", dirA)
	startIn("sess-other", dirOther)

	if sc := d.resolvePluginScope(""); sc.Scope != protocol.ScopeGlobal || sc.ScopeKey != "" {
		t.Fatalf("global scope = %+v", sc)
	}

	sp := d.resolvePluginScope("space:sp1")
	if sp.Scope != protocol.ScopeSpace || sp.ScopeKey != "space:sp1" || sp.SpaceID != "sp1" || sp.SpaceName != "Engineering" {
		t.Fatalf("space scope = %+v", sp)
	}

	p1 := d.resolvePluginScope("project:p1")
	if p1.Scope != protocol.ScopeProject || p1.ScopeKey != "project:p1" ||
		p1.ProjectID != "p1" || p1.ProjectName != "Alpha" ||
		!slices.Equal(p1.Roots, []string{dirA}) ||
		p1.SpaceID != "sp1" || p1.SpaceName != "Engineering" {
		t.Fatalf("project p1 scope = %+v", p1)
	}

	p2 := d.resolvePluginScope("project:p2")
	if p2.Scope != protocol.ScopeProject || p2.ScopeKey != "project:p2" ||
		p2.ProjectID != "p2" || p2.ProjectName != "Beta" ||
		!slices.Equal(p2.Roots, []string{dirB}) ||
		p2.SpaceID != "" || p2.SpaceName != "" {
		t.Fatalf("project p2 scope = %+v", p2)
	}

	sa := d.resolvePluginScope("session:sess-a")
	if sa.Scope != protocol.ScopeSession || sa.ScopeKey != "session:sess-a" ||
		sa.SessionID != "sess-a" || sa.ProjectID != "p1" || sa.ProjectName != "Alpha" ||
		!slices.Equal(sa.Roots, []string{dirA}) ||
		sa.SpaceID != "sp1" || sa.SpaceName != "Engineering" {
		t.Fatalf("session sess-a scope = %+v", sa)
	}

	so := d.resolvePluginScope("session:sess-other")
	if so.Scope != protocol.ScopeSession || so.ScopeKey != "session:sess-other" ||
		so.SessionID != "sess-other" || so.ProjectID != "" || so.SpaceID != "" ||
		!slices.Equal(so.Roots, []string{dirOther}) {
		t.Fatalf("session sess-other scope = %+v", so)
	}
}

// TestAgentMCPPluginToolStrictScopeBinding verifies that plugin MCP tools
// called through an agent's MCP server derive their ScopeContext strictly from
// the calling agent's session, ignoring any caller-supplied scope arguments.
func TestAgentMCPPluginToolStrictScopeBinding(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dirA := t.TempDir()
	dirB := t.TempDir()
	dirUnregistered := t.TempDir()
	setTestWorkspaceSpaceAndProjects(t, d, dirA, dirB)

	writePlugin(t, d, "scopedtools", map[string]string{
		"plugin.toml": `id = "scopedtools"
version = "1.0.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = []

[[contributes.tool]]
id = "project_echo"
description = "Echo project scope context"
scope = "project"
[contributes.tool.params]
scope = { type = "string" }
project_id = { type = "string" }

[[contributes.tool]]
id = "space_echo"
description = "Echo space scope context"
scope = "space"

[[contributes.tool]]
id = "session_echo"
description = "Echo session scope context"
scope = "session"

[[contributes.tool]]
id = "global_echo"
description = "Echo global scope context"
scope = "global"
`,
		"main.js": `export default {
  activate(m) {
    const echo = (args, ctx) => ({
      scope: ctx.scope,
      scopeKey: ctx.scopeKey,
      spaceId: ctx.spaceId,
      spaceName: ctx.spaceName,
      projectId: ctx.projectId,
      projectName: ctx.projectName,
      sessionId: ctx.sessionId,
      roots: ctx.roots,
      argScope: args && args.scope ? args.scope : "",
    });
    m.tools.register("project_echo", echo);
    m.tools.register("space_echo", echo);
    m.tools.register("session_echo", echo);
    m.tools.register("global_echo", echo);
  },
};`,
	})

	waitFor(t, "scopedtools plugin ready", func() bool {
		list, err := c.Plugins(ctx)
		p := findPlugin(list.GetPlugins(), "scopedtools")
		return err == nil && p != nil && p.GetState() == protocol.PluginReady
	})

	connectAgent := func(id, dir string) (*agentPeer, *sdk.ClientSession) {
		t.Helper()
		_, cleanup := d.projectAgents.server(ctx, "", id, "")
		t.Cleanup(cleanup)
		s, err := session.Start(session.Spec{
			ID: id, NodeID: d.config().NodeID, Agent: "claude", Project: dir,
			Command: []string{"sh", "-c", "sleep 30"}, Cols: 40, Rows: 6,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		d.sessions.add(s)
		peer := d.projectAgents.activate(s)
		if peer == nil {
			t.Fatalf("failed to activate agent peer %s", id)
		}
		srv, _ := d.newProjectAgentServer(peer, d.agentMCPSnapshot(), true)
		ct, st := sdk.NewInMemoryTransports()
		ss, err := srv.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ss.Close() })
		cli := sdk.NewClient(&sdk.Implementation{Name: "test-agent", Version: "1"}, nil)
		cs, err := cli.Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		return peer, cs
	}

	callToolCLI := func(peer *agentPeer, toolID string, args map[string]any) (map[string]any, error) {
		t.Helper()
		var rawArgs json.RawMessage
		if args != nil {
			rawArgs, _ = json.Marshal(args)
		}
		resp, err := dialAgentCLI(d.projectAgents.cliSocket, agentCLIRequest{
			Session:  peer.id,
			Token:    peer.token,
			Op:       "plugin_call",
			PluginID: "scopedtools",
			ToolID:   toolID,
			Args:     rawArgs,
		})
		if err != nil {
			return nil, err
		}
		if !resp.OK {
			return nil, errors.New(resp.Error)
		}
		var out map[string]any
		if err := json.Unmarshal(resp.Output, &out); err != nil {
			t.Fatalf("unmarshal %q: %v", string(resp.Output), err)
		}
		return out, nil
	}

	listToolIDs := func(peer *agentPeer) []string {
		t.Helper()
		resp, err := dialAgentCLI(d.projectAgents.cliSocket, agentCLIRequest{
			Session:  peer.id,
			Token:    peer.token,
			Op:       "plugin_list",
			PluginID: "scopedtools",
		})
		if err != nil || !resp.OK {
			t.Fatalf("plugin_list failed: resp=%+v err=%v", resp, err)
		}
		ids := make([]string, 0, len(resp.Tools))
		for _, tl := range resp.Tools {
			ids = append(ids, tl.ID)
		}
		slices.Sort(ids)
		return ids
	}

	// Agent in project p1 (space sp1): plugin tools are NOT exposed on MCP,
	// are all visible via mako-plugin CLI, and ignore spoofed scope args.
	peerA, csA := connectAgent("agent-a", dirA)
	mcpTools, err := csA.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range mcpTools.Tools {
		if strings.HasPrefix(tl.Name, "plugin_") {
			t.Fatalf("plugin tool %q should not be registered on MCP", tl.Name)
		}
	}
	if ids := listToolIDs(peerA); !slices.Equal(ids, []string{"global_echo", "project_echo", "session_echo", "space_echo"}) {
		t.Fatalf("agent-a visible CLI tools = %v", ids)
	}

	gotProj, err := callToolCLI(peerA, "project_echo", map[string]any{
		"scope":      "project:p2",
		"project_id": "p2",
	})
	if err != nil {
		t.Fatalf("project_echo failed: %v", err)
	}
	if gotProj["scope"] != "project" || gotProj["scopeKey"] != "project:p1" ||
		gotProj["projectId"] != "p1" || gotProj["projectName"] != "Alpha" ||
		gotProj["spaceId"] != "sp1" || gotProj["spaceName"] != "Engineering" ||
		gotProj["sessionId"] != "agent-a" {
		t.Fatalf("project_echo scope was not strictly bound to agent-a's project: %+v", gotProj)
	}

	gotSpace, err := callToolCLI(peerA, "space_echo", nil)
	if err != nil {
		t.Fatalf("space_echo failed: %v", err)
	}
	if gotSpace["scope"] != "space" || gotSpace["scopeKey"] != "space:sp1" ||
		gotSpace["spaceId"] != "sp1" || gotSpace["spaceName"] != "Engineering" {
		t.Fatalf("space_echo = %+v", gotSpace)
	}

	gotSess, err := callToolCLI(peerA, "session_echo", nil)
	if err != nil {
		t.Fatalf("session_echo failed: %v", err)
	}
	if gotSess["scope"] != "session" || gotSess["scopeKey"] != "session:agent-a" ||
		gotSess["sessionId"] != "agent-a" || gotSess["projectId"] != "p1" {
		t.Fatalf("session_echo = %+v", gotSess)
	}

	gotGlobal, err := callToolCLI(peerA, "global_echo", nil)
	if err != nil {
		t.Fatalf("global_echo failed: %v", err)
	}
	if gotGlobal["scope"] != "global" || gotGlobal["scopeKey"] != "" {
		t.Fatalf("global_echo = %+v", gotGlobal)
	}

	// Agent in an unregistered directory only sees usable global/session tools
	// in mako-plugin list and cannot invoke project/space tools even with spoofed args.
	peerOther, _ := connectAgent("agent-other", dirUnregistered)
	if ids := listToolIDs(peerOther); !slices.Equal(ids, []string{"global_echo", "session_echo"}) {
		t.Fatalf("agent-other should only see global/session tools, got %v", ids)
	}
	if got, err := callToolCLI(peerOther, "project_echo", map[string]any{
		"scope":      "project:p1",
		"project_id": "p1",
	}); err == nil {
		t.Fatalf("expected project_echo error for unregistered project, got %+v", got)
	}
	if got, err := callToolCLI(peerOther, "space_echo", nil); err == nil {
		t.Fatalf("expected space_echo error for unregistered project, got %+v", got)
	}
	gotOtherSess, err := callToolCLI(peerOther, "session_echo", nil)
	if err != nil {
		t.Fatalf("session_echo for unregistered dir failed: %v", err)
	}
	if gotOtherSess["scopeKey"] != "session:agent-other" || gotOtherSess["projectId"] != "" {
		t.Fatalf("session_echo for unregistered dir = %+v", gotOtherSess)
	}
}

// TestBundledMakoGitAutoLoadsAndHiddenFromCatalog checks that the bundled
// mako-git plugin loads automatically in Ready state without a grant prompt,
// and that its hidden panel block is excluded from list_blocks, BlockProps,
// and ListPanels while mako.git remains in the catalog.
func TestBundledMakoGitAutoLoadsAndHiddenFromCatalog(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	waitFor(t, "bundled mako-git ready", func() bool {
		list, err := c.Plugins(ctx)
		if err != nil {
			return false
		}
		p := findPlugin(list.GetPlugins(), "mako-git")
		return p != nil && p.GetBundled() && p.GetState() == protocol.PluginReady
	})

	// Hidden block must not appear in daemon catalog, BlockProps, or ListPanels.
	names := blockNames(d.blocks())
	if !strings.Contains(names, layoutdoc.BlockGit) {
		t.Fatalf("d.blocks() missing %s: %s", layoutdoc.BlockGit, names)
	}
	if strings.Contains(names, "plugin:mako-git/panel") {
		t.Fatalf("d.blocks() unexpectedly exposed hidden block: %s", names)
	}
	if _, ok := d.blockCatalog().BlockProps("plugin:mako-git/panel"); ok {
		t.Fatal("blockCatalog().BlockProps unexpectedly exposed hidden block")
	}
	for _, panel := range d.panels() {
		if panel.GetBlock() == "plugin:mako-git/panel" {
			t.Fatalf("d.panels() unexpectedly exposed hidden block: %+v", panel)
		}
	}

	// Verify MCP list_blocks tool omits plugin:mako-git/panel and includes mako.git.
	dir := t.TempDir()
	_, cleanup := d.projectAgents.server(ctx, "", "catalog-agent", "")
	t.Cleanup(cleanup)
	s, err := session.Start(session.Spec{
		ID: "catalog-agent", NodeID: d.config().NodeID, Agent: "claude", Project: dir,
		Command: []string{"sh", "-c", "sleep 30"}, Cols: 40, Rows: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	d.sessions.add(s)
	peer := d.projectAgents.activate(s)
	if peer == nil {
		t.Fatal("failed to activate agent peer")
	}
	srv, _ := d.newProjectAgentServer(peer, d.agentMCPSnapshot(), true)
	ct, st := sdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cli := sdk.NewClient(&sdk.Implementation{Name: "test-agent", Version: "1"}, nil)
	cs, err := cli.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: toolListBlocks})
	if err != nil {
		t.Fatalf("list_blocks MCP call failed: %v", err)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), layoutdoc.BlockGit) {
		t.Fatalf("MCP list_blocks missing %s: %s", layoutdoc.BlockGit, string(raw))
	}
	if strings.Contains(string(raw), "plugin:mako-git/panel") {
		t.Fatalf("MCP list_blocks exposed hidden plugin:mako-git/panel: %s", string(raw))
	}
}

// TestWatchBlockStandaloneView verifies that WatchBlock.Standalone is wired to
// plugin.Manager.WatchScopedView both locally (including AND aggregation across
// concurrent viewers) and when relayed across peers.
func TestWatchBlockStandaloneView(t *testing.T) {
	p := newBlockPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	writePlugin(t, p.server, "viewmode", map[string]string{
		"plugin.toml": `id = "viewmode"
version = "1.0.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = "panel"
title = "View Mode"
`,
		"main.js": `export default {
  activate(m) {
    m.blocks.register("panel", {
      state: {},
      render: (s, ui, ctx) => ui.text(ctx.view && ctx.view.standalone ? "STANDALONE" : "COMPACT"),
    });
  },
};`,
	})

	waitForWithin(t, 20*time.Second, "viewmode plugin ready", func() bool {
		pi := findPlugin(p.server.plugins.List(), "viewmode")
		return pi != nil && pi.GetState() == protocol.PluginReady
	})

	watchView := func(sock, node string, standalone bool) (<-chan string, context.CancelFunc) {
		wctx, wcancel := context.WithCancel(ctx)
		out := make(chan string, 16)
		go func() {
			stream, _, err := transport.Dial(wctx, sock, &protocol.Hello{Name: "test"})
			if err != nil {
				return
			}
			defer stream.Close()
			go func() {
				<-wctx.Done()
				_ = stream.Close()
			}()
			if err := stream.Send(protocol.TypeWatchBlock, "1", &protocol.WatchBlock{
				NodeId:     node,
				Block:      "plugin:viewmode/panel",
				Standalone: standalone,
			}); err != nil {
				return
			}
			var tree *protocol.UiNode
			for {
				env, err := stream.Recv()
				if err != nil {
					return
				}
				u, err := protocol.Body[*protocol.BlockUpdate](env)
				if err != nil {
					continue
				}
				if u.GetTree() != nil {
					tree = u.GetTree()
				} else if len(u.GetOps()) > 0 && tree != nil {
					next, err := protocol.ApplyUi(tree, u.GetOps())
					if err != nil {
						return
					}
					tree = next
				}
				if tree != nil {
					select {
					case out <- strings.TrimSpace(treeText(tree)):
					case <-wctx.Done():
						return
					}
				}
			}
		}()
		return out, wcancel
	}

	waitText := func(ch <-chan string, want string) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case got := <-ch:
				if got == want {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %q", want)
			}
		}
	}

	// Single standalone watcher over peer relay sees STANDALONE.
	ch1, stop1 := watchView(p.phone.Socket(), p.server.config().NodeID, true)
	defer stop1()
	waitText(ch1, "STANDALONE")

	// Adding a non-standalone local watcher flips the aggregate to COMPACT.
	ch2, stop2 := watchView(p.server.Socket(), "", false)
	waitText(ch2, "COMPACT")
	waitText(ch1, "COMPACT")

	// Closing the non-standalone watcher flips the remaining solo watcher back to STANDALONE.
	stop2()
	waitText(ch1, "STANDALONE")
}

// TestResolvePluginScopeFolder verifies folder scope resolution for local
// folders (with matching project/space/session), subdirectories, remote node
// workspace locations, and invalid keys.
func TestResolvePluginScopeFolder(t *testing.T) {
	d := meshDaemon(t, "a")
	dirA := t.TempDir()
	dirB := t.TempDir()
	dirBSub := filepath.Join(dirB, "sub")
	if err := os.MkdirAll(dirBSub, 0o755); err != nil {
		t.Fatal(err)
	}
	setTestWorkspaceSpaceAndProjects(t, d, dirA, dirB)

	// Add a remote node project location to the workspace.
	remoteNode := "peer-remote"
	remoteDelta := &protocol.WorkspaceState{
		Spaces: map[string]*protocol.SpaceEntry{
			"sp-rem": {
				CreatedAt: mark(),
				Name:      &protocol.StringReg{Value: "RemoteSpace"},
				Projects: map[string]*protocol.ElementMeta{
					"p-rem": {AddedAt: mark()},
				},
			},
		},
		Projects: map[string]*protocol.ProjectEntry{
			"p-rem": {
				CreatedAt: mark(),
				Name:      &protocol.StringReg{Value: "Gamma"},
				Roots: map[string]*protocol.ProjectRootEntry{
					"r-rem": {
						CreatedAt: mark(),
						Locations: map[string]*protocol.ElementMeta{
							workspace.LocationKey(remoteNode, "/srv/gamma"): {AddedAt: mark()},
						},
					},
				},
			},
		},
	}
	if !d.workspace.edit(remoteDelta) {
		t.Fatal("remote workspace edit had no effect")
	}

	s, err := session.Start(session.Spec{
		ID: "sess-folder-a", NodeID: d.config().NodeID, Agent: "claude", Project: dirA,
		Command: []string{"sh", "-c", "sleep 30"}, Cols: 40, Rows: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	d.sessions.add(s)

	keyA := protocol.FolderScope(protocol.LocalScopeNode, dirA)
	scA := d.resolvePluginScope(keyA)
	if scA.Scope != protocol.ScopeFolder || scA.ScopeKey != keyA ||
		scA.Node != protocol.LocalScopeNode || scA.Folder != dirA ||
		!slices.Equal(scA.Roots, []string{dirA}) ||
		scA.ProjectID != "p1" || scA.ProjectName != "Alpha" ||
		scA.SpaceID != "sp1" || scA.SpaceName != "Engineering" ||
		scA.SessionID != "sess-folder-a" {
		t.Fatalf("folder scope A = %+v", scA)
	}

	keyBSub := protocol.FolderScope(d.config().NodeID, dirBSub)
	scB := d.resolvePluginScope(keyBSub)
	if scB.Scope != protocol.ScopeFolder || scB.Node != d.config().NodeID ||
		scB.Folder != dirBSub || !slices.Equal(scB.Roots, []string{dirBSub}) ||
		scB.ProjectID != "p2" || scB.ProjectName != "Beta" ||
		scB.SpaceID != "" || scB.SessionID != "" {
		t.Fatalf("folder scope B/sub = %+v", scB)
	}

	keyRem := protocol.FolderScope(remoteNode, "/srv/gamma/api")
	scRem := d.resolvePluginScope(keyRem)
	if scRem.Scope != protocol.ScopeFolder || scRem.Node != remoteNode ||
		scRem.Folder != "/srv/gamma/api" || !slices.Equal(scRem.Roots, []string{"/srv/gamma/api"}) ||
		scRem.ProjectID != "p-rem" || scRem.ProjectName != "Gamma" ||
		scRem.SpaceID != "sp-rem" || scRem.SpaceName != "RemoteSpace" {
		t.Fatalf("remote folder scope = %+v", scRem)
	}

	if bad := d.resolvePluginScope("folder:not-valid"); bad.Scope != protocol.ScopeGlobal {
		t.Fatalf("invalid folder scope = %+v, want global fallback", bad)
	}
}
