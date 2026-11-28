package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestParseSideQuestionCommand(t *testing.T) {
	cases := []struct {
		agent   string
		draft   string
		wantQ   string
		wantHit bool
	}{
		{"claude", "/btw which file holds retry?", "which file holds retry?", true},
		{"claude", "/btw", "", true},
		{"claude", "  /BTW   why this approach?  ", "why this approach?", true},
		{"claude", "/side hello", "", false},
		{"codex", "/side which file?", "which file?", true},
		{"codex", "/btw which file?", "which file?", true},
		{"agy", "/btw what does this error mean?", "what does this error mean?", true},
		{"claude", "normal message about /btw", "", false},
		{"claude", "/compact now", "", false},
	}
	for _, tc := range cases {
		q, ok := parseSideQuestionCommand(tc.agent, tc.draft)
		if ok != tc.wantHit || q != tc.wantQ {
			t.Errorf("parseSideQuestionCommand(%q, %q) = (%q, %v), want (%q, %v)", tc.agent, tc.draft, q, ok, tc.wantQ, tc.wantHit)
		}
	}
}

func TestSideQuestionPreservesDraftAndHistory(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	m := newModel(&config.Config{NodeID: "n1"}, "")
	link := newFakeLink()
	conn, err := newTermConn("s1", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()

	m.conns["s1"] = conn
	m.Attached.Set([]string{"s1"})
	m.Sessions.Set([]*protocol.SessionInfo{{
		Id:      "s1",
		NodeId:  "n1",
		Agent:   "claude",
		Project: "/work/app",
		State:   protocol.StateRunning,
		Status:  protocol.AgentWorking,
		Caps:    protocol.CapChat | protocol.CapSideQuestion,
	}})

	state := m.chatState("s1")
	state.draft.Set("my unfinished main draft")
	state.result.Set(chatResult{
		Loaded: true,
		Chat: &protocol.Chat{
			Supported:    true,
			Conversation: "conv-1",
			Messages: []*protocol.ChatMessage{
				{Role: "user", Text: "Refactor the daemon"},
			},
		},
	})

	p := ggui.ProbeBuilder(func() ggui.Widget { return chatView(m, "s1", 1) }, ggui.Sz(900, 720))
	defer p.Close()
	p.Frame()

	// 1. Clicking /btw opens the inline side-question box without changing the main draft.
	btn, ok := p.Semantics().Find(ggui.RoleButton, T("Ask a side question"))
	if !ok {
		t.Fatalf("missing /btw button in composer bar:\n%s", p.Semantics().String())
	}
	p.Click(btn.Rect.Center())
	p.Frame()

	if !ggui.Peek(state.side.open) || !ggui.Peek(state.side.asking) {
		t.Fatal("expected side-question input to be open")
	}
	if got := m.composerDraft("s1"); got != "my unfinished main draft" {
		t.Fatalf("main composer draft mutated to %q", got)
	}

	// 2. Populating an answer displays the card with Copy and Send to composer,
	// without adding anything to chat messages, pending turns, or composer history.
	state.side.asking.Set(false)
	state.side.question.Set("which file holds retry logic?")
	state.side.answer.Set("It lives in `internal/daemon/codex_appserver.go`.")
	state.side.mode.Set(protocol.SideQuestionModeNative)
	p.Frame()

	if _, ok := p.Semantics().Find(ggui.RoleText, T("Side question")); !ok {
		t.Fatalf("side question card heading not rendered:\n%s", p.Semantics().String())
	}
	if _, ok := p.Semantics().Find(ggui.RoleButton, T("Copy")); !ok {
		t.Fatalf("Copy button missing in side question card:\n%s", p.Semantics().String())
	}
	sendBtn, ok := p.Semantics().Find(ggui.RoleButton, T("Send to composer"))
	if !ok {
		t.Fatalf("Send to composer button missing in side question card:\n%s", p.Semantics().String())
	}

	// Main chat history and pending turns remain completely untouched.
	if got := len(ggui.Peek(state.result).Chat.GetMessages()); got != 1 {
		t.Fatalf("chat history length changed to %d, want 1", got)
	}
	if got := len(ggui.Peek(state.pending)); got != 0 {
		t.Fatalf("pending messages = %d, want 0", got)
	}
	if got := len(state.sent); got != 0 {
		t.Fatalf("composer sent history = %d, want 0", got)
	}

	// 3. Clicking "Send to composer" appends the answer to the existing main draft.
	p.Click(sendBtn.Rect.Center())
	p.Frame()
	wantDraft := "my unfinished main draft\n\nIt lives in `internal/daemon/codex_appserver.go`."
	if got := m.composerDraft("s1"); got != wantDraft {
		t.Fatalf("composer draft = %q, want %q", got, wantDraft)
	}

	// 4. Dismissing closes the card.
	dismissBtn, ok := p.Semantics().Find(ggui.RoleButton, T("Dismiss side question"))
	if !ok {
		t.Fatalf("Dismiss button missing:\n%s", p.Semantics().String())
	}
	p.Click(dismissBtn.Rect.Center())
	p.Frame()
	if ggui.Peek(state.side.open) {
		t.Fatal("side question card stayed open after dismiss")
	}

	// 5. Typing `/btw ...` directly in the composer intercepts it without writing
	// to the PTY or queueing a pending turn.
	m.setComposerDraft("s1", "/btw what port does makod use?")
	m.sendComposer("s1")
	p.Frame()
	if got := link.typed(); got != "" {
		t.Fatalf("/btw leaked to PTY: %q", got)
	}
	if got := m.composerDraft("s1"); got != "" {
		t.Fatalf("expected /btw command cleared from composer draft, got %q", got)
	}
	if got := len(ggui.Peek(state.pending)); got != 0 {
		t.Fatalf("/btw added %d pending turns, want 0", got)
	}
	if got := ggui.Peek(state.side.question); got != "what port does makod use?" {
		t.Fatalf("side question = %q", got)
	}
}

func TestSideQuestionWorksInSpaceWindowAndHidesForUnsupported(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Spaces.Set(spaceList{
		Spaces: []space{
			{ID: "sp-oss", Name: "OSS", Color: "teal", Owners: []string{"github.com/oss"}},
		},
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{
			Id: "s-oss", NodeId: "n1", Agent: "claude", Project: "/oss/mako", Owner: "github.com/oss",
			State: protocol.StateRunning, Status: protocol.AgentWorking,
			Caps: protocol.CapChat | protocol.CapSideQuestion,
		},
		{
			Id: "s-shell", NodeId: "n1", Agent: "shell", Project: "/oss/mako", Owner: "github.com/oss",
			State: protocol.StateRunning, Caps: 0,
		},
	})
	m.Attached.Set([]string{"s-oss", "s-shell"})
	m.setChatMode("s-oss", true)
	m.chatState("s-oss").side.open.Set(true)
	m.chatState("s-oss").side.question.Set("why is it slow?")
	m.chatState("s-oss").side.answer.Set("It is indexing large files.")

	sw := m.openSpaceWindow("sp-oss")
	if sw == nil {
		t.Fatal("openSpaceWindow returned nil")
	}
	m.spaceWindowSelectSession(sw, "s-oss")

	spaceProbe := ggui.ProbeBuilder(func() ggui.Widget { return spaceWindowView(m, sw) }, ggui.Sz(1040, 720))
	defer spaceProbe.Close()
	spaceProbe.Frame()

	sem := spaceProbe.Semantics().String()
	if !strings.Contains(sem, T("Side question")) || !strings.Contains(sem, T("Send to composer")) {
		t.Fatalf("Space window did not render side question card:\n%s", sem)
	}

	// Plain shell session has no side-question capability -> button is hidden.
	if ok, reason := m.sideQuestionCapable("s-shell"); ok || reason == "" {
		t.Fatalf("sideQuestionCapable(shell) = (%v, %q), want (false, reason)", ok, reason)
	}
}
