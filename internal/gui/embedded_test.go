package gui

import (
	"context"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

// probeUntil runs frames until cond holds, so work the model posts lands.
func probeUntil(t *testing.T, p *ggui.Probe, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		p.Frame()
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAppRunsItsOwnDaemon(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Name = "inside"
	cfg.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	if err := savePrefs(prefs{DaemonMode: "embedded"}.normalized()); err != nil {
		t.Fatal(err)
	}

	m := newModel(cfg, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	p.Setup(func() {
		setup(m)
		m.start(ctx, p.Post)
	})
	defer p.Close()
	defer m.shutdown()

	probeUntil(t, p, "the app's daemon", func() bool { return ggui.Peek(m.DaemonState).Running })
	probeUntil(t, p, "the window to reach it", func() bool { return ggui.Peek(m.Conn).OK })
	if got := ggui.Peek(m.Conn).Self.GetName(); got != "inside" {
		t.Fatalf("connected to %q, want the app's daemon", got)
	}

	// A session started through the window runs in the app's daemon.
	m.launch("shell", "", t.TempDir())
	probeUntil(t, p, "a session", func() bool { return len(ggui.Peek(m.Sessions)) == 1 })
	if s := ggui.Peek(m.Sessions)[0]; s.GetState() != protocol.StateRunning {
		t.Fatalf("session state %q", s.GetState())
	}

	// Stopping it asks first, since that session would end.
	m.guardStop(m.stopEmbedded)
	if !ggui.Peek(m.StopOpen) || !ggui.Peek(m.DaemonState).Running {
		t.Fatal("the daemon stopped without asking while a session ran in it")
	}
	m.confirmedStop()
	if ggui.Peek(m.DaemonState).Running || m.embedded.running() {
		t.Fatal("the daemon still runs after a confirmed stop")
	}
	probeUntil(t, p, "the window to notice", func() bool { return !ggui.Peek(m.Conn).OK })
}

func TestAutoPrefersARunningDaemon(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}

	// Another daemon is already up: an app in auto mode uses it.
	other := newEmbeddedDaemon()
	ready := make(chan string, 1)
	outside := *cfg
	outside.Name = "outside"
	other.start(outside, func(addr string) { ready <- addr }, func(error) {})
	defer other.stop()
	addr := <-ready

	m := newModel(cfg, addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	p.Setup(func() {
		setup(m)
		m.start(ctx, p.Post)
	})
	defer p.Close()
	defer m.shutdown()

	probeUntil(t, p, "a decision", func() bool { return ggui.Peek(m.DaemonState).Mode != "" })
	if s := ggui.Peek(m.DaemonState); s.Mode != "external" || m.embedded.running() {
		t.Fatalf("state %+v: want the running daemon used, none started", s)
	}
	probeUntil(t, p, "the window to reach it", func() bool { return ggui.Peek(m.Conn).OK })
	if got := ggui.Peek(m.Conn).Self.GetName(); got != "outside" {
		t.Fatalf("connected to %q", got)
	}
}

// Restart restores both tabs, but only the visible one starts its process.
func TestRestartResumesOnlyVisibleSession(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	if err := savePrefs(prefs{DaemonMode: "embedded"}.normalized()); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	p.Setup(func() { setup(m); m.start(ctx, p.Post) })
	defer p.Close()
	defer m.shutdown()
	probeUntil(t, p, "connection", func() bool { return ggui.Peek(m.Conn).OK })
	m.launch("shell", "", t.TempDir())
	probeUntil(t, p, "first session", func() bool { return len(ggui.Peek(m.Sessions)) == 1 })
	first := ggui.Peek(m.Sessions)[0].GetId()
	m.launch("shell", "", t.TempDir())
	probeUntil(t, p, "second session", func() bool { return len(ggui.Peek(m.Sessions)) == 2 })
	second := ggui.Peek(m.Selected)
	if second == first {
		t.Fatal("second tab was not selected")
	}
	m.restartEmbedded()
	probeUntil(t, p, "restored visible session", func() bool {
		a, okA := m.session(first)
		b, okB := m.session(second)
		return ggui.Peek(m.Conn).OK && okA && okB && a.GetState() == protocol.StateStopped && b.GetState() == protocol.StateRunning
	})
	if m.conns[first] != nil {
		t.Fatal("hidden tab reattached on restart")
	}
	m.selectSession(first)
	probeUntil(t, p, "hidden session activated", func() bool {
		s, ok := m.session(first)
		return ok && s.GetState() == protocol.StateRunning
	})
}
