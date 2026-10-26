package gui

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

func TestLaunchablesGroupProfilesUnderTheirAgent(t *testing.T) {
	got := launchables([]string{"aider", "claude", "claude:opus", "claude:work", "codex", "shell"})
	want := []launchable{
		{Agent: "shell"},
		{Agent: "claude", Profiles: []string{"opus", "work"}},
		{Agent: "codex"},
		{Agent: "aider"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launchables = %+v, want %+v", got, want)
	}
}

// launcherApp opens the window on a daemon of its own, with claude as a
// shell with an opus profile, and waits for it to connect.
func launcherApp(t *testing.T, pr prefs) (*model, *ggui.Probe) {
	t.Helper()
	t.Setenv("SANBO_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	cfg.Agents = map[string]agentspec.Agent{
		"claude": {Command: []string{"sh"}, Profiles: []agentspec.Profile{{Name: "opus"}}},
	}
	pr.DaemonMode, pr.Language = "embedded", "en"
	if err := savePrefs(pr.normalized()); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")
	ctx, cancel := context.WithCancel(context.Background())
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1200, 800))
	p.Setup(func() {
		setup(m)
		m.start(ctx, p.Post)
	})
	t.Cleanup(func() {
		m.shutdown()
		p.Close()
		cancel()
	})
	probeUntil(t, p, "the window to reach the app's daemon", func() bool { return ggui.Peek(m.Conn).OK })
	return m, p
}

// The + fans out launchers that start an agent with a click, without
// asking where: an agent with profiles first offers them in a menu.
func TestQuickLauncherStartsAProfileWithoutAsking(t *testing.T) {
	home := t.TempDir()
	m, p := launcherApp(t, prefs{DefaultProject: home})

	if _, ok := p.Find("New claude session"); ok {
		t.Fatal("the launchers show before the pointer comes to the +")
	}
	plus, ok := p.Find("New session…")
	if !ok {
		t.Fatal("no + after the tabs")
	}
	p.Move(plus.Center())
	p.Frame()
	p.Advance(time.Second)
	menu, ok := p.Find("New claude session")
	if !ok {
		t.Fatal("the + fans out no launcher for claude")
	}
	p.Click(menu.Center())
	p.Frame()
	item, ok := p.Find("claude · opus")
	if !ok {
		t.Fatal("the launcher shows no menu of claude's profiles")
	}
	p.Click(item.Center())
	probeUntil(t, p, "the session", func() bool { return ggui.Peek(m.Selected) != "" })
	s, _ := m.session(ggui.Peek(m.Selected))
	if s.GetAgent() != "claude" || s.GetProfile() != "opus" || s.GetProject() != home {
		t.Fatalf("started %s:%s in %q, want claude:opus in %q", s.GetAgent(), s.GetProfile(), s.GetProject(), home)
	}
	if ggui.Peek(m.NewOpen) {
		t.Fatal("the launcher opened the new-session dialog")
	}
}

// A click on the + opens the new-session dialog, as it always has; the
// launchers it fans out go back in once the pointer leaves.
func TestPlusOpensTheDialogAndFoldsItsLaunchers(t *testing.T) {
	m, p := launcherApp(t, prefs{DefaultProject: t.TempDir()})
	plus, ok := p.Find("New session…")
	if !ok {
		t.Fatal("no + after the tabs")
	}
	p.Move(plus.Center())
	p.Frame()
	p.Advance(time.Second)
	launcher, ok := p.Find("New terminal")
	if !ok {
		t.Fatal("the + fans out no terminal launcher")
	}
	if launcher.Rect.Origin.X <= plus.Rect.Origin.X {
		t.Fatalf("launcher at %v, want right of the + at %v", launcher.Rect, plus.Rect)
	}
	p.Move(ggui.Pt(600, 600))
	p.Frame()
	p.Advance(time.Second)
	p.Frame()
	if _, ok := p.Find("New terminal"); ok {
		t.Fatal("the launchers stay out after the pointer left")
	}
	p.Click(plus.Center())
	p.Frame()
	if !ggui.Peek(m.NewOpen) {
		t.Fatal("a click on the + did not open the new-session dialog")
	}
}

// A session asked for without a working directory starts in the node's home.
func TestSessionWithoutADirectoryStartsAtHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m, p := launcherApp(t, prefs{})
	m.launch("shell", "", "")
	probeUntil(t, p, "the session", func() bool { return ggui.Peek(m.Selected) != "" })
	if s, _ := m.session(ggui.Peek(m.Selected)); s.GetProject() != home {
		t.Fatalf("started in %q, want the home folder %q", s.GetProject(), home)
	}
}

// Closing a tab ends its session, after asking while a command runs in
// it; a shell at its prompt closes at once.
func TestClosingATabEndsItsSession(t *testing.T) {
	m, p := launcherApp(t, prefs{DefaultProject: t.TempDir(), ConfirmKill: true})
	idle := func(want bool) func() bool {
		return func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			c, err := m.client(ctx)
			if err != nil {
				return false
			}
			list, err := c.Sessions(ctx, m.key())
			s, ok := sessionIn(list, ggui.Peek(m.Selected))
			return err == nil && ok && s.GetIdle() == want
		}
	}
	m.launch("shell", "", "")
	probeUntil(t, p, "the session", func() bool { return len(ggui.Peek(m.Tabs)) == 1 && m.conns[ggui.Peek(m.Selected)] != nil })
	probeUntil(t, p, "its prompt", idle(true))
	m.conns[ggui.Peek(m.Selected)].sendText("sleep 30")
	probeUntil(t, p, "the command", idle(false))
	x, ok := p.Find("Close shell")
	if !ok {
		t.Fatal("no close button on the tab")
	}
	p.Click(x.Center())
	probeUntil(t, p, "the question", func() bool { return ggui.Peek(m.ConfirmOpen) })
	m.ConfirmOpen.Set(false) // as the dialog's button closes it
	m.confirmedKill()
	probeUntil(t, p, "the session to end", func() bool {
		return len(ggui.Peek(m.Tabs)) == 0 && len(ggui.Peek(m.Sessions)) == 0
	})

	m.launch("shell", "", "")
	probeUntil(t, p, "another session", func() bool { return len(ggui.Peek(m.Tabs)) == 1 })
	probeUntil(t, p, "its prompt", idle(true))
	m.askKill(ggui.Peek(m.Selected))
	probeUntil(t, p, "the idle shell to end", func() bool {
		return len(ggui.Peek(m.Tabs)) == 0 && len(ggui.Peek(m.Sessions)) == 0
	})
	if ggui.Peek(m.ConfirmOpen) {
		t.Fatal("asked before closing a shell at its prompt")
	}
}

// The usage card over a live terminal, which holds the keyboard, opens its
// detail on hover alone, wherever the layout puts the card.
func TestUsageDetailOverTheTerminalOpensOnHoverAlone(t *testing.T) {
	m, p := launcherApp(t, prefs{DefaultProject: t.TempDir(), UsageOverlay: true})
	m.launch("claude", "", "")
	probeUntil(t, p, "the session", func() bool {
		id := ggui.Peek(m.Selected)
		_, listed := m.session(id)
		return listed && m.conns[id] != nil
	})
	state := m.usageState(ggui.Peek(m.Selected))
	state.loading = true
	u := &protocol.Usage{Input: 1000, Output: 500, Context: 50000, Window: 200000}
	state.usage.Set(u)
	p.Frame()
	p.Advance(2 * time.Second)
	p.Frame()
	card, ok := p.Semantics().Find(ggui.RoleText, Tf("%s / %s · %d%%", "50k", "200k", 25))
	if !ok {
		t.Fatal("no usage card over the terminal")
	}
	detail := func() bool { _, ok := p.Semantics().Find(ggui.RoleText, usageDetail(u)); return ok }
	if detail() {
		t.Fatal("the terminal's focus opened the card's detail")
	}
	p.Move(card.Rect.Origin.Add(ggui.Pt(card.Rect.Size.W/2, card.Rect.Size.H/2)))
	p.Frame()
	p.Advance(time.Second)
	if !detail() {
		t.Fatal("hover does not open the card's detail")
	}
}
