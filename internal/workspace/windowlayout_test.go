package workspace

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// windowLayout puts a window layout document into a delta.
func windowLayout(d *protocol.WorkspaceState, name, doc string) {
	if d.WindowLayouts == nil {
		d.WindowLayouts = map[string]*protocol.LayoutEntry{}
	}
	d.WindowLayouts[name] = &protocol.LayoutEntry{Doc: []byte(doc)}
}

// Two nodes changing one window layout at once end with the later whole,
// one document of the two, not a mix.
func TestAWindowLayoutIsOneRegister(t *testing.T) {
	a, b := pair(t)
	first, second := &protocol.WorkspaceState{}, &protocol.WorkspaceState{}
	windowLayout(first, "default", `{"from":"a"}`)
	windowLayout(second, "default", `{"from":"b"}`)
	a.edit(t, first)
	b.phys += 10 // b's edit is the later
	b.edit(t, second)
	exchange(a, b)
	sameState(t, a.st, b.st)
	if got := string(WindowLayout(a.st, "default").GetDoc()); got != `{"from":"b"}` {
		t.Fatalf("the later edit did not win whole: %s", got)
	}
}

// A space's layout register and the document it names merge apart: one
// node changes the document while the other points the space at another,
// and both stay.
func TestASpacesLayoutMergesApartFromTheDocument(t *testing.T) {
	a, b := pair(t, newSpace("work", "Work", "V"))
	doc := &protocol.WorkspaceState{}
	windowLayout(doc, "default", `{"v":1}`)
	a.edit(t, doc)
	point := &protocol.WorkspaceState{}
	space(point, "work").Layout = sreg("focus")
	b.edit(t, point)
	exchange(a, b)
	sameState(t, a.st, b.st)
	if got := a.st.GetSpaces()["work"].GetLayout().GetValue(); got != "focus" {
		t.Errorf("the space's layout %q", got)
	}
	if got := string(WindowLayout(a.st, "default").GetDoc()); got != `{"v":1}` {
		t.Errorf("the document %s", got)
	}
}

// A window's document carries the window layouts and each space's, so its
// edits of them are deltas like any: given, removed, and left alone when
// unchanged.
func TestAWindowsDocumentCarriesWindowLayouts(t *testing.T) {
	n := newNode("a", realTime, true)
	n.edit(t, newSpace("work", "Work", "V"))
	base := View(n.st)
	var doc map[string]any
	if err := json.Unmarshal(base, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["window_layouts"]; ok {
		t.Fatal("a state of no window layouts writes them")
	}
	edited := strings.Replace(string(base), `"name":"Work"`, `"name":"Work","layout":"focus"`, 1)
	edited = strings.TrimSuffix(edited, "}") + `,"window_layouts":{"focus":{"version":1}}}`
	delta, err := Diff(n.st, base, []byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	n.edit(t, delta)
	if got := string(WindowLayout(n.st, "focus").GetDoc()); got != `{"version":1}` {
		t.Fatalf("the document given: %q", got)
	}
	if got := n.st.GetSpaces()["work"].GetLayout().GetValue(); got != "focus" {
		t.Fatalf("the space's layout: %q", got)
	}
	view := View(n.st)
	if !strings.Contains(string(view), `"window_layouts":{"focus":{"version":1}}`) || !strings.Contains(string(view), `"layout":"focus"`) {
		t.Fatalf("the view: %s", view)
	}
	if delta, _ := Diff(n.st, view, view); !Empty(delta) {
		t.Fatalf("an unchanged document is an edit: %v", delta)
	}
	removed := strings.Replace(string(view), `,"window_layouts":{"focus":{"version":1}}`, "", 1)
	delta, _ = Diff(n.st, view, []byte(removed))
	n.edit(t, delta)
	if WindowLayout(n.st, "focus") != nil {
		t.Fatal("the document removed is still there")
	}
}
