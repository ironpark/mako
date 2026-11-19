package gui

import (
	"slices"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
)

// Each New session opens a tab of its own, with what it picks of its own,
// and a tab closed forgets its own alone.
func TestEachNewSessionOpensItsOwnTab(t *testing.T) {
	m := newModel(config.Default(), "")
	a := m.openNew("n", "/work/a")
	b := m.openNew("n", "/work/b")
	tabs := slices.DeleteFunc(slices.Clone(ggui.Peek(m.Tabs)), func(id string) bool { return !isLaunchTab(id) })
	if len(tabs) != 2 || a == b {
		t.Fatalf("new sessions' tabs = %q", tabs)
	}
	if m.launchShown() != b {
		t.Fatal("the tab opened last is not the one shown")
	}
	a.Form.Update(func(f newSession) newSession { f.Prompt = "first"; return f })
	if ggui.Peek(b.Form).Prompt != "" || ggui.Peek(b.Form).Project != "/work/b" {
		t.Fatalf("the second tab took the first's: %+v", ggui.Peek(b.Form))
	}
	m.closeLaunch(tabs[1])
	if m.launches[tabs[1]] != nil || m.launches[tabs[0]] != a || ggui.Peek(a.Form).Prompt != "first" {
		t.Fatal("closing one new session's tab lost the other's")
	}
}
