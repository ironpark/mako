package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

func startTestDaemonForGoal(t *testing.T) (*Daemon, *client.Client) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MAKO_HOME", dir)
	cfg := config.Default()
	cfg.P2P.Disabled = true
	d := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.dir = dir
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-d.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not become ready")
	}
	c, err := client.Dial(t.Context(), d.Socket())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return d, c
}

func TestCodexBridgeGoalNotificationsSubagentIsolationAndRPCs(t *testing.T) {
	var mu sync.Mutex
	var lastSetObj string
	var clearCount int
	unsupported := false

	mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		switch method {
		case "thread/goal/set":
			mu.Lock()
			unsup := unsupported
			mu.Unlock()
			if unsup {
				reply(map[string]any{
					"id":    id,
					"error": map[string]any{"code": -32601, "message": "method not found"},
				})
				return
			}
			var p codex.ThreadGoalSetParams
			_ = json.Unmarshal(params, &p)
			obj := ""
			if p.Objective != nil {
				obj = *p.Objective
			}
			mu.Lock()
			lastSetObj = obj
			mu.Unlock()
			reply(map[string]any{
				"id": id,
				"result": map[string]any{
					"goal": map[string]any{
						"threadId":  p.ThreadID,
						"objective": obj,
						"status":    "active",
						"updatedAt": 1781680099,
					},
				},
			})
		case "thread/goal/clear":
			mu.Lock()
			clearCount++
			mu.Unlock()
			reply(map[string]any{
				"id":     id,
				"result": map[string]any{"cleared": true},
			})
		}
	})

	d, cli := startTestDaemonForGoal(t)

	s, err := session.Start(session.Spec{
		ID:       "codex-goal-sess",
		NodeID:   d.config().NodeID,
		Agent:    "codex",
		Project:  t.TempDir(),
		Command:  testSleepCommand(),
		OnChange: d.pokeStatus,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d.sessions.add(s)

	wsClient, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	b := newCodexBridge(d.ctx, s.ID(), wsClient, nil, nil, nil)
	b.setThread("thread-parent", true)
	d.attachCodexBridge(s, b)
	t.Cleanup(b.Close)

	// 1. Parent thread goal notification updates SessionInfo.Goal.
	mock.sendNotification(t, "thread/goal/updated", map[string]any{
		"threadId": "thread-parent",
		"goal": map[string]any{
			"threadId":  "thread-parent",
			"objective": "ship unified goals",
			"status":    "paused",
			"updatedAt": 1781680010,
		},
	})
	waitUntil(t, 2*time.Second, func() bool {
		g := s.Info().GetGoal()
		return g != nil && g.GetStatus() == protocol.GoalActive && g.GetText() == "ship unified goals" && g.GetProviderStatus() == "paused"
	}, "parent goal update not applied")

	// 2. Subagent / other-thread goal notifications must NOT hijack parent goal state.
	mock.sendNotification(t, "thread/goal/updated", map[string]any{
		"threadId": "thread-subagent",
		"goal": map[string]any{
			"threadId":  "thread-subagent",
			"objective": "subagent goal hijack",
			"status":    "active",
			"updatedAt": 1781680020,
		},
	})
	mock.sendNotification(t, "thread/goal/cleared", map[string]any{
		"threadId": "thread-subagent",
	})
	// Wait briefly and ensure another valid notification settles after the ignored ones.
	mock.sendNotification(t, "thread/goal/updated", map[string]any{
		"threadId": "thread-parent",
		"goal": map[string]any{
			"threadId":  "thread-parent",
			"objective": "ship unified goals",
			"status":    "active",
			"updatedAt": 1781680021,
		},
	})
	waitUntil(t, 2*time.Second, func() bool {
		g := s.Info().GetGoal()
		return g != nil && g.GetProviderStatus() == "active" && g.GetText() == "ship unified goals"
	}, "parent goal was clobbered by subagent notification")

	// 3. GoalAction edit via client RPC invokes thread/goal/set and updates SessionInfo.
	info, err := cli.GoalAction(t.Context(), &protocol.GoalAction{
		SessionId: s.ID(),
		Action:    protocol.GoalActionEdit,
		Objective: "ship unified goals v2",
	})
	if err != nil {
		t.Fatalf("GoalAction edit: %v", err)
	}
	if info.GetGoal().GetText() != "ship unified goals v2" || !info.GetGoal().GetCanEdit() || !info.GetGoal().GetCanClear() {
		t.Fatalf("unexpected goal after edit: %v", info.GetGoal())
	}
	mu.Lock()
	if lastSetObj != "ship unified goals v2" {
		t.Fatalf("server saw objective %q", lastSetObj)
	}
	mu.Unlock()

	// 4. GoalAction clear via client RPC invokes thread/goal/clear.
	info, err = cli.GoalAction(t.Context(), &protocol.GoalAction{
		SessionId: s.ID(),
		Action:    protocol.GoalActionClear,
	})
	if err != nil {
		t.Fatalf("GoalAction clear: %v", err)
	}
	if info.GetGoal().GetStatus() != protocol.GoalInactive || info.GetGoal().GetReason() != protocol.GoalReasonCleared {
		t.Fatalf("unexpected goal after clear: %v", info.GetGoal())
	}
	mu.Lock()
	if clearCount != 1 {
		t.Fatalf("clearCount = %d, want 1", clearCount)
	}
	mu.Unlock()

	// 5. Unsupported Codex version (-32601) returns CodeUnsupported and disables CanEdit/CanClear.
	mock.sendNotification(t, "thread/goal/updated", map[string]any{
		"threadId": "thread-parent",
		"goal": map[string]any{
			"threadId":  "thread-parent",
			"objective": "active before unsupported",
			"status":    "active",
			"updatedAt": 1781680100,
		},
	})
	waitUntil(t, 2*time.Second, func() bool {
		return s.Info().GetGoal().GetText() == "active before unsupported"
	}, "goal did not become active")

	mu.Lock()
	unsupported = true
	mu.Unlock()
	_, err = cli.GoalAction(t.Context(), &protocol.GoalAction{
		SessionId: s.ID(),
		Action:    protocol.GoalActionEdit,
		Objective: "fail unsupported",
	})
	if !protocol.HasCode(err, protocol.CodeUnsupported) {
		t.Fatalf("expected CodeUnsupported, got %v", err)
	}
	if g := s.Info().GetGoal(); g.GetCanEdit() || g.GetCanClear() {
		t.Fatalf("expected CanEdit/CanClear disabled after unsupported error, got %v", g)
	}

	// 6. Switching to a new top-level thread clears the previous thread's goal.
	mock.sendNotification(t, "thread/started", map[string]any{
		"thread": map[string]any{"id": "thread-new"},
	})
	waitUntil(t, 2*time.Second, func() bool {
		return s.Info().GetGoal() == nil
	}, "switching thread did not clear old goal")
}

func TestCodexBridgeGoalSurvivesConversationBindingAndThreadSwitch(t *testing.T) {
	mock := startMockCodexWSServer(t, nil)
	d, _ := startTestDaemonForGoal(t)

	project := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	rollout1 := writeFakeCodexRollout(t, codexHome, "thread-first", project)
	rollout2 := writeFakeCodexRollout(t, codexHome, "thread-second", project)

	s, err := session.Start(session.Spec{
		ID:       "codex-bind-goal-sess",
		NodeID:   d.config().NodeID,
		Agent:    "codex",
		Project:  project,
		Command:  testSleepCommand(),
		Env:      map[string]string{"CODEX_HOME": codexHome},
		OnChange: d.pokeStatus,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d.sessions.add(s)
	d.sessionMu.Lock()
	d.saved[s.ID()] = savedSession{Info: s.Info()}
	d.sessionMu.Unlock()

	wsClient, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	b := newCodexBridge(d.ctx, s.ID(), wsClient, nil, nil, nil)
	b.setThread("thread-first", true)
	d.attachCodexBridge(s, b)
	t.Cleanup(b.Close)

	// 1. Goal arrives on a brand-new session before conversationID is bound,
	//    then rollout materializes and binds ("") -> "thread-first": goal must survive.
	mock.sendNotification(t, "thread/goal/updated", map[string]any{
		"threadId": "thread-first",
		"goal": map[string]any{
			"threadId":  "thread-first",
			"objective": "keep goal across initial bind",
			"status":    "active",
			"updatedAt": 1781680200,
		},
	})
	waitUntil(t, 2*time.Second, func() bool {
		return s.Info().GetGoal().GetText() == "keep goal across initial bind"
	}, "initial goal not observed before bind")

	mock.sendNotification(t, "thread/started", map[string]any{
		"thread": map[string]any{"id": "thread-first", "path": rollout1},
	})
	waitUntil(t, 2*time.Second, func() bool {
		return s.Info().GetConversationId() == "thread-first"
	}, "thread-first rollout did not bind")
	if got := s.Info().GetGoal(); got == nil || got.GetText() != "keep goal across initial bind" {
		t.Fatalf("initial bind wiped active goal: %v", got)
	}

	// 2. Switching to thread-second carrying a goal + rollout path binds thread-second
	//    immediately after reporting its goal: goal for thread-second must survive.
	mock.sendNotification(t, "thread/started", map[string]any{
		"thread": map[string]any{
			"id":   "thread-second",
			"path": rollout2,
			"goal": map[string]any{
				"threadId":  "thread-second",
				"objective": "second thread goal",
				"status":    "active",
				"updatedAt": 1781680300,
			},
		},
	})
	waitUntil(t, 2*time.Second, func() bool {
		return s.Info().GetConversationId() == "thread-second"
	}, "thread-second rollout did not bind")
	if got := s.Info().GetGoal(); got == nil || got.GetText() != "second thread goal" || got.GetSourceSessionId() != "thread-second" {
		t.Fatalf("thread switch bind wiped goal for thread-second: %v", got)
	}
}

func TestClaudeGoalTranscriptTrackingAndActionControl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell PTY fixture")
	}
	prevWait, prevPoll := claudeGoalConfirmWait, claudeGoalConfirmPoll
	// The fake agent below confirms as soon as it reads each command, so a
	// long wait costs nothing and keeps a loaded machine from timing out.
	claudeGoalConfirmWait = 10 * time.Second
	claudeGoalConfirmPoll = 20 * time.Millisecond
	t.Cleanup(func() {
		claudeGoalConfirmWait, claudeGoalConfirmPoll = prevWait, prevPoll
	})

	d, cli := startTestDaemonForGoal(t)

	project := t.TempDir()
	claudeDir := filepath.Join(t.TempDir(), "claude-config")
	logDir := filepath.Join(claudeDir, "projects", "test-proj")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	convID := "11111111-2222-3333-4444-555555555555"
	logPath := filepath.Join(logDir, convID+".jsonl")
	if err := os.WriteFile(logPath, []byte(
		`{"type":"user","sessionId":"`+convID+`","cwd":`+jsonQuote(project)+`,"message":{"role":"user","content":"start"}}`+"\n"+
			`{"type":"attachment","isSidechain":false,"uuid":"rev-1","sessionId":"`+convID+`","timestamp":"2026-06-19T14:38:48.095Z","attachment":{"type":"goal_status","met":false,"sentinel":true,"condition":"keep tests green"}}`+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	// PTY process captures whatever slash commands are typed into stdin into cmdLog.
	cmdLog := filepath.Join(t.TempDir(), "stdin.log")
	s, err := session.Start(session.Spec{
		ID:       "claude-goal-sess",
		NodeID:   d.config().NodeID,
		Agent:    "claude",
		Project:  project,
		Command:  []string{"sh", "-c", "stty -echo 2>/dev/null; cat > " + cmdLog},
		Env:      map[string]string{"CLAUDE_CONFIG_DIR": claudeDir},
		OnChange: d.pokeStatus,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.SetConversation(convID, logPath)
	d.sessions.add(s)
	d.trackConversation(s)

	// Refresh activity reads the initial active goal with CanEdit & CanClear.
	d.refreshActivity(s)
	g := s.Info().GetGoal()
	if g == nil || g.GetStatus() != protocol.GoalActive || g.GetText() != "keep tests green" || !g.GetCanEdit() || !g.GetCanClear() {
		t.Fatalf("unexpected initial Claude goal: %v", g)
	}

	// 1. Refuse goal action while Claude is working (not at prompt).
	s.SetStatus(protocol.AgentWorking)
	if _, err := cli.GoalAction(t.Context(), &protocol.GoalAction{
		SessionId: s.ID(),
		Action:    protocol.GoalActionClear,
	}); err == nil || !strings.Contains(err.Error(), "idle at its prompt") {
		t.Fatalf("expected busy/prompt readiness rejection, got %v", err)
	}

	// 2. Reject invalid actions and empty objectives.
	s.SetStatus(protocol.AgentIdle)
	if _, err := cli.GoalAction(t.Context(), &protocol.GoalAction{
		SessionId: s.ID(),
		Action:    "stop",
	}); err == nil {
		t.Fatal("expected unsupported action error")
	}
	if _, err := cli.GoalAction(t.Context(), &protocol.GoalAction{
		SessionId: s.ID(),
		Action:    protocol.GoalActionEdit,
		Objective: "   ",
	}); err == nil {
		t.Fatal("expected empty objective error")
	}

	// confirmWhenRead plays Claude: once cmd has reached the PTY's reader
	// (cat copies each submitted line to cmdLog), it appends the goal_status
	// line that the command makes Claude write. Tying the confirmation to the
	// input, not to a timer, means a GoalAction returns only after its
	// command was read, so no command is still unread when the test closes
	// the session.
	confirmWhenRead := func(cmd, line string) {
		go func() {
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				if raw, _ := os.ReadFile(cmdLog); strings.Contains(string(raw), cmd) {
					break
				}
				select {
				case <-t.Context().Done():
					return
				case <-tick.C:
				}
			}
			f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return
			}
			_, _ = f.WriteString(line + "\n")
			_ = f.Close()
		}()
	}

	// 3. Edit goal at idle prompt submits `/goal ...` and observes transcript confirmation.
	confirmWhenRead("/goal refactor parser cleanly",
		`{"type":"attachment","isSidechain":false,"uuid":"rev-2","sessionId":"`+convID+`","timestamp":"2026-06-19T14:39:00.000Z","attachment":{"type":"goal_status","met":false,"sentinel":true,"condition":"refactor parser cleanly"}}`)
	updated, err := cli.GoalAction(t.Context(), &protocol.GoalAction{
		SessionId: s.ID(),
		Action:    protocol.GoalActionEdit,
		Objective: "refactor parser\ncleanly",
	})
	if err != nil {
		t.Fatalf("Claude GoalAction edit: %v", err)
	}
	if updated.GetGoal().GetText() != "refactor parser cleanly" || updated.GetGoal().GetSourceRevision() != "rev-2" {
		t.Fatalf("unexpected goal after edit: %v", updated.GetGoal())
	}

	// 4. Clear goal at idle prompt submits `/goal clear` and observes cleared sentinel.
	confirmWhenRead("/goal clear",
		`{"type":"attachment","isSidechain":false,"uuid":"rev-3","sessionId":"`+convID+`","timestamp":"2026-06-19T14:40:00.000Z","attachment":{"type":"goal_status","met":true,"sentinel":true,"condition":"refactor parser cleanly"}}`)
	cleared, err := cli.GoalAction(t.Context(), &protocol.GoalAction{
		SessionId: s.ID(),
		Action:    protocol.GoalActionClear,
	})
	if err != nil {
		t.Fatalf("Claude GoalAction clear: %v", err)
	}
	if cleared.GetGoal().GetStatus() != protocol.GoalInactive || cleared.GetGoal().GetReason() != protocol.GoalReasonCleared {
		t.Fatalf("unexpected goal after clear: %v", cleared.GetGoal())
	}

	// 5. Clearing again with no active goal is rejected.
	if _, err := cli.GoalAction(t.Context(), &protocol.GoalAction{
		SessionId: s.ID(),
		Action:    protocol.GoalActionClear,
	}); err == nil || !strings.Contains(err.Error(), "no active goal") {
		t.Fatalf("expected no active goal error, got %v", err)
	}

	// Verify exact slash commands written to Claude's PTY.
	_ = s.Close()
	raw, err := os.ReadFile(cmdLog)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "/goal refactor parser cleanly") || !strings.Contains(got, "/goal clear") {
		t.Fatalf("expected `/goal refactor parser cleanly` and `/goal clear` in PTY input, got %q", got)
	}
}

func TestGoalActionRejectsUnsupportedAgentsAndUnbridgedCodex(t *testing.T) {
	d, cli := startTestDaemonForGoal(t)

	for _, tc := range []struct {
		id    string
		agent string
	}{
		{id: "shell-goal-sess", agent: "shell"},
		{id: "codex-pty-only", agent: "codex"},
	} {
		s, err := session.Start(session.Spec{
			ID:      tc.id,
			NodeID:  d.config().NodeID,
			Agent:   tc.agent,
			Project: t.TempDir(),
			Command: testSleepCommand(),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		d.sessions.add(s)

		_, err = cli.GoalAction(t.Context(), &protocol.GoalAction{
			SessionId: s.ID(),
			Action:    protocol.GoalActionSet,
			Objective: "do something",
		})
		if !protocol.HasCode(err, protocol.CodeUnsupported) {
			t.Fatalf("%s: expected CodeUnsupported, got %v", tc.agent, err)
		}
	}
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
