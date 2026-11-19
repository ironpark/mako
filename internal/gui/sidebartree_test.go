package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
)

// What is pinned shows at the top and not again below: a folder on its
// own, out of its project's tree, and a project whole, its folders under
// it, its owner's heading gone once it holds nothing else.
func TestPinnedProjectsShowOnce(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sidebarPreview(m) // ggui, a folder of XXGG/MAKO, pinned
	st := newSidebarState(m)
	p := ggui.ProbeBuilder(func() ggui.Widget { return projectTree(m, st, 0) }, ggui.Sz(280, 1200))
	defer p.Close()
	p.Frame()
	has := func(name string) bool { _, ok := p.Semantics().Find(ggui.RoleButton, name); return ok }
	if !has("XXGG/MAKO / ggui") || has("#ggui") || !has("#mako") {
		t.Fatalf("the folder pinned is not at the top alone:\n%s", p.Semantics().String())
	}
	m.togglePinScope(scopeKey{Section: "xxgg"})
	for range 3 {
		p.Frame()
	}
	sem := p.Semantics().String()
	if has("XXGG/MAKO / ggui") || !has("#ggui") || !has("#mako") || !has("XXGG/MAKO") {
		t.Fatalf("the project pinned is not at the top whole:\n%s", sem)
	}
	if strings.Count(sem, `button "#mako"`) != 1 || strings.Contains(sem, Tf("%s actions", "xxgg")) {
		t.Fatalf("the project pinned shows below too:\n%s", sem)
	}
}

func TestSidebarProjectHoverNewSession(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sidebarPreview(m)
	st := newSidebarState(m)
	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Scroll(projectTree(m, st, 0)) }, ggui.Sz(280, 1200))
	defer p.Close()
	p.Frame()

	center := func(r ggui.Rect) ggui.Point {
		return r.Origin.Add(ggui.Pt(r.Size.W/2, r.Size.H/2))
	}
	hover := func(name string) {
		t.Helper()
		n, ok := p.Semantics().Find(ggui.RoleButton, name)
		if !ok {
			t.Fatalf("button %q not found:\n%s", name, p.Semantics().String())
		}
		p.Move(center(n.Rect))
		p.Frame()
		p.Frame()
	}

	// Single-root project shows + on hover and opens a new session tab there.
	hover("acp-go")
	plus, ok := p.Semantics().Find(ggui.RoleButton, Tf("New session in %s", "acp-go"))
	if !ok {
		t.Fatalf("missing + button on hovered project:\n%s", p.Semantics().String())
	}
	p.Click(center(plus.Rect))
	p.Frame()
	tab := ggui.Peek(m.Selected)
	if !isLaunchTab(tab) || m.launches[tab] == nil || ggui.Peek(m.launches[tab].Form).Project != "/w/acp-go" {
		t.Fatalf("expected launch tab in /w/acp-go, got tab=%q", tab)
	}

	// Merged project parent row does not show +; its sub-project row does.
	hover("XXGG/MAKO")
	if _, ok := p.Semantics().Find(ggui.RoleButton, Tf("New session in %s", "XXGG/MAKO")); ok {
		t.Fatal("merged project parent row should not show a + button")
	}
	hover("#mako")
	subPlus, ok := p.Semantics().Find(ggui.RoleButton, Tf("New session in %s", "#mako"))
	if !ok {
		t.Fatalf("missing + button on hovered sub-project:\n%s", p.Semantics().String())
	}
	p.Click(center(subPlus.Rect))
	p.Frame()
	tab = ggui.Peek(m.Selected)
	if !isLaunchTab(tab) || m.launches[tab] == nil || ggui.Peek(m.launches[tab].Form).Project != "/w/mako" {
		t.Fatalf("expected launch tab in /w/mako, got tab=%q", tab)
	}
}

func TestSidebarProjectContextMenuHasOpenLocation(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sidebarPreview(m)
	st := newSidebarState(m)
	groups := m.spaceSections(st, 0)
	var single, merged sessionGroup
	for _, g := range groups {
		if g.Key == "xxgg" {
			merged = g
		}
		if g.Project == "/w/acp-go" {
			single = g
		}
	}
	if single.Key == "" || merged.Key == "" {
		t.Fatalf("expected single and merged groups, got %d groups", len(groups))
	}
	wantLabel := fileManagerLabel()

	// Single-root project menu has "Show in Finder" (or platform equivalent).
	if item := projectLocationItem(m, single); item == nil {
		t.Fatalf("expected %q item for single-root project", wantLabel)
	}
	// Merged project menu has a submenu for opening its folders.
	if item := projectLocationItem(m, merged); item == nil {
		t.Fatalf("expected %q submenu for merged project", wantLabel)
	}
	// Sub-project menu has "Show in Finder" (or platform equivalent).
	if node, path := m.rootLocation(merged, merged.Def.Roots[0]); node != "studio" || path != "/w/mako" {
		t.Fatalf("unexpected sub-project location (%q, %q)", node, path)
	}
}
