package gui

import (
	"fmt"
	"image/color"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/layoutdoc"
)

// oldWindowMain is the window's main area as view.go built it before the
// layout document, kept to hold the default document to it.
func oldWindowMain(m *model) ggui.Widget {
	tabs := []string{filesTab, gitTab, agentsTab, forgeTab}
	tab := ggui.Map(m.DetailTab, func(s string) int { return slices.Index(tabs, s) })
	details := func() ggui.Widget {
		pages := []ggui.Widget{filesPanel(m), makoViews[gitTab].Build(m, nil, blockPlace{Solo: false}).Body, projectAgentsPanel(m), forgePanel(m)}
		return widget.Box(widget.Column(
			ggui.View(ggui.Derived(m.forgeOf), func(f string) ggui.Widget {
				return panelTabs(tab, func(i int) { m.DetailTab.Set(tabs[i]) }, T("Files"), T("Git"), T("Agents"), forgeLabel(f))
			}),
			hline(),
			widget.Expanded(ggui.Key(tab, func(i int) ggui.Widget { return pages[min(max(i, 0), len(pages)-1)] })),
		).Stretch()).Fill(skin.Surface0)
	}
	panelEdge := widget.Pointer(widget.Box(widget.Row(vline(), widget.Spacer()).Stretch()).Width(panelEdgeWidth).Fill(skin.Surface0)).
		Cursor(ggui.CursorShapeEWResize)
	return widget.Row(
		newDrawer(ggui.Component(func() ggui.Widget {
			tint := spaceTint(m)
			line := widget.Row(widget.Spacer(), vline()).Stretch()
			edge := ggui.View(tint, func(c color.NRGBA) ggui.Widget {
				return widget.Box(line).Width(panelEdgeWidth).Fill(skin.Sidebar).Gradient(spaceWash(c))
			})
			return widget.Row(widget.Expanded(sidebarIn(m, tint)), widget.Pointer(edge).Cursor(ggui.CursorShapeEWResize)).Stretch()
		}), m.SidebarOpen, ggui.Map(m.SidebarWidth, func(w float64) float64 { return w + panelEdgeWidth }), false),
		widget.Expanded(widget.Box(widget.Column(workspaceHeader(m), hline(), widget.Expanded(layoutView(m))).Stretch()).Fill(skin.Surface0)),
		newDrawer(widget.Row(panelEdge, widget.Expanded(details())).Stretch(), m.DetailsOpen,
			ggui.Map(m.DetailsWidth, func(w float64) float64 { return w + panelEdgeWidth }), true),
	).Stretch()
}

// windowProbe draws the main area of a window of its own, with no daemon,
// as main builds it, after setting it up as change says.
func windowProbe(t *testing.T, main func(*model) ggui.Widget, change func(*model)) (*model, *ggui.Probe) {
	t.Helper()
	i18n.Set("en")
	m := newModel(config.Default(), "")
	if change != nil {
		change(m)
	}
	p := ggui.ProbeBuilder(func() ggui.Widget {
		watchForge(m)
		return ggui.Provide(ggui.ReducedMotionKey, true, main(m))
	}, ggui.Sz(1280, 800))
	t.Cleanup(p.Close)
	for range 3 {
		p.Frame()
	}
	return m, p
}

// drawn is what a probe drew, as far as a test without a screen can read
// it: the tree of what it shows, and every region painted to take input,
// with where it is. The pixels themselves are compared by rendering the
// window with mako-gui -render-dir before and after a change.
func drawn(p *ggui.Probe) string {
	var b strings.Builder
	for _, line := range strings.Split(p.Semantics().String(), "\n") {
		if strings.TrimSpace(line) == `menu "Context menu" collapsed` {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	for _, f := range p.FindAll("") {
		if f.Role == ggui.RoleMenu && f.Label == "Context menu" {
			continue
		}
		fmt.Fprintf(&b, "%s %q %v\n", f.Role, f.Label, f.Rect)
	}
	return b.String()
}

// The default document draws the window as it was drawn before there was
// a document, as far as drawn tells: as it starts, with the panels
// resized, another tab shown, and the panels closed.
func TestTheDefaultDocumentDrawsTheWindowAsBefore(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	for _, c := range []struct {
		name   string
		change func(*model)
	}{
		{"as it starts", nil},
		{"resized", func(m *model) { m.SidebarWidth.Set(321); m.DetailsWidth.Set(440) }},
		{"the git tab", func(m *model) { m.DetailTab.Set(gitTab) }},
		{"the forge tab", func(m *model) { m.DetailTab.Set(forgeTab) }},
		{"closed", func(m *model) { m.SidebarOpen.Set(false); m.DetailsOpen.Set(false) }},
	} {
		_, before := windowProbe(t, oldWindowMain, c.change)
		_, after := windowProbe(t, windowView, c.change)
		if a, b := drawn(before), drawn(after); a != b {
			t.Errorf("%s: drawn\n%s\nwhere before it was\n%s", c.name, b, a)
		}
	}
}

// withLayout has the window draw src, as the document of the space shown.
func withLayout(t *testing.T, src string) func(*model) {
	t.Helper()
	if _, err := layoutdoc.Parse([]byte(src), builtinBlocks()); err != nil {
		t.Fatal(err)
	}
	return func(m *model) {
		m.setWindowLayout(layoutdoc.DefaultName, []byte(src))
		m.showWindowLayout(winSource{body: src})
	}
}

func findNode(t *testing.T, p *ggui.Probe, role ggui.Role, label string) ggui.Rect {
	t.Helper()
	n, ok := p.Semantics().Find(role, label)
	if !ok {
		t.Fatalf("no %v %q:\n%s", role, label, p.Semantics().String())
	}
	return n.Rect
}

// A document with the right panel left of the panes draws it there, its
// edge on the side toward the panes.
func TestARightPanelMovedLeftIsDrawnThere(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	_, p := windowProbe(t, windowView, withLayout(t, `{"version":1,"root":{"id":"window","row":[
		{"id":"sidebar","block":"mako.sidebar","size":272,"collapsible":true},
		{"id":"details","stack":["mako.files","mako.git"],"size":320,"collapsible":true},
		{"id":"workspace","column":[{"id":"header","block":"mako.header"},{"panes":"main","grow":1}],"grow":1}]}}`))
	files := findNode(t, p, ggui.RoleButton, "Files")
	toggle := findNode(t, p, ggui.RoleButton, "Toggle sidebar")
	if files.Origin.X < 272 || files.Origin.X > 272+20 || toggle.Origin.X < 272+320 {
		t.Fatalf("files' tab at %v, the header's toggle at %v", files, toggle)
	}
	if _, ok := p.Semantics().Find(ggui.RoleButton, "Agents"); ok {
		t.Fatal("a tab the document left out is drawn")
	}
}

// A block moved from the right panel to the foot of the workspace is drawn
// there, and shown and polled as the panel's tab was: revealing it opens
// it.
func TestABlockMovedUnderThePanesIsDrawnThere(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m, p := windowProbe(t, windowView, withLayout(t, `{"version":1,"root":{"id":"window","row":[
		{"id":"sidebar","block":"mako.sidebar","size":272,"collapsible":true},
		{"id":"workspace","column":[
			{"id":"header","block":"mako.header"},
			{"panes":"main","grow":1},
			{"id":"bottom","block":"mako.git","size":200,"collapsible":true}],"grow":1},
		{"id":"details","stack":["mako.files","mako.agents"],"size":320,"collapsible":true}]}}`))
	git := findNode(t, p, ggui.RoleText, "Open a session to see its changes.")
	if git.Origin.Y < 800-200 || git.Origin.X < 272 || git.Origin.X > 1280-324 {
		t.Fatalf("the git block at %v", git)
	}
	if !ggui.Untrack(func() bool { return m.blockShown(gitTab) }) || ggui.Untrack(func() bool { return m.blockShown(forgeTab) }) {
		t.Fatal("the git block is not shown, or the forge's is")
	}
	bottom := m.nodeState(ggui.Peek(m.WinDoc).Node("bottom"))
	bottom.open.Set(false)
	m.DetailsOpen.Set(false)
	if ggui.Untrack(func() bool { return m.blockShown(gitTab) }) {
		t.Fatal("the git block is shown closed")
	}
	m.revealBlock(gitTab)
	if !ggui.Peek(bottom.open) || ggui.Peek(m.DetailsOpen) {
		t.Fatal("revealing the git block opened the wrong panel")
	}
}

// A block the registry does not hold is drawn as missing, in its place,
// and named on its tab.
func TestABlockNotRegisteredIsDrawnMissing(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	_, p := windowProbe(t, windowView, withLayout(t, `{"version":1,"root":{"id":"window","row":[
		{"id":"workspace","column":[{"panes":"main","grow":1},{"id":"ci","block":"plugin:ci-status/ci","size":150}],"grow":1},
		{"id":"details","stack":["mako.files","plugin:notes/board"],"size":320,"active":1}]}}`))
	if n := strings.Count(p.Semantics().String(), `text "Missing block"`); n != 2 {
		t.Fatalf("%d blocks drawn missing, want 2:\n%s", n, p.Semantics().String())
	}
	findNode(t, p, ggui.RoleText, "plugin:ci-status/ci")
	findNode(t, p, ggui.RoleButton, "plugin:notes/board")
}

// The default document's right panel is shown, and its tabs picked, as the
// model's own state of it says, as before there was a document.
func TestTheRightPanelFollowsTheModelsState(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	if ggui.Peek(m.DetailTab) != filesTab {
		t.Fatalf("the panel starts on %q", ggui.Peek(m.DetailTab))
	}
	for _, open := range []bool{true, false} {
		for _, tab := range []string{filesTab, gitTab, agentsTab, forgeTab} {
			m.DetailsOpen.Set(open)
			m.DetailTab.Set(tab)
			for _, b := range []string{filesTab, gitTab, agentsTab, forgeTab} {
				got := ggui.Untrack(func() bool { return m.blockShown(b) })
				if want := open && tab == b; got != want {
					t.Errorf("open %v on %s: %s shown %v", open, tab, b, got)
				}
			}
		}
	}
	m.DetailsOpen.Set(false)
	m.revealBlock(agentsTab)
	if !ggui.Peek(m.DetailsOpen) || ggui.Peek(m.DetailTab) != agentsTab {
		t.Fatal("revealing the agents' tab did not open the panel on it")
	}
}
