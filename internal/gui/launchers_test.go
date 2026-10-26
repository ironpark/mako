package gui

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/config"
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

// The tab bar starts an agent with a click, without asking where: an agent
// with profiles first offers them in a menu.
func TestQuickLauncherStartsAProfileWithoutAsking(t *testing.T) {
	home := t.TempDir()
	m, p := launcherApp(t, prefs{DefaultProject: home})

	menu, ok := p.Find("New claude session")
	if !ok {
		t.Fatal("no launcher for claude")
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
	if s.Agent != "claude" || s.Profile != "opus" || s.Project != home {
		t.Fatalf("started %s:%s in %q, want claude:opus in %q", s.Agent, s.Profile, s.Project, home)
	}
	if ggui.Peek(m.NewOpen) {
		t.Fatal("the launcher opened the new-session dialog")
	}
}

// A session asked for without a working directory starts in the node's home.
func TestSessionWithoutADirectoryStartsAtHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m, p := launcherApp(t, prefs{})
	m.launch("shell", "", "")
	probeUntil(t, p, "the session", func() bool { return ggui.Peek(m.Selected) != "" })
	if s, _ := m.session(ggui.Peek(m.Selected)); s.Project != home {
		t.Fatalf("started in %q, want the home folder %q", s.Project, home)
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
			return err == nil && ok && s.Idle == want
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
