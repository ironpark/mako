//go:build unix

package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/workspace"
)

// layoutDaemon is a daemon with an agent session whose MCP server the
// test calls, a workspace, and a window watching, until the test closes
// it. keep, sent to, times out a change waiting to be kept.
type layoutDaemon struct {
	d      *Daemon
	s      *session.Session
	cs     *sdk.ClientSession
	keep   chan time.Time
	window func() // closes the window
}

func newLayoutDaemon(t *testing.T) *layoutDaemon {
	t.Helper()
	d, s := projectMCPDaemon(t)
	d.workspace.load(d.dir, "node")
	d.layouts.load(d.dir)
	l := &layoutDaemon{d: d, s: s, cs: projectMCPClient(t, d, s), keep: make(chan time.Time, 1)}
	d.layouts.after = func(wait time.Duration) <-chan time.Time {
		if wait == layoutKeepFor {
			return l.keep
		}
		return nil // the test answers, or nothing does
	}
	l.window = d.layouts.hold()
	return l
}

// call calls a tool and returns its structured result, or the error it
// gave.
func (l *layoutDaemon) call(t *testing.T, name string, args any) (map[string]any, string) {
	t.Helper()
	result, err := l.cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.IsError {
		var text []string
		for _, c := range result.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				text = append(text, tc.Text)
			}
		}
		return nil, strings.Join(text, " ")
	}
	data, _ := json.Marshal(result.StructuredContent)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out, ""
}

// callLater calls a tool in the background, the result to come on the
// channel it returns.
func (l *layoutDaemon) callLater(t *testing.T, name string, args any) <-chan [2]any {
	done := make(chan [2]any, 1)
	go func() {
		result, err := l.cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			done <- [2]any{nil, err.Error()}
			return
		}
		if result.IsError {
			text := ""
			for _, c := range result.Content {
				if tc, ok := c.(*sdk.TextContent); ok {
					text += tc.Text
				}
			}
			done <- [2]any{nil, text}
			return
		}
		data, _ := json.Marshal(result.StructuredContent)
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		done <- [2]any{out, ""}
	}()
	return done
}

// waitStage waits for the change staged to be in phase.
func (l *layoutDaemon) waitStage(t *testing.T, phase string) *protocol.LayoutStage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := l.d.layouts.current(); st.GetPhase() == phase {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no change staged in phase %s; %v", phase, l.d.layouts.current())
	return nil
}

func result(t *testing.T, done <-chan [2]any) (map[string]any, string) {
	t.Helper()
	select {
	case r := <-done:
		out, _ := r[0].(map[string]any)
		return out, r[1].(string)
	case <-time.After(5 * time.Second):
		t.Fatal("the tool did not return")
		return nil, ""
	}
}

// committed is the document of the name in the workspace, and in what the
// peers are sent.
func (l *layoutDaemon) committed(name string) (state, shared []byte) {
	state = workspace.WindowLayout(l.d.workspace.get(false).GetState(), name).GetDoc()
	shared = workspace.WindowLayout(l.d.workspace.shared().GetState(), name).GetDoc()
	return state, shared
}

func movedDetailsDoc(t *testing.T) map[string]any {
	d, err := layoutdoc.Apply(layoutdoc.Default(), []layoutdoc.Op{{Op: "move", ID: layoutdoc.DetailsNode, Parent: "window", Index: new(int)}}, layoutdoc.BuiltinCatalog())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(d)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

// The catalog lists the window's blocks, and a workspace with no document
// gives the built-in default, of no revision.
func TestTheLayoutToolsRead(t *testing.T) {
	l := newLayoutDaemon(t)
	blocks, why := l.call(t, toolListBlocks, struct{}{})
	if why != "" || len(blocks["blocks"].([]any)) != len(layoutdoc.Builtin()) {
		t.Fatalf("blocks: %v %s", blocks, why)
	}
	got, why := l.call(t, toolGetLayout, struct{}{})
	if why != "" || got["document"] != "default" || got["revision"] != "" || got["builtin"] != true {
		t.Fatalf("get_layout: %v %s", got, why)
	}
	body, _ := json.Marshal(got["doc"])
	if doc, err := layoutdoc.Parse(body, layoutdoc.BuiltinCatalog()); err != nil || !reflect.DeepEqual(doc, layoutdoc.Default()) {
		t.Fatalf("the built-in document: %s, %v", body, err)
	}
}

// A document set waits for the user to allow it, then, shown, to keep it;
// all the while it is on this node alone, in what its windows are told and
// in neither the workspace nor what the peers are sent. Kept, it is in
// both, with a revision.
func TestASetLayoutIsAllowedThenKept(t *testing.T) {
	l := newLayoutDaemon(t)
	done := l.callLater(t, toolSetLayout, setLayoutArgs{Doc: movedDetailsDoc(t), IfRev: ""})
	st := l.waitStage(t, protocol.LayoutApprove)
	// The stage is told the windows a moment before the session holds its
	// approval.
	waitFor(t, "the session's approval", func() bool { return l.s.Info().GetApproval() != nil })
	if a := l.s.Info().GetApproval(); a.GetId() != st.GetId() || a.GetKind() != protocol.ToolLayout || len(a.GetOptions()) != 2 {
		t.Fatalf("the session's approval: %v", a)
	}
	if front := l.d.frontWorkspace(); !proto.Equal(front.GetStage(), st) {
		t.Fatal("the windows are not told of the change")
	}
	if l.d.workspace.shared().GetStage() != nil {
		t.Fatal("the peers would be told of the change")
	}
	if state, shared := l.committed("default"); state != nil || shared != nil {
		t.Fatal("a change waiting for approval is in the workspace")
	}
	if err := l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.DecisionAllow}); err != nil {
		t.Fatal(err)
	}
	st = l.waitStage(t, protocol.LayoutKeep)
	if st.GetDeadline() == 0 || l.s.Info().GetApproval() != nil {
		t.Fatalf("keeping: %v, approval %v", st, l.s.Info().GetApproval())
	}
	if state, _ := l.committed("default"); state != nil {
		t.Fatal("a change waiting to be kept is in the workspace")
	}
	if err := l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.LayoutKeep}); err != nil {
		t.Fatal(err)
	}
	out, why := result(t, done)
	state, shared := l.committed("default")
	if why != "" || out["revision"] == "" || state == nil || string(state) != string(shared) {
		t.Fatalf("kept: %v %s; state %s", out, why, state)
	}
	if l.d.layouts.current() != nil {
		t.Fatal("the change is still staged")
	}
	if got, _ := l.call(t, toolGetLayout, struct{}{}); got["revision"] != out["revision"] || got["builtin"] == true {
		t.Fatalf("get_layout after: %v", got)
	}
}

// A document not kept within its time, or reverted, or denied, was never
// in the workspace, nor in what the peers are sent.
func TestALayoutNotKeptLeavesNoTrace(t *testing.T) {
	l := newLayoutDaemon(t)
	for _, c := range []struct {
		name string
		end  func(st *protocol.LayoutStage)
		why  error
	}{
		{"timed out", func(*protocol.LayoutStage) { l.keep <- time.Now() }, errLayoutReverted},
		{"reverted", func(st *protocol.LayoutStage) {
			_ = l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.LayoutRevert})
		}, errLayoutReverted},
	} {
		done := l.callLater(t, toolSetLayout, setLayoutArgs{Doc: movedDetailsDoc(t), IfRev: ""})
		st := l.waitStage(t, protocol.LayoutApprove)
		_ = l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.DecisionAllow})
		c.end(l.waitStage(t, protocol.LayoutKeep))
		if _, why := result(t, done); !strings.Contains(why, c.why.Error()) {
			t.Fatalf("%s: %s", c.name, why)
		}
		if state, shared := l.committed("default"); state != nil || shared != nil || l.d.layouts.current() != nil {
			t.Fatalf("%s: a change not kept left %s", c.name, state)
		}
	}
	done := l.callLater(t, toolSetLayout, setLayoutArgs{Doc: movedDetailsDoc(t), IfRev: ""})
	st := l.waitStage(t, protocol.LayoutApprove)
	_ = l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.DecisionDeny, message: "not now"})
	if _, why := result(t, done); !strings.Contains(why, "declined") || !strings.Contains(why, "not now") {
		t.Fatalf("denied: %s", why)
	}
	if state, _ := l.committed("default"); state != nil {
		t.Fatal("a change denied is in the workspace")
	}
}

// With no window of the node's open, a change the user is to see is
// refused at once; one already allowed and not kept goes through.
func TestALayoutChangeWithNoWindow(t *testing.T) {
	l := newLayoutDaemon(t)
	l.window()
	if _, why := l.call(t, toolSetLayout, setLayoutArgs{Doc: movedDetailsDoc(t), IfRev: ""}); !strings.Contains(why, "no window") {
		t.Fatalf("set with no window: %s", why)
	}
	resize := patchLayoutArgs{Ops: []layoutdoc.Op{{Op: "resize", ID: "sidebar", Size: new(300.0)}}}
	if _, why := l.call(t, toolPatchLayout, resize); !strings.Contains(why, "no window") {
		t.Fatalf("patch with no window: %s", why)
	}
	l.d.layouts.allow(protocol.LayoutAllowAlways, "")
	if _, why := l.call(t, toolPatchLayout, resize); why != "" {
		t.Fatalf("an allowed patch with no window: %s", why)
	}
	if _, why := l.call(t, toolSetLayout, setLayoutArgs{Doc: movedDetailsDoc(t), IfRev: "x"}); !strings.Contains(why, "revision") {
		t.Fatalf("a set of a stale revision: %s", why)
	}
}

// The allowances are node-local and scoped: once allows the one change; a
// space's allows changes of its document alone; always allows every one;
// forgetting has agents ask again. They outlast the daemon.
func TestLayoutAllowancesAreScoped(t *testing.T) {
	l := newLayoutDaemon(t)
	resize := func(doc string, size float64) patchLayoutArgs {
		return patchLayoutArgs{Document: doc, Ops: []layoutdoc.Op{{Op: "resize", ID: "sidebar", Size: new(size)}}}
	}
	ask := func(args patchLayoutArgs, option string) {
		t.Helper()
		done := l.callLater(t, toolPatchLayout, args)
		st := l.waitStage(t, protocol.LayoutApprove)
		_ = l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.DecisionAllow, option: option})
		if _, why := result(t, done); why != "" {
			t.Fatal(why)
		}
	}
	straight := func(args patchLayoutArgs) {
		t.Helper()
		if _, why := l.call(t, toolPatchLayout, args); why != "" {
			t.Fatalf("an allowed change: %s", why)
		}
	}
	ask(resize("", 300), "")
	ask(resize("", 310), protocol.LayoutAllowSpace) // once asked for again
	straight(resize("", 320))
	ask(resize("focus", 300), protocol.LayoutAllowAlways) // another document's asked for
	straight(resize("other", 300))
	reread := &windowLayouts{}
	reread.load(l.d.dir)
	if !reread.allows("anything") {
		t.Fatal("always was not kept on disk")
	}
	l.d.layouts.forget()
	ask(resize("", 330), "")
	if data, err := os.ReadFile(filepath.Join(l.d.dir, "layout-allow.json")); err != nil || strings.Contains(string(data), "always") {
		t.Fatalf("forgotten: %s %v", data, err)
	}
}

// undo goes back, as a new edit, through this node's versions of the
// document, as its edits left them, to none; and it remembers none of its
// own.
func TestUndoGoesBackThroughThisNodesVersions(t *testing.T) {
	l := newLayoutDaemon(t)
	l.d.layouts.allow(protocol.LayoutAllowAlways, "")
	size := func() float64 {
		doc, _, builtin, _ := l.d.layoutNow("default")
		if builtin {
			return -1
		}
		return doc.Node("sidebar").Size
	}
	for _, s := range []float64{300, 400} {
		if _, why := l.call(t, toolPatchLayout, patchLayoutArgs{Ops: []layoutdoc.Op{{Op: "resize", ID: "sidebar", Size: new(s)}}}); why != "" {
			t.Fatal(why)
		}
	}
	if size() != 400 {
		t.Fatalf("size %v", size())
	}
	for _, want := range []float64{300, -1} {
		if _, why := l.call(t, toolUndoLayout, struct{}{}); why != "" {
			t.Fatal(why)
		}
		if size() != want {
			t.Fatalf("undone to %v, want %v", size(), want)
		}
	}
	if _, why := l.call(t, toolUndoLayout, struct{}{}); !strings.Contains(why, "no earlier version") {
		t.Fatalf("a third undo: %s", why)
	}
	if l := workspace.WindowLayout(l.d.workspace.get(false).GetState(), "default"); l != nil {
		t.Fatal("undone to none, a document is left")
	}
}

// A change made of a revision another edit replaced meanwhile is refused
// when it would be written.
func TestALayoutChangedMeanwhileIsRefused(t *testing.T) {
	l := newLayoutDaemon(t)
	done := l.callLater(t, toolPatchLayout, patchLayoutArgs{Ops: []layoutdoc.Op{{Op: "resize", ID: "sidebar", Size: new(300.0)}}})
	st := l.waitStage(t, protocol.LayoutApprove)
	other, _ := json.Marshal(layoutdoc.Default())
	if _, err := l.d.editWorkspace(&protocol.WorkspaceState{WindowLayouts: map[string]*protocol.LayoutEntry{"default": {Doc: other}}}, nil, true); err != nil {
		t.Fatal(err)
	}
	_ = l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.DecisionAllow})
	if _, why := result(t, done); !strings.Contains(why, "changed since") {
		t.Fatalf("a stale change: %s", why)
	}
}

// A window's own edit of a document needs no asking, and leaves the
// version before for undo as an agent's does.
func TestAWindowsEditIsUndoneAsAnAgentsIs(t *testing.T) {
	l := newLayoutDaemon(t)
	doc, _ := json.Marshal(layoutdoc.Default())
	if _, err := l.d.editWorkspace(&protocol.WorkspaceState{WindowLayouts: map[string]*protocol.LayoutEntry{"default": {Doc: doc}}}, nil, true); err != nil {
		t.Fatal(err)
	}
	if state, _ := l.committed("default"); state == nil {
		t.Fatal("the window's edit is not in the workspace")
	}
	l.d.layouts.allow(protocol.LayoutAllowAlways, "")
	if _, why := l.call(t, toolUndoLayout, struct{}{}); why != "" {
		t.Fatal(why)
	}
	if state, _ := l.committed("default"); state != nil {
		t.Fatal("undo did not go back to before the window's edit")
	}
}
