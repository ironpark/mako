package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

// layout.json, the window's document before the workspace held them, is
// taken in as the default document once, when the first workspace the
// window takes holds none, and moved aside; never over one the workspace
// holds, and not when it does not read.
func TestALayoutFileIsTakenInOnce(t *testing.T) {
	src := `{"version":1,"root":{"id":"r","row":[{"panes":"main","grow":1},"mako.git"]}}`
	setup := func(body string) (*model, string) {
		t.Setenv("MAKO_HOME", t.TempDir())
		dir, _ := config.Dir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, layoutFileName)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return newModel(config.Default(), ""), path
	}
	m, path := setup(src)
	m.importLayoutFile(&protocol.WorkspaceState{})
	got := ggui.Peek(m.Spaces).WindowLayouts[layoutdoc.DefaultName]
	if d, err := layoutdoc.Parse(got, m.blocks); err != nil || d.Node("mako.git") == nil {
		t.Fatalf("taken in: %s, %v", got, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the file was not moved aside")
	}
	if _, err := os.Stat(path + ".imported"); err != nil {
		t.Fatal(err)
	}

	m, path = setup(src)
	held := &protocol.WorkspaceState{WindowLayouts: map[string]*protocol.LayoutEntry{layoutdoc.DefaultName: {Doc: []byte(`{}`), Clock: &protocol.HLC{WallTime: 1}}}}
	m.importLayoutFile(held)
	m.importLayoutFile(&protocol.WorkspaceState{}) // once, at the first workspace
	if ggui.Peek(m.Spaces).WindowLayouts != nil {
		t.Fatal("taken in over the workspace's")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a file not taken in was moved")
	}

	m, path = setup(`{"version":1,"root":`)
	m.importLayoutFile(&protocol.WorkspaceState{})
	if ggui.Peek(m.Spaces).WindowLayouts != nil {
		t.Fatal("a file that does not read was taken in")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a file that does not read was moved")
	}
}

// The window draws the document of the space shown, or the change of it
// an agent asked for while one is staged, and the document again once
// none is.
func TestTheWindowDrawsTheStagedDocument(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.setWindowLayout(layoutdoc.DefaultName, []byte(`{"version":1,"root":{"id":"r","row":[{"panes":"main","grow":1},{"id":"mine","block":"mako.git"}]}}`))
	p := ggui.ProbeBuilder(func() ggui.Widget { return windowView(m) }, ggui.Sz(800, 600))
	defer p.Close()
	p.Setup(func() { followWindowLayout(m) })
	// What is set from outside a frame is drawn in the one after next.
	frames := func() { p.Frame(); p.Frame() }
	frames()
	has := func(id string) bool { return ggui.Peek(m.WinDoc).Node(id) != nil }
	if !has("mine") {
		t.Fatal("the document is not drawn")
	}
	staged, _ := json.Marshal(layoutdoc.Default())
	m.LayoutStage.Set(&protocol.LayoutStage{Id: "s", Document: layoutdoc.DefaultName, Doc: staged, Phase: protocol.LayoutApprove})
	frames()
	if !has(layoutdoc.SidebarNode) || has("mine") {
		t.Fatal("the staged document is not drawn")
	}
	m.LayoutStage.Set(&protocol.LayoutStage{Id: "s", Document: "other", Doc: staged, Phase: protocol.LayoutApprove})
	frames()
	if !has("mine") {
		t.Fatal("a change of another document is drawn")
	}
	m.LayoutStage.Set(nil)
	frames()
	if !has("mine") {
		t.Fatal("the document is not drawn again")
	}
}
