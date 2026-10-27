package gui

import (
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// The overlay shows the context filled and the tokens spent, in the corner
// the settings choose, and goes when they turn it off.
func TestUsageOverlayFollowsItsSetting(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	state := m.usageState("s")
	state.loading = true
	state.usage.Set(&protocol.Usage{Input: 1000, CachedInput: 2000, Output: 500, Context: 50000, Window: 200000})
	p := ggui.ProbeBuilder(func() ggui.Widget { return usageOverlay(m, "s", nil) }, ggui.Sz(800, 600))
	defer p.Close()
	p.Frame()
	find := func() (ggui.Rect, bool) {
		n, ok := p.Semantics().Find(ggui.RoleText, Tf("%d%%", 25))
		return n.Rect, ok
	}
	r, ok := find()
	if !ok {
		t.Fatalf("no context shown:\n%s", p.Semantics().String())
	}
	if r.Origin.X < 400 || r.Origin.Y > 100 {
		t.Fatalf("overlay at %v, want the top right", r)
	}
	if _, ok := p.Semantics().Find(ggui.RoleText, Tf("%s in · %s out", "3k", "500")); !ok {
		t.Fatalf("no total shown:\n%s", p.Semantics().String())
	}
	m.Prefs.Update(func(v prefs) prefs { v.UsageCorner = "bottom-left"; return v })
	p.Frame()
	if r, _ = find(); r.Origin.X > 100 || r.Origin.Y < 400 {
		t.Fatalf("overlay at %v, want the bottom left", r)
	}
	m.Prefs.Update(func(v prefs) prefs { v.UsageOverlay = false; return v })
	p.Frame()
	if _, ok := find(); ok {
		t.Fatal("overlay still shown when turned off")
	}
}

// A shell has no conversation, so no overlay.
func TestShellHasNoUsageOverlay(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "shell", Project: "/tmp"}})
	usageOverlay(m, "s", nil)
	if m.usages["s"] != nil {
		t.Fatal("a shell polls for usage")
	}
}

// With the account's limits known, the overlay shows them, each with when
// it starts over, in place of the tokens spent.
func TestUsageOverlayShowsTheAccountsLimits(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	now := time.Unix(1_800_000_000, 0)
	m.Minute.Set(now)
	state := m.usageState("s")
	state.loading = true
	state.usage.Set(&protocol.Usage{Input: 1000, Output: 500, Context: 50000, Window: 1000000,
		Limits: []*protocol.RateLimit{
			{Minutes: protocol.FiveHours, Used: 23.5, Resets: now.Add(2*time.Hour + 10*time.Minute).Unix()},
			{Minutes: protocol.Week, Used: 41.2, Resets: now.Add(3*24*time.Hour + 4*time.Hour).Unix()},
		}})
	p := ggui.ProbeBuilder(func() ggui.Widget { return usageOverlay(m, "s", nil) }, ggui.Sz(800, 600))
	defer p.Close()
	p.Frame()
	for _, want := range []string{Tf("%d%%", 5), Tf("%dh", 5), Tf("%d%%", 24), T("Week"), Tf("%d%%", 41)} {
		if _, ok := p.Semantics().Find(ggui.RoleText, want); !ok {
			t.Errorf("no %q:\n%s", want, p.Semantics().String())
		}
	}
	if _, ok := p.Semantics().Find(ggui.RoleText, Tf("%s in · %s out", "1k", "500")); ok {
		t.Error("tokens shown beside the limits")
	}
	// When they start over is told under the pointer.
	detail := usageDetail(ggui.Peek(state.usage), now)
	for _, want := range []string{Tf("%dh %dm", 2, 10), Tf("%dd %dh", 3, 4)} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q has no %q", detail, want)
		}
	}
	// Past its reset, a window has started over.
	m.Minute.Set(now.Add(3 * time.Hour))
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, Tf("%d%%", 0)); !ok {
		t.Errorf("five hours past their reset:\n%s", p.Semantics().String())
	}
}

// The detail opens over the card while the pointer rests on it, and not
// for the terminal under it holding the keyboard.
func TestUsageDetailOpensOnHoverAlone(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	state := m.usageState("s")
	state.loading = true
	u := &protocol.Usage{Input: 1000, Output: 500, Context: 50000, Window: 200000}
	state.usage.Set(u)
	terminal := ggui.Focus(ggui.Box().Size(800, 600))
	p := ggui.ProbeBuilder(func() ggui.Widget { return ggui.Stack(terminal, usageOverlay(m, "s", nil)).Expand() }, ggui.Sz(800, 600))
	defer p.Close()
	p.Advance(0)
	p.Type(ggui.Mods{}, ggui.KeyTab) // the terminal takes the keyboard
	p.Frame()
	p.Advance(time.Second)
	detail := func() bool {
		_, ok := p.Semantics().Find(ggui.RoleText, usageDetail(u, ggui.Peek(m.Minute)))
		return ok
	}
	if detail() {
		t.Fatal("the terminal's focus opened the detail")
	}
	ctx, ok := p.Semantics().Find(ggui.RoleText, Tf("%d%%", 25))
	if !ok {
		t.Fatal("no card")
	}
	p.Move(ctx.Rect.Origin.Add(ggui.Pt(ctx.Rect.Size.W/2, ctx.Rect.Size.H/2)))
	p.Frame()
	p.Advance(time.Second)
	if !detail() {
		t.Fatalf("detail not open under the pointer:\n%s", p.Semantics().String())
	}
}
