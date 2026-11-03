package gui

import (
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// The composer's command menu is chosen from with ↑ and ↓; Enter takes the
// one chosen, which writes on for one taking arguments, and Tab completes
// to it.
func TestTheCommandMenuTakesKeys(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	state := m.chatState("s")
	state.menu.read = time.Now()
	state.menu.commands.Set([]*protocol.Command{
		{Name: "clear", Args: "x"}, {Name: "compact", Args: "[instructions]"}, {Name: "config", Args: "x"},
	})
	p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(600, 400))
	defer p.Close()
	p.Frame()
	field, ok := p.Semantics().Find(ggui.RoleTextField, T("Message"))
	if !ok {
		t.Fatal("no message field")
	}
	p.Click(field.Rect.Origin.Add(ggui.Pt(10, field.Rect.Size.H/2)))
	for _, key := range []ggui.KeyboardKey{ggui.KeyEnter, ggui.KeyTab} {
		p.Text("/c")
		p.Frame()
		p.Type(ggui.Mods{}, ggui.KeyArrowDown)
		p.Type(ggui.Mods{}, ggui.KeyArrowDown)
		p.Type(ggui.Mods{}, ggui.KeyArrowUp)
		p.Frame()
		if got := ggui.Peek(state.menu.pick); got != 1 {
			t.Fatalf("picked %d after ↓↓↑", got)
		}
		p.Type(ggui.Mods{}, key)
		p.Frame()
		if got := ggui.Peek(state.draft); got != "/compact " {
			t.Fatalf("draft %q after %v, want the command chosen", got, key)
		}
		m.setComposerDraft("s", "")
		p.Frame()
	}
}
