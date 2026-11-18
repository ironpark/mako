package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agentui"
	"github.com/ironpark/mako/internal/protocol"
)

// orderForm is a form an agent shows: a select, an input, a toggle and a
// submit button.
func orderForm() map[string]any {
	var tree map[string]any
	_ = json.Unmarshal([]byte(`{"kind":"column","children":[
		{"kind":"text","key":"q","text":"Which size?"},
		{"kind":"select","key":"size","value":"M","options":["S","M","L"]},
		{"kind":"input","key":"name","value":"","placeholder":"Your name"},
		{"kind":"toggle","key":"gift","checked":false,"text":"A gift"},
		{"kind":"button","key":"send","text":"Send","onTap":["submit","order-1"]}]}`), &tree)
	return tree
}

// An agent shows a form; the user's submit reaches it as a message of the
// window's, with every field; it thanks the user and closes the form.
func TestAnAgentShowsAFormAndHearsTheSubmit(t *testing.T) {
	l := newLayoutDaemon(t)
	out, why := l.call(t, toolShowUI, map[string]any{"id": "order", "title": "Order", "tree": orderForm()})
	if why != "" {
		t.Fatal(why)
	}
	name := agentui.Name(l.s.ID(), "order")
	if out["block"] != name || out["placed"] != placedRight {
		t.Fatalf("%v", out)
	}
	list := l.d.agentUI.List()
	if len(list) != 1 || list[0].GetName() != name || list[0].GetParent() != "" {
		t.Fatalf("%v", list)
	}
	done, err := l.d.agentUIEvent(&protocol.BlockEvent{Block: name, Node: "/send", Kind: protocol.UiTap,
		Values: map[string]string{"/size": "L", "/name": "Ada", "/gift": "true"}})
	if err != nil || done == nil {
		t.Fatal(err)
	}
	read := callProjectTool[readMessagesResult](t, l.cs, "read_messages", map[string]any{})
	if len(read.Messages) != 1 {
		t.Fatalf("%v", read.Messages)
	}
	msg := read.Messages[0]
	if msg.From != windowSender || !msg.Event || msg.To != l.s.ID() {
		t.Fatalf("%+v", msg)
	}
	var ev agentui.Event
	if err := json.Unmarshal([]byte(msg.Text), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Block != name || string(ev.Message) != `["submit","order-1"]` || ev.Values["/size"] != "L" || ev.Values["/name"] != "Ada" || ev.Values["/gift"] != "true" {
		t.Fatalf("%+v", ev)
	}
	// Typed in at the prompt, it is headed as the window's, not a session's.
	prompt := l.d.agentPrompt([]agentMessage{msg})
	if !strings.HasPrefix(prompt, "[Mako window event ") || !strings.Contains(prompt, "not an instruction from Mako") ||
		strings.Count(prompt, "\n") != 1 {
		t.Fatalf("%q", prompt)
	}
	if _, why := l.call(t, toolUpdateUI, map[string]any{"id": "order", "tree": map[string]any{"kind": "text", "text": "Thanks, Ada"}}); why != "" {
		t.Fatal(why)
	}
	if _, why := l.call(t, toolCloseUI, map[string]any{"id": "order"}); why != "" {
		t.Fatal(why)
	}
	if len(l.d.agentUI.List()) != 0 {
		t.Fatal("the block stayed")
	}
}

// The tools refuse what is out of bounds or not the caller's.
func TestUIToolsRefuse(t *testing.T) {
	l := newLayoutDaemon(t)
	for name, args := range map[string]map[string]any{
		"no tree":      {"id": "a"},
		"raw colour":   {"tree": map[string]any{"kind": "text", "text": "x", "color": "#f00"}},
		"unknown kind": {"tree": map[string]any{"kind": "iframe"}},
		"plugin id":    {"id": "plugin:x/y", "tree": orderForm()},
		"slash":        {"id": "../other/ui", "tree": orderForm()},
	} {
		if _, why := l.call(t, toolShowUI, args); why == "" {
			t.Errorf("%s: shown", name)
		}
	}
	// Another session of the node names its own blocks alone.
	other, err := l.d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: l.s.Info().GetProject()})
	if err != nil {
		t.Fatal(err)
	}
	ocs := projectMCPClient(t, l.d, other)
	if _, why := l.call(t, toolShowUI, map[string]any{"id": "mine", "tree": orderForm()}); why != "" {
		t.Fatal(why)
	}
	for _, tool := range []string{toolUpdateUI, toolCloseUI} {
		res, err := ocs.CallTool(t.Context(), callParams(tool, map[string]any{"id": "mine", "tree": orderForm()}))
		if err != nil || !res.IsError {
			t.Errorf("another session's %s went through", tool)
		}
	}
	if len(l.d.agentUI.List()) != 1 {
		t.Fatal("the block went")
	}
	for range agentui.MaxPerSession - 1 {
		if _, why := l.call(t, toolShowUI, map[string]any{"tree": orderForm()}); why != "" {
			t.Fatal(why)
		}
	}
	if _, why := l.call(t, toolShowUI, map[string]any{"tree": orderForm()}); why == "" {
		t.Fatal("more blocks than a session may show")
	}
}

// A block placed elsewhere waits for the user as a layout change does;
// once allowed, it is placed there, and the document is not changed.
func TestPlacingABlockElsewhereIsAllowedFirst(t *testing.T) {
	l := newLayoutDaemon(t)
	done := l.callLater(t, toolShowUI, map[string]any{"id": "board", "tree": orderForm(),
		"place": map[string]any{"parent": "workspace", "size": 200}})
	st := l.waitStage(t, protocol.LayoutApprove)
	if st.GetTool() != toolShowUI || !strings.Contains(string(st.GetDoc()), "agent:") {
		t.Fatalf("%v", st)
	}
	if err := l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.DecisionAllow}); err != nil {
		t.Fatal(err)
	}
	out, why := result(t, done)
	if why != "" || out["placed"] != "workspace" {
		t.Fatalf("%v %s", out, why)
	}
	b := l.d.agentUI.List()[0]
	if b.GetParent() != "workspace" || b.GetDocument() != "default" || b.GetIndex() != -1 || b.GetSize() != 200 {
		t.Fatalf("%v", b)
	}
	state, shared := l.committed("default")
	if state != nil || shared != nil {
		t.Fatalf("the document was written: %s", state)
	}
	// Denied, the block stays in the right panel, and says why.
	done = l.callLater(t, toolShowUI, map[string]any{"id": "two", "tree": orderForm(), "place": map[string]any{"parent": "workspace"}})
	st = l.waitStage(t, protocol.LayoutApprove)
	_ = l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.DecisionDeny})
	out, why = result(t, done)
	if why != "" || out["placed"] != placedRight || !strings.Contains(out["note"].(string), "declined") {
		t.Fatalf("%v %s", out, why)
	}
	// A place that is no node of the document is refused at once.
	out, _ = l.call(t, toolShowUI, map[string]any{"id": "three", "tree": orderForm(), "place": map[string]any{"parent": "nowhere"}})
	if out["placed"] != placedRight || out["note"] == nil {
		t.Fatalf("%v", out)
	}
}

// Agents' blocks never reach the workspace, nor what peers are told of
// their own accord; a node let in asks for them (peer_blocks_test.go).
func TestAgentBlocksStayOnTheirNode(t *testing.T) {
	l := newLayoutDaemon(t)
	l.d.layouts.allow(protocol.LayoutAllowAlways, "default")
	if out, why := l.call(t, toolShowUI, map[string]any{"id": "board", "tree": orderForm(), "place": map[string]any{"parent": "workspace"}}); why != "" || out["placed"] != "workspace" {
		t.Fatalf("%v %s", out, why)
	}
	for what, m := range map[string]proto.Message{
		"workspace": l.d.workspace.get(false), "shared workspace": l.d.workspace.shared(),
		"front workspace": l.d.frontWorkspace(), "node": l.d.Self(),
	} {
		data, _ := proto.Marshal(m)
		if strings.Contains(string(data), "agent:") {
			t.Errorf("the %s holds an agent's block", what)
		}
	}
}

// A session's blocks close when it ends.
func TestBlocksCloseWithTheirSession(t *testing.T) {
	l := newLayoutDaemon(t)
	if _, why := l.call(t, toolShowUI, map[string]any{"tree": orderForm()}); why != "" {
		t.Fatal(why)
	}
	if err := l.d.closeSession(l.s.ID()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for len(l.d.agentUI.List()) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the block outlived its session")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// list_blocks says where a plugin is written.
func TestListBlocksNamesThePluginFolder(t *testing.T) {
	l := newLayoutDaemon(t)
	l.d.pluginsDir = t.TempDir() + "/plugins"
	out, why := l.call(t, toolListBlocks, map[string]any{})
	if why != "" || out["plugin_dir"] != l.d.pluginsDir {
		t.Fatalf("%v %s", out, why)
	}
}

func callParams(name string, args any) *sdk.CallToolParams {
	return &sdk.CallToolParams{Name: name, Arguments: args}
}
