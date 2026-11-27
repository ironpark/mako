package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	ggruntime "github.com/ironpark/ggui/runtime"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestAuthErrorStatusAndAttention(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Prefs.Update(func(p prefs) prefs { p.Notify = false; return p })

	ae := &protocol.AuthError{
		Kind:     protocol.AuthErrorOAuthExpired,
		Provider: "claude",
		Summary:  "Claude Code login expired on the host",
		Guidance: "Run `claude auth login` on the host machine.",
		Command:  "claude auth login",
	}
	s := &protocol.SessionInfo{
		Id:        "s1",
		Agent:     "claude",
		State:     protocol.StateRunning,
		Status:    protocol.AgentIdle,
		AuthError: ae,
	}
	m.Sessions.Set([]*protocol.SessionInfo{s})

	if got := m.dotOf(s); got != dotError {
		t.Fatalf("dotOf = %v, want dotError", got)
	}
	if !asksNow(s) {
		t.Fatal("asksNow should be true for active auth error")
	}
	if !attends(s) {
		t.Fatal("attends should be true for active auth error")
	}
	if got := m.statusOf(s); got != agentWaiting {
		t.Fatalf("statusOf = %v, want agentWaiting", got)
	}
	if got := statusLabel(s); got != T("Authentication required") {
		t.Fatalf("statusLabel = %q, want %q", got, T("Authentication required"))
	}

	// Stopped/exited sessions do not show active error dot or asksNow.
	stopped := &protocol.SessionInfo{Id: "s2", State: protocol.StateStopped, AuthError: ae}
	if got := m.dotOf(stopped); got != dotRest {
		t.Fatalf("stopped dotOf = %v, want dotRest", got)
	}
	if asksNow(stopped) {
		t.Fatal("stopped session should not askNow")
	}
}

func TestAuthErrorCardAndBarRenderAndCopyCommand(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	ae := &protocol.AuthError{
		Kind:     protocol.AuthErrorOAuthExpired,
		Provider: "claude",
		Summary:  "Claude Code login expired on the host",
		Guidance: "Run `claude auth login` on the host machine.",
		Command:  "claude auth login",
	}
	m.Sessions.Set([]*protocol.SessionInfo{{
		Id:        "s1",
		Agent:     "claude",
		State:     protocol.StateRunning,
		Status:    protocol.AgentIdle,
		AuthError: ae,
	}})

	p := ggui.ProbeBuilder(func() ggui.Widget { return authErrorCard(m, "s1") }, ggui.Sz(640, 260))
	defer p.Close()
	var clip *ggruntime.MemoryClipboard
	p.OnFrame(func() { clip, _ = ggui.CurrentClipboard().(*ggruntime.MemoryClipboard) })
	p.Frame()

	sem := p.Semantics().String()
	if !strings.Contains(sem, ae.GetSummary()) {
		t.Fatalf("authErrorCard missing summary:\n%s", sem)
	}
	btn, ok := p.Semantics().Find(ggui.RoleButton, T("Copy command"))
	if !ok {
		t.Fatalf("Copy command button not found:\n%s", sem)
	}
	p.Click(btn.Rect.Origin.Add(ggui.Pt(btn.Rect.Size.W/2, btn.Rect.Size.H/2)))
	p.Frame()
	if clip == nil || clip.Read() != "claude auth login" {
		t.Fatalf("clipboard after click = %q, want %q", clip.Read(), "claude auth login")
	}

	// Clearing AuthError collapses the card cleanly.
	p.Post(func() {
		m.Sessions.Set([]*protocol.SessionInfo{{
			Id:     "s1",
			Agent:  "claude",
			State:  protocol.StateRunning,
			Status: protocol.AgentWorking,
		}})
	})
	p.Frame()
	if strings.Contains(p.Semantics().String(), ae.GetSummary()) {
		t.Fatalf("authErrorCard still shown after clearing:\n%s", p.Semantics().String())
	}

	// authErrorBar renders cleanly when active.
	m.Sessions.Set([]*protocol.SessionInfo{{
		Id:        "s1",
		Agent:     "claude",
		State:     protocol.StateRunning,
		Status:    protocol.AgentIdle,
		AuthError: ae,
	}})
	pb := ggui.ProbeBuilder(func() ggui.Widget { return authErrorBar(m, "s1") }, ggui.Sz(640, 80))
	defer pb.Close()
	pb.Frame()
	if !strings.Contains(pb.Semantics().String(), ae.GetSummary()) {
		t.Fatalf("authErrorBar missing summary:\n%s", pb.Semantics().String())
	}
}
