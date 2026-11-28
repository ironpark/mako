package gui

import (
	"fmt"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// semNode is the node p shows named name, if any.
func semNode(p *ggui.Probe, name string) (ggui.SemNode, bool) {
	for _, n := range p.Semantics().All() {
		if n.Name == name {
			return n, true
		}
	}
	return ggui.SemNode{}, false
}

// TestAccountAnalyticsFollowsChatsRefreshAndScrolls fills the Analytics
// tab with ten projects in a short window: the dialog scrolls to the last,
// a chat's read shows in it at once, and Refresh updates a session whose
// chat is open as well as the others.
func TestAccountAnalyticsFollowsChatsRefreshAndScrolls(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)
	fd := startFakeRaceDaemon(t)
	m := newModel(&config.Config{NodeID: "local"}, "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "local", Name: "local"}})
	m.link.setDaemon(fd.sock)
	m.link.setLinked(true)
	t.Cleanup(func() { m.link.hangUp() })
	var sessions []*protocol.SessionInfo
	for i := range 10 {
		id := fmt.Sprintf("s%d", i)
		sessions = append(sessions, &protocol.SessionInfo{Id: id, NodeId: "local", Agent: "claude", Project: fmt.Sprintf("/work/proj-%d", i), State: protocol.StateRunning, Caps: protocol.CapChat})
		fd.usage[id] = &protocol.Usage{Input: int64(10-i) * 1000, CostUsd: float64(10 - i)}
	}
	m.Sessions.Set(sessions)
	m.AccountUsage.Data.Set(&protocol.AccountUsage{})

	p := ggui.ProbeBuilder(func() ggui.Widget { return accountUsageDialog(m) }, ggui.Sz(640, 480))
	defer p.Close()
	m.post = p.Post
	p.Frame()
	m.AccountUsage.Open.Set(true)
	p.Frame()
	p.Tap("Project & session usage")
	total := func(want string) func() bool {
		return func() bool { _, ok := semNode(p, want); return ok }
	}
	refresh := func() {
		btn, ok := p.FindRole(ggui.RoleButton, "Refresh")
		if !ok {
			t.Fatalf("no Refresh button:\n%s", p.Semantics().String())
		}
		p.Click(btn.Rect.Center())
	}
	refresh()
	pump(t, "the refreshed totals", total("Loaded sessions total: 55k in · 0 out · $55.00"), p)

	// The cheapest project is last, below the window, until scrolled to.
	last, ok := semNode(p, "proj-9")
	if !ok || !last.Rect.Empty() {
		t.Fatalf("proj-9 = %+v %v, want it laid out below the window", last.Rect, ok)
	}
	for range 10 {
		p.Scroll(ggui.Pt(320, 300), ggui.Pt(0, -120))
	}
	if last, _ = semNode(p, "proj-9"); last.Rect.Empty() {
		t.Fatalf("scrolling did not bring proj-9 into view:\n%s", p.Semantics().String())
	}

	// A chat's read shows at once.
	fd.mu.Lock()
	fd.usage["s9"] = &protocol.Usage{Input: 50000, CostUsd: 20}
	fd.mu.Unlock()
	m.loadChat("s9", m.chatState("s9"))
	pump(t, "the chat's usage", total("Loaded sessions total: 104k in · 0 out · $74.00"), p)

	// Refresh reads the session with an open chat too.
	fd.mu.Lock()
	fd.usage["s9"] = &protocol.Usage{Input: 60000, CostUsd: 30}
	fd.mu.Unlock()
	for range 10 {
		p.Scroll(ggui.Pt(320, 300), ggui.Pt(0, 120))
	}
	refresh()
	pump(t, "the refresh of an open chat", total("Loaded sessions total: 114k in · 0 out · $84.00"), p)
}
