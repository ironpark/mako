package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
)

// Typing a schedule keeps its field, with its focus and caret, and only the
// hint below it follows what is typed.
func TestRoutineScheduleFieldKeepsFocusAsItIsTyped(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	p := ggui.ProbeBuilder(func() ggui.Widget { return settingsContent(m, "routines") }, ggui.Sz(1100, 1400))
	defer p.Close()
	p.Frame()
	field, ok := p.Semantics().Find(ggui.RoleTextField, T("Schedule"))
	if !ok {
		t.Fatalf("no schedule field:\n%s", p.Semantics().String())
	}
	p.Click(field.Rect.Origin.Add(ggui.Pt(field.Rect.Size.W-4, field.Rect.Size.H/2)))
	p.Key("End")
	for _, s := range []string{"x", "y", "z"} {
		p.Text(s)
		p.Frame()
		got, ok := p.Semantics().Focused()
		if !ok || got.ID != field.ID {
			t.Fatalf("after typing %q the focus is on %+v (%v), not the schedule field", s, got.Node, ok)
		}
	}
	got, _ := p.Semantics().Find(ggui.RoleTextField, T("Schedule"))
	if !strings.HasSuffix(got.Value, "xyz") {
		t.Fatalf("schedule field = %q, want what was typed at its end", got.Value)
	}
	if sem := p.Semantics().String(); !strings.Contains(sem, "day-of-week field") {
		t.Fatalf("the hint does not follow the schedule typed:\n%s", sem)
	}
}
