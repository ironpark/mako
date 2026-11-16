package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

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
