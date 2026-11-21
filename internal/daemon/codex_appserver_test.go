package daemon

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

func testSleepCommand() []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell.exe", "-NoLogo", "-NoProfile", "-Command", "Start-Sleep -Seconds 30"}
	}
	return []string{"sh", "-c", "sleep 30"}
}

type mockCodexWSServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	writeMu sync.Mutex
	conn    *websocket.Conn
	handler func(id json.RawMessage, method string, params json.RawMessage, reply func(any))
}

func startMockCodexWSServer(t *testing.T, handler func(id json.RawMessage, method string, params json.RawMessage, reply func(any))) *mockCodexWSServer {
	t.Helper()
	m := &mockCodexWSServer{handler: handler}
	upgrader := websocket.Upgrader{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		m.mu.Lock()
		m.conn = conn
		m.mu.Unlock()
		defer conn.Close()
		reply := func(v any) {
			m.writeMu.Lock()
			defer m.writeMu.Unlock()
			_ = conn.WriteJSON(v)
		}
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(data, &req) != nil {
				continue
			}
			if m.handler != nil {
				m.handler(req.ID, req.Method, req.Params, reply)
			}
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockCodexWSServer) wsURL() string {
	return "ws" + strings.TrimPrefix(m.srv.URL, "http")
}

func (m *mockCodexWSServer) sendNotificationAt(t *testing.T, method string, params any, emittedAtMs int64) {
	t.Helper()
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		t.Fatal("mock server has no active connection")
	}
	msg := map[string]any{"method": method, "params": params}
	if emittedAtMs > 0 {
		msg["emittedAtMs"] = emittedAtMs
	}
	m.writeMu.Lock()
	err := conn.WriteJSON(msg)
	m.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func (m *mockCodexWSServer) sendNotification(t *testing.T, method string, params any) {
	t.Helper()
	m.sendNotificationAt(t, method, params, 0)
}

func writeFakeCodexRollout(t *testing.T, codexHome, threadID, cwd string) string {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "11", "21")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-21T00-00-00-"+threadID+".jsonl")
	line, _ := json.Marshal(map[string]any{
		"type":      "session_meta",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"payload": map[string]any{
			"id":        threadID,
			"cwd":       cwd,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		},
	})
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexBridgeNativeDeliveryAndFallback(t *testing.T) {
	var mu sync.Mutex
	var queued []string
	var started []string
	rejectQueue := false

	mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		switch method {
		case "thread/queue/add":
			var p codex.ThreadQueueAddParams
			_ = json.Unmarshal(params, &p)
			mu.Lock()
			reject := rejectQueue
			if !reject && len(p.Input) > 0 {
				queued = append(queued, p.Input[0].Text)
			}
			mu.Unlock()
			if reject {
				reply(map[string]any{
					"id":    id,
					"error": map[string]any{"code": -32600, "message": "ephemeral thread does not support queued submissions"},
				})
				return
			}
			reply(map[string]any{
				"id":     id,
				"result": map[string]any{"queuedSubmission": map[string]any{"id": "q-1", "clientUserMessageId": p.ClientUserMessageID}},
			})
		case "turn/start":
			var p codex.TurnStartParams
			_ = json.Unmarshal(params, &p)
			mu.Lock()
			if len(p.Input) > 0 {
				started = append(started, p.Input[0].Text)
			}
			mu.Unlock()
			reply(map[string]any{
				"id":     id,
				"result": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress"}},
			})
		}
	})

	client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}

	s, err := session.Start(session.Spec{
		ID: "codex-s1", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
		Command: testSleepCommand(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetStatus(protocol.AgentWorking) // even mid-turn, native delivery pushes immediately

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)

	b := newCodexBridge(t.Context(), s.ID(), client, nil, nil, nil)
	b.setThread("th-live-1", true)
	d.attachCodexBridge(s, b)
	defer b.Close()

	_, release := d.projectAgents.server(t.Context(), "", s.ID(), "")
	defer release()
	peer := d.projectAgents.activate(s)
	go d.deliverAgentMessages(peer)

	// 1. Primary path: thread/queue/add delivers mid-turn without waiting for PTY prompt.
	if _, err := d.projectAgents.messages.send("boss", s.ID(), "ping over app-server", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "queued message not delivered", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(queued) == 1 && strings.Contains(queued[0], "ping over app-server")
	})

	// 2. Fallback path: -32600 from thread/queue/add falls back to turn/start.
	mu.Lock()
	rejectQueue = true
	mu.Unlock()
	if _, err := d.projectAgents.messages.send("boss", s.ID(), "fallback to turn/start", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "turn/start fallback not invoked", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(started) == 1 && strings.Contains(started[0], "fallback to turn/start")
	})
}

func TestCodexBridgeLiveStreamingQuotaStatusAndCleanup(t *testing.T) {
	var mu sync.Mutex
	var resumedIDs []string
	codexHome := t.TempDir()
	projDir := t.TempDir()
	th2Path := writeFakeCodexRollout(t, codexHome, "th-2", projDir)

	mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		switch method {
		case "thread/read":
			var p codex.ThreadReadParams
			_ = json.Unmarshal(params, &p)
			path := ""
			if p.ThreadID == "th-2" {
				path = th2Path
			}
			reply(map[string]any{
				"id":     id,
				"result": map[string]any{"thread": map[string]any{"id": p.ThreadID, "path": path}},
			})
		case "thread/resume":
			var p codex.ThreadResumeParams
			_ = json.Unmarshal(params, &p)
			mu.Lock()
			resumedIDs = append(resumedIDs, p.ThreadID)
			mu.Unlock()
			path := ""
			if p.ThreadID == "th-2" {
				path = th2Path
			}
			reply(map[string]any{
				"id":     id,
				"result": map[string]any{"thread": map[string]any{"id": p.ThreadID, "path": path}},
			})
		}
	})

	client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}

	s, err := session.Start(session.Spec{
		ID: "codex-s2", NodeID: "test-node", Agent: "codex", Project: projDir,
		Command: testSleepCommand(), Env: map[string]string{"CODEX_HOME": codexHome}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)
	d.saved = map[string]savedSession{
		s.ID(): {Info: s.Info(), Request: &protocol.OpenSession{Agent: "codex"}},
	}

	tmpSock := filepath.Join(t.TempDir(), "fake.sock")
	if err := os.WriteFile(tmpSock, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newCodexBridge(t.Context(), s.ID(), client, nil, nil, func() { _ = os.Remove(tmpSock) })
	b.setThread("th-1", true)
	b.initUsage = &protocol.Usage{Model: "gpt-6.2-sol", Effort: "low"}
	d.attachCodexBridge(s, b)

	// Initial usage is reported immediately on attach.
	if rep := s.Reported(); rep.GetModel() != "gpt-6.2-sol" || rep.GetEffort() != "low" {
		t.Fatalf("initial Reported = %+v", rep)
	}

	// 1. Turn start + reasoning + tool + agentMessage deltas -> liveHub blocks + AgentWorking.
	mock.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-1",
		"turn":     map[string]any{"id": "turn-1", "status": "inProgress"},
	})
	mock.sendNotification(t, "item/started", map[string]any{
		"threadId": "th-1", "turnId": "turn-1",
		"item": map[string]any{"type": "reasoning", "id": "r-1"},
	})
	mock.sendNotification(t, "item/started", map[string]any{
		"threadId": "th-1", "turnId": "turn-1",
		"item": map[string]any{"type": "commandExecution", "id": "cmd-1", "command": "go test ./..."},
	})
	mock.sendNotification(t, "item/agentMessage/delta", map[string]any{
		"threadId": "th-1", "turnId": "turn-1", "itemId": "msg-1", "delta": "All ",
	})
	mock.sendNotification(t, "item/agentMessage/delta", map[string]any{
		"threadId": "th-1", "turnId": "turn-1", "itemId": "msg-1", "delta": "green!",
	})

	key := liveKey(s.ID(), "")
	waitFor(t, "live blocks not populated", func() bool {
		blocks := d.live.blocks(key)
		return len(blocks) == 3 &&
			blocks[0].GetKind() == "thinking" &&
			blocks[1].GetKind() == "tool" && blocks[1].GetTool() == "Bash" && blocks[1].GetText() == "go test ./..." &&
			blocks[2].GetKind() == "text" && blocks[2].GetText() == "All green!"
	})
	if got := s.Info().GetStatus(); got != protocol.AgentWorking {
		t.Fatalf("status = %q, want working", got)
	}

	// 2. Waiting on approval via thread/status/changed activeFlags.
	mock.sendNotification(t, "thread/status/changed", map[string]any{
		"threadId": "th-1",
		"status":   map[string]any{"type": "active", "activeFlags": []string{"waitingOnApproval"}},
	})
	waitFor(t, "waiting status not reported", func() bool {
		return s.Info().GetStatus() == protocol.AgentWaiting
	})

	// 3. Token usage + rate limits updates -> s.Reported().
	mock.sendNotification(t, "thread/tokenUsage/updated", map[string]any{
		"threadId": "th-1", "turnId": "turn-1",
		"tokenUsage": map[string]any{
			"total":              map[string]any{"inputTokens": 1200, "cachedInputTokens": 200, "outputTokens": 300, "totalTokens": 1500},
			"last":               map[string]any{"inputTokens": 900},
			"modelContextWindow": 200000,
		},
	})
	mock.sendNotification(t, "account/rateLimits/updated", map[string]any{
		"rateLimits": map[string]any{
			"limitId":   "codex",
			"primary":   map[string]any{"usedPercent": 15.0, "windowDurationMins": 300, "resetsAt": 1700001000},
			"secondary": map[string]any{"usedPercent": 42.5, "windowDurationMins": 10080, "resetsAt": 1700500000},
		},
	})
	waitFor(t, "usage and rate limits not reported", func() bool {
		rep := s.Reported()
		return rep.GetWindow() == 200000 && rep.GetInput() == 1000 && rep.GetOutput() == 300 &&
			len(rep.GetLimits()) == 2 &&
			rep.GetLimits()[0].GetMinutes() == 300 && rep.GetLimits()[0].GetUsed() == 15.0 &&
			rep.GetLimits()[1].GetMinutes() == 10080 && rep.GetLimits()[1].GetUsed() == 42.5
	})

	// 4. Turn completion -> AgentDone.
	mock.sendNotification(t, "turn/completed", map[string]any{
		"threadId": "th-1",
		"turn":     map[string]any{"id": "turn-1", "status": "completed"},
	})
	waitFor(t, "done status not reported", func() bool {
		return s.Info().GetStatus() == protocol.AgentDone
	})

	// 5. In-session thread switch (/new or /resume) -> updates active thread & subscribes.
	mock.sendNotification(t, "thread/status/changed", map[string]any{
		"threadId": "th-2",
		"status":   map[string]any{"type": "active", "activeFlags": []string{}},
	})
	waitFor(t, "switched thread not subscribed", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return b.threadID() == "th-2" && len(resumedIDs) == 1 && resumedIDs[0] == "th-2"
	})
	if got := s.Info().GetConversationId(); got != "th-2" {
		t.Fatalf("ConversationId = %q, want th-2", got)
	}

	// 6. Close cleans up bridge registration and socket file.
	b.Close()
	waitFor(t, "bridge not removed after close", func() bool {
		return d.codexBridge(s.ID()) == nil
	})
	if _, err := os.Stat(tmpSock); !os.IsNotExist(err) {
		t.Fatalf("socket file was not cleaned up: %v", err)
	}
}

func TestCodexBridgeDefersConversationBindUntilRolloutExists(t *testing.T) {
	codexHome := t.TempDir()
	projDir := t.TempDir()
	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "11", "21")
	rolloutPath := filepath.Join(rolloutDir, "rollout-2026-11-21T00-00-00-th-new.jsonl")

	mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		if method == "thread/read" {
			reply(map[string]any{
				"id":     id,
				"result": map[string]any{"thread": map[string]any{"id": "th-new", "path": rolloutPath}},
			})
		}
	})
	client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}

	s, err := session.Start(session.Spec{
		ID: "codex-0turn", NodeID: "test-node", Agent: "codex", Project: projDir,
		Command: testSleepCommand(), Env: map[string]string{"CODEX_HOME": codexHome}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)
	d.saved = map[string]savedSession{
		s.ID(): {Info: s.Info(), Request: &protocol.OpenSession{Agent: "codex"}},
	}

	b := newCodexBridge(t.Context(), s.ID(), client, nil, nil, nil)
	b.mu.Lock()
	b.tid = "th-new"
	b.pendingPath = rolloutPath
	b.subscribed = true
	b.mu.Unlock()
	d.attachCodexBridge(s, b)
	defer b.Close()

	// Before Turn 1 materializes the rollout file, ConversationId must stay empty
	// so sleepSession/reattach cannot brick an unmaterialized 0-turn session.
	mock.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-new",
		"turn":     map[string]any{"id": "turn-1", "status": "inProgress"},
	})
	waitFor(t, "turn/started status not applied", func() bool {
		return s.Info().GetStatus() == protocol.AgentWorking
	})
	if got := s.Info().GetConversationId(); got != "" {
		t.Fatalf("bound ConversationId %q before rollout existed on disk", got)
	}

	// Once the rollout file is written on disk, turn/completed binds ConversationId + TranscriptPath.
	written := writeFakeCodexRollout(t, codexHome, "th-new", projDir)
	mock.sendNotification(t, "turn/completed", map[string]any{
		"threadId": "th-new",
		"turn":     map[string]any{"id": "turn-1", "status": "completed"},
	})
	waitFor(t, "conversation not bound after rollout materialized", func() bool {
		return s.Info().GetConversationId() == "th-new" && s.TranscriptPath() == written
	})
}

func TestCodexBridgeIgnoresSubagentEphemeralAndAbandonedThreads(t *testing.T) {
	codexHome := t.TempDir()
	projDir := t.TempDir()
	th1Path := writeFakeCodexRollout(t, codexHome, "th-1", projDir)
	thNewPath := writeFakeCodexRollout(t, codexHome, "th-new-main", projDir)

	mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		switch method {
		case "thread/read":
			var p codex.ThreadReadParams
			_ = json.Unmarshal(params, &p)
			thread := map[string]any{"id": p.ThreadID}
			switch p.ThreadID {
			case "th-sub-read":
				thread["parentThreadId"] = "th-1"
			case "th-eph-read":
				thread["ephemeral"] = true
			case "th-1":
				thread["path"] = th1Path
			case "th-new-main":
				thread["path"] = thNewPath
			}
			reply(map[string]any{"id": id, "result": map[string]any{"thread": thread}})
		case "thread/resume":
			var p codex.ThreadResumeParams
			_ = json.Unmarshal(params, &p)
			reply(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": p.ThreadID, "path": thNewPath}}})
		}
	})

	client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Start(session.Spec{
		ID: "codex-hijack", NodeID: "test-node", Agent: "codex", Project: projDir,
		Command: testSleepCommand(), Env: map[string]string{"CODEX_HOME": codexHome}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetConversation("th-1", th1Path)

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)
	d.saved = map[string]savedSession{
		s.ID(): {Info: s.Info(), Request: &protocol.OpenSession{Agent: "codex"}, TranscriptPath: th1Path},
	}

	b := newCodexBridge(t.Context(), s.ID(), client, nil, nil, nil)
	b.setThread("th-1", true)
	b.mu.Lock()
	b.bound = true
	b.mu.Unlock()
	d.attachCodexBridge(s, b)
	defer b.Close()

	// 1. Subagent and ephemeral thread/started notifications must be ignored.
	mock.sendNotification(t, "thread/started", map[string]any{
		"thread": map[string]any{"id": "th-sub-1", "parentThreadId": "th-1", "ephemeral": false},
	})
	mock.sendNotification(t, "thread/started", map[string]any{
		"thread": map[string]any{"id": "th-sub-src", "source": map[string]any{"subAgent": "worker"}},
	})
	mock.sendNotification(t, "thread/started", map[string]any{
		"thread": map[string]any{"id": "th-eph-1", "ephemeral": true},
	})
	// 2. Subsequent status/changed for ignored threads or unannounced subagent/ephemeral threads must not hijack.
	mock.sendNotification(t, "thread/status/changed", map[string]any{
		"threadId": "th-sub-1", "status": map[string]any{"type": "active", "activeFlags": []string{}},
	})
	mock.sendNotification(t, "thread/status/changed", map[string]any{
		"threadId": "th-sub-read", "status": map[string]any{"type": "active", "activeFlags": []string{}},
	})
	mock.sendNotification(t, "thread/status/changed", map[string]any{
		"threadId": "th-eph-read", "status": map[string]any{"type": "active", "activeFlags": []string{}},
	})

	// 3. Genuine /new switches to th-new-main; abandoned th-1 going idle must NOT switch back.
	mock.sendNotification(t, "thread/started", map[string]any{
		"thread": map[string]any{"id": "th-new-main", "path": thNewPath, "ephemeral": false},
	})
	mock.sendNotification(t, "thread/status/changed", map[string]any{
		"threadId": "th-1", "status": map[string]any{"type": "idle"},
	})

	waitFor(t, "genuine /new thread not bound", func() bool {
		return b.threadID() == "th-new-main" && s.Info().GetConversationId() == "th-new-main"
	})
	// Send a marker delta on th-new-main to ensure the idle notification for th-1 was already processed before checking.
	mock.sendNotification(t, "item/agentMessage/delta", map[string]any{
		"threadId": "th-new-main", "turnId": "tu-1", "itemId": "it-1", "delta": "ok",
	})
	waitFor(t, "delta on new thread not received", func() bool {
		blocks := d.live.blocks(liveKey(s.ID(), ""))
		return len(blocks) == 1 && blocks[0].GetText() == "ok"
	})
	if got := b.threadID(); got != "th-new-main" {
		t.Fatalf("abandoned thread hijacked active thread back to %q", got)
	}
}

func TestCodexBridgeApprovalPreservationAndSeqAnchoring(t *testing.T) {
	mock := startMockCodexWSServer(t, nil)
	client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Start(session.Spec{
		ID: "codex-approval", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
		Command: testSleepCommand(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)

	b := newCodexBridge(t.Context(), s.ID(), client, nil, nil, nil)
	b.setThread("th-a", true)
	d.attachCodexBridge(s, b)
	defer b.Close()

	// 1. A delayed WS notification emitted 1s ago uses emittedAtMs, so a newer
	// PermissionRequest hook at `now` is not dropped by stale seq ordering.
	oldMs := time.Now().Add(-time.Second).UnixMilli()
	mock.sendNotificationAt(t, "turn/started", map[string]any{
		"threadId": "th-a",
		"turn":     map[string]any{"id": "turn-a", "status": "inProgress"},
	}, oldMs)
	waitFor(t, "turn/started not applied", func() bool {
		return s.Info().GetStatus() == protocol.AgentWorking
	})

	hookSeq := time.Now().UnixNano()
	if !s.ReportStatus(protocol.AgentWaiting, hookSeq) {
		t.Fatal("PermissionRequest hook status was dropped by delayed WS notification seq")
	}
	if _, ok := s.BeginApproval(hookSeq, &protocol.Approval{Id: "ap-1", Tool: "Bash"}); !ok {
		t.Fatal("BeginApproval failed")
	}

	// 2. Mid-turn thread/status/changed {active, activeFlags: []} must NOT withdraw the live approval!
	mock.sendNotificationAt(t, "thread/status/changed", map[string]any{
		"threadId": "th-a",
		"status":   map[string]any{"type": "active", "activeFlags": []string{}},
	}, time.Now().Add(time.Second).UnixMilli())
	// Send a follow-up live delta to synchronize before checking approval.
	mock.sendNotification(t, "item/agentMessage/delta", map[string]any{
		"threadId": "th-a", "turnId": "turn-a", "itemId": "m-1", "delta": "sync",
	})
	waitFor(t, "sync delta not received", func() bool {
		return len(d.live.blocks(liveKey(s.ID(), ""))) == 1
	})
	if s.Info().GetApproval().GetId() != "ap-1" {
		t.Fatalf("live approval was prematurely withdrawn: %+v", s.Info().GetApproval())
	}

	// 3. systemError clears AgentWorking to AgentIdle.
	s.FinishApproval(hookSeq, "ap-1", protocol.AgentWorking)
	mock.sendNotificationAt(t, "thread/status/changed", map[string]any{
		"threadId": "th-a",
		"status":   map[string]any{"type": "systemError"},
	}, time.Now().Add(2*time.Second).UnixMilli())
	waitFor(t, "systemError did not clear AgentWorking to AgentIdle", func() bool {
		return s.Info().GetStatus() == protocol.AgentIdle
	})
}

// TestCodexLiveAppServerSmoke verifies live integration against the locally
// installed `codex` binary without making any LLM requests. Opt-in via
// MAKO_LIVE_CODEX_SMOKE=1.
func TestCodexLiveAppServerSmoke(t *testing.T) {
	if os.Getenv("MAKO_LIVE_CODEX_SMOKE") != "1" {
		t.Skip("set MAKO_LIVE_CODEX_SMOKE=1 to run live codex app-server smoke test")
	}
	program, err := agent.Find("codex", codex.New().Info().Dirs...)
	if err != nil {
		t.Skipf("codex binary not installed: %v", err)
	}

	// 1. Live Catalog(refresh=true) via `codex app-server --stdio` model/list.
	models, err := codex.New().Catalog(t.Context(), nil, true)
	if err != nil {
		t.Fatalf("Catalog(refresh=true): %v", err)
	}
	if len(models) == 0 {
		t.Fatal("Catalog(refresh=true) returned 0 models")
	}
	t.Logf("live catalog returned %d models (first: %s / %s, efforts=%v)", len(models), models[0].ID, models[0].Name, models[0].Efforts)

	// 2. Full prepareCodexAppServer lifecycle with isolated CODEX_HOME (zero user ~/.codex pollution).
	tmpHome := t.TempDir()
	projDir := t.TempDir()
	runRoot := t.TempDir()
	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.dir = runRoot

	launch := agentspec.Launch{
		Launch: agent.Launch{
			Command: []string{
				program,
				"-c", `model_reasoning_effort="low"`,
				"--model", models[0].ID,
				"--add-dir", projDir,
				"--no-daemon",
				"--",
				"-fix-me",
			},
			Env: map[string]string{"CODEX_HOME": tmpHome},
		},
	}
	var convID, transcriptPath string
	b, stop := d.prepareCodexAppServer(t.Context(), "smoke-1", projDir, &launch, &convID, &transcriptPath)
	if b == nil {
		t.Fatal("prepareCodexAppServer returned nil bridge (unexpected fallback)")
	}
	defer stop()

	// Brand-new session must NOT pre-create an unmaterialized thread or pass `resume <id>`
	// (Codex TUI's `thread/resume` bootstrap fails with -32600 when no rollout exists on disk yet).
	if b.threadID() != "" {
		t.Fatalf("new session unexpectedly pre-created thread %q", b.threadID())
	}
	if convID != "" {
		t.Fatalf("new session prematurely bound convID=%q", convID)
	}
	joined := strings.Join(launch.Command, " ")
	if !strings.Contains(joined, "--remote") || strings.Contains(joined, " resume ") || strings.Contains(joined, "--no-daemon") || strings.Contains(joined, "--add-dir") || !strings.Contains(joined, " -- -fix-me") {
		t.Fatalf("unexpected rewritten TUI command: %q", launch.Command)
	}
	t.Logf("live bridge ready: cmd=%q", launch.Command)

	// 3. Clean teardown: process exits and socket file is removed.
	stop()
	select {
	case <-b.exited:
	default:
		t.Fatal("codex app-server process still running after Close")
	}
	if entries, err := os.ReadDir(filepath.Join(runRoot, "run")); err == nil && len(entries) != 0 {
		t.Fatalf("leftover files in run dir: %v", entries)
	}
}
