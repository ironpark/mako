package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"google.golang.org/protobuf/proto"
)

func TestHookReportsFollowTheConversation(t *testing.T) {
	command, root := fakeAgent(t, "claude")
	cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{"claude": {Command: []string{command}, Env: map[string]string{"MAKO_FAKE_AGENT": "claude", "MAKO_FAKE_ROOT": root, "CLAUDE_CONFIG_DIR": root}}}}
	d := New(cfg, nil)
	d.mcp = nil
	if err := d.loadSessions(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer d.sessions.closeAll()
	// A socket's path is short, which a test's temporary directory is not.
	run, err := os.MkdirTemp("", "mako-hooks")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	d.hookBinary = "/opt/mako's/makod"
	d.listenHooks(t.Context(), run)
	socket := filepath.Join(run, "hooks.sock")

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
	if h := settings.Hooks["SessionStart"]; len(h) != 1 || h[0].Hooks[0].Command != agentspec.CommandLine([]string{"/opt/mako's/makod", "hook", socket}) {
		t.Fatalf("hook %+v", settings)
	}
	if settings.StatusLine.Command != agentspec.CommandLine([]string{"/opt/mako's/makod", "statusline", socket, "claude"}) {
		t.Fatalf("status line %+v", settings)
	}

	// The status line reports the window and the account's limits.
	t.Setenv("MAKO_SESSION", s.ID())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	status := `{"context_window":{"context_window_size":1000000},"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1738425600},"seven_day":{"used_percentage":41.2,"resets_at":1738857600}}}`
	runStatusLine([]string{socket, "claude"}, strings.NewReader(status), io.Discard)
	want := &protocol.Usage{Window: 1000000, Limits: []*protocol.RateLimit{{Minutes: 300, Used: 23.5, Resets: 1738425600}, {Minutes: 10080, Used: 41.2, Resets: 1738857600}}}
	for deadline := time.Now().Add(5 * time.Second); !proto.Equal(s.Reported(), want); {
		if time.Now().After(deadline) {
			t.Fatalf("reported %+v", s.Reported())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// One before the first reply has no limits, and keeps the last; a
	// launch before drivers named no agent, which was Claude.
	runStatusLine([]string{socket}, strings.NewReader(`{"context_window":{"context_window_size":1000000}}`), io.Discard)
	time.Sleep(50 * time.Millisecond)
	if !proto.Equal(s.Reported(), want) {
		t.Fatalf("limits lost: %+v", s.Reported())
	}

	// /clear: a new conversation, whose log the agent writes only later.
	cleared := conversationUUID()
	path := filepath.Join(root, "projects", "project", cleared+".jsonl")
	t.Setenv("MAKO_SESSION", s.ID())
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
		return d.reportConversation(s, seq, agent.Event{Kind: agent.Started, SessionID: id, Transcript: path})
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
	return hookedDaemonIn(t, t.TempDir())
}

// hookedDaemonIn is hookedDaemon with its session's project at project.
func hookedDaemonIn(t *testing.T, project string) (*Daemon, *session.Session, string) {
	t.Helper()
	command, root := fakeAgent(t, "claude")
	cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{"claude": {Command: []string{command}, Env: map[string]string{"MAKO_FAKE_AGENT": "claude", "MAKO_FAKE_ROOT": root, "CLAUDE_CONFIG_DIR": root}}}}
	d := New(cfg, nil)
	d.mcp = nil
	if err := d.loadSessions(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)
	run, err := os.MkdirTemp("", "mako-hooks")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	d.hookBinary = "/bin/makod"
	d.listenHooks(t.Context(), run)
	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAKO_SESSION", s.ID())
	return d, s, filepath.Join(run, "hooks.sock")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitForWithin(t, 5*time.Second, what, cond)
}

// waitForWithin polls cond until it holds, failing with what after d.
func waitForWithin(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
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
		{`{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, protocol.AgentIdle},
		{`{"hook_event_name":"Stop"}`, protocol.AgentDone},
	} {
		runHook([]string{socket}, strings.NewReader(step.input), io.Discard)
		waitFor(t, step.input+" not reported", func() bool { return s.Info().GetStatus() == step.want })
	}
}

func TestRunHookPreservesExtensionEventOrder(t *testing.T) {
	dir, err := os.MkdirTemp("", "mako-seq")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "hooks.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("MAKO_SESSION", "s")
	for _, seq := range []string{"123", "122", "bad", "-1", ""} {
		runHook([]string{socket}, strings.NewReader(`{"hook_event_name":"Stop","mako_hook_seq":"`+seq+`"}`), io.Discard)
		conn, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var msg hookMessage
		err = json.NewDecoder(conn).Decode(&msg)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		want, _ := strconv.ParseInt(seq, 10, 64)
		if want > 0 {
			if msg.Seq != want {
				t.Fatalf("producer sequence %s became %d", seq, msg.Seq)
			}
		} else if msg.Seq < time.Now().Add(-time.Minute).UnixNano() {
			t.Fatalf("invalid sequence %q did not fall back to the hook clock", seq)
		}
	}
}

func TestChildHooksDelayCompletionAndIgnoreLateParentEvents(t *testing.T) {
	d, s, socket := hookedDaemon(t)
	base := time.Now().UnixNano()
	send := func(seq int64, input string) {
		t.Helper()
		conn, err := sendHook(socket, hookMessage{Session: s.ID(), Token: d.hookToken(s.ID()), Seq: base + seq, Input: json.RawMessage(input)})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := io.Copy(io.Discard, conn); err != nil {
			t.Fatal(err)
		}
	}
	send(1, `{"hook_event_name":"UserPromptSubmit"}`)
	send(2, `{"hook_event_name":"SubagentStart","agent_id":"a"}`)
	send(3, `{"hook_event_name":"SubagentStart","agent_id":"b"}`)
	send(4, `{"hook_event_name":"Stop"}`)
	if s.Info().GetStatus() != protocol.AgentWorking {
		t.Fatal("parent completion ignored running children")
	}
	send(5, `{"hook_event_name":"SubagentStop","agent_id":"a"}`)
	if s.Info().GetStatus() != protocol.AgentWorking {
		t.Fatal("one child ended the whole session")
	}
	send(6, `{"hook_event_name":"SubagentStop","agent_id":"b"}`)
	if s.Info().GetStatus() != protocol.AgentDone {
		t.Fatal("final child did not release completion")
	}
	send(3, `{"hook_event_name":"PostToolUse"}`)
	if s.Info().GetStatus() != protocol.AgentDone {
		t.Fatal("late parent event revived completion")
	}
}

// A prompt clears the agent's suggestion, but a late hook of an earlier
// prompt does not, and a failure to sign in does.
func TestHooksClearTheSuggestionOfTheirTurn(t *testing.T) {
	d, s, socket := hookedDaemon(t)
	base := time.Now().UnixNano()
	send := func(seq int64, input string) {
		t.Helper()
		conn, err := sendHook(socket, hookMessage{Session: s.ID(), Token: d.hookToken(s.ID()), Seq: base + seq, Input: json.RawMessage(input)})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := io.Copy(io.Discard, conn); err != nil {
			t.Fatal(err)
		}
	}
	key := liveKey(s.ID(), "")
	suggested := func() string { return suggestionBlock(d.live.blocks(key)) }

	send(10, `{"hook_event_name":"UserPromptSubmit"}`)
	send(11, `{"hook_event_name":"Stop"}`)
	d.live.setSuggestion(s.ID(), "Run the tests")
	send(5, `{"hook_event_name":"UserPromptSubmit"}`)
	if got := suggested(); got != "Run the tests" {
		t.Fatalf("a late prompt hook of an earlier turn left suggestion %q", got)
	}
	send(12, `{"hook_event_name":"UserPromptSubmit"}`)
	if got := suggested(); got != "" {
		t.Fatalf("a prompt kept suggestion %q", got)
	}

	send(13, `{"hook_event_name":"Stop"}`)
	d.live.setSuggestion(s.ID(), "Commit the changes")
	send(14, `{"hook_event_name":"StopFailure","error":"authentication_failed","error_details":"Failed to authenticate: OAuth session expired and could not be refreshed."}`)
	if got := suggested(); got != "" {
		t.Fatalf("a failure to sign in kept suggestion %q", got)
	}
}

func TestCompletionFollowsBackgroundLogsWithoutAChatReader(t *testing.T) {
	d, s, socket := hookedDaemon(t)
	waitConversation(t, s)
	path := s.TranscriptPath()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"assistant","uuid":"background-call","message":{"role":"assistant","content":[{"type":"tool_use","id":"bg","name":"Agent","input":{"prompt":"Review it."}}]}}` + "\n" +
		`{"type":"user","uuid":"background-result","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"bg","content":"launched"}]}}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(strings.TrimSuffix(path, filepath.Ext(path)), "subagents")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	meta := filepath.Join(dir, "agent-b.meta.json")
	if err := os.WriteFile(meta, []byte(`{"toolUseId":"bg","requestShape":"background"}`), 0600); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"role":"user","content":"Review it."}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "agent-b.jsonl"), []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	runHook([]string{socket}, strings.NewReader(`{"hook_event_name":"Stop"}`), io.Discard)
	waitFor(t, "parent completion was not read", func() bool { return s.Info().GetStatus() == protocol.AgentWorking })
	if err := os.WriteFile(meta, []byte(`{"toolUseId":"bg","requestShape":"background","finished":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	d.refreshActivity(s)
	if s.Info().GetStatus() != protocol.AgentDone {
		t.Fatal("background completion was not followed")
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

	d.approvals.watch(true)
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

// A question is the agent's to ask, watched or not: no approval is shown
// for it, and the hook leaves it to the agent's prompt.
func TestQuestionsAreNotApprovals(t *testing.T) {
	d, s, socket := hookedDaemon(t)
	d.approvals.watch(true)
	var out bytes.Buffer
	runHook([]string{socket}, strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[]}}`), &out)
	if out.Len() != 0 || s.Info().GetApproval() != nil {
		t.Fatalf("decided a question: %q, approval %+v", out.String(), s.Info().GetApproval())
	}
	if s.Info().GetStatus() != protocol.AgentWaiting {
		t.Fatalf("status %q", s.Info().GetStatus())
	}
}

// A question an agent's hook can answer waits for the window, as an
// approval does, and is answered with the user's answers; allowed with
// none, it goes to the agent's prompt.
func TestQuestionsAreAnsweredInTheWindow(t *testing.T) {
	d, s, socket := hookedDaemon(t)
	d.approvals.watch(true)
	ask := func() <-chan string {
		out := make(chan string, 1)
		go func() {
			var buf bytes.Buffer
			runHook([]string{socket}, strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[`+
				`{"question":"Which fruits?","header":"Fruit","multiSelect":true,"options":[{"label":"Apple","description":"red"},{"label":"Pear","description":"green"}]},`+
				`{"question":"Which color?","header":"Color","multiSelect":false,"options":[{"label":"Red","description":""},{"label":"Blue","description":""}]}]}}`), &buf)
			out <- buf.String()
		}()
		return out
	}
	for _, c := range []struct {
		name    string
		answers []*protocol.QuestionAnswer
		want    string
	}{
		{"answered", []*protocol.QuestionAnswer{{Picked: []int32{0, 1}}, {Text: "Purple"}}, `"answers":{"Which color?":"Purple","Which fruits?":"Apple, Pear"}`},
		{"unanswered", nil, ""},
	} {
		out := ask()
		waitFor(t, "no question shown", func() bool { return s.Info().GetApproval() != nil })
		a := s.Info().GetApproval()
		if len(a.GetQuestions()) != 2 || !a.GetQuestions()[0].GetMultiSelect() || a.GetQuestions()[1].GetOptions()[1].GetLabel() != "Blue" {
			t.Fatalf("%s: questions %+v", c.name, a.GetQuestions())
		}
		// Claude, its own dialog shown beside the hook's, says it waits.
		runHook([]string{socket}, strings.NewReader(`{"hook_event_name":"Notification","notification_type":"permission_prompt"}`), io.Discard)
		time.Sleep(100 * time.Millisecond)
		if s.Info().GetApproval().GetId() != a.GetId() {
			t.Fatalf("%s: the agent saying it waits withdrew its question", c.name)
		}
		if err := d.approvals.answer(s.ID(), &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: protocol.DecisionAllow, Answers: c.answers}); err != nil {
			t.Fatal(err)
		}
		got := <-out
		if c.want == "" && got != "" || !strings.Contains(got, c.want) || c.want != "" && !strings.Contains(got, `"behavior":"allow"`) {
			t.Fatalf("%s: hook printed %q", c.name, got)
		}
		if c.want != "" && !strings.Contains(got, `"questions":[`) {
			t.Fatalf("%s: the questions were not kept: %q", c.name, got)
		}
		waitFor(t, "question kept", func() bool { return s.Info().GetApproval() == nil })
	}
}

// A plan, and a call offered rules or a mode to allow it from then on, is
// allowed in the window with the option the user chose; a plan with its
// input given back, which Claude takes it only with.
func TestApprovalsAreAllowedWithAnOption(t *testing.T) {
	d, s, socket := hookedDaemon(t)
	d.approvals.watch(true)
	for _, c := range []struct {
		name, input, option string
		kind                string
		options             int
		want                []string
	}{
		{"plan", `{"hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan\n1. Do it","planFilePath":"/p.md"}}`,
			"acceptEdits", protocol.ToolExitPlan, 3,
			[]string{`"updatedInput":{"plan":"# Plan\n1. Do it","planFilePath":"/p.md"}`, `"mode":"acceptEdits"`}},
		{"plan once", `{"hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan"}}`,
			"", protocol.ToolExitPlan, 3, []string{`"updatedInput":{"plan":"# Plan"}`, `"mode":"default"`}},
		{"rules", `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"wc -c a"},` +
			`"permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"wc *"}],"behavior":"allow","destination":"localSettings"}]}`,
			"0", protocol.ToolShell, 1,
			[]string{`"updatedPermissions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"wc *"}],"behavior":"allow","destination":"localSettings"}]`}},
	} {
		out := make(chan string, 1)
		go func() {
			var buf bytes.Buffer
			runHook([]string{socket}, strings.NewReader(c.input), &buf)
			out <- buf.String()
		}()
		waitFor(t, "no approval shown", func() bool { return s.Info().GetApproval() != nil })
		a := s.Info().GetApproval()
		if a.GetKind() != c.kind || len(a.GetOptions()) != c.options {
			t.Fatalf("%s: kind %q, options %+v", c.name, a.GetKind(), a.GetOptions())
		}
		if err := d.approvals.answer(s.ID(), &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: protocol.DecisionAllow, Option: c.option}); err != nil {
			t.Fatal(err)
		}
		got := <-out
		for _, w := range append(c.want, `"behavior":"allow"`) {
			if !strings.Contains(got, w) {
				t.Fatalf("%s: hook printed %q, want %s", c.name, got, w)
			}
		}
		waitFor(t, "approval kept", func() bool { return s.Info().GetApproval() == nil })
	}
}

// An agent that gives up on its hook -- answered in its terminal,
// interrupted, timed out -- takes its approval out of the window.
func TestAnApprovalTheAgentAbandonsIsWithdrawn(t *testing.T) {
	d, s, socket := hookedDaemon(t)
	d.approvals.watch(true)
	conn, err := sendHook(socket, hookMessage{Session: s.ID(), Token: d.hookToken(s.ID()), Seq: 1,
		Input: json.RawMessage(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "no approval shown", func() bool { return s.Info().GetApproval() != nil })
	conn.Close()
	waitFor(t, "abandoned approval kept", func() bool { return s.Info().GetApproval() == nil })
	if s.Info().GetStatus() != protocol.AgentIdle {
		t.Fatal("abandoned approval still waits for the user")
	}
}

// mako's status line takes the place of the user's, so it prints theirs:
// the project's local settings first, then its shared ones, then the
// user's.
func TestStatusLinePrintsTheUsersOwn(t *testing.T) {
	config, project := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("MAKO_SESSION", "")
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

// An agent that names its event on the hook's command line has it named
// in the input passed on, as the others name it themselves.
func TestNamedEvent(t *testing.T) {
	got, err := namedEvent([]byte(`{"conversationId":"c1"}`), "Stop")
	if err != nil || string(got) != `{"conversationId":"c1","hook_event_name":"Stop"}` {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := namedEvent([]byte(`[1]`), "Stop"); err == nil {
		t.Fatal("named an event in what is no object")
	}
}

// An agy swarm worker / subagent inherits MAKO_SESSION and fires the same
// lifecycle hooks under its own conversationId. Those hooks must never hijack
// the parent session's ConversationId, TranscriptPath, or AgentStatus, while a
// genuine new user conversation (/new) still rebinds cleanly.
func TestAgySubagentHooksDoNotHijackParentConversation(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	agyRoot := filepath.Join(home, ".gemini", "antigravity-cli")

	parentID := "0492e25a-38a3-40ec-8f63-03cd7b76b08a"
	monitorID := "4aa27cb9-78c6-475e-be5a-8479b7a708c3"
	newConvID := "12345678-1234-4123-8123-123456789abc"

	mkLog := func(id, content string) string {
		p := filepath.Join(agyRoot, "brain", id, ".system_generated", "logs", "transcript_full.jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	parentLog := mkLog(parentID, `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nStart swarm\n</USER_REQUEST>"}`)
	monitorLog := mkLog(monitorID, `{"step_index":0,"source":"SYSTEM","type":"SYSTEM_MESSAGE","content":"[Message] sender=`+parentID+`"}`)
	newLog := mkLog(newConvID, `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nFresh chat\n</USER_REQUEST>"}`)

	subDir := filepath.Join(agyRoot, "brain", parentID, ".system_generated", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, monitorID+".json"), []byte(`{"conversationId":"`+monitorID+`","state":"SUBAGENT_STATE_ALIVE"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	d := meshDaemon(t, "agy-subagent-hook")
	s, err := session.Start(session.Spec{
		ID: "agy-coord", NodeID: d.config().NodeID, Agent: "agy", Project: project,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     map[string]string{"HOME": home},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d.sessions.add(s)
	d.sessionMu.Lock()
	if d.saved == nil {
		d.saved = map[string]savedSession{}
	}
	d.saved[s.ID()] = savedSession{Info: s.Info(), Request: &protocol.OpenSession{Agent: "agy", Project: project}}
	d.sessionMu.Unlock()
	d.trackConversation(s)

	send := func(seq int64, payload string) {
		t.Helper()
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.serveHook(t.Context(), server)
		}()
		if err := json.NewEncoder(client).Encode(hookMessage{Session: s.ID(), Token: d.hookToken(s.ID()), Seq: seq, Input: json.RawMessage(payload)}); err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, client)
		_ = client.Close()
		<-done
	}

	// 1. Coordinator binds parent conversation and finishes its own turn while
	// monitor is still SUBAGENT_STATE_ALIVE: parent stays bound to parentID and
	// remains AgentWorking while its child is alive.
	send(1, `{"hook_event_name":"SessionStart","conversationId":"`+parentID+`","transcriptPath":"`+parentLog+`"}`)
	send(2, `{"hook_event_name":"Stop","conversationId":"`+parentID+`","transcriptPath":"`+parentLog+`"}`)
	if gotID, gotLog := s.Info().GetConversationId(), s.TranscriptPath(); gotID != parentID || gotLog != parentLog {
		t.Fatalf("parent bind = (%q, %q), want (%q, %q)", gotID, gotLog, parentID, parentLog)
	}
	if s.Info().GetStatus() != protocol.AgentWorking {
		t.Fatalf("parent status with alive subagent = %s, want working", s.Info().GetStatus())
	}

	// 2. Monitor worker fires SessionStart, PreInvocation, Stop — parent must
	// never be hijacked to monitor's conversation or transcript.
	send(3, `{"hook_event_name":"SessionStart","conversationId":"`+monitorID+`","transcriptPath":"`+monitorLog+`"}`)
	send(4, `{"hook_event_name":"PreInvocation","conversationId":"`+monitorID+`","transcriptPath":"`+monitorLog+`"}`)
	send(5, `{"hook_event_name":"Stop","conversationId":"`+monitorID+`","transcriptPath":"`+monitorLog+`"}`)
	if gotID, gotLog := s.Info().GetConversationId(), s.TranscriptPath(); gotID != parentID || gotLog != parentLog {
		t.Fatalf("subagent hijacked parent: got (%q, %q), want (%q, %q)", gotID, gotLog, parentID, parentLog)
	}

	// 3. Once monitor is killed and parent stops, parent becomes AgentDone, and
	// a late PreInvocation from monitor still cannot hijack or overwrite status.
	if err := os.WriteFile(filepath.Join(subDir, monitorID+".json"), []byte(`{"conversationId":"`+monitorID+`","state":"SUBAGENT_STATE_KILLED"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	send(6, `{"hook_event_name":"Stop","conversationId":"`+parentID+`","transcriptPath":"`+parentLog+`"}`)
	if s.Info().GetStatus() != protocol.AgentDone {
		t.Fatalf("parent status after subagent killed = %s, want done", s.Info().GetStatus())
	}
	send(7, `{"hook_event_name":"PreInvocation","conversationId":"`+monitorID+`","transcriptPath":"`+monitorLog+`"}`)
	if gotID, gotLog := s.Info().GetConversationId(), s.TranscriptPath(); gotID != parentID || gotLog != parentLog {
		t.Fatalf("late subagent hook hijacked parent: got (%q, %q), want (%q, %q)", gotID, gotLog, parentID, parentLog)
	}
	if s.Info().GetStatus() != protocol.AgentDone {
		t.Fatalf("late subagent hook overwrote parent status: got %s, want done", s.Info().GetStatus())
	}

	// 4. Genuine /new conversation (not a subagent) still rebinds cleanly.
	send(8, `{"hook_event_name":"SessionStart","conversationId":"`+newConvID+`","transcriptPath":"`+newLog+`"}`)
	if gotID, gotLog := s.Info().GetConversationId(), s.TranscriptPath(); gotID != newConvID || gotLog != newLog {
		t.Fatalf("genuine /new did not rebind: got (%q, %q), want (%q, %q)", gotID, gotLog, newConvID, newLog)
	}
}

func TestServeHookDoesNotStallWhenSessionMuHeld(t *testing.T) {
	d := New(&config.Config{NodeID: "node"}, nil)
	s, err := session.Start(session.Spec{
		ID: "hook-fast", NodeID: "node", Agent: "claude", Project: t.TempDir(),
		Command: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d.sessions.add(s)

	d.sessionMu.Lock()
	locked := true
	defer func() {
		if locked {
			d.sessionMu.Unlock()
		}
	}()

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.serveHook(t.Context(), server)
	}()
	if err := json.NewEncoder(client).Encode(hookMessage{
		Session: s.ID(),
		Token:   d.hookToken(s.ID()),
		Seq:     1,
		Input:   json.RawMessage(`{"hook_event_name":"UserPromptSubmit"}`),
	}); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("serveHook stalled while d.sessionMu was held")
	}
	locked = false
	d.sessionMu.Unlock()

	if got := s.Info().GetStatus(); got != protocol.AgentWorking {
		t.Fatalf("session status = %s, want %s", got, protocol.AgentWorking)
	}
}

func TestAgyStatusLineYoloPermissionModeAndLiveUpdates(t *testing.T) {
	d := New(&config.Config{NodeID: "node"}, nil)

	sendStatus := func(sessID string, seq int64, payload string) {
		t.Helper()
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.serveHook(t.Context(), server)
		}()
		if err := json.NewEncoder(client).Encode(hookMessage{
			Session: sessID, Token: d.hookToken(sessID), Seq: seq, Kind: statusArg, Input: json.RawMessage(payload),
		}); err != nil {
			t.Fatal(err)
		}
		_ = client.Close()
		<-done
	}

	// 1. agy started with --dangerously-skip-permissions surfaces bypassPermissions
	// for auto / accept-edits / omitted startup mode, while preserving plan mode.
	yoloSess, err := session.Start(session.Spec{
		ID: "agy-yolo", NodeID: "node", Agent: "agy", Project: t.TempDir(),
		Command: []string{"/bin/sh", "-c", "sleep 30", "agy", "--dangerously-skip-permissions"},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = yoloSess.Close() })
	d.sessions.add(yoloSess)

	sendStatus(yoloSess.ID(), 1, `{"model":{"id":"Gemini 4 Neon","effort":"high"},"cycle_mode":"auto"}`)
	if got := yoloSess.Reported().GetPermissionMode(); got != "bypassPermissions" {
		t.Fatalf("agy YOLO auto mode = %q, want bypassPermissions", got)
	}
	// Switching to plan mode updates live reported mode to "plan".
	sendStatus(yoloSess.ID(), 2, `{"model":{"id":"Gemini 4 Hydrogen","effort":"medium"},"cycle_mode":"plan"}`)
	if r := yoloSess.Reported(); r.GetPermissionMode() != "plan" || r.GetModel() != "Gemini 4 Hydrogen" || r.GetEffort() != "medium" {
		t.Fatalf("agy YOLO plan update = %+v, want plan / Gemini 4 Hydrogen / medium", r)
	}
	// An idle heartbeat omitting cycle_mode must not clobber plan mode.
	sendStatus(yoloSess.ID(), 3, `{"model":{"id":"Gemini 4 Hydrogen"}}`)
	if got := yoloSess.Reported().GetPermissionMode(); got != "plan" {
		t.Fatalf("agy YOLO omitted cycle_mode in plan = %q, want plan", got)
	}
	// Switching back to accept-edits still runs under --dangerously-skip-permissions.
	sendStatus(yoloSess.ID(), 4, `{"cycle_mode":"accept-edits"}`)
	if got := yoloSess.Reported().GetPermissionMode(); got != "bypassPermissions" {
		t.Fatalf("agy YOLO accept-edits mode = %q, want bypassPermissions", got)
	}

	// 2. Non-YOLO agy session reflects cycle_mode transitions faithfully.
	normalSess, err := session.Start(session.Spec{
		ID: "agy-normal", NodeID: "node", Agent: "agy", Project: t.TempDir(),
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = normalSess.Close() })
	d.sessions.add(normalSess)

	sendStatus(normalSess.ID(), 1, `{"cycle_mode":"auto"}`)
	if got := normalSess.Reported().GetPermissionMode(); got != "auto" {
		t.Fatalf("non-YOLO agy auto = %q, want auto", got)
	}
	sendStatus(normalSess.ID(), 2, `{"cycle_mode":"accept-edits"}`)
	if got := normalSess.Reported().GetPermissionMode(); got != "acceptEdits" {
		t.Fatalf("non-YOLO agy accept-edits = %q, want acceptEdits", got)
	}
	sendStatus(normalSess.ID(), 3, `{"cycle_mode":"plan"}`)
	if got := normalSess.Reported().GetPermissionMode(); got != "plan" {
		t.Fatalf("non-YOLO agy plan = %q, want plan", got)
	}
}
