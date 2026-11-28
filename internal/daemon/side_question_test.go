package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

func TestAskSideQuestionNativeWorkingTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY shell script test is POSIX-only")
	}
	origPoll, origSettle := sideQuestionPoll, sideQuestionSettle
	sideQuestionPoll = 20 * time.Millisecond
	sideQuestionSettle = 40 * time.Millisecond
	t.Cleanup(func() {
		sideQuestionPoll = origPoll
		sideQuestionSettle = origSettle
	})

	d, cli := startTestDaemonForGoal(t)
	logFile := filepath.Join(t.TempDir(), "pty-events.log")

	// Simulates Claude Code working on a turn, answering /btw in an overlay,
	// and closing the overlay on Escape without stopping the running turn.
	script := `
stty -echo 2>/dev/null || true
printf '\033]0;⠂ Fix retry\007'
printf '✻ Pondering… (12s · esc to interrupt)\n'
printf '────────────────────────────────────────────────\n'
printf '❯ \n'
printf '────────────────────────────────────────────────\n'
printf '  ? for shortcuts\n'
IFS= read -r line
printf 'CMD:%s\n' "$line" >> "$EVENT_LOG"
printf '\033[2J\033[H'
printf '✻ Pondering… (13s · esc to interrupt)\n'
printf '────────────────────────────────────────────────\n'
printf '/btw which file holds retry logic?\n\n'
printf 'The retry logic is in internal/daemon/codex_appserver.go.\n\n'
printf 'Esc to close\n'
printf '────────────────────────────────────────────────\n'
# Wait for a single ESC byte to dismiss the overlay.
stty -icanon min 1 time 0 2>/dev/null || true
esc=$(dd bs=1 count=1 2>/dev/null | od -An -tx1 | tr -d ' \n')
printf 'KEY:%s\n' "$esc" >> "$EVENT_LOG"
printf '\033[2J\033[H'
printf '✻ Pondering… (14s · esc to interrupt)\n'
printf '────────────────────────────────────────────────\n'
printf '❯ \n'
printf '────────────────────────────────────────────────\n'
sleep 30
`
	s, err := session.Start(session.Spec{
		ID:       "claude-btw-live",
		NodeID:   d.config().NodeID,
		Agent:    "claude",
		Project:  t.TempDir(),
		Command:  []string{"/bin/sh", "-c", script},
		Env:      map[string]string{"EVENT_LOG": logFile},
		Cols:     100,
		Rows:     30,
		OnChange: d.pokeStatus,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d.sessions.add(s)
	s.ReportStatus(protocol.AgentWorking, time.Now().UnixNano())

	// Wait until the simulated working prompt box is rendered on screen.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		screen, _, ok := s.ScreenState()
		if ok && strings.Contains(screen, "❯") && strings.Contains(screen, "Pondering") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	res, err := cli.AskSideQuestion(ctx, &protocol.AskSideQuestion{
		NodeId:    d.config().NodeID,
		SessionId: s.ID(),
		Question:  "which file holds retry logic?",
	})
	if err != nil {
		t.Fatalf("AskSideQuestion failed: %v", err)
	}
	if res.GetMode() != protocol.SideQuestionModeNative {
		t.Fatalf("mode = %q, want %q", res.GetMode(), protocol.SideQuestionModeNative)
	}
	if res.GetAnswer() != "The retry logic is in internal/daemon/codex_appserver.go." {
		t.Fatalf("answer = %q", res.GetAnswer())
	}

	// Verify the PTY received the /btw command and then ESC to dismiss.
	deadline = time.Now().Add(2 * time.Second)
	var events string
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(logFile)
		events = string(raw)
		if strings.Contains(events, "KEY:1b") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(events, "/btw which file holds retry logic?") {
		t.Fatalf("expected /btw command in PTY log, got:\n%s", events)
	}
	if !strings.Contains(events, "KEY:1b") {
		t.Fatalf("expected ESC dismissal in PTY log, got:\n%s", events)
	}
	// Session must still be running and working on its turn.
	if info := s.Info(); info.GetState() != protocol.StateRunning || info.GetStatus() != protocol.AgentWorking {
		t.Fatalf("session state/status changed to %s/%s, want running/working", info.GetState(), info.GetStatus())
	}
}

func TestAskSideQuestionNativeNotNowRefusesWithoutTyping(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY shell script test is POSIX-only")
	}

	d, cli := startTestDaemonForGoal(t)

	cases := []struct {
		name     string
		screen   string
		setup    func(s *session.Session)
		wantPart string
	}{
		{
			name:   "pending approval",
			screen: "✻ Pondering… (5s · esc to interrupt)\n────────────────────\n❯ \n────────────────────\n",
			setup: func(s *session.Session) {
				s.HoldApproval(&protocol.Approval{Id: "ap-1", Tool: "Bash", Detail: "rm -rf /tmp/x"})
			},
			wantPart: "approval",
		},
		{
			name: "permission dialog on screen",
			screen: "⏺ Bash(go test ./...)\n" +
				"────────────────────────────────────────────────\n" +
				" Bash command\n\n   go test ./...\n\n" +
				" Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n" +
				" Esc to cancel · Tab to amend\n",
			wantPart: "approval, dialog, or form",
		},
		{
			name:   "compacting status",
			screen: "✻ Pondering…\n────────────────────\n❯ \n────────────────────\n",
			setup: func(s *session.Session) {
				s.ReportStatus(protocol.AgentCompacting, time.Now().UnixNano())
			},
			wantPart: "compacting",
		},
		{
			name: "btw overlay already open",
			screen: "/btw existing question?\n" +
				"Existing answer.\n" +
				"Esc to close\n",
			wantPart: "already open",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typedFile := filepath.Join(t.TempDir(), "typed.log")
			script := `
stty -echo 2>/dev/null || true
printf '%s' "$SCREEN_CONTENT"
cat >> "$TYPED_LOG"
`
			s, err := session.Start(session.Spec{
				ID:      "claude-notnow-" + string(rune('a'+i)),
				NodeID:  d.config().NodeID,
				Agent:   "claude",
				Project: t.TempDir(),
				Command: []string{"/bin/sh", "-c", script},
				Env: map[string]string{
					"SCREEN_CONTENT": tc.screen,
					"TYPED_LOG":      typedFile,
				},
				Cols:     100,
				Rows:     30,
				OnChange: d.pokeStatus,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			d.sessions.add(s)

			// Wait for initial screen text to render.
			firstLine := strings.TrimSpace(strings.Split(tc.screen, "\n")[0])
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				screen, _, ok := s.ScreenState()
				if ok && strings.Contains(screen, firstLine) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if tc.setup != nil {
				tc.setup(s)
			}

			_, err = cli.AskSideQuestion(t.Context(), &protocol.AskSideQuestion{
				NodeId:    d.config().NodeID,
				SessionId: s.ID(),
				Question:  "should never be typed",
			})
			if err == nil {
				t.Fatal("expected refusal error, got nil")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantPart) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantPart)
			}
			time.Sleep(60 * time.Millisecond)
			if raw, _ := os.ReadFile(typedFile); len(raw) > 0 {
				t.Fatalf("question was typed into PTY while blocked: %q", string(raw))
			}
		})
	}
}

func TestAskSideQuestionCodexAppServerEphemeralFork(t *testing.T) {
	var (
		mu         sync.Mutex
		forkParams codex.ThreadForkParams
		turnParams codex.TurnStartParams
	)

	var mock *mockCodexWSServer
	mock = startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		switch method {
		case "thread/fork":
			_ = json.Unmarshal(params, &forkParams)
			reply(map[string]any{
				"id": id,
				"result": map[string]any{
					"thread": map[string]any{
						"id":        "thread-side-ephemeral",
						"ephemeral": true,
					},
				},
			})
		case "turn/start":
			mu.Lock()
			_ = json.Unmarshal(params, &turnParams)
			mu.Unlock()
			reply(map[string]any{
				"id": id,
				"result": map[string]any{
					"turn": map[string]any{"id": "side-turn-1", "status": "inProgress"},
				},
			})
			go func() {
				mock.sendNotification(t, "item/agentMessage/delta", map[string]any{
					"threadId": "thread-side-ephemeral",
					"turnId":   "side-turn-1",
					"itemId":   "msg-1",
					"delta":    "Retry logic is in ",
				})
				mock.sendNotification(t, "item/agentMessage/delta", map[string]any{
					"threadId": "thread-side-ephemeral",
					"turnId":   "side-turn-1",
					"itemId":   "msg-1",
					"delta":    "codex_appserver.go.",
				})
				mock.sendNotification(t, "turn/completed", map[string]any{
					"threadId": "thread-side-ephemeral",
					"turn":     map[string]any{"id": "side-turn-1", "status": "completed"},
				})
			}()
		}
	})

	d, cli := startTestDaemonForGoal(t)
	s, err := session.Start(session.Spec{
		ID:       "codex-side-sess",
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
	s.ReportStatus(protocol.AgentWorking, time.Now().UnixNano())

	wsClient, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	b := newCodexBridge(d.ctx, s.ID(), wsClient, nil, nil, nil)
	b.setThread("thread-main", true)
	d.attachCodexBridge(s, b)
	t.Cleanup(b.Close)

	res, err := cli.AskSideQuestion(t.Context(), &protocol.AskSideQuestion{
		NodeId:    d.config().NodeID,
		SessionId: s.ID(),
		Question:  "where is retry logic?",
	})
	if err != nil {
		t.Fatalf("AskSideQuestion failed: %v", err)
	}
	if res.GetMode() != protocol.SideQuestionModeAppServer {
		t.Fatalf("mode = %q, want %q", res.GetMode(), protocol.SideQuestionModeAppServer)
	}
	if res.GetAnswer() != "Retry logic is in codex_appserver.go." {
		t.Fatalf("answer = %q", res.GetAnswer())
	}

	mu.Lock()
	defer mu.Unlock()
	if forkParams.ThreadID != "thread-main" {
		t.Fatalf("forked threadId = %q, want thread-main", forkParams.ThreadID)
	}
	if forkParams.Ephemeral == nil || !*forkParams.Ephemeral {
		t.Fatalf("expected ephemeral=true on thread/fork, got %+v", forkParams)
	}
	if forkParams.ApprovalPolicy != "never" || forkParams.Sandbox != "read-only" {
		t.Fatalf("expected read-only no-approval fork, got policy=%q sandbox=%q", forkParams.ApprovalPolicy, forkParams.Sandbox)
	}
	if turnParams.ThreadID != "thread-side-ephemeral" {
		t.Fatalf("turn/start threadId = %q, want thread-side-ephemeral", turnParams.ThreadID)
	}
	// Main bridge thread and working state must remain untouched.
	if got := b.threadID(); got != "thread-main" {
		t.Fatalf("main bridge thread switched to %q", got)
	}
	if info := s.Info(); info.GetStatus() != protocol.AgentWorking {
		t.Fatalf("main session status = %q, want working", info.GetStatus())
	}
}

func TestAskSideQuestionTranscriptFallbackAndBudget(t *testing.T) {
	d, cli := startTestDaemonForGoal(t)

	var capturedPrompt string
	origRunner := testSideFallbackRunner
	testSideFallbackRunner = func(_ context.Context, agentName, project, prompt string) (string, error) {
		capturedPrompt = prompt
		return "The retry helper is in retry.go.", nil
	}
	t.Cleanup(func() { testSideFallbackRunner = origRunner })

	// OpenCode has no native SideQuestion driver, so it takes the read-only fallback path.
	s, err := session.Start(session.Spec{
		ID:       "opencode-fallback-sess",
		NodeID:   d.config().NodeID,
		Agent:    "opencode",
		Project:  t.TempDir(),
		Command:  testSleepCommand(),
		OnChange: d.pokeStatus,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d.sessions.add(s)

	res, err := cli.AskSideQuestion(t.Context(), &protocol.AskSideQuestion{
		NodeId:    d.config().NodeID,
		SessionId: s.ID(),
		Question:  "which file holds retry logic?",
	})
	if err != nil {
		t.Fatalf("fallback AskSideQuestion failed: %v", err)
	}
	if res.GetMode() != protocol.SideQuestionModeFallback {
		t.Fatalf("mode = %q, want %q", res.GetMode(), protocol.SideQuestionModeFallback)
	}
	if res.GetAnswer() != "The retry helper is in retry.go." {
		t.Fatalf("answer = %q", res.GetAnswer())
	}
	if !strings.Contains(capturedPrompt, "Do NOT invoke tools") || !strings.Contains(capturedPrompt, "which file holds retry logic?") {
		t.Fatalf("unexpected fallback prompt:\n%s", capturedPrompt)
	}

	// Verify budgeted context slicing keeps the most recent messages.
	msgs := []*protocol.ChatMessage{
		{Role: "user", Text: "oldest message that should be dropped"},
		{Role: "assistant", Text: "middle reply"},
		{Role: "tool", Tool: "Read", Detail: "retry.go", Result: "func retry() {}"},
		{Role: "user", Text: "latest question"},
	}
	slice := formatSideContext(msgs, 120)
	if !strings.Contains(slice, "latest question") {
		t.Fatalf("expected latest message in budgeted slice, got %q", slice)
	}
	if strings.Contains(slice, "oldest message") {
		t.Fatalf("expected oldest message trimmed out of tight budget, got %q", slice)
	}
}
