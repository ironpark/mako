package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/session"
	"github.com/ironpark/sanbo/internal/transcript"
	"google.golang.org/protobuf/proto"
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
	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	waitConversation(t, s) // found from its open log before any report
	first, firstPath := s.Info().GetConversationId(), s.TranscriptPath()
	var args []string
	data, _ := os.ReadFile(filepath.Join(root, "args.json"))
	_ = json.Unmarshal(data, &args)
	var settings struct {
		Hooks      map[string][]struct{ Hooks []struct{ Command string } }
		StatusLine struct{ Command string }
	}
	if i := slices.Index(args, "--settings"); i < 0 || json.Unmarshal([]byte(args[i+1]), &settings) != nil {
		t.Fatalf("launch has no hook: %q", args)
	}
	if h := settings.Hooks["SessionStart"]; len(h) != 1 || h[0].Hooks[0].Command != `'/opt/sanbo'\''s/sanbod' hook `+socket {
		t.Fatalf("hook %+v", settings)
	}
	if settings.StatusLine.Command != `'/opt/sanbo'\''s/sanbod' statusline `+socket {
		t.Fatalf("status line %+v", settings)
	}

	// The status line reports the window and the account's limits.
	t.Setenv("SANBO_SESSION", s.ID())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	status := `{"context_window":{"context_window_size":1000000},"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1738425600},"seven_day":{"used_percentage":41.2,"resets_at":1738857600}}}`
	runStatusLine([]string{socket}, strings.NewReader(status), io.Discard)
	want := &protocol.Usage{Window: 1000000, Limits: []*protocol.RateLimit{{Minutes: 300, Used: 23.5, Resets: 1738425600}, {Minutes: 10080, Used: 41.2, Resets: 1738857600}}}
	for deadline := time.Now().Add(5 * time.Second); !proto.Equal(s.Reported(), want); {
		if time.Now().After(deadline) {
			t.Fatalf("reported %+v", s.Reported())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// One before the first reply has no limits, and keeps the last.
	runStatusLine([]string{socket}, strings.NewReader(`{"context_window":{"context_window_size":1000000}}`), io.Discard)
	time.Sleep(50 * time.Millisecond)
	if !proto.Equal(s.Reported(), want) {
		t.Fatalf("limits lost: %+v", s.Reported())
	}

	// /clear: a new conversation, whose log the agent writes only later.
	cleared := conversationUUID()
	path := filepath.Join(root, "projects", "project", cleared+".jsonl")
	t.Setenv("SANBO_SESSION", s.ID())
	input, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "source": "clear", "session_id": cleared, "transcript_path": path})
	runHook([]string{socket}, strings.NewReader(string(input)), io.Discard)
	deadline := time.Now().Add(5 * time.Second)
	for s.Info().GetConversationId() != cleared {
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
	if s.Info().GetConversationId() != cleared {
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
	if s.TranscriptPath() != path || s.Info().GetConversationId() != cleared {
		t.Fatalf("bound %s %s", s.Info().GetConversationId(), s.TranscriptPath())
	}
	d.sessionMu.Lock()
	saved := d.saved[s.ID()]
	d.sessionMu.Unlock()
	if saved.Info.GetConversationId() != cleared || saved.TranscriptPath != path {
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
	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
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
		waitFor(t, step.input+" not reported", func() bool { return s.Info().GetStatus() == step.want })
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
	if s.Info().GetStatus() != protocol.AgentWaiting {
		t.Fatalf("status %q", s.Info().GetStatus())
	}

	d.approvals.watch()
	for _, c := range []struct{ decision, want string }{
		{protocol.DecisionAllow, `"behavior":"allow"`},
		{protocol.DecisionDeny, `"behavior":"deny","message":"not now"`},
		{protocol.DecisionTerminal, ""},
	} {
		out := ask()
		waitFor(t, "no approval shown", func() bool { return s.Info().GetApproval() != nil })
		a := s.Info().GetApproval()
		if a.GetTool() != "Bash" || a.GetDetail() != "rm -rf build" || !strings.Contains(a.GetInput(), `"command": "rm -rf build"`) {
			t.Fatalf("approval %+v", a)
		}
		if err := d.approvals.answer("other", &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: c.decision}); err == nil {
			t.Fatal("answered for another session")
		}
		if err := d.approvals.answer(s.ID(), &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: c.decision, Message: "not now"}); err != nil {
			t.Fatal(err)
		}
		got := string(<-out)
		if c.want == "" && got != "" || !strings.Contains(got, c.want) || c.want != "" && !strings.Contains(got, `"hookEventName":"PermissionRequest"`) {
			t.Fatalf("%s: hook printed %q", c.decision, got)
		}
		waitFor(t, "approval kept", func() bool { return s.Info().GetApproval() == nil })
	}
}

// sanbo's status line takes the place of the user's, so it prints theirs:
// the project's local settings first, then its shared ones, then the
// user's.
func TestStatusLinePrintsTheUsersOwn(t *testing.T) {
	config, project := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("SANBO_SESSION", "")
	write := func(path, command string) {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		body, _ := json.Marshal(map[string]any{"statusLine": map[string]string{"type": "command", "command": command}})
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	input := `{"cwd":"/elsewhere","workspace":{"project_dir":` + strconv.Quote(project) + `},"model":{"display_name":"Opus"}}`
	run := func() string {
		var out bytes.Buffer
		runStatusLine([]string{"/nowhere.sock"}, strings.NewReader(input), &out)
		return out.String()
	}
	if got := run(); got != "" {
		t.Fatalf("no status line printed %q", got)
	}
	write(filepath.Join(config, "settings.json"), `printf user`)
	if got := run(); got != "user" {
		t.Fatalf("user's: %q", got)
	}
	write(filepath.Join(project, ".claude", "settings.json"), `printf project`)
	write(filepath.Join(project, ".claude", "settings.local.json"), `sed -n 's/.*"display_name":"\([^"]*\)".*/\1/p'`)
	if got := strings.TrimSpace(run()); got != "Opus" {
		t.Fatalf("local's, reading the input: %q", got)
	}
}
