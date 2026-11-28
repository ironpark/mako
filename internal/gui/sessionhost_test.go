package gui

import (
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// The draft another front-end writes shows in the composer, unless it is
// being written in here, and who else has the session open shows over it.
func TestComposerTakesTheHostsDraft(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	state := m.chatState("s")
	phone := &protocol.HostClient{Id: "phone", Name: "phone"}
	clients := []*protocol.HostClient{{Id: hostClient.id, Name: hostClient.name}, phone}
	took := func(text, by string, first bool) {
		m.tookHost("s", state, &protocol.HostDraft{Text: text, ClientId: by}, clients, first)
	}

	took("from the phone", "phone", true)
	if got := m.composerDraft("s"); got != "from the phone" {
		t.Fatalf("composer %q", got)
	}
	if others := ggui.Peek(state.host.others); len(others) != 1 || others[0].GetId() != "phone" {
		t.Fatalf("others: %v", others)
	}

	// Written in here: the phone's next draft waits.
	m.setComposerDraft("s", "mine")
	state.host.edited = time.Now()
	took("the phone again", "phone", false)
	if got := m.composerDraft("s"); got != "mine" {
		t.Fatalf("a draft written here was replaced by %q", got)
	}
	state.host.edited = time.Now().Add(-hostDraftTyping)
	took("the phone once more", "phone", false)
	if got := m.composerDraft("s"); got != "the phone once more" {
		t.Fatalf("composer %q", got)
	}

	// This window's own draft, as the host says it back, changes nothing.
	m.setComposerDraft("s", "typed on")
	took("typed", hostClient.id, false)
	if got := m.composerDraft("s"); got != "typed on" {
		t.Fatalf("its own draft came back as %q", got)
	}

	// Followed anew with a draft written here, the composer keeps it.
	took("stale", "phone", true)
	if got := m.composerDraft("s"); got != "typed on" {
		t.Fatalf("a draft written before the host was followed was replaced by %q", got)
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return hostPresence(state) }, ggui.Sz(400, 40))
	defer p.Close()
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, Tf("Also open on %s", "phone")); !ok {
		t.Fatalf("no presence:\n%s", p.Semantics().String())
	}
}
