package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestAppServerClientWSHandshakeCallsAndNotifications(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var gotAuth string
	var sawInitialized bool
	var sawExperimental bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
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
			switch req.Method {
			case "initialize":
				var p InitializeParams
				_ = json.Unmarshal(req.Params, &p)
				sawExperimental = p.Capabilities != nil && p.Capabilities.ExperimentalAPI
				_ = conn.WriteJSON(map[string]any{
					"id": req.ID,
					"result": map[string]any{
						"userAgent":      "mako/1.0",
						"codexHome":      "/tmp/.codex",
						"platformFamily": "unix",
						"platformOs":     "macos",
					},
				})
			case "initialized":
				sawInitialized = true
				// Emit an unsolicited notification and a ServerRequest (approval)
				// to ensure the client delivers notifications and stays passive on ServerRequests.
				_ = conn.WriteJSON(map[string]any{
					"id":     "srv-approval-1",
					"method": "item/commandExecution/requestApproval",
					"params": map[string]any{"threadId": "th-1", "turnId": "tu-1", "itemId": "it-1"},
				})
				_ = conn.WriteJSON(map[string]any{
					"method":      "item/agentMessage/delta",
					"emittedAtMs": 123456,
					"params":      map[string]any{"threadId": "th-1", "turnId": "tu-1", "itemId": "it-1", "delta": "hi"},
				})
			case "thread/start":
				_ = conn.WriteJSON(map[string]any{
					"id": req.ID,
					"result": map[string]any{
						"thread":          map[string]any{"id": "th-1", "path": "/tmp/rollout.jsonl", "cwd": "/work"},
						"model":           "gpt-6.2-sol",
						"reasoningEffort": "high",
					},
				})
			case "thread/queue/add":
				var p ThreadQueueAddParams
				_ = json.Unmarshal(req.Params, &p)
				if len(p.Input) > 0 && p.Input[0].Text == "ephemeral" {
					_ = conn.WriteJSON(map[string]any{
						"id":    req.ID,
						"error": map[string]any{"code": -32600, "message": "ephemeral thread does not support queued submissions"},
					})
				} else {
					_ = conn.WriteJSON(map[string]any{
						"id":     req.ID,
						"result": map[string]any{"queuedSubmission": map[string]any{"id": "q-1", "clientUserMessageId": p.ClientUserMessageID}},
					})
				}
			case "account/rateLimits/read":
				_ = conn.WriteJSON(map[string]any{
					"id": req.ID,
					"result": map[string]any{
						"rateLimits": map[string]any{
							"limitId": "codex",
							"primary": map[string]any{"usedPercent": 12.5, "windowDurationMins": 300, "resetsAt": 1700000000},
						},
					},
				})
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, err := DialAppServerWS(t.Context(), wsURL, "secret-tok")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	initRes, err := client.Initialize(t.Context(), "mako", "1.0", true)
	if err != nil {
		t.Fatal(err)
	}
	if initRes.CodexHome != "/tmp/.codex" || !sawExperimental {
		t.Fatalf("initRes=%+v sawExperimental=%v", initRes, sawExperimental)
	}
	if gotAuth != "Bearer secret-tok" {
		t.Fatalf("Authorization = %q", gotAuth)
	}

	// Receive the notification emitted on initialized.
	select {
	case n := <-client.Notifications():
		if n.Method != "item/agentMessage/delta" {
			t.Fatalf("notification = %+v", n)
		}
		var delta AgentMessageDeltaNotification
		if err := json.Unmarshal(n.Params, &delta); err != nil || delta.Delta != "hi" {
			t.Fatalf("delta = %+v err=%v", delta, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notification")
	}
	if !sawInitialized {
		t.Fatal("server did not receive initialized notification")
	}

	// thread/start
	ts, err := client.ThreadStart(t.Context(), ThreadStartParams{Cwd: "/work", Model: "gpt-6.2-sol"})
	if err != nil || ts.Thread.ID != "th-1" || ts.Model != "gpt-6.2-sol" {
		t.Fatalf("ThreadStart = %+v err=%v", ts, err)
	}

	// thread/queue/add ok and -32600
	if _, err := client.ThreadQueueAdd(t.Context(), ThreadQueueAddParams{
		ThreadID:            "th-1",
		ClientUserMessageID: "c-1",
		Input:               []UserInput{{Type: "text", Text: "hello"}},
	}); err != nil {
		t.Fatalf("ThreadQueueAdd failed: %v", err)
	}
	_, err = client.ThreadQueueAdd(t.Context(), ThreadQueueAddParams{
		ThreadID:            "th-1",
		ClientUserMessageID: "c-2",
		Input:               []UserInput{{Type: "text", Text: "ephemeral"}},
	})
	if !IsInvalidRequest(err) {
		t.Fatalf("expected IsInvalidRequest, got %v", err)
	}

	// account/rateLimits/read
	rl, err := client.RateLimitsRead(t.Context())
	if err != nil || rl.RateLimits.Primary == nil || rl.RateLimits.Primary.WindowDurationMins != 300 || rl.RateLimits.Primary.UsedPercent != 12.5 {
		t.Fatalf("RateLimitsRead = %+v err=%v", rl, err)
	}
}

func TestAppServerClientUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix domain sockets are tested on unix")
	}
	sockPath := filepath.Join(os.TempDir(), "mako-codex-unit-"+time.Now().Format("150405.000000")+".sock")
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	upgrader := websocket.Upgrader{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(data, &req) != nil {
				continue
			}
			if req.Method == "initialize" {
				_ = conn.WriteJSON(map[string]any{
					"id":     req.ID,
					"result": map[string]any{"userAgent": "uds", "codexHome": "/uds", "platformFamily": "unix", "platformOs": runtime.GOOS},
				})
			}
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	client, err := DialAppServerUnix(t.Context(), sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	res, err := client.Initialize(t.Context(), "mako", "1.0", true)
	if err != nil || res.CodexHome != "/uds" {
		t.Fatalf("UDS Initialize = %+v err=%v", res, err)
	}
}

func TestQueryModelsStdioPaginationAndFiltering(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	defer clientR.Close()
	defer clientW.Close()

	go func() {
		defer serverW.Close()
		defer serverR.Close()
		sc := bufio.NewScanner(serverR)
		write := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = serverW.Write(append(b, '\n'))
		}
		for sc.Scan() {
			var req struct {
				ID     int64           `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			switch req.Method {
			case "initialize":
				write(map[string]any{"id": req.ID, "result": map[string]any{"codexHome": "/tmp", "userAgent": "test", "platformFamily": "unix", "platformOs": "macos"}})
			case "model/list":
				// Send an interleaved notification first to verify it is skipped.
				write(map[string]any{"method": "account/updated", "params": map[string]any{}})
				var p ModelListParams
				_ = json.Unmarshal(req.Params, &p)
				if p.Cursor == nil {
					write(map[string]any{
						"id": req.ID,
						"result": map[string]any{
							"nextCursor": "page-2",
							"data": []map[string]any{
								{
									"id":          "gpt-6.2-sol",
									"displayName": "GPT-6.2-Sol",
									"hidden":      false,
									"supportedReasoningEfforts": []map[string]any{
										{"reasoningEffort": "low"},
										{"reasoningEffort": "high"},
									},
								},
								{
									"id":          "gpt-secret",
									"displayName": "Hidden",
									"hidden":      true,
								},
							},
						},
					})
				} else {
					write(map[string]any{
						"id": req.ID,
						"result": map[string]any{
							"nextCursor": nil,
							"data": []map[string]any{
								{"id": "gpt-6.2-sol", "displayName": "Duplicate", "hidden": false},
								{
									"id":          "gpt-6.2-astra",
									"displayName": "",
									"hidden":      false,
									"supportedReasoningEfforts": []map[string]any{
										{"reasoningEffort": "xhigh"},
									},
								},
							},
						},
					})
				}
			}
		}
	}()

	got, err := queryModelsStdioConn(t.Context(), clientR, clientW)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
	if got[0].ID != "gpt-6.2-sol" || got[0].Name != "GPT-6.2-Sol" || !slices.Equal(got[0].Efforts, []string{"low", "high"}) {
		t.Fatalf("model[0] = %+v", got[0])
	}
	if got[1].ID != "gpt-6.2-astra" || got[1].Name != "gpt-6.2-astra" || !slices.Equal(got[1].Efforts, []string{"xhigh"}) {
		t.Fatalf("model[1] = %+v", got[1])
	}
}

func TestThreadInfoIsSubagent(t *testing.T) {
	if (ThreadInfo{ID: "t1", Source: json.RawMessage(`"vscode"`)}).IsSubagent() {
		t.Fatal("vscode thread misclassified as subagent")
	}
	if !(ThreadInfo{ID: "t2", ParentThreadID: "t1"}).IsSubagent() {
		t.Fatal("thread with parentThreadId not classified as subagent")
	}
	if !(ThreadInfo{ID: "t3", Source: json.RawMessage(`{"subAgent":{"parent":"t1"}}`)}).IsSubagent() {
		t.Fatal("thread with subAgent source not classified as subagent")
	}
}

func TestAppServerClientTurnInterruptAndPendingSerialization(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var mu sync.Mutex
	var events []string
	var gotInterrupt TurnInterruptParams
	interruptSeen := make(chan struct{}, 1)
	releaseInterrupt := make(chan struct{})
	stallInterrupt := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var writeMu sync.Mutex
		reply := func(v any) {
			writeMu.Lock()
			defer writeMu.Unlock()
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
			switch req.Method {
			case "turn/interrupt":
				var p TurnInterruptParams
				_ = json.Unmarshal(req.Params, &p)
				mu.Lock()
				gotInterrupt = p
				events = append(events, "interrupt:start")
				stall := stallInterrupt
				mu.Unlock()
				select {
				case interruptSeen <- struct{}{}:
				default:
				}
				if stall {
					continue // never respond; caller times out
				}
				go func(id json.RawMessage) {
					<-releaseInterrupt
					mu.Lock()
					events = append(events, "interrupt:reply")
					mu.Unlock()
					reply(map[string]any{"id": id, "result": map[string]any{}})
				}(req.ID)
			case "turn/start":
				mu.Lock()
				events = append(events, "turn/start")
				mu.Unlock()
				reply(map[string]any{"id": req.ID, "result": map[string]any{"turn": map[string]any{"id": "turn-next", "status": "inProgress"}}})
			case "thread/queue/add":
				mu.Lock()
				events = append(events, "thread/queue/add")
				mu.Unlock()
				reply(map[string]any{"id": req.ID, "result": map[string]any{"queuedSubmission": map[string]any{"id": "q-1"}}})
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, err := DialAppServerWS(t.Context(), wsURL, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// 1. In-flight TurnInterrupt blocks concurrent TurnStart & ThreadQueueAdd
	// until turn/interrupt completes so a stale interrupt cannot abort a later turn.
	intErr := make(chan error, 1)
	go func() {
		intErr <- client.TurnInterrupt(t.Context(), TurnInterruptParams{ThreadID: "th-1", TurnID: "turn-1"})
	}()
	select {
	case <-interruptSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive turn/interrupt")
	}

	startDone := make(chan error, 1)
	go func() {
		_, err := client.TurnStart(t.Context(), TurnStartParams{
			ThreadID: "th-1",
			Input:    []UserInput{{Type: "text", Text: "follow-up"}},
		})
		startDone <- err
	}()
	queueDone := make(chan error, 1)
	go func() {
		_, err := client.ThreadQueueAdd(t.Context(), ThreadQueueAddParams{
			ThreadID:            "th-1",
			ClientUserMessageID: "c-1",
			Input:               []UserInput{{Type: "text", Text: "queued"}},
		})
		queueDone <- err
	}()

	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	midEvents := slices.Clone(events)
	mu.Unlock()
	if !slices.Equal(midEvents, []string{"interrupt:start"}) {
		t.Fatalf("subsequent turn RPC raced ahead of pending interrupt: %v", midEvents)
	}

	close(releaseInterrupt)
	if err := <-intErr; err != nil {
		t.Fatalf("TurnInterrupt: %v", err)
	}
	if err := <-startDone; err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if err := <-queueDone; err != nil {
		t.Fatalf("ThreadQueueAdd: %v", err)
	}

	mu.Lock()
	gotParams := gotInterrupt
	finalEvents := slices.Clone(events)
	mu.Unlock()
	if gotParams.ThreadID != "th-1" || gotParams.TurnID != "turn-1" {
		t.Fatalf("TurnInterrupt params = %+v, want th-1 / turn-1", gotParams)
	}
	if len(finalEvents) != 4 || finalEvents[0] != "interrupt:start" || finalEvents[1] != "interrupt:reply" {
		t.Fatalf("unexpected RPC ordering: %v", finalEvents)
	}

	// 2. Bounded timeout on stalled TurnInterrupt returns context.DeadlineExceeded
	// and still releases pending-interrupt waiters cleanly.
	mu.Lock()
	stallInterrupt = true
	mu.Unlock()
	tctx, tcancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer tcancel()
	if err := client.TurnInterrupt(tctx, TurnInterruptParams{ThreadID: "th-1", TurnID: "turn-2"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled TurnInterrupt err = %v, want DeadlineExceeded", err)
	}
	if _, err := client.TurnStart(t.Context(), TurnStartParams{
		ThreadID: "th-1",
		Input:    []UserInput{{Type: "text", Text: "after timeout"}},
	}); err != nil {
		t.Fatalf("TurnStart after timed-out interrupt failed: %v", err)
	}

	// 3. StartTurnInterrupt registers the pending interrupt synchronously before
	// returning so an immediate TurnStart with zero delay cannot slip ahead of it.
	releaseAsync := make(chan struct{})
	mu.Lock()
	stallInterrupt = false
	events = nil
	mu.Unlock()
	// Replace releaseInterrupt by gating via a dedicated server for step 3.
	asyncSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var wmu sync.Mutex
		send := func(v any) {
			wmu.Lock()
			defer wmu.Unlock()
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
			}
			if json.Unmarshal(data, &req) != nil {
				continue
			}
			switch req.Method {
			case "turn/interrupt":
				mu.Lock()
				events = append(events, "async-interrupt:start")
				mu.Unlock()
				go func(id json.RawMessage) {
					<-releaseAsync
					mu.Lock()
					events = append(events, "async-interrupt:reply")
					mu.Unlock()
					send(map[string]any{"id": id, "result": map[string]any{}})
				}(req.ID)
			case "turn/start":
				mu.Lock()
				events = append(events, "async-turn/start")
				mu.Unlock()
				send(map[string]any{"id": req.ID, "result": map[string]any{"turn": map[string]any{"id": "turn-after", "status": "inProgress"}}})
			}
		}
	}))
	defer asyncSrv.Close()

	asyncClient, err := DialAppServerWS(t.Context(), "ws"+strings.TrimPrefix(asyncSrv.URL, "http"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer asyncClient.Close()

	intDone := asyncClient.StartTurnInterrupt(t.Context(), TurnInterruptParams{ThreadID: "th-1", TurnID: "turn-3"})
	// Immediately invoke TurnStart with zero sleep; pending interrupt is already registered.
	asyncStartDone := make(chan error, 1)
	go func() {
		_, err := asyncClient.TurnStart(t.Context(), TurnStartParams{
			ThreadID: "th-1",
			Input:    []UserInput{{Type: "text", Text: "immediate"}},
		})
		asyncStartDone <- err
	}()
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	beforeRelease := slices.Clone(events)
	mu.Unlock()
	if slices.Contains(beforeRelease, "async-turn/start") {
		t.Fatalf("TurnStart bypassed StartTurnInterrupt before reply: %v", beforeRelease)
	}
	close(releaseAsync)
	if err := <-intDone; err != nil {
		t.Fatalf("StartTurnInterrupt: %v", err)
	}
	if err := <-asyncStartDone; err != nil {
		t.Fatalf("TurnStart after StartTurnInterrupt: %v", err)
	}
	mu.Lock()
	afterRelease := slices.Clone(events)
	mu.Unlock()
	if !slices.Equal(afterRelease, []string{"async-interrupt:start", "async-interrupt:reply", "async-turn/start"}) {
		t.Fatalf("unexpected async interrupt ordering: %v", afterRelease)
	}
}
