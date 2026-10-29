package gui

import (
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestQuestionPrompt(t *testing.T) {
	question := &protocol.ChatMessage{Id: "ask", Role: "tool", Kind: protocol.ToolQuestion, Pending: true}
	for _, tc := range []struct {
		name, state, status string
		messages            []*protocol.ChatMessage
		approval            *protocol.Approval
		want                string
	}{
		{"waiting notification", protocol.StateRunning, protocol.AgentWaiting, nil, nil, "waiting"},
		{"pending question", protocol.StateRunning, protocol.AgentWorking, []*protocol.ChatMessage{question}, nil, "question:ask"},
		{"completed turn", protocol.StateRunning, protocol.AgentDone, []*protocol.ChatMessage{question}, nil, ""},
		{"idle", protocol.StateRunning, protocol.AgentIdle, []*protocol.ChatMessage{question}, nil, ""},
		{"exited", protocol.StateExited, protocol.AgentWaiting, []*protocol.ChatMessage{question}, nil, ""},
		{"approval", protocol.StateRunning, protocol.AgentWaiting, nil, &protocol.Approval{Id: "approval"}, ""},
		{"abandoned question", protocol.StateRunning, protocol.AgentWorking, []*protocol.ChatMessage{question, {Role: "user", Text: "continue"}}, nil, ""},
		{"answered", protocol.StateRunning, protocol.AgentWorking, []*protocol.ChatMessage{{Role: "tool", Kind: protocol.ToolQuestion}}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &protocol.SessionInfo{State: tc.state, Status: tc.status, Approval: tc.approval}
			if got := questionPrompt(s, tc.messages); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChatQuestionInputReachesSession(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	link := newFakeLink()
	conn, err := newTermConn("s", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	m.conns["s"] = conn
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", State: protocol.StateRunning, Status: protocol.AgentWaiting}})
	state := m.chatState("s")
	state.draft.Set("keep this draft")
	p := ggui.ProbeBuilder(func() ggui.Widget { return chatInput(m, "s", 0, state) }, ggui.Sz(800, 450))
	t.Cleanup(func() { p.Close(); close(link.in); conn.close() })
	p.Frame()
	if _, ok := p.Find(T("Back to message")); !ok {
		t.Fatal("question input did not open")
	}
	p.Click(ggui.Pt(400, 200))
	p.Type(ggui.Mods{}, ggui.KeyArrowDown)
	p.Text("answer")
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if got := link.typed(); got != "\x1b[Banswer\r" {
		t.Fatalf("input %q", got)
	}
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", State: protocol.StateRunning, Status: protocol.AgentDone}})
	p.Frame()
	if _, ok := p.Find(T("Message")); !ok {
		t.Fatal("composer did not return")
	}
	if state.draft.Get() != "keep this draft" {
		t.Fatal("draft lost")
	}
	p.Tap(T("Answer the agent"))
	p.Frame()
	if _, ok := p.Find(T("Back to message")); !ok {
		t.Fatal("manual question input did not open")
	}
	p.Tap(T("Back to message"))
	p.Frame()
	if _, ok := p.Find(T("Message")); !ok {
		t.Fatal("manual close did not restore composer")
	}
}
