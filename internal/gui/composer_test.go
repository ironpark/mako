package gui

import (
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

// Enter sends what is written at once; Shift+Enter breaks the line, and
// the lines reach the agent as one paste, then Enter.
func TestComposerSendsSeveralLinesOnEnter(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	link := newFakeLink()
	conn, err := newTermConn("s", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()
	m.conns["s"] = conn
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	link.output(t, "\x1b[?2004h") // the agent takes bracketed pastes
	waitFor(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return conn.vt.Terminal().ModeEnabled(gostty.ModeBracketedPaste)
	})

	draft := m.chatState("s").draft
	p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", draft) }, ggui.Sz(600, 300))
	defer p.Close()
	p.Frame()
	field, ok := p.Semantics().Find(ggui.RoleTextField, T("Message"))
	if !ok {
		t.Fatalf("no composer field:\n%s", p.Semantics().String())
	}
	p.Click(field.Rect.Origin.Add(ggui.Pt(10, field.Rect.Size.H/2)))
	p.Text("one")
	p.Type(ggui.Mods{Shift: true}, ggui.KeyEnter)
	p.Text("two")
	if got := ggui.Peek(draft); got != "one\ntwo" {
		t.Fatalf("draft %q, want two lines", got)
	}
	if got := link.typed(); got != "" {
		t.Fatalf("Shift+Enter sent %q", got)
	}
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if got, want := link.typed(), "\x1b[200~one\ntwo\x1b[201~\r"; got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
	if ggui.Peek(draft) != "" {
		t.Fatal("the draft stayed after sending")
	}
}

// A call turns a spinner while it is at work: unanswered while its agent
// works, or with a subagent it started still running; one left open by an
// agent that has stopped does not.
func TestToolsAtWorkAreTold(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	session := func(state, status string) {
		m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", State: state, Status: status}})
	}
	open := &protocol.ChatMessage{Role: "tool", Id: "t", Tool: "Bash", Pending: true}
	done := &protocol.ChatMessage{Role: "tool", Id: "d", Tool: "Agent"}
	running := &protocol.SubAgent{CallId: "d", Running: true}
	for _, c := range []struct {
		state, status string
		msg           *protocol.ChatMessage
		agent         *protocol.SubAgent
		want          bool
	}{
		{protocol.StateRunning, string(protocol.AgentWorking), open, nil, true},
		{protocol.StateRunning, string(protocol.AgentWaiting), open, nil, true},
		{protocol.StateRunning, string(protocol.AgentIdle), open, nil, false},
		{protocol.StateExited, string(protocol.AgentWorking), open, nil, false},
		{protocol.StateRunning, string(protocol.AgentWorking), done, nil, false},
		{protocol.StateRunning, string(protocol.AgentIdle), done, running, true},
		{protocol.StateExited, string(protocol.AgentIdle), done, running, false},
	} {
		session(c.state, c.status)
		if got := toolLive(m, "s", c.msg, c.agent); got != c.want {
			t.Errorf("%s/%s, pending %v, agent running %v: live %v, want %v", c.state, c.status, c.msg.GetPending(), c.agent.GetRunning(), got, c.want)
		}
	}
}

// The subagents at work show over the composer, each with what it was
// asked and how far it has got, and go once they finish.
func TestRunningSubagentsShowOverTheComposer(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", State: protocol.StateRunning}})
	state := m.chatState("s")
	agent := &protocol.SubAgent{Id: "a", CallId: "c", Kind: "Explore", Description: "Find the tests", Running: true, ToolCalls: 3, Tool: "Bash", Detail: "go test ./..."}
	state.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Agents: []*protocol.SubAgent{agent}}})
	p := ggui.ProbeBuilder(func() ggui.Widget { return runningAgents(m, "s", state) }, ggui.Sz(700, 200))
	defer p.Close()
	p.Frame()
	for _, want := range []string{"Find the tests", "Explore · " + Tf("%d tool calls", 3), "Bash  go test ./..."} {
		if _, ok := p.Semantics().Find(ggui.RoleText, want); !ok {
			t.Errorf("no %q:\n%s", want, p.Semantics().String())
		}
	}
	finished := &protocol.SubAgent{Id: "a", CallId: "c", Description: "Find the tests", ToolCalls: 4}
	state.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Agents: []*protocol.SubAgent{finished}}})
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, "Find the tests"); ok {
		t.Error("a finished subagent still shows as running")
	}
}
