package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/session"
	"github.com/ironpark/sanbo/internal/transcript"
)

func TestHookReportsFollowTheConversation(t *testing.T) {
	command, root := fakeAgent(t, "claude")
	cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{"claude": {Command: []string{command}, Env: map[string]string{"SANBO_FAKE_AGENT": "claude", "SANBO_FAKE_ROOT": root, "CLAUDE_CONFIG_DIR": root}}}}
	d := New(cfg, nil)
	d.mcp = nil
	if err := d.loadSessions(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer d.sessions.closeAll()
	// A socket's path is short, which a test's temporary directory is not.
	run, err := os.MkdirTemp("", "sanbo-hooks")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	d.hookBinary = "/opt/sanbo's/sanbod"
	d.listenHooks(t.Context(), run)
	socket := filepath.Join(run, "hooks-0.sock")

	project := t.TempDir()
	s, err := d.openSession(t.Context(), protocol.OpenSession{Agent: "claude", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	waitConversation(t, s) // found from its open log before any report
	first, firstPath := s.Info().ConversationID, s.TranscriptPath()
	var args []string
	data, _ := os.ReadFile(filepath.Join(root, "args.json"))
	_ = json.Unmarshal(data, &args)
	var settings struct {
		Hooks map[string][]struct{ Hooks []struct{ Command string } }
	}
	if i := slices.Index(args, "--settings"); i < 0 || json.Unmarshal([]byte(args[i+1]), &settings) != nil {
		t.Fatalf("launch has no hook: %q", args)
	}
	if h := settings.Hooks["SessionStart"]; len(h) != 1 || h[0].Hooks[0].Command != `'/opt/sanbo'\''s/sanbod' hook `+socket {
		t.Fatalf("hook %+v", settings)
	}

	// /clear: a new conversation, whose log the agent writes only later.
	cleared := conversationUUID()
	path := filepath.Join(root, "projects", "project", cleared+".jsonl")
	t.Setenv("SANBO_SESSION", s.ID())
	input, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "source": "clear", "session_id": cleared, "transcript_path": path})
	runHook([]string{socket}, strings.NewReader(string(input)), io.Discard)
	deadline := time.Now().Add(5 * time.Second)
	for s.Info().ConversationID != cleared {
		if time.Now().After(deadline) {
			t.Fatal("hook report not bound")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.TranscriptPath() != "" {
		t.Fatalf("unwritten log bound: %s", s.TranscriptPath())
	}

	// A report that ran earlier but arrives late changes nothing.
	report := func(seq int64, id, path string) error {
		return d.reportConversation(s, seq, transcript.HookEvent{Event: "SessionStart", SessionID: id, TranscriptPath: path})
	}
	if err := report(1, first, firstPath); err != nil {
		t.Fatal(err)
	}
	if s.Info().ConversationID != cleared {
		t.Fatal("a late report replaced a newer one")
	}
	// Nor does a log outside the agent's own place.
	if err := report(time.Now().UnixNano(), cleared, filepath.Join(project, cleared+".jsonl")); err == nil {
		t.Fatal("foreign log accepted")
	}

	// The log is bound once the agent writes it, though the agent still
	// has its first log open.
	record, _ := json.Marshal(map[string]any{"type": "user", "sessionId": cleared, "cwd": project, "timestamp": time.Now(), "message": map[string]any{"role": "user", "content": "after clear"}})
	if err := os.WriteFile(path, append(record, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	waitConversation(t, s)
	if s.TranscriptPath() != path || s.Info().ConversationID != cleared {
		t.Fatalf("bound %s %s", s.Info().ConversationID, s.TranscriptPath())
	}
	d.sessionMu.Lock()
	saved := d.saved[s.ID()]
	d.sessionMu.Unlock()
	if saved.Info.ConversationID != cleared || saved.TranscriptPath != path {
		t.Fatalf("saved %+v", saved)
	}
}

// hookedDaemon is a daemon serving hooks, with a Claude session started in
// it, and the hook socket.
func hookedDaemon(t *testing.T) (*Daemon, *session.Session, string) {
	t.Helper()
	command, root := fakeAgent(t, "claude")
	cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{"claude": {Command: []string{command}, Env: map[string]string{"SANBO_FAKE_AGENT": "claude", "SANBO_FAKE_ROOT": root, "CLAUDE_CONFIG_DIR": root}}}}
	d := New(cfg, nil)
	d.mcp = nil
	if err := d.loadSessions(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)
	run, err := os.MkdirTemp("", "sanbo-hooks")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	d.hookBinary = "/bin/sanbod"
	d.listenHooks(t.Context(), run)
	s, err := d.openSession(t.Context(), protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANBO_SESSION", s.ID())
	return d, s, filepath.Join(run, "hooks-0.sock")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHooksReportWhatTheAgentDoes(t *testing.T) {
	_, s, socket := hookedDaemon(t)
	for _, step := range []struct {
		input string
		want  protocol.AgentStatus
	}{
		{`{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`, protocol.AgentWorking},
		{`{"hook_event_name":"Notification","notification_type":"permission_prompt"}`, protocol.AgentWaiting},
		{`{"hook_event_name":"PostToolUse","tool_name":"Bash"}`, protocol.AgentWorking},
		{`{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, protocol.AgentWorking},
		{`{"hook_event_name":"Stop"}`, protocol.AgentDone},
	} {
		runHook([]string{socket}, strings.NewReader(step.input), io.Discard)
		waitFor(t, step.input+" not reported", func() bool { return s.Info().Status == step.want })
	}
}

func TestApprovalsWaitForTheWindow(t *testing.T) {
	d, s, socket := hookedDaemon(t)
	ask := func() <-chan []byte {
		out := make(chan []byte, 1)
		go func() {
			var buf bytes.Buffer
			runHook([]string{socket}, strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build"}}`), &buf)
			out <- buf.Bytes()
		}()
		return out
	}

	// With no window watching, the agent asks in its terminal at once.
	if got := <-ask(); len(got) != 0 {
		t.Fatalf("decided unwatched: %s", got)
	}
	if s.Info().Status != protocol.AgentWaiting {
		t.Fatalf("status %q", s.Info().Status)
	}

	d.approvals.watch()
	for _, c := range []struct{ decision, want string }{
		{protocol.DecisionAllow, `"behavior":"allow"`},
		{protocol.DecisionDeny, `"behavior":"deny","message":"not now"`},
		{protocol.DecisionTerminal, ""},
	} {
		out := ask()
		waitFor(t, "no approval shown", func() bool { return s.Info().Approval != nil })
		a := s.Info().Approval
		if a.Tool != "Bash" || a.Detail != "rm -rf build" || !strings.Contains(a.Input, `"command": "rm -rf build"`) {
			t.Fatalf("approval %+v", a)
		}
		if err := d.approvals.answer("other", protocol.AnswerApproval{ApprovalID: a.ID, Decision: c.decision}); err == nil {
			t.Fatal("answered for another session")
		}
		if err := d.approvals.answer(s.ID(), protocol.AnswerApproval{ApprovalID: a.ID, Decision: c.decision, Message: "not now"}); err != nil {
			t.Fatal(err)
		}
		got := string(<-out)
		if c.want == "" && got != "" || !strings.Contains(got, c.want) || c.want != "" && !strings.Contains(got, `"hookEventName":"PermissionRequest"`) {
			t.Fatalf("%s: hook printed %q", c.decision, got)
		}
		waitFor(t, "approval kept", func() bool { return s.Info().Approval == nil })
	}
}
