package gui

import (
	"reflect"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
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

	t.Setenv("MAKO_HOME", t.TempDir())
	if err := savePrefs(prefs{DetailsWidth: 333, LayoutSizes: map[string]float64{"bottom": 250, "gone": -1}}.normalized()); err != nil {
		t.Fatal(err)
	}
	m = newModel(config.Default(), "")
	withLayout(t, `{"version":1,"root":{"id":"window","row":[
		{"id":"sidebar","block":"mako.sidebar","size":600,"collapsible":true},
		{"id":"workspace","column":[{"panes":"main","grow":1},{"id":"bottom","block":"mako.git","size":200}],"grow":1},
		{"id":"details","stack":["mako.files"],"size":400}]}}`)(m)
	doc := ggui.Peek(m.WinDoc)
	if ggui.Peek(m.SidebarWidth) != maxSidebarWidth {
		t.Errorf("the sidebar is %v wide, not its widest for a document's 600", ggui.Peek(m.SidebarWidth))
	}
	if ggui.Peek(m.DetailsWidth) != 333 {
		t.Errorf("the right panel is %v wide, not as kept", ggui.Peek(m.DetailsWidth))
	}
	bottom := m.nodeState(doc.Node("bottom"))
	if ggui.Peek(bottom.size) != 250 {
		t.Errorf("the bottom block is %v tall, not as kept", ggui.Peek(bottom.size))
	}
	if _, ok := ggui.Peek(m.Prefs).LayoutSizes["gone"]; ok {
		t.Error("a size below 0 was kept")
	}

	// Dragged, a size is kept by the node's ID; the document is not
	// written.
	m.Prefs.Update(func(p prefs) prefs { bottom.keep(&p, 180); return p })
	if ggui.Peek(m.Prefs).LayoutSizes["bottom"] != 180 || doc.Node("bottom").Size != 200 {
		t.Fatalf("kept %v, document %v", ggui.Peek(m.Prefs).LayoutSizes, doc.Node("bottom").Size)
	}

	// Another document moves the sizes not kept, and only those.
	m.Prefs.Update(func(p prefs) prefs { p.LayoutSizes = nil; return p })
	next := layoutdoc.Default()
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

// Resetting the layout shows the default document at once, whether the
// daemon is there to hear of it or not: the space shown goes back to the
// default document, and, showing it, the default to the built-in one; a
// change staged is turned down.
func TestResettingTheLayoutShowsTheDefault(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	withLayout(t, `{"version":1,"root":{"id":"r","row":[{"panes":"main","grow":1},"mako.git"]}}`)(m)
	m.Spaces.Update(func(l spaceList) spaceList {
		l.Spaces = []space{{ID: "work", Name: "Work", Layout: "focus"}}
		l.WindowLayouts["focus"] = l.WindowLayouts[layoutdoc.DefaultName]
		return l
	})
	m.setSpace(1)
	m.LayoutStage.Set(&protocol.LayoutStage{Id: "s", Document: "focus", Phase: protocol.LayoutKeep})
	m.resetWindowDoc()
	l := ggui.Peek(m.Spaces)
	if l.Spaces[0].Layout != "" || l.WindowLayouts[layoutdoc.DefaultName] == nil || ggui.Peek(m.LayoutStage) != nil {
		t.Fatalf("reset in a space of a document of its own: %+v, stage %v", l, ggui.Peek(m.LayoutStage))
	}
	m.resetWindowDoc()
	m.showWindowLayout(winSource{body: string(ggui.Peek(m.Spaces).WindowLayouts[layoutdoc.DefaultName])})
	if l := ggui.Peek(m.Spaces); l.WindowLayouts[layoutdoc.DefaultName] != nil || l.WindowLayouts["focus"] == nil || !reflect.DeepEqual(ggui.Peek(m.WinDoc), layoutdoc.Default()) {
		t.Fatalf("reset of the default: %v", l.WindowLayouts)
	}
}
