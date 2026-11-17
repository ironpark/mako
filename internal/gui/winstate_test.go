package gui

import (
	"os"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
)

// A document's sizes are defaults: a width this device kept wins over
// them, the sidebar's and the right panel's where gui.json always kept
// them, and none kept is 0 there, the document's.
func TestSizesKeptOnTheDeviceOverrideTheDocuments(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	if ggui.Peek(m.SidebarWidth) != sidebarWidth || ggui.Peek(m.DetailsWidth) != detailsWidth {
		t.Fatalf("widths %v and %v with none kept", ggui.Peek(m.SidebarWidth), ggui.Peek(m.DetailsWidth))
	}
	if p := ggui.Peek(m.Prefs); p.SidebarWidth != 0 || p.DetailsWidth != 0 {
		t.Fatalf("the defaults were kept as the device's: %v, %v", p.SidebarWidth, p.DetailsWidth)
	}

	writeLayout(t, `{"version":1,"root":{"id":"window","row":[
		{"id":"sidebar","block":"mako.sidebar","size":600,"collapsible":true},
		{"id":"workspace","column":[{"panes":"main","grow":1},{"id":"bottom","block":"mako.git","size":200}],"grow":1},
		{"id":"details","stack":["mako.files"],"size":400}]}}`)
	if err := savePrefs(prefs{DetailsWidth: 333, LayoutSizes: map[string]float64{"bottom": 250, "gone": -1}}.normalized()); err != nil {
		t.Fatal(err)
	}
	m = newModel(config.Default(), "")
	doc := ggui.Peek(m.WinDoc)
	if ggui.Peek(m.SidebarWidth) != maxSidebarWidth {
		t.Errorf("the sidebar is %v wide, not its widest for a document's 600", ggui.Peek(m.SidebarWidth))
	}
	if ggui.Peek(m.DetailsWidth) != 333 {
		t.Errorf("the right panel is %v wide, not as kept", ggui.Peek(m.DetailsWidth))
	}
	bottom := m.nodeState(doc.node("bottom"))
	if ggui.Peek(bottom.size) != 250 {
		t.Errorf("the bottom block is %v tall, not as kept", ggui.Peek(bottom.size))
	}
	if _, ok := ggui.Peek(m.Prefs).LayoutSizes["gone"]; ok {
		t.Error("a size below 0 was kept")
	}

	// Dragged, a size is kept by the node's ID; the document is not
	// written.
	m.Prefs.Update(func(p prefs) prefs { bottom.keep(&p, 180); return p })
	if ggui.Peek(m.Prefs).LayoutSizes["bottom"] != 180 || doc.node("bottom").Size != 200 {
		t.Fatalf("kept %v, document %v", ggui.Peek(m.Prefs).LayoutSizes, doc.node("bottom").Size)
	}

	// Another document moves the sizes not kept, and only those.
	m.Prefs.Update(func(p prefs) prefs { p.LayoutSizes = nil; return p })
	next := defaultWindowDoc()
	next.Root.Children[0].Size = 300
	next.Root.Children[2].Size = 500
	m.setWindowDoc(next)
	if ggui.Peek(m.SidebarWidth) != 300 || ggui.Peek(m.DetailsWidth) != 333 {
		t.Fatalf("widths %v and %v after another document", ggui.Peek(m.SidebarWidth), ggui.Peek(m.DetailsWidth))
	}
}

// The sidebar's edge drags its width, kept in gui.json's sidebar_width as
// before, and its width stays within its bounds.
func TestDraggingAnEdgeKeepsTheSize(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m, p := windowProbe(t, windowView, nil)
	at := ggui.Pt(sidebarWidth+panelEdgeWidth/2, 400)
	p.Press(at)
	p.Move(ggui.Pt(at.X+30, at.Y))
	p.Release(ggui.Pt(at.X+30, at.Y))
	if ggui.Peek(m.SidebarWidth) != sidebarWidth+30 || ggui.Peek(m.Prefs).SidebarWidth != sidebarWidth+30 {
		t.Fatalf("width %v, kept %v", ggui.Peek(m.SidebarWidth), ggui.Peek(m.Prefs).SidebarWidth)
	}
	p.Press(ggui.Pt(sidebarWidth+30+panelEdgeWidth/2, 400))
	p.Move(ggui.Pt(1200, 400))
	p.Release(ggui.Pt(1200, 400))
	if ggui.Peek(m.SidebarWidth) != maxSidebarWidth {
		t.Fatalf("dragged past its widest to %v", ggui.Peek(m.SidebarWidth))
	}
}

// Resetting the layout shows the default document and moves the file
// aside, kept.
func TestResettingTheLayoutShowsTheDefault(t *testing.T) {
	writeLayout(t, `{"version":1,"root":{"id":"r","row":[{"panes":"main","grow":1},"mako.git"]}}`)
	m := newModel(config.Default(), "")
	if ggui.Peek(m.WinDoc).node(sidebarNode) != nil {
		t.Fatal("the file was not read")
	}
	m.resetWindowDoc()
	if ggui.Peek(m.WinDoc).node(sidebarNode) == nil {
		t.Fatal("the default is not shown")
	}
	path, _ := configFile(layoutFileName)
	if d, err := loadWindowDoc(m.blocks); err != nil || d.node(sidebarNode) == nil {
		t.Fatalf("the file is still read: %v", err)
	}
	if data, err := os.ReadFile(path + ".bak"); err != nil || !strings.Contains(string(data), `"mako.git"`) {
		t.Fatalf("the file was not kept aside: %v", err)
	}
}
