package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
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
				"--dangerously-bypass-approvals-and-sandbox",
				"--add-dir", projDir,
				"--no-daemon",
				"--",
				"-fix-me",
			},
			Env: map[string]string{"CODEX_HOME": tmpHome},
		},
	}
	var convID, transcriptPath string
	b, stop := d.prepareCodexAppServer(t.Context(), "smoke-1", projDir, &launch, nil, &convID, &transcriptPath)
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
	if !strings.Contains(joined, "--remote") || strings.Contains(joined, " resume ") || strings.Contains(joined, "--no-daemon") || strings.Contains(joined, "--add-dir") || !strings.Contains(joined, "--dangerously-bypass-approvals-and-sandbox -- -fix-me") {
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

func TestCodexStartupCoordinatorSerializesSameHome(t *testing.T) {
	var coord codexStartupCoordinator
	homeA := filepath.Join(t.TempDir(), "codex-a")
	homeB := filepath.Join(t.TempDir(), "codex-b")

	// 1. First holder acquires homeA.
	rel1, err := coord.acquire(t.Context(), homeA)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}

	// 2. Different homeB acquires immediately without waiting for homeA.
	relB, err := coord.acquire(t.Context(), homeB)
	if err != nil {
		t.Fatalf("acquire homeB: %v", err)
	}
	relB()

	// 3. Cancelled waiter B on homeA must NOT let waiter C pass active holder 1.
	ctxB, cancelB := context.WithCancel(t.Context())
	errB := make(chan error, 1)
	go func() {
		_, err := coord.acquire(ctxB, homeA)
		errB <- err
	}()
	time.Sleep(15 * time.Millisecond)

	var enteredC atomic.Bool
	doneC := make(chan struct{})
	go func() {
		relC, err := coord.acquire(t.Context(), homeA)
		if err != nil {
			t.Errorf("acquire C: %v", err)
			return
		}
		enteredC.Store(true)
		relC()
		close(doneC)
	}()
	time.Sleep(15 * time.Millisecond)

	cancelB()
	if err := <-errB; err == nil {
		t.Fatal("expected cancelled waiter B to return error")
	}
	time.Sleep(20 * time.Millisecond)
	if enteredC.Load() {
		t.Fatal("waiter C entered homeA before holder 1 released!")
	}

	rel1()
	select {
	case <-doneC:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter C did not acquire after holder 1 released")
	}
}

func TestCodexResumeAutoUnarchive(t *testing.T) {
	var mu sync.Mutex
	archived := true
	unarchivedCalls := 0
	mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		switch method {
		case "thread/resume":
			mu.Lock()
			isArc := archived
			mu.Unlock()
			if isArc {
				reply(map[string]any{
					"id":    id,
					"error": map[string]any{"code": -32600, "message": "thread th-arc is archived"},
				})
				return
			}
			reply(map[string]any{
				"id":     id,
				"result": map[string]any{"thread": map[string]any{"id": "th-arc", "path": "/tmp/th-arc.jsonl"}},
			})
		case "thread/unarchive":
			mu.Lock()
			unarchivedCalls++
			archived = false
			mu.Unlock()
			// Also verify tolerating "no archived rollout found" when unarchiving!
			reply(map[string]any{
				"id":    id,
				"error": map[string]any{"code": -32600, "message": "no archived rollout found for thread th-arc"},
			})
		}
	})

	client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	res, err := resumeCodexThread(t.Context(), client, codex.ThreadResumeParams{ThreadID: "th-arc", ExcludeTurns: true})
	if err != nil {
		t.Fatalf("resumeCodexThread: %v", err)
	}
	if res.Thread.ID != "th-arc" {
		t.Fatalf("thread ID = %q, want th-arc", res.Thread.ID)
	}
	mu.Lock()
	calls := unarchivedCalls
	mu.Unlock()
	if calls != 1 {
		t.Fatalf("unarchive calls = %d, want 1", calls)
	}
}

func TestCodexBridgeBackgroundTerminalsAndForegroundFilter(t *testing.T) {
	origPoll := codexBGPollInterval
	codexBGPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { codexBGPollInterval = origPoll })

	var mu sync.Mutex
	bgRows := []map[string]any{
		{"itemId": "cmd-fg", "processId": "p-fg", "command": "go test ./..."},
		{"itemId": "cmd-bg", "processId": "p-bg", "command": "npm run dev"},
	}
	unsupported := false
	var listCalls atomic.Int32

	mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		if method == "thread/backgroundTerminals/list" {
			listCalls.Add(1)
			mu.Lock()
			unsup := unsupported
			rows := bgRows
			mu.Unlock()
			if unsup {
				reply(map[string]any{
					"id":    id,
					"error": map[string]any{"code": -32601, "message": "method not found"},
				})
				return
			}
			reply(map[string]any{
				"id":     id,
				"result": map[string]any{"data": rows, "nextCursor": nil},
			})
		}
	})

	client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Start(session.Spec{
		ID: "codex-bg-1", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
		Command: testSleepCommand(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)

	b := newCodexBridge(t.Context(), s.ID(), client, nil, nil, nil)
	b.setThread("th-bg", true)
	// Simulate foreground command in progress before attach triggers first poll.
	b.mu.Lock()
	b.fgItems["cmd-fg"] = true
	b.fgProcesses["p-fg"] = true
	b.mu.Unlock()
	d.attachCodexBridge(s, b)
	defer b.Close()

	// Only p-bg should appear while p-fg is running in foreground.
	waitFor(t, "background terminal not reported or foreground not filtered", func() bool {
		tasks := s.Info().GetTasks()
		return len(tasks) == 1 && tasks[0].GetId() == "p-bg" && tasks[0].GetCommand() == "npm run dev" && tasks[0].GetRunning()
	})

	// Once turn completes and p-fg disappears, only p-bg remains; when bgRows empties, tasks clear.
	mu.Lock()
	bgRows = nil
	mu.Unlock()
	mock.sendNotification(t, "turn/completed", map[string]any{
		"threadId": "th-bg",
		"turn":     map[string]any{"id": "turn-1", "status": "completed"},
	})
	waitFor(t, "background terminals did not clear", func() bool {
		return len(s.Info().GetTasks()) == 0
	})

	// -32601 MethodNotFound marks background terminals unsupported and suppresses future calls.
	mu.Lock()
	unsupported = true
	mu.Unlock()
	b.scheduleBackgroundRefresh(s)
	waitFor(t, "bgUnsupported not set after -32601", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.bgUnsupported
	})
	before := listCalls.Load()
	b.scheduleBackgroundRefresh(s)
	time.Sleep(40 * time.Millisecond)
	if listCalls.Load() != before {
		t.Fatalf("expected no further RPC calls after -32601, got %d -> %d", before, listCalls.Load())
	}
}

func TestCodexRewindThreadPaginatedAndLegacy(t *testing.T) {
	t.Run("paginated beforeTurnId", func(t *testing.T) {
		var gotBeforeTurn string
		mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			switch method {
			case "thread/read":
				reply(map[string]any{
					"id": id,
					"result": map[string]any{"thread": map[string]any{
						"id":          "th-orig",
						"historyMode": "paginated",
						"turns":       []map[string]any{{"id": "t0"}, {"id": "t1"}, {"id": "t2"}},
					}},
				})
			case "thread/fork":
				var p codex.ThreadForkParams
				_ = json.Unmarshal(params, &p)
				gotBeforeTurn = p.BeforeTurnID
				reply(map[string]any{
					"id":     id,
					"result": map[string]any{"thread": map[string]any{"id": "th-forked", "path": "/tmp/th-forked.jsonl"}},
				})
			}
		})
		client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()

		res, err := codex.RewindThread(t.Context(), client, codex.RewindThreadOptions{
			ThreadID:      "th-orig",
			UserTurnIndex: 1,
			Cwd:           "/tmp/proj",
		})
		if err != nil {
			t.Fatalf("RewindThread paginated: %v", err)
		}
		if res.ThreadID != "th-forked" || gotBeforeTurn != "t1" {
			t.Fatalf("res=%+v beforeTurnId=%q, want th-forked / t1", res, gotBeforeTurn)
		}
	})

	t.Run("legacy fork plus rollback", func(t *testing.T) {
		var rolledTurns int
		mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			switch method {
			case "thread/read":
				reply(map[string]any{
					"id": id,
					"result": map[string]any{"thread": map[string]any{
						"id":          "th-leg",
						"historyMode": "legacy",
						"turns":       []map[string]any{{"id": "t0"}, {"id": "t1"}, {"id": "t2"}},
					}},
				})
			case "thread/fork":
				reply(map[string]any{
					"id":     id,
					"result": map[string]any{"thread": map[string]any{"id": "th-leg-fork", "path": "/tmp/th-leg-fork.jsonl"}},
				})
			case "thread/rollback":
				var p codex.ThreadRollbackParams
				_ = json.Unmarshal(params, &p)
				rolledTurns = p.NumTurns
				reply(map[string]any{
					"id":     id,
					"result": map[string]any{"thread": map[string]any{"id": p.ThreadID, "path": "/tmp/th-leg-fork.jsonl"}},
				})
			}
		})
		client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()

		res, err := codex.RewindThread(t.Context(), client, codex.RewindThreadOptions{
			ThreadID:        "th-leg",
			UserTurnIndex:   1,
			ServerUserAgent: "codex/0.150.0",
		})
		if err != nil {
			t.Fatalf("RewindThread legacy: %v", err)
		}
		if res.ThreadID != "th-leg-fork" || rolledTurns != 2 {
			t.Fatalf("res=%+v rolledTurns=%d, want th-leg-fork / 2", res, rolledTurns)
		}
	})
}

func TestCodexAbortGracefulInterruptAndStaleTurnGuard(t *testing.T) {
	var mu sync.Mutex
	var interrupts []codex.TurnInterruptParams
	var restartCalled atomic.Int32
	var mock *mockCodexWSServer
	mock = startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		if method == "turn/interrupt" {
			var p codex.TurnInterruptParams
			_ = json.Unmarshal(params, &p)
			mu.Lock()
			interrupts = append(interrupts, p)
			mu.Unlock()
			reply(map[string]any{"id": id, "result": map[string]any{}})
			go func(turnID string) {
				// First emit a stale completion for a different turn; it must be ignored.
				mock.sendNotification(t, "turn/completed", map[string]any{
					"threadId": "th-grace",
					"turn":     map[string]any{"id": "turn-stale", "status": "completed"},
				})
				time.Sleep(15 * time.Millisecond)
				if turnID == "turn-1" {
					mock.sendNotification(t, "turn/completed", map[string]any{
						"threadId": "th-grace",
						"turn":     map[string]any{"id": "turn-1", "status": "interrupted"},
					})
				} else {
					mock.sendNotification(t, "codex/event/turn_aborted", map[string]any{
						"threadId": "th-grace",
						"msg":      map[string]any{"type": "turn_aborted", "turn_id": turnID},
					})
				}
			}(p.TurnID)
		}
	})

	client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Start(session.Spec{
		ID: "codex-abort-grace", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
		Command: testSleepCommand(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)
	b := newCodexBridge(t.Context(), s.ID(), client, nil, nil, nil)
	b.setThread("th-grace", true)
	b.restartServer = func(context.Context, string) (*codexRestartOutcome, error) {
		restartCalled.Add(1)
		return nil, nil
	}
	d.attachCodexBridge(s, b)
	defer b.Close()

	// 1. Start turn-1, stream a delta, then interrupt via handleSend("\x1b").
	mock.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-grace",
		"turn":     map[string]any{"id": "turn-1", "status": "inProgress"},
	})
	mock.sendNotification(t, "item/agentMessage/delta", map[string]any{
		"threadId": "th-grace", "turnId": "turn-1", "itemId": "m1", "delta": "working...",
	})
	waitFor(t, "turn-1 did not become working", func() bool {
		return s.Info().GetStatus() == protocol.AgentWorking && len(d.live.blocks(liveKey(s.ID(), ""))) == 1
	})

	st, done := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
		return d.handleSend(t.Context(), st, env)
	}, protocol.TypeSend, &protocol.Send{SessionId: s.ID(), Keys: []byte("\x1b")})
	env, err := st.Recv()
	if err != nil || env.Type() != protocol.TypeSessionInfo {
		t.Fatalf("handleSend interrupt reply = %v, %v", env, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("handleSend interrupt err: %v", err)
	}
	waitFor(t, "status did not settle to idle", func() bool {
		return s.Info().GetStatus() == protocol.AgentIdle
	})
	if restartCalled.Load() != 0 {
		t.Fatalf("graceful interrupt unexpectedly triggered force restart (%d)", restartCalled.Load())
	}

	// 2. Start turn-2, ensure subagent codex/event notifications with snake_case
	// and camelCase thread IDs cannot settle the parent turn, then settle via
	// legacy codex/event/turn_aborted for the parent thread.
	mock.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-grace",
		"turn":     map[string]any{"id": "turn-2", "status": "inProgress"},
	})
	waitFor(t, "turn-2 did not become working", func() bool {
		return s.Info().GetStatus() == protocol.AgentWorking
	})
	// Subagent codex/event variants must all be ignored for parent thread "th-grace".
	mock.sendNotification(t, "codex/event/turn_aborted", map[string]any{
		"thread_id": "th-sub-snake",
		"msg":       map[string]any{"type": "turn_aborted", "turn_id": "turn-2"},
	})
	mock.sendNotification(t, "codex/event/turn_aborted", map[string]any{
		"conversation_id": "th-sub-conv",
		"msg":             map[string]any{"type": "turn_aborted", "turn_id": "turn-2"},
	})
	mock.sendNotification(t, "codex/event/turn_aborted", map[string]any{
		"msg": map[string]any{"type": "turn_aborted", "turnId": "turn-2", "threadId": "th-sub-nested"},
	})
	// Synchronize with a parent delta to guarantee all three subagent events were processed.
	mock.sendNotification(t, "item/agentMessage/delta", map[string]any{
		"threadId": "th-grace", "turnId": "turn-2", "itemId": "m2", "delta": "still-running",
	})
	waitFor(t, "turn-2 sync delta not received", func() bool {
		blocks := d.live.blocks(liveKey(s.ID(), ""))
		return len(blocks) == 1 && blocks[0].GetText() == "still-running"
	})
	b.mu.Lock()
	stillActive, stillTurnID := b.turnActive, b.activeTurnID
	b.mu.Unlock()
	if !stillActive || stillTurnID != "turn-2" || s.Info().GetStatus() != protocol.AgentWorking {
		t.Fatalf("subagent codex/event settled parent turn: active=%v turnID=%q status=%q", stillActive, stillTurnID, s.Info().GetStatus())
	}

	res := d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{GracePeriod: time.Second})
	if !res.HadActiveTurn || !res.Aborted || res.ForcedRestart {
		t.Fatalf("turn-2 abort result = %+v, want graceful abort", res)
	}
	waitFor(t, "turn-2 status did not settle to idle", func() bool {
		return s.Info().GetStatus() == protocol.AgentIdle
	})
	mu.Lock()
	gotInterrupts := slices.Clone(interrupts)
	mu.Unlock()
	if len(gotInterrupts) != 2 || gotInterrupts[0].TurnID != "turn-1" || gotInterrupts[1].TurnID != "turn-2" {
		t.Fatalf("interrupts = %+v, want [turn-1, turn-2]", gotInterrupts)
	}

	// 3. When idle (no active turn), abortCodexTurn reports HadActiveTurn=false.
	idleRes := d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{GracePeriod: 100 * time.Millisecond})
	if idleRes.HadActiveTurn || idleRes.Aborted || idleRes.ForcedRestart {
		t.Fatalf("idle abort result = %+v, want empty", idleRes)
	}
}

func TestCodexAbortPendingApprovalDeniedFirst(t *testing.T) {
	mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		if method == "turn/interrupt" {
			reply(map[string]any{"id": id, "result": map[string]any{}})
		}
	})
	client, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Start(session.Spec{
		ID: "codex-abort-ap", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
		Command: testSleepCommand(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)
	d.approvals.watch(true)

	b := newCodexBridge(t.Context(), s.ID(), client, nil, nil, nil)
	b.setThread("th-ap", true)
	d.attachCodexBridge(s, b)
	defer b.Close()

	mock.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-ap",
		"turn":     map[string]any{"id": "turn-ap", "status": "inProgress"},
	})
	waitFor(t, "turn-ap not working", func() bool {
		return s.Info().GetStatus() == protocol.AgentWorking
	})

	hookSeq := time.Now().UnixNano() + int64(time.Second)
	s.ReportStatus(protocol.AgentWaiting, hookSeq)
	replyCh := make(chan hookReply, 1)
	go func() {
		replyCh <- d.approvals.ask(t.Context(), s, "codex", agent.Event{Tool: "Bash", Input: json.RawMessage(`{"command":"sleep 99"}`)}, hookSeq)
	}()
	waitFor(t, "approval never became pending", func() bool {
		return s.Info().GetApproval() != nil
	})

	forceOff := false
	res := d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{
		GracePeriod:           60 * time.Millisecond,
		ForceRestartOnTimeout: &forceOff,
	})
	if !res.HadActiveTurn {
		t.Fatalf("expected HadActiveTurn=true, got %+v", res)
	}
	select {
	case rep := <-replyCh:
		if rep.Decision != protocol.DecisionDeny {
			t.Fatalf("approval decision = %q, want deny", rep.Decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending approval was not denied on abort")
	}
}

func TestCodexAbortTimeoutTriggersForceRestartResumeAndPTYRespawn(t *testing.T) {
	origGrace := codexAbortGracePeriod
	codexAbortGracePeriod = 80 * time.Millisecond
	t.Cleanup(func() { codexAbortGracePeriod = origGrace })

	// Server 1: wedged — acknowledges or ignores turn/interrupt and NEVER sends turn/completed.
	var int1Count atomic.Int32
	srv1 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		if method == "turn/interrupt" {
			int1Count.Add(1)
			// Intentionally do not respond or emit turn/completed to simulate a wedged server.
		}
	})

	// Server 2: fresh restarted server — handles thread/resume and subsequent turns/deliveries.
	var mu2 sync.Mutex
	var resumedIDs []string
	var delivered2 []string
	srv2 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		switch method {
		case "thread/resume":
			var p codex.ThreadResumeParams
			_ = json.Unmarshal(params, &p)
			mu2.Lock()
			resumedIDs = append(resumedIDs, p.ThreadID)
			mu2.Unlock()
			reply(map[string]any{
				"id":     id,
				"result": map[string]any{"thread": map[string]any{"id": p.ThreadID, "path": ""}},
			})
		case "thread/queue/add":
			var p codex.ThreadQueueAddParams
			_ = json.Unmarshal(params, &p)
			if len(p.Input) > 0 {
				mu2.Lock()
				delivered2 = append(delivered2, p.Input[0].Text)
				mu2.Unlock()
			}
			reply(map[string]any{
				"id":     id,
				"result": map[string]any{"queuedSubmission": map[string]any{"id": "q-after-restart"}},
			})
		case "thread/backgroundTerminals/list":
			reply(map[string]any{"id": id, "result": map[string]any{"data": []any{}}})
		}
	})

	client1, err := codex.DialAppServerWS(t.Context(), srv1.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}

	// Start a real dummy old server process group + socket file to verify cleanup on restart.
	oldCmd := exec.Command(testSleepCommand()[0], testSleepCommand()[1:]...)
	configureCodexServerCmd(oldCmd)
	if err := oldCmd.Start(); err != nil {
		t.Fatal(err)
	}
	oldExited := make(chan struct{})
	go func() {
		_ = oldCmd.Wait()
		close(oldExited)
	}()
	oldSock := filepath.Join(t.TempDir(), "old-codex.sock")
	if err := os.WriteFile(oldSock, []byte("sock"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := session.Start(session.Spec{
		ID: "codex-force-1", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
		Command: testSleepCommand(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	oldPTYPID := s.PID()
	sub := s.Subscribe()
	defer sub.Close()
	reattachedSeen := make(chan struct{}, 1)
	go func() {
		var buf strings.Builder
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		for {
			chunk, ok := sub.Next(ctx)
			if !ok {
				return
			}
			buf.Write(chunk)
			if strings.Contains(buf.String(), "reattached-tui") {
				select {
				case reattachedSeen <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)

	b := newCodexBridge(t.Context(), s.ID(), client1, oldCmd, oldExited, func() { _ = os.Remove(oldSock) })
	b.setThread("th-wedged", true)
	var restartCount atomic.Int32
	b.restartServer = func(ctx context.Context, prevThreadID string) (*codexRestartOutcome, error) {
		restartCount.Add(1)
		c2, err := codex.DialAppServerWS(ctx, srv2.wsURL(), "")
		if err != nil {
			return nil, err
		}
		res, err := resumeCodexThread(ctx, c2, codex.ThreadResumeParams{ThreadID: prevThreadID, ExcludeTurns: true})
		if err != nil {
			_ = c2.Close()
			return nil, err
		}
		tuiCmd := testSleepCommand()
		if runtime.GOOS != "windows" {
			tuiCmd = []string{"sh", "-c", "printf 'reattached-tui'; sleep 30"}
		}
		return &codexRestartOutcome{
			client:        c2,
			threadID:      res.Thread.ID,
			resumedThread: true,
			tuiCmd:        tuiCmd,
		}, nil
	}
	d.attachCodexBridge(s, b)
	defer b.Close()

	// Start wedged turn with live streaming blocks + background task.
	srv1.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-wedged",
		"turn":     map[string]any{"id": "turn-wedged", "status": "inProgress"},
	})
	srv1.sendNotification(t, "item/started", map[string]any{
		"threadId": "th-wedged", "turnId": "turn-wedged",
		"item": map[string]any{"type": "commandExecution", "id": "cmd-1", "command": "sleep 9999"},
	})
	s.ReportBridgeTasks([]*protocol.BackgroundTask{{Id: "bg-1", Command: "old-bg", Running: true}})
	waitFor(t, "wedged turn state not established", func() bool {
		return s.Info().GetStatus() == protocol.AgentWorking && len(d.live.blocks(liveKey(s.ID(), ""))) == 1
	})

	// Trigger interrupt via handleSend("\x1b").
	st, done := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
		return d.handleSend(t.Context(), st, env)
	}, protocol.TypeSend, &protocol.Send{SessionId: s.ID(), Keys: []byte("\x1b")})
	env, err := st.Recv()
	if err != nil || env.Type() != protocol.TypeSessionInfo {
		t.Fatalf("handleSend reply = %v, %v", env, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("handleSend err: %v", err)
	}

	if int1Count.Load() != 1 {
		t.Fatalf("turn/interrupt calls on server 1 = %d, want 1", int1Count.Load())
	}
	if restartCount.Load() != 1 {
		t.Fatalf("restart calls = %d, want 1", restartCount.Load())
	}
	// Old server process and socket file cleaned up.
	select {
	case <-oldExited:
	case <-time.After(3 * time.Second):
		t.Fatal("old app-server process was not terminated")
	}
	if _, err := os.Stat(oldSock); !os.IsNotExist(err) {
		t.Fatalf("old socket file was not removed: %v", err)
	}
	// Old client disconnected.
	select {
	case <-client1.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("old AppServerClient was not closed")
	}
	// Thread resumed on server 2.
	mu2.Lock()
	gotResumed := slices.Clone(resumedIDs)
	mu2.Unlock()
	if !slices.Equal(gotResumed, []string{"th-wedged"}) {
		t.Fatalf("resumed thread IDs = %v, want [th-wedged]", gotResumed)
	}
	// Session UI state: not stuck in working, live blocks cleared, tasks cleared, notice posted, PTY alive & respawned.
	info := s.Info()
	if info.GetState() != protocol.StateRunning {
		t.Fatalf("session state = %q, want running", info.GetState())
	}
	if info.GetStatus() != protocol.AgentIdle {
		t.Fatalf("session status = %q, want idle", info.GetStatus())
	}
	if len(d.live.blocks(liveKey(s.ID(), ""))) != 0 {
		t.Fatalf("live blocks not cleared after force restart: %+v", d.live.blocks(liveKey(s.ID(), "")))
	}
	if len(info.GetTasks()) != 0 {
		t.Fatalf("stale tasks not cleared after force restart: %+v", info.GetTasks())
	}
	if !strings.Contains(info.GetNotice().GetBody(), "restarted and the previous thread was resumed") {
		t.Fatalf("unexpected recovery notice: %+v", info.GetNotice())
	}
	if s.PID() == oldPTYPID {
		t.Fatalf("session PTY PID did not change after restart (%d)", oldPTYPID)
	}
	if runtime.GOOS != "windows" {
		select {
		case <-reattachedSeen:
		case <-time.After(3 * time.Second):
			t.Fatal("subscriber did not receive respawned PTY output")
		}
	}
	if d.codexBridge(s.ID()) != b {
		t.Fatal("codexBridge was unexpectedly unregistered after restart")
	}

	// Post-restart health: server 2 notifications & new prompt delivery work cleanly.
	srv2.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-wedged",
		"turn":     map[string]any{"id": "turn-after", "status": "inProgress"},
	})
	srv2.sendNotification(t, "item/agentMessage/delta", map[string]any{
		"threadId": "th-wedged", "turnId": "turn-after", "itemId": "m-after", "delta": "recovered!",
	})
	waitFor(t, "post-restart delta not received", func() bool {
		blocks := d.live.blocks(liveKey(s.ID(), ""))
		return s.Info().GetStatus() == protocol.AgentWorking && len(blocks) == 1 && blocks[0].GetText() == "recovered!"
	})
	srv2.sendNotification(t, "turn/completed", map[string]any{
		"threadId": "th-wedged",
		"turn":     map[string]any{"id": "turn-after", "status": "completed"},
	})
	waitFor(t, "post-restart turn did not complete", func() bool {
		return s.Info().GetStatus() == protocol.AgentDone
	})

	if !d.deliverViaCodexBridge(t.Context(), s.ID(), "next prompt") {
		t.Fatal("post-restart deliverViaCodexBridge failed")
	}
	mu2.Lock()
	gotDelivered := slices.Clone(delivered2)
	mu2.Unlock()
	if !slices.Equal(gotDelivered, []string{"next prompt"}) {
		t.Fatalf("post-restart delivered = %v, want [next prompt]", gotDelivered)
	}
}

func TestCodexAbortStaleEpochExitAndNotificationsIgnored(t *testing.T) {
	srv1 := startMockCodexWSServer(t, nil)
	srv2 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		if method == "thread/resume" {
			reply(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "th-epoch"}}})
		}
	})
	c1, err := codex.DialAppServerWS(t.Context(), srv1.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Start(session.Spec{
		ID: "codex-epoch", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
		Command: testSleepCommand(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)

	oldExited := make(chan struct{})
	b := newCodexBridge(t.Context(), s.ID(), c1, nil, oldExited, nil)
	b.setThread("th-epoch", true)
	b.restartServer = func(ctx context.Context, prevThreadID string) (*codexRestartOutcome, error) {
		c2, err := codex.DialAppServerWS(ctx, srv2.wsURL(), "")
		if err != nil {
			return nil, err
		}
		_, _ = c2.ThreadResume(ctx, codex.ThreadResumeParams{ThreadID: prevThreadID, ExcludeTurns: true})
		return &codexRestartOutcome{
			client:        c2,
			threadID:      prevThreadID,
			resumedThread: true,
			tuiCmd:        testSleepCommand(),
		}, nil
	}
	d.attachCodexBridge(s, b)
	defer b.Close()

	srv1.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-epoch", "turn": map[string]any{"id": "turn-1", "status": "inProgress"},
	})
	waitFor(t, "turn-1 not active", func() bool { return s.Info().GetStatus() == protocol.AgentWorking })

	// Force restart advances epoch 1 -> 2.
	res := d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{GracePeriod: 40 * time.Millisecond})
	if !res.ForcedRestart || !res.ResumedThread {
		t.Fatalf("abort result = %+v, want forced restart + resumed", res)
	}

	// Start a new active turn on epoch 2.
	srv2.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-epoch", "turn": map[string]any{"id": "turn-2", "status": "inProgress"},
	})
	waitFor(t, "turn-2 not active on epoch 2", func() bool {
		b.mu.Lock()
		ok := b.turnActive && b.activeTurnID == "turn-2" && b.epoch == 2
		b.mu.Unlock()
		return ok && s.Info().GetStatus() == protocol.AgentWorking
	})

	// Fire a stale exit from epoch 1 AND a stale turn/completed notification tagged with epoch 1.
	close(oldExited)
	b.onProcessExit(1)
	staleParams, _ := json.Marshal(map[string]any{
		"threadId": "th-epoch",
		"turn":     map[string]any{"id": "turn-2", "status": "interrupted"},
	})
	d.handleCodexNotification(s, b, codex.Notification{Method: "turn/completed", Params: staleParams}, 1)

	// Epoch 2's active turn must remain completely untouched.
	b.mu.Lock()
	active, turnID, epoch := b.turnActive, b.activeTurnID, b.epoch
	b.mu.Unlock()
	if !active || turnID != "turn-2" || epoch != 2 {
		t.Fatalf("stale epoch 1 corrupted epoch 2 state: active=%v turnID=%q epoch=%d", active, turnID, epoch)
	}
	if s.Info().GetStatus() != protocol.AgentWorking {
		t.Fatalf("stale epoch 1 altered session status to %q", s.Info().GetStatus())
	}
}

func TestCodexAbortConcurrentDeduplicatedAndDeliverSerialized(t *testing.T) {
	var intCalls atomic.Int32
	srv1 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		if method == "turn/interrupt" {
			intCalls.Add(1)
		}
	})

	var deliveredMu sync.Mutex
	var delivered2 []string
	srv2 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		switch method {
		case "thread/resume":
			reply(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "th-conc"}}})
		case "thread/queue/add":
			var p codex.ThreadQueueAddParams
			_ = json.Unmarshal(params, &p)
			if len(p.Input) > 0 {
				deliveredMu.Lock()
				delivered2 = append(delivered2, p.Input[0].Text)
				deliveredMu.Unlock()
			}
			reply(map[string]any{"id": id, "result": map[string]any{"queuedSubmission": map[string]any{"id": "q-conc"}}})
		}
	})

	c1, err := codex.DialAppServerWS(t.Context(), srv1.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Start(session.Spec{
		ID: "codex-conc", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
		Command: testSleepCommand(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)

	b := newCodexBridge(t.Context(), s.ID(), c1, nil, nil, nil)
	b.setThread("th-conc", true)
	var restartCalls atomic.Int32
	restartStarted := make(chan struct{}, 1)
	b.restartServer = func(ctx context.Context, prevThreadID string) (*codexRestartOutcome, error) {
		restartCalls.Add(1)
		select {
		case restartStarted <- struct{}{}:
		default:
		}
		time.Sleep(40 * time.Millisecond)
		c2, err := codex.DialAppServerWS(ctx, srv2.wsURL(), "")
		if err != nil {
			return nil, err
		}
		_, _ = c2.ThreadResume(ctx, codex.ThreadResumeParams{ThreadID: prevThreadID, ExcludeTurns: true})
		return &codexRestartOutcome{
			client:        c2,
			threadID:      prevThreadID,
			resumedThread: true,
			tuiCmd:        testSleepCommand(),
		}, nil
	}
	d.attachCodexBridge(s, b)
	defer b.Close()

	srv1.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-conc", "turn": map[string]any{"id": "turn-conc", "status": "inProgress"},
	})
	waitFor(t, "turn-conc not active", func() bool { return s.Info().GetStatus() == protocol.AgentWorking })

	const callers = 5
	results := make([]codexAbortResult, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{GracePeriod: 60 * time.Millisecond})
		}(i)
	}

	// Launch a message delivery while force restart is underway; it must wait
	// for abort/restart to finish and deliver cleanly to server 2.
	deliverErr := make(chan error, 1)
	go func() {
		<-restartStarted
		deliverErr <- b.deliverMessage(t.Context(), "after-concurrent-abort")
	}()

	wg.Wait()
	if err := <-deliverErr; err != nil {
		t.Fatalf("deliverMessage during restart failed: %v", err)
	}
	if intCalls.Load() != 1 {
		t.Fatalf("turn/interrupt calls = %d, want 1", intCalls.Load())
	}
	if restartCalls.Load() != 1 {
		t.Fatalf("restart calls = %d, want 1", restartCalls.Load())
	}
	for i, r := range results {
		if !r.HadActiveTurn || !r.Aborted || !r.ForcedRestart || !r.ResumedThread || r.Err != nil {
			t.Fatalf("caller %d result = %+v, want deduped forced restart success", i, r)
		}
	}
	deliveredMu.Lock()
	gotDelivered := slices.Clone(delivered2)
	deliveredMu.Unlock()
	if !slices.Equal(gotDelivered, []string{"after-concurrent-abort"}) {
		t.Fatalf("delivered to server 2 = %v, want [after-concurrent-abort]", gotDelivered)
	}
}

func TestCodexAbortResumeFailureFallsBackCleanly(t *testing.T) {
	srv1 := startMockCodexWSServer(t, nil)
	srv2 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		if method == "thread/resume" {
			reply(map[string]any{
				"id":    id,
				"error": map[string]any{"code": -32600, "message": "no rollout found for thread id"},
			})
		}
	})
	c1, err := codex.DialAppServerWS(t.Context(), srv1.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.Start(session.Spec{
		ID: "codex-noresume", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
		Command: testSleepCommand(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)

	b := newCodexBridge(t.Context(), s.ID(), c1, nil, nil, nil)
	b.setThread("th-missing", true)
	b.restartServer = func(ctx context.Context, prevThreadID string) (*codexRestartOutcome, error) {
		c2, err := codex.DialAppServerWS(ctx, srv2.wsURL(), "")
		if err != nil {
			return nil, err
		}
		_, err = resumeCodexThread(ctx, c2, codex.ThreadResumeParams{ThreadID: prevThreadID, ExcludeTurns: true})
		return &codexRestartOutcome{
			client:        c2,
			resumedThread: err == nil,
			tuiCmd:        testSleepCommand(),
		}, nil
	}
	d.attachCodexBridge(s, b)
	defer b.Close()

	srv1.sendNotification(t, "turn/started", map[string]any{
		"threadId": "th-missing", "turn": map[string]any{"id": "turn-1", "status": "inProgress"},
	})
	waitFor(t, "turn-1 not working", func() bool { return s.Info().GetStatus() == protocol.AgentWorking })

	res := d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{GracePeriod: 40 * time.Millisecond})
	if !res.ForcedRestart || res.ResumedThread || res.Err != nil {
		t.Fatalf("result = %+v, want ForcedRestart=true ResumedThread=false Err=nil", res)
	}
	if b.threadID() != "" {
		t.Fatalf("stale threadID %q not cleared after failed resume", b.threadID())
	}
	if !strings.Contains(s.Info().GetNotice().GetBody(), "could not be resumed") {
		t.Fatalf("unexpected notice = %+v", s.Info().GetNotice())
	}
	if s.Info().GetState() != protocol.StateRunning || s.Info().GetStatus() != protocol.AgentIdle {
		t.Fatalf("post-fallback state=%q status=%q", s.Info().GetState(), s.Info().GetStatus())
	}
}

func TestBuildCodexRemoteTUICommandRestartOmitsInitialPrompt(t *testing.T) {
	split := codex.SplitRemoteLaunch([]string{
		"codex", "--model", "gpt-6.2-sol", "--dangerously-bypass-approvals-and-sandbox", "--", "-initial-task",
	})
	tr := &codexAppServerTransport{
		extraTUIArgs: []string{"--remote-auth-token-env", "MAKO_CODEX_WS_TOKEN"},
		extraTUIEnv:  map[string]string{"MAKO_CODEX_WS_TOKEN": "tok-123"},
	}

	resumedCmd, env := buildCodexRemoteTUICommand(split, tr, "ws://127.0.0.1:4321", "th-1", false)
	joined := strings.Join(resumedCmd, " ")
	if strings.Contains(joined, "-initial-task") {
		t.Fatalf("restart resumed command leaked initial prompt: %q", resumedCmd)
	}
	if !strings.Contains(joined, "--remote ws://127.0.0.1:4321") || !strings.Contains(joined, "resume --model gpt-6.2-sol th-1") {
		t.Fatalf("unexpected resumed command: %q", resumedCmd)
	}
	if env["MAKO_CODEX_WS_TOKEN"] != "tok-123" {
		t.Fatalf("extraTUIEnv = %v", env)
	}

	freshCmd, _ := buildCodexRemoteTUICommand(split, tr, "ws://127.0.0.1:4321", "", false)
	freshJoined := strings.Join(freshCmd, " ")
	if strings.Contains(freshJoined, "-initial-task") || strings.Contains(freshJoined, " resume ") {
		t.Fatalf("fresh restart command unexpected: %q", freshCmd)
	}
	if !strings.Contains(freshJoined, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("fresh restart command missing NewTUIArgs: %q", freshCmd)
	}
}

func TestCodexAbortIdleFallsBackToPTYAndDiscoversTurnViaThreadRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh to capture PTY Escape")
	}
	dir := t.TempDir()
	ptyOut := filepath.Join(dir, "esc.out")
	s, err := session.Start(session.Spec{
		ID: "codex-idle-esc", NodeID: "test-node", Agent: "codex", Project: dir,
		Command: []string{"sh", "-c", `stty raw -echo; dd bs=1 count=1 of=esc.out 2>/dev/null; sleep 30`},
		Cols:    80, Rows: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var mu sync.Mutex
	var gotInterrupt codex.TurnInterruptParams
	var srv *mockCodexWSServer
	srv = startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		switch method {
		case "thread/read":
			reply(map[string]any{
				"id": id,
				"result": map[string]any{
					"thread": map[string]any{
						"id": "th-disc",
						"turns": []map[string]any{
							{"id": "turn-old", "status": "completed"},
							{"id": "turn-discovered", "status": "inProgress"},
						},
					},
				},
			})
		case "turn/interrupt":
			var p codex.TurnInterruptParams
			_ = json.Unmarshal(params, &p)
			mu.Lock()
			gotInterrupt = p
			mu.Unlock()
			reply(map[string]any{"id": id, "result": map[string]any{}})
			go srv.sendNotification(t, "turn/completed", map[string]any{
				"threadId": "th-disc",
				"turn":     map[string]any{"id": p.TurnID, "status": "interrupted"},
			})
		}
	})

	client, err := codex.DialAppServerWS(t.Context(), srv.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)
	b := newCodexBridge(t.Context(), s.ID(), client, nil, nil, nil)
	b.setThread("th-disc", true)
	d.attachCodexBridge(s, b)
	defer b.Close()

	// 1. When idle (no active turn), Escape via handleSend falls back to PTY.
	st, done := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
		return d.handleSend(t.Context(), st, env)
	}, protocol.TypeSend, &protocol.Send{SessionId: s.ID(), Keys: []byte("\x1b")})
	if env, err := st.Recv(); err != nil || env.Type() != protocol.TypeSessionInfo {
		t.Fatalf("idle handleSend reply = %v, %v", env, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("idle handleSend err: %v", err)
	}
	waitFor(t, "idle Escape never reached PTY", func() bool {
		raw, _ := os.ReadFile(ptyOut)
		return string(raw) == "\x1b"
	})

	// 2. When working without a prior turn/started notification, abort discovers
	// the inProgress turn via thread/read and sends turn/interrupt for it.
	s.SetStatus(protocol.AgentWorking)
	res := d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{GracePeriod: time.Second})
	if !res.HadActiveTurn || !res.Aborted || res.ForcedRestart {
		t.Fatalf("discovered-turn abort = %+v, want graceful abort", res)
	}
	mu.Lock()
	got := gotInterrupt
	mu.Unlock()
	if got.ThreadID != "th-disc" || got.TurnID != "turn-discovered" {
		t.Fatalf("discovered TurnInterrupt = %+v, want th-disc / turn-discovered", got)
	}
}

func TestCodexAbortStaleEpochGoalAndAuthNotificationsIgnored(t *testing.T) {
	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s, err := session.Start(session.Spec{
		ID: "codex-stale-goal-auth", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
		Command: testSleepCommand(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	b := newCodexBridge(t.Context(), s.ID(), nil, nil, nil, nil)
	defer b.Close()
	b.mu.Lock()
	b.epoch = 2
	b.tid = "th-ep2"
	b.bound = true
	b.mu.Unlock()

	emit := func(epoch uint64, method string, params any) {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		d.handleCodexNotification(s, b, codex.Notification{
			Method:      method,
			Params:      raw,
			EmittedAtMs: time.Now().UnixMilli(),
		}, epoch)
	}

	// Establish active goal and auth error on epoch 2.
	emit(2, "thread/goal/updated", map[string]any{
		"threadId": "th-ep2",
		"goal": map[string]any{
			"threadId":  "th-ep2",
			"objective": "epoch-2 objective",
			"status":    "active",
			"updatedAt": 1781690000,
		},
	})
	emit(2, "error", map[string]any{
		"threadId":  "th-ep2",
		"turnId":    "turn-ep2",
		"willRetry": false,
		"error": map[string]any{
			"codexErrorInfo": "unauthorized",
			"message":        "Your access token could not be refreshed.",
		},
	})
	if got := s.Info().GetGoal(); got == nil || got.GetText() != "epoch-2 objective" {
		t.Fatalf("epoch-2 goal = %v, want epoch-2 objective", got)
	}
	if ae := s.Info().GetAuthError(); ae == nil || ae.GetKind() != protocol.AuthErrorOAuthExpired {
		t.Fatalf("epoch-2 authError = %v, want oauth_expired", ae)
	}

	// Stale epoch 1 notifications must not overwrite or clear epoch 2 goal or auth error.
	emit(1, "thread/goal/updated", map[string]any{
		"threadId": "th-ep2",
		"goal": map[string]any{
			"threadId":  "th-ep2",
			"objective": "stale epoch-1 objective",
			"status":    "active",
			"updatedAt": 1781690099,
		},
	})
	emit(1, "thread/goal/cleared", map[string]any{"threadId": "th-ep2"})
	emit(1, "account/login/completed", map[string]any{"loginId": "l-stale", "success": true})
	emit(1, "turn/completed", map[string]any{
		"threadId": "th-ep2",
		"turn":     map[string]any{"id": "turn-ep1", "status": "completed"},
	})

	if got := s.Info().GetGoal(); got == nil || got.GetText() != "epoch-2 objective" {
		t.Fatalf("stale epoch 1 corrupted epoch 2 goal: %v", got)
	}
	if ae := s.Info().GetAuthError(); ae == nil || ae.GetKind() != protocol.AuthErrorOAuthExpired {
		t.Fatalf("stale epoch 1 corrupted epoch 2 authError: %v", ae)
	}
}

func TestCodexAbortRestartPropagatesAndClearsGoal(t *testing.T) {
	t.Run("resumed thread propagates goal and supports post-restart goal RPCs", func(t *testing.T) {
		srv1 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			if method == "thread/read" {
				reply(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "th-goal-r"}}})
			}
		})
		var setCalls atomic.Int32
		srv2 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			switch method {
			case "thread/read":
				reply(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "th-goal-r"}}})
			case "thread/resume":
				reply(map[string]any{
					"id": id,
					"result": map[string]any{
						"thread": map[string]any{
							"id": "th-goal-r",
							"goal": map[string]any{
								"threadId":  "th-goal-r",
								"objective": "resumed goal objective",
								"status":    "active",
								"updatedAt": 1781691000,
							},
						},
					},
				})
			case "thread/goal/set":
				setCalls.Add(1)
				var p codex.ThreadGoalSetParams
				_ = json.Unmarshal(params, &p)
				obj := ""
				if p.Objective != nil {
					obj = *p.Objective
				}
				reply(map[string]any{
					"id": id,
					"result": map[string]any{
						"goal": map[string]any{
							"threadId":  p.ThreadID,
							"objective": obj,
							"status":    "active",
							"updatedAt": 1781691005,
						},
					},
				})
			case "thread/backgroundTerminals/list":
				reply(map[string]any{"id": id, "result": map[string]any{"data": []any{}}})
			}
		})

		c1, err := codex.DialAppServerWS(t.Context(), srv1.wsURL(), "")
		if err != nil {
			t.Fatal(err)
		}
		s, err := session.Start(session.Spec{
			ID: "codex-restart-goal-ok", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
			Command: testSleepCommand(), Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		d.sessions.add(s)

		b := newCodexBridge(t.Context(), s.ID(), c1, nil, nil, nil)
		b.setThread("th-goal-r", true)
		b.restartServer = func(ctx context.Context, prevThreadID string) (*codexRestartOutcome, error) {
			c2, err := codex.DialAppServerWS(ctx, srv2.wsURL(), "")
			if err != nil {
				return nil, err
			}
			res, err := resumeCodexThread(ctx, c2, codex.ThreadResumeParams{ThreadID: prevThreadID, ExcludeTurns: true})
			if err != nil {
				_ = c2.Close()
				return nil, err
			}
			var initGoal *protocol.GoalStatus
			if res.Thread.Goal != nil {
				initGoal = codex.MapThreadGoal(res.Thread.Goal, res.Thread.ID, true, true, time.Now())
			}
			return &codexRestartOutcome{
				client:        c2,
				threadID:      res.Thread.ID,
				resumedThread: true,
				initGoal:      initGoal,
				tuiCmd:        testSleepCommand(),
			}, nil
		}
		d.attachCodexBridge(s, b)
		defer b.Close()

		srv1.sendNotification(t, "turn/started", map[string]any{
			"threadId": "th-goal-r", "turn": map[string]any{"id": "t1", "status": "inProgress"},
		})
		waitFor(t, "turn did not start", func() bool { return s.Info().GetStatus() == protocol.AgentWorking })

		res := d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{GracePeriod: 40 * time.Millisecond})
		if !res.ForcedRestart || !res.ResumedThread || res.Err != nil {
			t.Fatalf("abort result = %+v, want forced restart + resumed", res)
		}
		if got := s.Info().GetGoal(); got == nil || got.GetText() != "resumed goal objective" || !got.GetCanEdit() {
			t.Fatalf("post-restart goal = %v, want resumed goal objective (editable)", got)
		}

		// Subsequent setGoal uses the swapped client safely under -race.
		if err := b.setGoal(t.Context(), s, "updated post-restart goal"); err != nil {
			t.Fatalf("post-restart setGoal: %v", err)
		}
		if setCalls.Load() != 1 || s.Info().GetGoal().GetText() != "updated post-restart goal" {
			t.Fatalf("setCalls=%d goal=%v", setCalls.Load(), s.Info().GetGoal())
		}
	})

	t.Run("failed thread resume clears previous goal", func(t *testing.T) {
		srv1 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			if method == "thread/read" {
				reply(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "th-goal-fail"}}})
			}
		})
		srv2 := startMockCodexWSServer(t, nil)

		c1, err := codex.DialAppServerWS(t.Context(), srv1.wsURL(), "")
		if err != nil {
			t.Fatal(err)
		}
		s, err := session.Start(session.Spec{
			ID: "codex-restart-goal-clear", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
			Command: testSleepCommand(), Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		d.sessions.add(s)

		b := newCodexBridge(t.Context(), s.ID(), c1, nil, nil, nil)
		b.setThread("th-goal-fail", true)
		b.restartServer = func(ctx context.Context, prevThreadID string) (*codexRestartOutcome, error) {
			c2, err := codex.DialAppServerWS(ctx, srv2.wsURL(), "")
			if err != nil {
				return nil, err
			}
			return &codexRestartOutcome{
				client:        c2,
				resumedThread: false,
				tuiCmd:        testSleepCommand(),
			}, nil
		}
		d.attachCodexBridge(s, b)
		defer b.Close()

		s.ReportBridgeGoal(&protocol.GoalStatus{
			Source:          "codex",
			Status:          protocol.GoalActive,
			Text:            "old thread goal",
			SourceSessionId: "th-goal-fail",
			SourceRevision:  "1",
		})
		srv1.sendNotification(t, "turn/started", map[string]any{
			"threadId": "th-goal-fail", "turn": map[string]any{"id": "t1", "status": "inProgress"},
		})
		waitFor(t, "turn did not start", func() bool { return s.Info().GetStatus() == protocol.AgentWorking })

		res := d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{GracePeriod: 40 * time.Millisecond})
		if !res.ForcedRestart || res.ResumedThread || res.Err != nil {
			t.Fatalf("abort result = %+v, want forced restart with resumed=false", res)
		}
		if got := s.Info().GetGoal(); got != nil {
			t.Fatalf("stale goal survived failed thread resume: %v", got)
		}
	})
}

func TestCodexBridgeFastModeEndToEnd(t *testing.T) {
	t.Run("unsupported agent rejects fast_mode configure", func(t *testing.T) {
		d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		s, err := session.Start(session.Spec{
			ID: "agy-no-fast", NodeID: "test-node", Agent: "agy", Project: t.TempDir(),
			Command: testSleepCommand(), Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		d.sessions.add(s)

		if s.Info().GetControls().GetFastMode() {
			t.Fatalf("agy session should not advertise Controls.FastMode")
		}
		_, done := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
			return d.handleConfigureAgent(t.Context(), st, env)
		}, protocol.TypeConfigureAgent, &protocol.ConfigureAgent{
			SessionId: s.ID(),
			FastMode:  proto.Bool(true),
		})
		if err := <-done; err == nil || !strings.Contains(err.Error(), "does not support fast mode") {
			t.Fatalf("expected unsupported fast mode error, got %v", err)
		}
	})

	t.Run("claude session advertises and toggles fast_mode", func(t *testing.T) {
		d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		s, err := session.Start(session.Spec{
			ID: "claude-fast", NodeID: "test-node", Agent: "claude", Project: t.TempDir(),
			Command: testSleepCommand(), Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		d.sessions.add(s)

		if !s.Info().GetControls().GetFastMode() {
			t.Fatalf("claude session should advertise Controls.FastMode")
		}
		st, done := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
			return d.handleConfigureAgent(t.Context(), st, env)
		}, protocol.TypeConfigureAgent, &protocol.ConfigureAgent{
			SessionId: s.ID(),
			FastMode:  proto.Bool(true),
		})
		env, err := st.Recv()
		if err != nil || env.Type() != protocol.TypeSessionInfo {
			t.Fatalf("claude enable reply = %v, %v", env, err)
		}
		if err := <-done; err != nil {
			t.Fatalf("claude fast mode enable failed: %v", err)
		}
		if !env.GetSessionInfo().GetFastMode() || !s.Info().GetFastMode() {
			t.Fatalf("expected claude fast mode enabled, info=%v session=%v", env.GetSessionInfo().GetFastMode(), s.Info().GetFastMode())
		}

		st, done = ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
			return d.handleConfigureAgent(t.Context(), st, env)
		}, protocol.TypeConfigureAgent, &protocol.ConfigureAgent{
			SessionId: s.ID(),
			FastMode:  proto.Bool(false),
		})
		env, err = st.Recv()
		if err != nil || env.Type() != protocol.TypeSessionInfo {
			t.Fatalf("claude disable reply = %v, %v", env, err)
		}
		if err := <-done; err != nil {
			t.Fatalf("claude fast mode disable failed: %v", err)
		}
		if env.GetSessionInfo().GetFastMode() || s.Info().GetFastMode() {
			t.Fatalf("expected claude fast mode disabled, info=%v session=%v", env.GetSessionInfo().GetFastMode(), s.Info().GetFastMode())
		}
	})

	t.Run("idle toggle uses thread/resume and never mutates global config", func(t *testing.T) {
		var mu sync.Mutex
		var resumeTiers []string
		var wroteGlobalConfig bool

		mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			switch method {
			case "config/value/write":
				mu.Lock()
				wroteGlobalConfig = true
				mu.Unlock()
				reply(map[string]any{"id": id, "result": map[string]any{}})
			case "thread/resume":
				var p codex.ThreadResumeParams
				_ = json.Unmarshal(params, &p)
				mu.Lock()
				resumeTiers = append(resumeTiers, p.ServiceTier)
				mu.Unlock()
				retTier := "default"
				if p.ServiceTier == "fast" {
					retTier = "priority"
				}
				reply(map[string]any{
					"id": id,
					"result": map[string]any{
						"thread":      map[string]any{"id": p.ThreadID},
						"serviceTier": retTier,
					},
				})
			}
		})

		wsClient, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
		if err != nil {
			t.Fatal(err)
		}
		s, err := session.Start(session.Spec{
			ID: "codex-fast-idle", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
			Command: testSleepCommand(), Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		d.sessions.add(s)

		b := newCodexBridge(t.Context(), s.ID(), wsClient, nil, nil, nil)
		b.setThread("th-idle", true)
		b.initFastMode = false
		d.attachCodexBridge(s, b)
		defer b.Close()

		if info := s.Info(); !info.GetControls().GetFastMode() || info.GetFastMode() {
			t.Fatalf("after attach: controls=%v fastMode=%v", info.GetControls(), info.GetFastMode())
		}

		// Enable Fast Mode via handleConfigureAgent.
		st, done := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
			return d.handleConfigureAgent(t.Context(), st, env)
		}, protocol.TypeConfigureAgent, &protocol.ConfigureAgent{
			SessionId: s.ID(),
			FastMode:  proto.Bool(true),
		})
		env, err := st.Recv()
		if err != nil || env.Type() != protocol.TypeSessionInfo {
			t.Fatalf("enable fast mode reply = %v, %v", env, err)
		}
		if err := <-done; err != nil {
			t.Fatalf("enable handler err: %v", err)
		}
		if !env.GetSessionInfo().GetFastMode() || !env.GetSessionInfo().GetControls().GetFastMode() {
			t.Fatalf("SessionInfo after enable: fast=%v controls=%v", env.GetSessionInfo().GetFastMode(), env.GetSessionInfo().GetControls())
		}
		if rep := s.Reported(); rep == nil || rep.FastMode == nil || !rep.GetFastMode() {
			t.Fatalf("s.Reported() after enable = %v, want FastMode=true", rep)
		}

		// Disable Fast Mode via handleConfigureAgent.
		st, done = ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
			return d.handleConfigureAgent(t.Context(), st, env)
		}, protocol.TypeConfigureAgent, &protocol.ConfigureAgent{
			SessionId: s.ID(),
			FastMode:  proto.Bool(false),
		})
		env, err = st.Recv()
		if err != nil || env.Type() != protocol.TypeSessionInfo {
			t.Fatalf("disable fast mode reply = %v, %v", env, err)
		}
		if err := <-done; err != nil {
			t.Fatalf("disable handler err: %v", err)
		}
		if env.GetSessionInfo().GetFastMode() {
			t.Fatalf("SessionInfo after disable still has FastMode=true")
		}
		if rep := s.Reported(); rep == nil || rep.FastMode == nil || rep.GetFastMode() {
			t.Fatalf("s.Reported() after disable = %v, want FastMode=false", rep)
		}

		mu.Lock()
		defer mu.Unlock()
		if wroteGlobalConfig {
			t.Fatal("fast mode toggle must not write global Codex config")
		}
		if !slices.Equal(resumeTiers, []string{"fast", "default"}) {
			t.Fatalf("resume serviceTiers = %v, want [fast default]", resumeTiers)
		}
	})

	t.Run("unmaterialized -32600 defers tier to next turn/start and non-32600 fails cleanly", func(t *testing.T) {
		var mu sync.Mutex
		resumeErrCode := -32600
		var turnStartTiers []string
		var queueCalls int

		mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			switch method {
			case "thread/resume":
				mu.Lock()
				code := resumeErrCode
				mu.Unlock()
				reply(map[string]any{
					"id":    id,
					"error": map[string]any{"code": code, "message": "no rollout found for thread id th-0"},
				})
			case "turn/start":
				var p codex.TurnStartParams
				_ = json.Unmarshal(params, &p)
				mu.Lock()
				turnStartTiers = append(turnStartTiers, p.ServiceTier)
				mu.Unlock()
				reply(map[string]any{
					"id":     id,
					"result": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress"}},
				})
			case "thread/queue/add":
				mu.Lock()
				queueCalls++
				mu.Unlock()
				reply(map[string]any{
					"id":     id,
					"result": map[string]any{"queuedSubmission": map[string]any{"id": "q-1"}},
				})
			}
		})

		wsClient, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
		if err != nil {
			t.Fatal(err)
		}
		s, err := session.Start(session.Spec{
			ID: "codex-fast-0turn", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
			Command: testSleepCommand(), Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		d.sessions.add(s)
		b := newCodexBridge(t.Context(), s.ID(), wsClient, nil, nil, nil)
		b.setThread("th-0", false)
		d.attachCodexBridge(s, b)
		defer b.Close()

		// 1. -32600 from thread/resume succeeds and marks serviceTierDirty=true.
		if err := b.setFastMode(t.Context(), s, true); err != nil {
			t.Fatalf("setFastMode on -32600 failed: %v", err)
		}
		if !s.Info().GetFastMode() {
			t.Fatal("expected SessionInfo.FastMode=true after -32600 defer")
		}

		// 2. Next deliverMessage calls turn/start with serviceTier="fast" and clears dirty.
		if err := b.deliverMessage(t.Context(), "first turn"); err != nil {
			t.Fatalf("deliverMessage first turn: %v", err)
		}
		mu.Lock()
		if !slices.Equal(turnStartTiers, []string{"fast"}) || queueCalls != 0 {
			mu.Unlock()
			t.Fatalf("first turnStartTiers=%v queueCalls=%d, want [fast] / 0", turnStartTiers, queueCalls)
		}
		mu.Unlock()

		// 3. Subsequent deliverMessage uses normal thread/queue/add since dirty was cleared.
		if err := b.deliverMessage(t.Context(), "second turn"); err != nil {
			t.Fatalf("deliverMessage second turn: %v", err)
		}
		mu.Lock()
		if queueCalls != 1 {
			mu.Unlock()
			t.Fatalf("second turn queueCalls=%d, want 1", queueCalls)
		}
		// 4. Non--32600 RPC error does not flip state.
		resumeErrCode = -32603
		mu.Unlock()

		b.mu.Lock()
		b.settleTurnLocked()
		b.mu.Unlock()
		if err := b.setFastMode(t.Context(), s, false); err == nil {
			t.Fatal("expected error for -32603 thread/resume failure")
		}
		if !s.Info().GetFastMode() {
			t.Fatal("fastMode flipped despite non--32600 RPC error")
		}
	})

	t.Run("active turn defers tier and busy turn/start falls back to queue keeping dirty", func(t *testing.T) {
		var mu sync.Mutex
		var resumeCalls int
		turnStartBusy := true
		var turnStartTiers []string
		var queueCalls int

		mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			switch method {
			case "thread/resume":
				mu.Lock()
				resumeCalls++
				mu.Unlock()
				reply(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "th-busy"}}})
			case "turn/start":
				var p codex.TurnStartParams
				_ = json.Unmarshal(params, &p)
				mu.Lock()
				busy := turnStartBusy
				turnStartTiers = append(turnStartTiers, p.ServiceTier)
				mu.Unlock()
				if busy {
					reply(map[string]any{
						"id":    id,
						"error": map[string]any{"code": -32600, "message": "turn already in progress"},
					})
					return
				}
				reply(map[string]any{
					"id":     id,
					"result": map[string]any{"turn": map[string]any{"id": "turn-applied", "status": "inProgress"}},
				})
			case "thread/queue/add":
				mu.Lock()
				queueCalls++
				mu.Unlock()
				reply(map[string]any{
					"id":     id,
					"result": map[string]any{"queuedSubmission": map[string]any{"id": "q-busy"}},
				})
			}
		})

		wsClient, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
		if err != nil {
			t.Fatal(err)
		}
		s, err := session.Start(session.Spec{
			ID: "codex-fast-busy", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
			Command: testSleepCommand(), Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		b := newCodexBridge(t.Context(), s.ID(), wsClient, nil, nil, nil)
		b.setThread("th-busy", true)
		b.mu.Lock()
		b.noteTurnStartedLocked("turn-running")
		b.mu.Unlock()
		d.attachCodexBridge(s, b)
		defer b.Close()

		// Toggle during active turn skips thread/resume and marks dirty.
		if err := b.setFastMode(t.Context(), s, true); err != nil {
			t.Fatalf("setFastMode during active turn: %v", err)
		}
		mu.Lock()
		if resumeCalls != 0 {
			mu.Unlock()
			t.Fatalf("expected 0 thread/resume calls while turn active, got %d", resumeCalls)
		}
		mu.Unlock()
		if !s.Info().GetFastMode() {
			t.Fatal("expected FastMode=true")
		}

		// Simulate bridge thinking turn settled while server still reports busy:
		// deliverMessage tries turn/start, falls back to queue/add, keeps dirty=true.
		b.mu.Lock()
		b.settleTurnLocked()
		b.mu.Unlock()
		if err := b.deliverMessage(t.Context(), "queued while busy"); err != nil {
			t.Fatalf("deliverMessage busy fallback: %v", err)
		}
		mu.Lock()
		if queueCalls != 1 {
			mu.Unlock()
			t.Fatalf("queueCalls=%d, want 1", queueCalls)
		}
		turnStartBusy = false
		mu.Unlock()

		// Next turn succeeds via turn/start with serviceTier="fast" and clears dirty.
		if err := b.deliverMessage(t.Context(), "apply now"); err != nil {
			t.Fatalf("deliverMessage apply now: %v", err)
		}
		mu.Lock()
		if !slices.Equal(turnStartTiers, []string{"fast", "fast"}) {
			mu.Unlock()
			t.Fatalf("turnStartTiers=%v, want [fast fast]", turnStartTiers)
		}
		mu.Unlock()
		b.mu.Lock()
		stillDirty := b.serviceTierDirty
		b.mu.Unlock()
		if stillDirty {
			t.Fatal("expected serviceTierDirty cleared after turn/start succeeded")
		}
	})

	t.Run("thread/settings/updated, trySubscribe, stale epoch guard, and force-restart propagation", func(t *testing.T) {
		var mu sync.Mutex
		subscribeTier := "default"
		srv1 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			switch method {
			case "thread/resume":
				mu.Lock()
				tier := subscribeTier
				mu.Unlock()
				reply(map[string]any{
					"id": id,
					"result": map[string]any{
						"thread":      map[string]any{"id": "th-settings"},
						"serviceTier": tier,
					},
				})
			case "thread/read":
				reply(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "th-settings"}}})
			}
		})
		srv2 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			switch method {
			case "thread/read":
				reply(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "th-settings"}}})
			case "thread/backgroundTerminals/list":
				reply(map[string]any{"id": id, "result": map[string]any{"data": []any{}}})
			}
		})

		c1, err := codex.DialAppServerWS(t.Context(), srv1.wsURL(), "")
		if err != nil {
			t.Fatal(err)
		}
		s, err := session.Start(session.Spec{
			ID: "codex-fast-settings", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
			Command: testSleepCommand(), Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		d.sessions.add(s)

		b := newCodexBridge(t.Context(), s.ID(), c1, nil, nil, nil)
		b.setThread("th-settings", false)
		b.restartServer = func(ctx context.Context, prevThreadID string) (*codexRestartOutcome, error) {
			c2, err := codex.DialAppServerWS(ctx, srv2.wsURL(), "")
			if err != nil {
				return nil, err
			}
			return &codexRestartOutcome{
				client:        c2,
				threadID:      prevThreadID,
				resumedThread: true,
				initFastMode:  true,
				tuiCmd:        testSleepCommand(),
			}, nil
		}
		d.attachCodexBridge(s, b)
		defer b.Close()

		// 1. thread/settings/updated sets fastMode=true + model/effort/permissionMode.
		srv1.sendNotification(t, "thread/settings/updated", map[string]any{
			"threadId": "th-settings",
			"threadSettings": map[string]any{
				"model":          "gpt-5.4",
				"effort":         "xhigh",
				"approvalPolicy": "never",
				"sandboxPolicy":  map[string]any{"type": "dangerFullAccess"},
				"serviceTier":    "priority",
			},
		})
		waitFor(t, "thread/settings/updated priority not applied", func() bool {
			rep := s.Reported()
			return s.Info().GetFastMode() && rep.GetModel() == "gpt-5.4" &&
				rep.GetEffort() == "xhigh" && rep.GetPermissionMode() == "bypassPermissions" && rep.GetFastMode()
		})

		// 2. Other thread's settings notification is ignored.
		srv1.sendNotification(t, "thread/settings/updated", map[string]any{
			"threadId":       "th-subagent",
			"threadSettings": map[string]any{"serviceTier": "default"},
		})
		// 3. Explicit null serviceTier turns fast mode OFF.
		srv1.sendNotification(t, "thread/settings/updated", map[string]any{
			"threadId":       "th-settings",
			"threadSettings": map[string]any{"serviceTier": nil},
		})
		waitFor(t, "thread/settings/updated null did not disable fast mode", func() bool {
			rep := s.Reported()
			return !s.Info().GetFastMode() && rep.FastMode != nil && !rep.GetFastMode()
		})

		// 4. trySubscribeCodexThread respects serviceTierDirty override.
		b.mu.Lock()
		b.fastMode = true
		b.serviceTierOverride = "fast"
		b.serviceTierDirty = true
		b.subscribed = false
		epoch := b.epoch
		b.mu.Unlock()
		s.ReportFastMode(true)
		d.trySubscribeCodexThread(s, b, "th-settings", epoch)
		if !s.Info().GetFastMode() {
			t.Fatal("trySubscribeCodexThread clobbered dirty fastMode override")
		}
		// When not dirty, trySubscribeCodexThread adopts server serviceTier.
		b.mu.Lock()
		b.serviceTierDirty = false
		b.subscribed = false
		b.mu.Unlock()
		d.trySubscribeCodexThread(s, b, "th-settings", epoch)
		if s.Info().GetFastMode() {
			t.Fatal("trySubscribeCodexThread did not adopt server default serviceTier")
		}

		// 5. Force-restart propagates initFastMode=true from codexRestartOutcome.
		srv1.sendNotification(t, "turn/started", map[string]any{
			"threadId": "th-settings", "turn": map[string]any{"id": "t-stuck", "status": "inProgress"},
		})
		waitFor(t, "turn did not start", func() bool { return s.Info().GetStatus() == protocol.AgentWorking })

		res := d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{GracePeriod: 40 * time.Millisecond})
		if !res.ForcedRestart || !res.ResumedThread || res.Err != nil {
			t.Fatalf("abort result = %+v", res)
		}
		if !s.Info().GetFastMode() {
			t.Fatal("force-restart did not propagate initFastMode=true to SessionInfo")
		}

		// 6. Stale epoch thread/settings/updated is ignored.
		raw, _ := json.Marshal(map[string]any{
			"threadId":       "th-settings",
			"threadSettings": map[string]any{"serviceTier": "default"},
		})
		d.handleCodexNotification(s, b, codex.Notification{Method: "thread/settings/updated", Params: raw}, epoch)
		if !s.Info().GetFastMode() {
			t.Fatal("stale epoch thread/settings/updated clobbered post-restart fastMode")
		}
	})

	t.Run("serviceTierOverride stays in sync on subscribe, thread switch, and unresumed restart", func(t *testing.T) {
		var mu sync.Mutex
		resumeTier := "default"
		readTier := "default"

		srv1 := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			switch method {
			case "thread/resume":
				mu.Lock()
				tier := resumeTier
				mu.Unlock()
				reply(map[string]any{
					"id": id,
					"result": map[string]any{
						"thread":      map[string]any{"id": "th-sync-1"},
						"serviceTier": tier,
					},
				})
			case "thread/read":
				var p struct {
					ThreadID string `json:"threadId"`
				}
				_ = json.Unmarshal(params, &p)
				mu.Lock()
				tier := readTier
				mu.Unlock()
				reply(map[string]any{
					"id": id,
					"result": map[string]any{
						"thread":      map[string]any{"id": p.ThreadID},
						"serviceTier": tier,
					},
				})
			}
		})
		srv2 := startMockCodexWSServer(t, nil)

		c1, err := codex.DialAppServerWS(t.Context(), srv1.wsURL(), "")
		if err != nil {
			t.Fatal(err)
		}
		s, err := session.Start(session.Spec{
			ID: "codex-fast-sync", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
			Command: testSleepCommand(), Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		d.sessions.add(s)

		b := newCodexBridge(t.Context(), s.ID(), c1, nil, nil, nil)
		b.setThread("th-sync-1", false)
		b.mu.Lock()
		b.fastMode = true
		b.serviceTierOverride = "fast"
		b.serviceTierDirty = false
		epoch := b.epoch
		b.mu.Unlock()
		d.attachCodexBridge(s, b)
		defer b.Close()

		// 1. trySubscribeCodexThread reporting "default" must sync serviceTierOverride="default".
		d.trySubscribeCodexThread(s, b, "th-sync-1", epoch)
		b.mu.Lock()
		gotOverride, gotFast := b.serviceTierOverride, b.fastMode
		b.mu.Unlock()
		if gotFast || gotOverride != "default" {
			t.Fatalf("after trySubscribeCodexThread(default): fast=%v override=%q, want false / default", gotFast, gotOverride)
		}

		// 2. maybeSwitchCodexThread reporting "priority" then "default" syncs serviceTierOverride.
		mu.Lock()
		readTier = "priority"
		mu.Unlock()
		if !d.maybeSwitchCodexThread(s, b, "th-sync-2", "active", epoch) {
			t.Fatal("maybeSwitchCodexThread to th-sync-2 failed")
		}
		b.mu.Lock()
		gotOverride, gotFast = b.serviceTierOverride, b.fastMode
		b.mu.Unlock()
		if !gotFast || gotOverride != "fast" {
			t.Fatalf("after switch to priority thread: fast=%v override=%q, want true / fast", gotFast, gotOverride)
		}
		mu.Lock()
		readTier = "default"
		mu.Unlock()
		if !d.maybeSwitchCodexThread(s, b, "th-sync-3", "active", epoch) {
			t.Fatal("maybeSwitchCodexThread to th-sync-3 failed")
		}
		b.mu.Lock()
		gotOverride, gotFast = b.serviceTierOverride, b.fastMode
		b.mu.Unlock()
		if gotFast || gotOverride != "default" {
			t.Fatalf("after switch to default thread: fast=%v override=%q, want false / default", gotFast, gotOverride)
		}

		// 3. Unresumed force-restart (resumedThread=false, initFastMode=false) clears stale "fast" override.
		b.mu.Lock()
		b.fastMode = true
		b.serviceTierOverride = "fast"
		b.mu.Unlock()
		b.restartServer = func(ctx context.Context, prevThreadID string) (*codexRestartOutcome, error) {
			c2, err := codex.DialAppServerWS(ctx, srv2.wsURL(), "")
			if err != nil {
				return nil, err
			}
			return &codexRestartOutcome{
				client:        c2,
				resumedThread: false,
				initFastMode:  false,
				tuiCmd:        testSleepCommand(),
			}, nil
		}
		srv1.sendNotification(t, "turn/started", map[string]any{
			"threadId": "th-sync-3", "turn": map[string]any{"id": "t-unresumed", "status": "inProgress"},
		})
		waitFor(t, "turn did not start", func() bool { return s.Info().GetStatus() == protocol.AgentWorking })
		res := d.abortCodexTurn(t.Context(), s, b, codexAbortOptions{GracePeriod: 40 * time.Millisecond})
		if !res.ForcedRestart || res.ResumedThread || res.Err != nil {
			t.Fatalf("abort result = %+v, want forced restart with resumed=false", res)
		}
		b.mu.Lock()
		gotOverride, gotFast = b.serviceTierOverride, b.fastMode
		b.mu.Unlock()
		if gotFast || gotOverride != "" {
			t.Fatalf("after unresumed restart: fast=%v override=%q, want false / empty", gotFast, gotOverride)
		}
	})

	t.Run("setFastMode keeps serviceTierDirty when active thread switches mid-resume", func(t *testing.T) {
		inResume := make(chan struct{})
		releaseResume := make(chan struct{})
		var mu sync.Mutex
		var turnStartThread, turnStartTier string

		mock := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
			switch method {
			case "thread/resume":
				close(inResume)
				<-releaseResume
				reply(map[string]any{
					"id": id,
					"result": map[string]any{
						"thread":      map[string]any{"id": "th-old"},
						"serviceTier": "priority",
					},
				})
			case "turn/start":
				var p codex.TurnStartParams
				_ = json.Unmarshal(params, &p)
				mu.Lock()
				turnStartThread = p.ThreadID
				turnStartTier = p.ServiceTier
				mu.Unlock()
				reply(map[string]any{
					"id":     id,
					"result": map[string]any{"turn": map[string]any{"id": "turn-new", "status": "inProgress"}},
				})
			}
		})

		wsClient, err := codex.DialAppServerWS(t.Context(), mock.wsURL(), "")
		if err != nil {
			t.Fatal(err)
		}
		s, err := session.Start(session.Spec{
			ID: "codex-fast-switch-race", NodeID: "test-node", Agent: "codex", Project: t.TempDir(),
			Command: testSleepCommand(), Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		b := newCodexBridge(t.Context(), s.ID(), wsClient, nil, nil, nil)
		b.setThread("th-old", true)
		d.attachCodexBridge(s, b)
		defer b.Close()

		errCh := make(chan error, 1)
		go func() {
			errCh <- b.setFastMode(t.Context(), s, true)
		}()

		<-inResume
		// Switch active thread while thread/resume for th-old is still in flight.
		b.mu.Lock()
		b.tid = "th-new"
		b.mu.Unlock()
		close(releaseResume)

		if err := <-errCh; err != nil {
			t.Fatalf("setFastMode returned error: %v", err)
		}
		b.mu.Lock()
		dirty := b.serviceTierDirty
		override := b.serviceTierOverride
		b.mu.Unlock()
		if !dirty || override != "fast" {
			t.Fatalf("after thread switch during resume: dirty=%v override=%q, want true / fast", dirty, override)
		}

		// Delivering a message on th-new applies the deferred serviceTier="fast".
		if err := b.deliverMessage(t.Context(), "hello new thread"); err != nil {
			t.Fatalf("deliverMessage on th-new: %v", err)
		}
		mu.Lock()
		gotThread, gotTier := turnStartThread, turnStartTier
		mu.Unlock()
		if gotThread != "th-new" || gotTier != "fast" {
			t.Fatalf("turn/start got thread=%q tier=%q, want th-new / fast", gotThread, gotTier)
		}
	})
}
