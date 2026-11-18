//go:build unix

package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

// userCompact is the default document with a compact root the user
// wrote, kept by a window's own edit.
func userCompact(t *testing.T, l *layoutDaemon) *layoutdoc.Doc {
	t.Helper()
	doc := layoutdoc.Default()
	doc.Compact = &layoutdoc.Node{ID: "phone", Kind: layoutdoc.KindColumn, Children: []*layoutdoc.Node{
		{ID: "main", Kind: layoutdoc.KindPanes, Panes: layoutdoc.MainPanes},
	}}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.d.editWorkspace(&protocol.WorkspaceState{WindowLayouts: map[string]*protocol.LayoutEntry{"default": {Doc: body}}}, nil, true); err != nil {
		t.Fatal(err)
	}
	return doc
}

// compactOf is the compact root of the document committed, written.
func compactOf(t *testing.T, l *layoutDaemon) string {
	t.Helper()
	state, _ := l.committed("default")
	doc, err := layoutdoc.Parse(state, l.d.blockCatalog())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc.Compact)
	return string(b)
}

// An agent does not write a document's variants, which the window asking
// the user does not draw: a document carrying others is refused; one
// repeating those it has, or leaving them out, keeps the user's, as a
// patch of the root does.
func TestAnAgentDoesNotWriteVariants(t *testing.T) {
	l := newLayoutDaemon(t)
	l.d.layouts.allow(protocol.LayoutAllowAlways, "")
	user := userCompact(t, l)
	want := compactOf(t, l)
	got, why := l.call(t, toolGetLayout, struct{}{})
	if why != "" {
		t.Fatal(why)
	}
	rev := got["revision"].(string)

	// variants the agent wrote itself.
	doc := movedDetailsDoc(t)
	doc["version"] = 2
	doc["variants"] = map[string]any{"compact": map[string]any{"id": "evil", "column": []any{"mako.forge"}}}
	if _, why := l.call(t, toolSetLayout, setLayoutArgs{Doc: doc, IfRev: rev}); !strings.Contains(why, "does not write a document's variants") {
		t.Fatalf("a document with variants of the agent's: %s", why)
	}

	// The document as get_layout gave it, its root changed, variants and
	// all: the user's compact root stays.
	doc = got["doc"].(map[string]any)
	moved := movedDetailsDoc(t)
	doc["root"] = moved["root"]
	done := l.callLater(t, toolSetLayout, setLayoutArgs{Doc: doc, IfRev: rev})
	st := l.waitStage(t, protocol.LayoutKeep)
	_ = l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.LayoutKeep})
	out, why := result(t, done)
	if why != "" {
		t.Fatal(why)
	}
	if c := compactOf(t, l); c != want {
		t.Fatalf("the compact root after a set repeating it: %s; want %s", c, want)
	}

	// Left out, the user's compact root stays too.
	done = l.callLater(t, toolSetLayout, setLayoutArgs{Doc: map[string]any{"version": 1, "root": user.Root}, IfRev: out["revision"].(string)})
	st = l.waitStage(t, protocol.LayoutKeep)
	_ = l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.LayoutKeep})
	if _, why := result(t, done); why != "" {
		t.Fatal(why)
	}
	if c := compactOf(t, l); c != want {
		t.Fatalf("the compact root after a set leaving it out: %s; want %s", c, want)
	}

	// A patch changes the root alone.
	if _, why := l.call(t, toolPatchLayout, patchLayoutArgs{Ops: []layoutdoc.Op{{Op: "resize", ID: "sidebar", Size: ptrTo(300.0)}}}); why != "" {
		t.Fatal(why)
	}
	if c := compactOf(t, l); c != want {
		t.Fatalf("the compact root after a patch: %s; want %s", c, want)
	}
}
